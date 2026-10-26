package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"k8s.io/client-go/tools/clientcmd"
)

// azureDriver: AKS with a service principal (client credentials). It talks to
// ARM over REST (list and describe clusters, request the kubeconfig) and to the
// API server with an Entra ID token for the fixed audience of the AKS server,
// which is the same thing kubelogin does. If the cluster does NOT have Entra ID
// integrated, the kubeconfig carries a client certificate and that is used.
//
// No Azure SDK on purpose: azidentity+armcontainerservice drag in MSAL and half
// a module for four GET/POST requests with JSON.
type azureDriver struct{}

const (
	armBase = "https://management.azure.com"
	// aksServerAppID is the Entra ID app of the AKS API server; it is the same
	// in every tenant (documented by Microsoft for kubelogin).
	aksServerAppID = "6dae42f8-4368-4678-94ff-3960e28e3630"
	aksAPIVersion  = "2024-09-01"
)

func (azureDriver) tokenConfig(cred *Credential, scope string) *clientcredentials.Config {
	return &clientcredentials.Config{
		ClientID:     cred.Config.ClientID,
		ClientSecret: cred.Secret.ClientSecret,
		TokenURL:     fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(cred.Config.TenantID)),
		Scopes:       []string{scope},
	}
}

func (d azureDriver) armClient(ctx context.Context, cred *Credential) *http.Client {
	c := d.tokenConfig(cred, armBase+"/.default").Client(ctx)
	c.Timeout = providerTimeout
	return c
}

// armDo performs a request against ARM and decodes the JSON; ARM errors come
// as {"error":{"code","message"}} and are returned with their real message.
func armDo(ctx context.Context, client *http.Client, method, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Azure: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("Azure (%s): %s", e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("Azure: HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func (d azureDriver) Validate(ctx context.Context, cred *Credential) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	tok, err := d.tokenConfig(cred, armBase+"/.default").Token(ctx)
	if err != nil {
		return "", fmt.Errorf("Entra ID rejects the service principal: %w", err)
	}
	// Check that the subscription exists and the SP sees it (without this the
	// listing would return empty without error, which is worse than failing
	// here).
	if err := armDo(ctx, oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok)), http.MethodGet,
		fmt.Sprintf("%s/subscriptions/%s?api-version=2022-12-01", armBase, url.PathEscape(cred.Config.SubscriptionID)), nil); err != nil {
		return "", err
	}
	oid, err := jwtClaim(tok.AccessToken, "oid")
	if err != nil {
		return "", fmt.Errorf("Azure: the token carries no object id: %w", err)
	}
	return oid, nil
}

// jwtClaim reads a claim from a JWT payload WITHOUT validating the signature:
// the issuer just handed us the token over TLS; we only want to know whom it
// represents, to show it and put it in the RBAC.
func jwtClaim(token, claim string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	v, _ := claims[claim].(string)
	if v == "" {
		return "", fmt.Errorf("claim %q missing", claim)
	}
	return v, nil
}

type aksCluster struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	Properties struct {
		KubernetesVersion string `json:"kubernetesVersion"`
		ProvisioningState string `json:"provisioningState"`
		PowerState        struct {
			Code string `json:"code"`
		} `json:"powerState"`
		FQDN                   string `json:"fqdn"`
		PrivateFQDN            string `json:"privateFQDN"`
		APIServerAccessProfile struct {
			EnablePrivateCluster bool `json:"enablePrivateCluster"`
		} `json:"apiServerAccessProfile"`
		AADProfile *struct {
			Managed bool `json:"managed"`
		} `json:"aadProfile"`
	} `json:"properties"`
}

