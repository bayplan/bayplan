package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Credentials is what is needed to talk to an API server, already in clear
// text. It is the intermediate form between what the admin pastes (loose fields
// or a kubeconfig) and what is persisted encrypted (ClusterConfig).
type Credentials struct {
	APIEndpoint string
	CACert      string // PEM; empty only if Insecure
	Insecure    bool   // InsecureSkipTLSVerify: explicitly accepted by the admin
	Token       string // bearer token (ServiceAccount or other)
	ClientCert  string // PEM; alternative to the token
	ClientKey   string // PEM
}

// AuthType returns "token" or "clientCert" for display in the UI.
func (c Credentials) AuthType() string {
	if c.ClientKey != "" {
		return "clientCert"
	}
	return "token"
}

// Validate checks the consistency of the fields before attempting to connect.
func (c Credentials) Validate() error {
	if c.APIEndpoint == "" {
		return errors.New("API endpoint is missing")
	}
	if !strings.HasPrefix(c.APIEndpoint, "https://") && !strings.HasPrefix(c.APIEndpoint, "http://") {
		return errors.New("the endpoint must start with https:// (or http://)")
	}
	if c.CACert == "" && !c.Insecure {
		return errors.New("CA certificate is missing (or explicitly choose to skip TLS verification)")
	}
	if c.Token == "" && (c.ClientCert == "" || c.ClientKey == "") {
		return errors.New("credential is missing: ServiceAccount token or client certificate+key")
	}
	return nil
}

// RestConfig builds the base rest.Config (without impersonation). It carries
// no Timeout on purpose: rest.Config.Timeout becomes http.Client.Timeout,
// which also cuts the BODY of watches and `logs -f`, so informers would
// reconnect every few seconds and log streams would die. Short calls are
// bounded with their context instead.
func (c Credentials) RestConfig() rest.Config {
	cfg := rest.Config{
		Host: c.APIEndpoint,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: c.Insecure,
		},
	}
	if !c.Insecure {
		cfg.TLSClientConfig.CAData = []byte(c.CACert)
	}
	if c.ClientKey != "" {
		cfg.TLSClientConfig.CertData = []byte(c.ClientCert)
		cfg.TLSClientConfig.KeyData = []byte(c.ClientKey)
	} else {
		cfg.BearerToken = c.Token
	}
	return cfg
}

// ParseKubeconfig extracts the credentials of the current context from a
// kubeconfig pasted by the admin. It only accepts EMBEDDED data
// (certificate-authority-data, client-certificate-data, token…): a reference to
// a file or to an exec plugin (aws/gcloud/az/kubelogin) is useless on the
// server, and is rejected with a message that says what to do instead.
func ParseKubeconfig(raw string) (Credentials, error) {
	cfg, err := clientcmd.Load([]byte(raw))
	if err != nil {
		return Credentials{}, fmt.Errorf("invalid kubeconfig: %w", err)
	}
	ctxName := cfg.CurrentContext
	if ctxName == "" {
		if len(cfg.Contexts) != 1 {
			return Credentials{}, errors.New("the kubeconfig has no current-context and contains several contexts")
		}
		for name := range cfg.Contexts {
			ctxName = name
		}
	}
	kctx, ok := cfg.Contexts[ctxName]
	if !ok {
		return Credentials{}, fmt.Errorf("context %q does not exist in the kubeconfig", ctxName)
	}
	cluster, ok := cfg.Clusters[kctx.Cluster]
	if !ok {
		return Credentials{}, fmt.Errorf("the context's cluster %q does not exist in the kubeconfig", kctx.Cluster)
	}
	user, ok := cfg.AuthInfos[kctx.AuthInfo]
	if !ok {
		return Credentials{}, fmt.Errorf("the context's user %q does not exist in the kubeconfig", kctx.AuthInfo)
	}

	creds := Credentials{APIEndpoint: cluster.Server, Insecure: cluster.InsecureSkipTLSVerify}
	switch {
	case len(cluster.CertificateAuthorityData) > 0:
		creds.CACert = string(cluster.CertificateAuthorityData)
	case cluster.CertificateAuthority != "":
		return Credentials{}, fmt.Errorf("the kubeconfig references the CA by file (%q): export it with `kubectl config view --raw --flatten`", cluster.CertificateAuthority)
	}

	switch {
	case user.Exec != nil:
		return Credentials{}, fmt.Errorf("the kubeconfig uses an exec plugin (%s): the server cannot run it. Create a ServiceAccount token (see the onboarding guide) or use the cloud credentials connection", user.Exec.Command)
	case user.AuthProvider != nil:
		return Credentials{}, fmt.Errorf("the kubeconfig uses an auth-provider (%s) not supported on the server; create a ServiceAccount token", user.AuthProvider.Name)
	case user.Token != "":
		creds.Token = user.Token
	case user.TokenFile != "":
		return Credentials{}, errors.New("the kubeconfig references the token by file; paste the token directly")
	case len(user.ClientCertificateData) > 0 && len(user.ClientKeyData) > 0:
		creds.ClientCert = string(user.ClientCertificateData)
		creds.ClientKey = string(user.ClientKeyData)
	case user.ClientCertificate != "" || user.ClientKey != "":
		return Credentials{}, errors.New("the kubeconfig references the client certificate by file: export it with `kubectl config view --raw --flatten`")
	case user.Username != "":
		return Credentials{}, errors.New("basic authentication (username/password) is not supported")
	default:
		return Credentials{}, errors.New("the kubeconfig contains no usable credential (token or client certificate)")
	}
	return creds, creds.Validate()
}

// validateTimeout is the ceiling of ValidateCredentials.
const validateTimeout = 10 * time.Second

// ValidateCredentials checks that the credentials work against the real
// cluster BEFORE persisting them, and returns the server version to show it as
// confirmation that the right cluster was reached.
func ValidateCredentials(ctx context.Context, creds Credentials) (serverVersion string, err error) {
	if err := creds.Validate(); err != nil {
		return "", err
	}
	restCfg := creds.RestConfig()
	clientset, err := kubernetes.NewForConfig(&restCfg)
	if err != nil {
		return "", fmt.Errorf("invalid client configuration: %w", err)
	}
	// Bounded by the caller's context, with a ceiling of our own in case it
	// carries no deadline: an endpoint that never answers must not hang the
	// onboarding request.
	ctx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	raw, err := clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return "", fmt.Errorf("could not connect to the cluster: %w", err)
	}
	return parseGitVersion(raw), nil
}
