package helm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ErrReleaseNotFound: the release does not exist in that namespace.
var ErrReleaseNotFound = errors.New("helm: release not found")

// ErrInvalidInput: invalid values or chart (400, not 500).
var ErrInvalidInput = errors.New("helm: invalid input")

// actionTimeout bounds installs and upgrades. No Wait (we do not wait for the
// pods to be ready: the UI already shows the workloads' state), so this is
// only the time to apply the manifests.
const actionTimeout = 5 * time.Minute

// restGetter adapts a rest.Config (already impersonated) to what the Helm SDK
// needs to talk to the cluster. It is the piece that allows using Helm without
// a kubeconfig on disk: Helm is designed for the CLI and expects a file; here
// cluster access comes from the Manager and the user's identity.
type restGetter struct {
	cfg       *rest.Config
	namespace string
}

func (g *restGetter) ToRESTConfig() (*rest.Config, error) { return rest.CopyConfig(g.cfg), nil }

func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewShortcutExpander(restmapper.NewDeferredDiscoveryRESTMapper(dc), dc, nil), nil
}

func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	// Only used to resolve the default namespace: an empty kubeconfig with the
	// override is enough.
	return clientcmd.NewDefaultClientConfig(clientcmdapi.Config{}, &clientcmd.ConfigOverrides{
		Context: clientcmdapi.Context{Namespace: g.namespace},
	})
}

// Client runs Helm actions on a cluster with a specific identity.
type Client struct {
	repos  *Repos
	logger *slog.Logger
}

func NewClient(repos *Repos, logger *slog.Logger) *Client {
	return &Client{repos: repos, logger: logger}
}

func (c *Client) config(cfg *rest.Config, namespace string) (*action.Configuration, error) {
	ac := new(action.Configuration)
	getter := &restGetter{cfg: cfg, namespace: namespace}
	debug := func(format string, v ...any) { c.logger.Debug("helm: " + fmt.Sprintf(format, v...)) }
	if err := ac.Init(getter, namespace, "secrets", debug); err != nil {
		return nil, fmt.Errorf("helm: failed to initialize: %w", err)
	}
	return ac, nil
}

// ReleaseSummary is the row of the release list.
type ReleaseSummary struct {
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
	Revision    int       `json:"revision"`
	Status      string    `json:"status"`
	Chart       string    `json:"chart"`
	Version     string    `json:"chartVersion"`
	AppVersion  string    `json:"appVersion"`
	Updated     time.Time `json:"updated"`
	Description string    `json:"description,omitempty"`
}

func summarize(r *release.Release) ReleaseSummary {
	s := ReleaseSummary{Name: r.Name, Namespace: r.Namespace, Revision: r.Version}
	if r.Info != nil {
		s.Status = string(r.Info.Status)
		s.Updated = r.Info.LastDeployed.Time
		s.Description = r.Info.Description
	}
	if r.Chart != nil && r.Chart.Metadata != nil {
		s.Chart = r.Chart.Metadata.Name
		s.Version = r.Chart.Metadata.Version
		s.AppVersion = r.Chart.Metadata.AppVersion
	}
	return s
}

// List returns the releases of the given namespaces (empty = all), including
// failed and in-progress ones: hiding them is exactly what must not be done.
func (c *Client) List(ctx context.Context, cfg *rest.Config, namespaces []string) ([]ReleaseSummary, error) {
	run := func(ns string) ([]*release.Release, error) {
		ac, err := c.config(cfg, ns)
		if err != nil {
			return nil, err
		}
		l := action.NewList(ac)
		l.All = true
		l.AllNamespaces = ns == ""
		l.SetStateMask()
		return l.Run()
	}
	var all []*release.Release
	if len(namespaces) == 0 {
		rs, err := run("")
		if err != nil {
			return nil, fmt.Errorf("helm: failed to list releases: %w", err)
		}
		all = rs
	} else {
		for _, ns := range namespaces {
			rs, err := run(ns)
			if err != nil {
				return nil, fmt.Errorf("helm: failed to list releases in %s: %w", ns, err)
			}
			all = append(all, rs...)
		}
	}
	out := make([]ReleaseSummary, 0, len(all))
	for _, r := range all {
		out = append(out, summarize(r))
	}
	_ = ctx
	return out, nil
}

// ReleaseDetail is what the release panel shows.
type ReleaseDetail struct {
	ReleaseSummary
	Notes    string           `json:"notes,omitempty"`
	Values   string           `json:"values"`   // user-supplied values (YAML)
	Manifest string           `json:"manifest"` // rendered manifests
	History  []ReleaseSummary `json:"history"`
}