// resourceGroupOf extracts the resource group from the ARM id
// (/subscriptions/x/resourceGroups/RG/providers/...). ARM does not expose it
// as a field.
func resourceGroupOf(armID string) string {
	parts := strings.Split(armID, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}

func (d azureDriver) List(ctx context.Context, cred *Credential) ([]Discovered, error) {
	client := d.armClient(ctx, cred)
	next := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.ContainerService/managedClusters?api-version=%s",
		armBase, url.PathEscape(cred.Config.SubscriptionID), aksAPIVersion)
	out := []Discovered{}
	for next != "" {
		var page struct {
			Value    []aksCluster `json:"value"`
			NextLink string       `json:"nextLink"`
		}
		if err := armDo(ctx, client, http.MethodGet, next, &page); err != nil {
			return nil, err
		}
		for _, c := range page.Value {
			status := c.Properties.ProvisioningState
			if c.Properties.PowerState.Code != "" && c.Properties.PowerState.Code != "Running" {
				status = c.Properties.PowerState.Code
			}
			rg := resourceGroupOf(c.ID)
			out = append(out, Discovered{
				Ref:      Ref{Name: c.Name, ResourceGroup: rg, Location: c.Location},
				Name:     c.Name,
				Location: c.Location + " / " + rg,
				Version:  c.Properties.KubernetesVersion,
				Status:   status,
				Endpoint: c.Properties.FQDN,
				Private:  c.Properties.APIServerAccessProfile.EnablePrivateCluster,
			})
		}
		next = page.NextLink
	}
	return out, nil
}

func (d azureDriver) clusterURL(cred *Credential, ref Ref, suffix string) string {
	return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s%s?api-version=%s",
		armBase, url.PathEscape(cred.Config.SubscriptionID), url.PathEscape(ref.ResourceGroup), url.PathEscape(ref.Name), suffix, aksAPIVersion)
}

// Describe requests the cluster's user kubeconfig: it is the only way through
// ARM to obtain the CA. If the cluster has no Entra ID integrated, that
// kubeconfig also carries a client certificate, and then it is used.
func (d azureDriver) Describe(ctx context.Context, cred *Credential, ref Ref) (Endpoint, error) {
	if ref.ResourceGroup == "" || ref.Name == "" {
		return Endpoint{}, errors.New("AKS: the reference needs resource group and name")
	}
	client := d.armClient(ctx, cred)
	var creds struct {
		Kubeconfigs []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"kubeconfigs"`
	}
	if err := armDo(ctx, client, http.MethodPost, d.clusterURL(cred, ref, "/listClusterUserCredential"), &creds); err != nil {
		return Endpoint{}, fmt.Errorf("AKS %s/%s: %w", ref.ResourceGroup, ref.Name, err)
	}
	if len(creds.Kubeconfigs) == 0 {
		return Endpoint{}, fmt.Errorf("AKS %s/%s: ARM returned no kubeconfig", ref.ResourceGroup, ref.Name)
	}
	raw, err := base64.StdEncoding.DecodeString(creds.Kubeconfigs[0].Value)
	if err != nil {
		return Endpoint{}, fmt.Errorf("AKS: invalid kubeconfig: %w", err)
	}
	kc, err := clientcmd.Load(raw)
	if err != nil {
		return Endpoint{}, fmt.Errorf("AKS: invalid kubeconfig: %w", err)
	}
	kctx, ok := kc.Contexts[kc.CurrentContext]
	if !ok {
		return Endpoint{}, errors.New("AKS: kubeconfig without current context")
	}
	cluster, ok := kc.Clusters[kctx.Cluster]
	if !ok {
		return Endpoint{}, errors.New("AKS: kubeconfig without cluster")
	}
	ep := Endpoint{Server: cluster.Server, CAData: cluster.CertificateAuthorityData}
	if auth, ok := kc.AuthInfos[kctx.AuthInfo]; ok && len(auth.ClientKeyData) > 0 {
		ep.ClientCert, ep.ClientKey = auth.ClientCertificateData, auth.ClientKeyData
	}
	return ep, nil
}

func (d azureDriver) TokenSource(ctx context.Context, cred *Credential, _ Ref) (oauth2.TokenSource, error) {
	// clientcredentials already caches and renews the token on expiry.
	return d.tokenConfig(cred, aksServerAppID+"/.default").TokenSource(tokenSourceContext(ctx)), nil
}

// Subject: with Entra ID integrated, AKS identifies a service principal by its
// object id (not by the client id).
func (azureDriver) Subject(cred *Credential) (string, string) {
	return "User", cred.Config.Identity
}
