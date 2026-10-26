package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Service joins Store and Driver: it is what the handlers and the k8s.Manager
// use (through the k8s.CloudConnector interface, so that k8s imports neither
// this package nor its SDKs).
type Service struct {
	Store *Store
}

func NewService(store *Store) *Service {
	return &Service{Store: store}
}

// Validate checks the credential against the provider and fills in
// Config.Identity with what it returns.
func (s *Service) Validate(ctx context.Context, cred *Credential) error {
	if err := cred.Validate(true); err != nil {
		return err
	}
	d, err := DriverFor(cred.Provider)
	if err != nil {
		return err
	}
	identity, err := d.Validate(ctx, cred)
	if err != nil {
		return err
	}
	cred.Config.Identity = identity
	return nil
}

// Discover lists the provider's clusters and marks those already registered.
func (s *Service) Discover(ctx context.Context, cred *Credential) ([]Discovered, error) {
	d, err := DriverFor(cred.Provider)
	if err != nil {
		return nil, err
	}
	found, err := d.List(ctx, cred)
	if err != nil {
		return nil, err
	}
	using, err := s.Store.ClustersUsing(ctx, cred.ID)
	if err != nil {
		return nil, err
	}
	for i := range found {
		if id, ok := using[found[i].Ref.Key()]; ok {
			found[i].ClusterID = &id
		}
	}
	return found, nil
}

// Subject is the identity to grant the panel's RBAC to in a cluster of this
// credential.
func (s *Service) Subject(cred *Credential) (kind, name string) {
	d, err := DriverFor(cred.Provider)
	if err != nil {
		return "", ""
	}
	return d.Subject(cred)
}

// RestConfig builds the base rest.Config of a cloud cluster: it describes the
// cluster (fresh endpoint and CA) and sets up a transport that adds the
// provider's token, renewing it on its own before it expires. It is the
// equivalent of Credentials.RestConfig for the direct connector. Implements
// k8s.CloudConnector.
func (s *Service) RestConfig(ctx context.Context, credID int64, rawRef json.RawMessage) (*rest.Config, error) {
	cred, err := s.Store.Get(ctx, credID)
	if err != nil {
		return nil, err
	}
	var ref Ref
	if err := json.Unmarshal(rawRef, &ref); err != nil {
		return nil, fmt.Errorf("cloud: corrupt cluster reference: %w", err)
	}
	return s.restConfigFor(ctx, cred, ref)
}

func (s *Service) restConfigFor(ctx context.Context, cred *Credential, ref Ref) (*rest.Config, error) {
	d, err := DriverFor(cred.Provider)
	if err != nil {
		return nil, err
	}
	return restConfigWith(ctx, d, cred, ref)
}

// restConfigWith is restConfigFor with the driver injected (testable without a
// provider).
func restConfigWith(ctx context.Context, d Driver, cred *Credential, ref Ref) (*rest.Config, error) {
	ep, err := d.Describe(ctx, cred, ref)
	if err != nil {
		return nil, err
	}
	cfg := &rest.Config{
		Host:            ep.Server,
		TLSClientConfig: rest.TLSClientConfig{CAData: ep.CAData},
	}
	if len(ep.ClientKey) > 0 {
		cfg.TLSClientConfig.CertData = ep.ClientCert
		cfg.TLSClientConfig.KeyData = ep.ClientKey
		return cfg, nil
	}
	src, err := d.TokenSource(ctx, cred, ref)
	if err != nil {
		return nil, err
	}
	// ReuseTokenSource caches the token and renews it on expiry; it lives in
	// the closure, so the copies of the rest.Config (impersonation, Helm)
	// share the cache instead of each requesting a new token.
	ts := oauth2.ReuseTokenSource(nil, src)
	// WrapTransport is client-go's innermost wrapper: the Impersonate-* header
	// is set by an outer wrapper and arrives here intact.
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return &oauth2.Transport{Source: ts, Base: rt}
	}
	return cfg, nil
}

// Probe describes the cluster, builds its rest.Config and checks that the API
// server answers with the provider's identity. Returns the rest.Config (to
// store endpoint and CA) and the server version.
func (s *Service) Probe(ctx context.Context, cred *Credential, ref Ref) (*rest.Config, string, error) {
	cfg, err := s.restConfigFor(ctx, cred, ref)
	if err != nil {
		return nil, "", err
	}
	version, err := serverVersion(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	return cfg, version, nil
}

func serverVersion(ctx context.Context, cfg *rest.Config) (string, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("invalid client configuration: %w", err)
	}
	raw, err := clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return "", fmt.Errorf("could not connect to the cluster: %w", err)
	}
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.GitVersion, nil
}
