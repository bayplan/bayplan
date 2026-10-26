// Package cloud connects the panel to the managed clusters of the cloud
// providers (EKS, AKS, GKE) from a provider credential, instead of a
// ServiceAccount token pasted by hand per cluster.
//
// The credential serves two purposes: DISCOVERING the clusters (listing the
// provider's and choosing which ones to manage) and
// AUTHENTICATING against their API server with short-lived tokens issued by
// the provider itself (STS presign on AWS, Entra ID on Azure, OAuth2 on
// Google). The panel stores no cluster secret: endpoint and CA come from the
// provider and are refreshed on every reconnection; rotating the credential
// rotates all its clusters at once.
//
// The API server sees the provider's identity, not a ServiceAccount, so the
// panel's RBAC has to be granted to that identity (see Driver.Subject and
// agent.RenderRBAC). It is the only part the operator has to apply on the
// cluster, and it is the same for the three providers.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Provider is the cloud provider of a credential.
type Provider string

const (
	AWS   Provider = "aws"
	Azure Provider = "azure"
	GCP   Provider = "gcp"
)

// Config is the NON-sensitive part of a credential: the UI shows it and it is
// stored in the clear (JSONB). Which fields apply depends on the provider.
type Config struct {
	// AWS: access key id + regions to look for clusters in (EKS is regional).
	AccessKeyID string   `json:"accessKeyId,omitempty"`
	Regions     []string `json:"regions,omitempty"`
	// Azure: service principal (tenant + client id) and the subscription.
	TenantID       string `json:"tenantId,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	// GCP: project (by default the service account's) and its email.
	ProjectID   string `json:"projectId,omitempty"`
	ClientEmail string `json:"clientEmail,omitempty"`
	// Identity is the identity the provider resolves for this credential when
	// validating it (ARN on AWS, object id of the SP on Azure, email on GCP).
	// It is what has to be granted in the cluster's RBAC; stored to show it.
	Identity string `json:"identity,omitempty"`
}

// Secret is the sensitive part: it is encrypted with the keyring and never
// leaves towards the frontend. Which field applies depends on the provider.
type Secret struct {
	SecretAccessKey    string `json:"secretAccessKey,omitempty"`
	ClientSecret       string `json:"clientSecret,omitempty"`
	ServiceAccountJSON string `json:"serviceAccountJson,omitempty"`
}

// Credential is a cloud_credentials row. Secret is only filled in when loaded
// with Store.Get (to use it), never in listings.
type Credential struct {
	ID           int64
	Name         string
	Provider     Provider
	Config       Config
	Secret       Secret
	ClusterCount int
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Validate checks that the credential carries what its provider needs, before
// spending a call to the provider. With `requireSecret` false (an edit that
// does not touch the secret) only the configuration is checked.
func (c *Credential) Validate(requireSecret bool) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	switch c.Provider {
	case AWS:
		if c.Config.AccessKeyID == "" {
			return errors.New("AWS: access key id is missing")
		}
		if len(c.Config.Regions) == 0 {
			return errors.New("AWS: specify at least one region to look for EKS clusters in")
		}
		if requireSecret && c.Secret.SecretAccessKey == "" {
			return errors.New("AWS: secret access key is missing")
		}
	case Azure:
		if c.Config.TenantID == "" || c.Config.ClientID == "" || c.Config.SubscriptionID == "" {
			return errors.New("Azure: tenant id, client id and subscription id are required")
		}
		if requireSecret && c.Secret.ClientSecret == "" {
			return errors.New("Azure: the service principal's client secret is missing")
		}
	case GCP:
		if requireSecret && c.Secret.ServiceAccountJSON == "" {
			return errors.New("GCP: the service account JSON is missing")
		}
	default:
		return fmt.Errorf("unknown provider %q (aws, azure or gcp)", c.Provider)
	}
	return nil
}

// Ref identifies a cluster within the provider; it is stored in
// clusters.cloud_ref. Which fields are filled in depends on the provider.
type Ref struct {
	Name          string `json:"name"`
	Region        string `json:"region,omitempty"`        // AWS
	ResourceGroup string `json:"resourceGroup,omitempty"` // Azure
	Location      string `json:"location,omitempty"`      // Azure (region) and GCP (zone or region)
	Project       string `json:"project,omitempty"`       // GCP
}

// Key is a unique representation of the reference, used to cross the
// discovered list with the clusters already registered.
func (r Ref) Key() string {
	return strings.Join([]string{r.Project, r.ResourceGroup, r.Region, r.Location, r.Name}, "/")
}

// Discovered is a cluster that exists at the provider, as the registration
// screen lists it.
type Discovered struct {
	Ref      Ref    `json:"ref"`
	Name     string `json:"name"`
	Location string `json:"location"`
	Version  string `json:"version,omitempty"`
	Status   string `json:"status,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	// Private: the API server has no public endpoint. The server will only
	// reach it from inside that network; the UI warns instead of letting the
	// registration fail with an opaque timeout.
	Private bool `json:"private"`
	// ClusterID: already registered in the panel with this credential.
	ClusterID *int64 `json:"clusterId,omitempty"`
}

// Endpoint is what the provider returns when describing a cluster. If it
// carries a client certificate (AKS without Entra ID) there is no token to
// renew.
type Endpoint struct {
	Server     string
	CAData     []byte
	ClientCert []byte
	ClientKey  []byte
}

// Driver is what each provider implements. Every call receives the credential
// with the secret already decrypted.
type Driver interface {
	// Validate checks that the credential works and returns the identity the
	// provider attributes to it (ARN, object id, email).
	Validate(ctx context.Context, cred *Credential) (identity string, err error)
	// List returns the clusters visible with the credential.
	List(ctx context.Context, cred *Credential) ([]Discovered, error)
	// Describe returns endpoint and CA (or client certificate) of a cluster.
	Describe(ctx context.Context, cred *Credential, ref Ref) (Endpoint, error)
	// TokenSource issues short-lived tokens for that cluster's API server. It
	// may return nil if Describe brought a client certificate.
	TokenSource(ctx context.Context, cred *Credential, ref Ref) (oauth2.TokenSource, error)
	// Subject is the identity to grant the panel's RBAC to in the cluster.
	Subject(cred *Credential) (kind, name string)
}

// DriverFor returns a provider's driver.
func DriverFor(p Provider) (Driver, error) {
	switch p {
	case AWS:
		return awsDriver{}, nil
	case Azure:
		return azureDriver{}, nil
	case GCP:
		return gcpDriver{}, nil
	}
	return nil, fmt.Errorf("unknown provider %q", p)
}

// providerTimeout bounds every call to the provider's API: a bad credential or
// an unreachable region has to fail in seconds, not hang the registration
// screen.
const providerTimeout = 20 * time.Second

// tokenSourceContext is the context a long-lived token source keeps for its
// renewals: detached from the request that created it (the source outlives
// it) but with an HTTP client bounded by providerTimeout. oauth2 otherwise
// falls back to http.DefaultClient, which has no timeout: a hung token
// endpoint would block every request to that cluster forever. A client
// already in ctx (tests with a fake provider) is kept.
func tokenSourceContext(ctx context.Context) context.Context {
	ctx = context.WithoutCancel(ctx)
	if _, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: providerTimeout})
}