func (c *Client) Get(ctx context.Context, cfg *rest.Config, namespace, name string) (ReleaseDetail, error) {
	ac, err := c.config(cfg, namespace)
	if err != nil {
		return ReleaseDetail{}, err
	}
	rel, err := action.NewGet(ac).Run(name)
	if err != nil {
		return ReleaseDetail{}, translateErr(err)
	}
	d := ReleaseDetail{ReleaseSummary: summarize(rel), Manifest: rel.Manifest}
	if rel.Info != nil {
		d.Notes = rel.Info.Notes
	}
	if vals, err := chartutil.Values(rel.Config).YAML(); err == nil {
		d.Values = vals
	}
	hist, err := action.NewHistory(ac).Run(name)
	if err == nil {
		for _, h := range hist {
			d.History = append(d.History, summarize(h))
		}
	}
	_ = ctx
	return d, nil
}

// ChartInfo is what the UI needs to prepare an install: the default values
// (values.yaml) and the README.
type ChartInfo struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	AppVersion    string `json:"appVersion,omitempty"`
	Description   string `json:"description,omitempty"`
	DefaultValues string `json:"defaultValues"`
	Readme        string `json:"readme,omitempty"`
}

// ChartRef identifies a chart within a repo.
type ChartRef struct {
	Repo    Repo
	Chart   string
	Version string
}

// chartSegment is one component of a chart name: what Helm repo indexes and
// OCI repositories use, without anything that could read as a path ("..",
// a leading dot, separators, spaces, ':' or '@').
var chartSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*(\.[A-Za-z0-9_-]+)*$`)

// ValidateChartName rejects chart names that are not a plain name. It matters
// because the SDK's LocateChart looks for a LOCAL path with that name before
// asking the repo (Helm issue #7862), and the name comes from the URL: with
// "../../etc" (or even "etc" if the server's cwd is /) the panel would read
// and return values.yaml/README of the server's own directories. OCI repos
// may nest the chart ("team/chart"), so "/" is allowed there between valid
// segments.
func ValidateChartName(name string, oci bool) error {
	if name == "" || len(name) > 255 {
		return fmt.Errorf("%w: invalid chart name", ErrInvalidInput)
	}
	segs := []string{name}
	if oci {
		segs = strings.Split(name, "/")
	}
	for _, seg := range segs {
		if !chartSegment.MatchString(seg) {
			return fmt.Errorf("%w: invalid chart name %q", ErrInvalidInput, name)
		}
	}
	return nil
}

// loadChart downloads (or takes from cache) the chart and loads it.
func (c *Client) loadChart(ref ChartRef) (*chart.Chart, error) {
	if err := ValidateChartName(ref.Chart, ref.Repo.IsOCI()); err != nil {
		return nil, err
	}
	entry, err := c.repos.Entry(ref.Repo)
	if err != nil {
		return nil, err
	}
	c.repos.maybePruneCache(time.Now())
	settings := c.repos.EnvSettings()
	cpo := action.ChartPathOptions{Version: ref.Version, Username: entry.Username, Password: entry.Password, InsecureSkipTLSverify: ref.Repo.InsecureSkipTLS}
	name := ref.Chart
	if ref.Repo.IsOCI() {
		name = strings.TrimSuffix(ref.Repo.URL, "/") + "/" + ref.Chart
		rc, err := c.repos.registryClient(ref.Repo)
		if err != nil {
			return nil, err
		}
		// LocateChart needs the registry client for oci://; it is injected via
		// a temporary Install, which is the SDK's only public door.
		tmp := action.NewInstall(new(action.Configuration))
		tmp.SetRegistryClient(rc)
		tmp.ChartPathOptions = cpo
		path, err := tmp.ChartPathOptions.LocateChart(name, settings)
		if err != nil {
			return nil, fmt.Errorf("%w: could not fetch %s: %v", ErrInvalidInput, name, err)
		}
		return loader.Load(path)
	}
	cpo.RepoURL = ref.Repo.URL
	// A valid name can still match a directory relative to the server's cwd
	// ("etc", "tmp"), and LocateChart would load it instead of the repo's.
	if _, err := os.Stat(name); err == nil {
		return nil, fmt.Errorf("%w: chart name %q collides with a local path", ErrInvalidInput, name)
	}
	path, err := cpo.LocateChart(name, settings)
	if err != nil {
		return nil, fmt.Errorf("%w: could not fetch chart %s (%s): %v", ErrInvalidInput, name, ref.Version, err)
	}
	ch, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid chart: %v", ErrInvalidInput, err)
	}
	return ch, nil
}

// Chart returns a chart's metadata, default values.yaml and README.
func (c *Client) Chart(ref ChartRef) (ChartInfo, error) {
	ch, err := c.loadChart(ref)
	if err != nil {
		return ChartInfo{}, err
	}
	info := ChartInfo{Name: ch.Name(), Version: ch.Metadata.Version, AppVersion: ch.Metadata.AppVersion, Description: ch.Metadata.Description}
	for _, f := range ch.Raw {
		switch strings.ToLower(f.Name) {
		case "values.yaml":
			info.DefaultValues = string(f.Data)
		case "readme.md":
			info.Readme = string(f.Data)
		}
	}
	return info, nil
}

func parseValues(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	vals, err := chartutil.ReadValues([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid values YAML: %v", ErrInvalidInput, err)
	}
	return vals, nil
}

// InstallOptions are the parameters of an install or upgrade.
type InstallOptions struct {
	Name            string
	Namespace       string
	Values          string // YAML
	CreateNamespace bool
	DryRun          bool
}

// Install installs the chart. With DryRun it renders and validates on the
// server without creating anything (equivalent to `helm install
// --dry-run=server`).
func (c *Client) Install(ctx context.Context, cfg *rest.Config, ref ChartRef, opts InstallOptions) (ReleaseSummary, string, error) {
	ch, err := c.loadChart(ref)
	if err != nil {
		return ReleaseSummary{}, "", err
	}
	if req := ch.Metadata.Dependencies; req != nil {
		if err := action.CheckDependencies(ch, req); err != nil {
			return ReleaseSummary{}, "", fmt.Errorf("%w: the chart has unpackaged dependencies: %v", ErrInvalidInput, err)
		}
	}
	vals, err := parseValues(opts.Values)
	if err != nil {
		return ReleaseSummary{}, "", err
	}
	ac, err := c.config(cfg, opts.Namespace)
	if err != nil {
		return ReleaseSummary{}, "", err
	}
	inst := action.NewInstall(ac)
	inst.ReleaseName = opts.Name
	inst.Namespace = opts.Namespace
	inst.CreateNamespace = opts.CreateNamespace
	inst.Timeout = actionTimeout
	inst.DryRun = opts.DryRun
	if opts.DryRun {
		inst.DryRunOption = "server"
	}
	rctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	rel, err := inst.RunWithContext(rctx, ch, vals)
	if err != nil {
		return ReleaseSummary{}, "", translateErr(err)
	}
	return summarize(rel), rel.Manifest, nil
}

