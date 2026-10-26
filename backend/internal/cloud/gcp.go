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

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// gcpDriver: GKE with a Google service account (key JSON). The same OAuth2
// token (cloud-platform scope) serves both the GKE API (list and describe) and
// the cluster's API server, which accepts Google tokens directly; the identity
// Kubernetes sees is the SA's email.
type gcpDriver struct{}

const gkeBase = "https://container.googleapis.com/v1"

// gcpCredentials is the SA JSON once loaded: TokenSource plus the fields that
// are shown or used for routing.
type gcpCredentials struct {
	TokenSource oauth2.TokenSource
	ProjectID   string
	ClientEmail string
}

// credentials loads ONLY keys of type "service_account" (JWTConfigFromJSON).
// google.CredentialsFromJSON is deliberately not used: it also accepts configs
// of type external_account/impersonated, which point to a local file or an
// arbitrary URL from which to read the credential; pasted from the UI that
// would be a server file read or an SSRF with admin permissions.
func (gcpDriver) credentials(ctx context.Context, cred *Credential) (*gcpCredentials, error) {
	raw := []byte(cred.Secret.ServiceAccountJSON)
	var meta struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("GCP: invalid service account JSON: %w", err)
	}
	if meta.Type != "service_account" {
		return nil, fmt.Errorf("GCP: only keys of type service_account are supported (got %q)", meta.Type)
	}
	conf, err := google.JWTConfigFromJSON(raw, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("GCP: invalid service account JSON: %w", err)
	}
	// The token URL is Google's: the JSON carries it and no other one is
	// accepted, for the same reason as above.
	conf.TokenURL = google.JWTTokenURL
	return &gcpCredentials{TokenSource: conf.TokenSource(ctx), ProjectID: meta.ProjectID, ClientEmail: meta.ClientEmail}, nil
}

// project returns the configured project or, failing that, the SA's own.
func (gcpDriver) project(cred *Credential, c *gcpCredentials) (string, error) {
	if cred.Config.ProjectID != "" {
		return cred.Config.ProjectID, nil
	}
	if c.ProjectID != "" {
		return c.ProjectID, nil
	}
	return "", errors.New("GCP: specify the project (the SA JSON does not carry it)")
}

func gkeDo(ctx context.Context, client *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GCP: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("GCP (%s): %s", e.Error.Status, e.Error.Message)
		}
		return fmt.Errorf("GCP: HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

func (d gcpDriver) Validate(ctx context.Context, cred *Credential) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	c, err := d.credentials(ctx, cred)
	if err != nil {
		return "", err
	}
	if c.ClientEmail == "" {
		return "", errors.New("GCP: the JSON carries no client_email")
	}
	if _, err := c.TokenSource.Token(); err != nil {
		return "", fmt.Errorf("Google rejects the service account: %w", err)
	}
	// Check that the SA reaches the project's GKE API: without
	// container.clusters.list the registration would be useless.
	project, err := d.project(cred, c)
	if err != nil {
		return "", err
	}
	var probe struct{}
	if err := gkeDo(ctx, oauth2.NewClient(ctx, c.TokenSource), fmt.Sprintf("%s/projects/%s/locations/-/clusters", gkeBase, url.PathEscape(project)), &probe); err != nil {
		return "", err
	}
	return c.ClientEmail, nil
}

type gkeCluster struct {
	Name                 string `json:"name"`
	Location             string `json:"location"`
	Endpoint             string `json:"endpoint"`
	CurrentMasterVersion string `json:"currentMasterVersion"`
	Status               string `json:"status"`
	MasterAuth           struct {
		ClusterCACertificate string `json:"clusterCaCertificate"`
	} `json:"masterAuth"`
	PrivateClusterConfig *struct {
		EnablePrivateEndpoint bool   `json:"enablePrivateEndpoint"`
		PublicEndpoint        string `json:"publicEndpoint"`
	} `json:"privateClusterConfig"`
}

func (d gcpDriver) List(ctx context.Context, cred *Credential) ([]Discovered, error) {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	c, err := d.credentials(ctx, cred)
	if err != nil {
		return nil, err
	}
	project, err := d.project(cred, c)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Clusters []gkeCluster `json:"clusters"`
	}
	if err := gkeDo(ctx, oauth2.NewClient(ctx, c.TokenSource), fmt.Sprintf("%s/projects/%s/locations/-/clusters", gkeBase, url.PathEscape(project)), &resp); err != nil {
		return nil, err
	}
	out := []Discovered{}
	for _, k := range resp.Clusters {
		out = append(out, Discovered{
			Ref:      Ref{Name: k.Name, Location: k.Location, Project: project},
			Name:     k.Name,
			Location: k.Location,
			Version:  k.CurrentMasterVersion,
			Status:   k.Status,
			Endpoint: k.Endpoint,
			Private:  k.PrivateClusterConfig != nil && k.PrivateClusterConfig.EnablePrivateEndpoint,
		})
	}
	return out, nil
}

func (d gcpDriver) Describe(ctx context.Context, cred *Credential, ref Ref) (Endpoint, error) {
	if ref.Project == "" || ref.Location == "" || ref.Name == "" {
		return Endpoint{}, errors.New("GKE: the reference needs project, location and name")
	}
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	c, err := d.credentials(ctx, cred)
	if err != nil {
		return Endpoint{}, err
	}
	var k gkeCluster
	u := fmt.Sprintf("%s/projects/%s/locations/%s/clusters/%s", gkeBase, url.PathEscape(ref.Project), url.PathEscape(ref.Location), url.PathEscape(ref.Name))
	if err := gkeDo(ctx, oauth2.NewClient(ctx, c.TokenSource), u, &k); err != nil {
		return Endpoint{}, fmt.Errorf("GKE %s/%s: %w", ref.Location, ref.Name, err)
	}
	if k.Endpoint == "" {
		return Endpoint{}, fmt.Errorf("GKE %s/%s: the cluster has no endpoint yet (status %s)", ref.Location, ref.Name, k.Status)
	}
	ca, err := base64.StdEncoding.DecodeString(k.MasterAuth.ClusterCACertificate)
	if err != nil {
		return Endpoint{}, fmt.Errorf("GKE: invalid CA: %w", err)
	}
	return Endpoint{Server: "https://" + k.Endpoint, CAData: ca}, nil
}

func (d gcpDriver) TokenSource(ctx context.Context, cred *Credential, _ Ref) (oauth2.TokenSource, error) {
	c, err := d.credentials(tokenSourceContext(ctx), cred)
	if err != nil {
		return nil, err
	}
	return c.TokenSource, nil
}

func (gcpDriver) Subject(cred *Credential) (string, string) {
	return "User", cred.Config.Identity
}