// Upgrade updates an existing release to the given version/values. Always
// with the given values (no ReuseValues): the UI starts from the current
// values, so what arrives is the complete truth.
func (c *Client) Upgrade(ctx context.Context, cfg *rest.Config, ref ChartRef, opts InstallOptions) (ReleaseSummary, string, error) {
	ch, err := c.loadChart(ref)
	if err != nil {
		return ReleaseSummary{}, "", err
	}
	vals, err := parseValues(opts.Values)
	if err != nil {
		return ReleaseSummary{}, "", err
	}
	ac, err := c.config(cfg, opts.Namespace)
	if err != nil {
		return ReleaseSummary{}, "", err
	}
	up := action.NewUpgrade(ac)
	up.Namespace = opts.Namespace
	up.Timeout = actionTimeout
	up.DryRun = opts.DryRun
	if opts.DryRun {
		up.DryRunOption = "server"
	}
	rctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	rel, err := up.RunWithContext(rctx, opts.Name, ch, vals)
	if err != nil {
		return ReleaseSummary{}, "", translateErr(err)
	}
	return summarize(rel), rel.Manifest, nil
}

// Rollback goes back to the given revision (0 = the previous one).
func (c *Client) Rollback(ctx context.Context, cfg *rest.Config, namespace, name string, revision int) error {
	ac, err := c.config(cfg, namespace)
	if err != nil {
		return err
	}
	rb := action.NewRollback(ac)
	rb.Version = revision
	rb.Timeout = actionTimeout
	_ = ctx
	return translateErr(rb.Run(name))
}

// Uninstall uninstalls the release (without keeping history).
func (c *Client) Uninstall(ctx context.Context, cfg *rest.Config, namespace, name string) error {
	ac, err := c.config(cfg, namespace)
	if err != nil {
		return err
	}
	un := action.NewUninstall(ac)
	un.Timeout = actionTimeout
	_ = ctx
	_, err = un.Run(name)
	return translateErr(err)
}

func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return ErrReleaseNotFound
	}
	return err
}
