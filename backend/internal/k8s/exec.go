package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	httpstreamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// ExecOptions groups the parameters of an interactive terminal session on a
// container. Mirrors the parameters of "kubectl exec -it".
type ExecOptions struct {
	Namespace     string
	Pod           string
	Container     string
	Command       []string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer // ignored if TTY=true (TTY multiplexes stderr over stdout)
	TTY           bool
	TerminalSizes remotecommand.TerminalSizeQueue
}

// impersonatedRestConfig clones restConfig (base, without impersonation) and
// adds Impersonate.UserName/Groups. Mirror of the clone used in
// actionClientset (actions.go), but returning the *rest.Config instead of a
// *kubernetes.Clientset: the remotecommand constructors need the rest.Config
// directly to open the pods/exec subresource, which has no high-level method in
// the Clientset. It ALWAYS returns a fresh copy (cfg is a value, not a pointer
// to the connection's internal restConfig), so the shared state of clusterConn
// is never mutated.
func (c *clusterConn) impersonatedRestConfig(actingUser, actingGroup string) *rest.Config {
	cfg := c.restConfig
	cfg.Impersonate.UserName = impersonatedUsername(actingUser)
	cfg.Impersonate.Groups = []string{actingGroup}
	return &cfg
}

// ExecInPod runs an interactive command inside a container (equivalent to
// "kubectl exec -it"), always impersonating the real user with the admins
// group (exec is a write capability, just like restart/scale — see router.go,
// gated admin-on-cluster).
//
// It uses the SPDY executor: it is the pods/exec mechanism supported by every
// Kubernetes version since forever (unlike the newer WebSocket executor, whose
// server-side support is not guaranteed on every managed cluster), and it also
// avoids dragging in the extra gorilla/websocket dependency just for a
// non-essential alternative path.
//
// It returns the command's exit code (0 if it finished fine) and an error only
// when something other than "the command exited with code != 0" failed (e.g.
// could not connect, insufficient RBAC, container does not exist). An
// exec.CodeExitError is not a transport failure: it is translated to (code,
// nil) so the caller does not treat it as a stream error.
func (m *Manager) ExecInPod(ctx context.Context, id int64, opts ExecOptions, actingUser string) (int, error) {
	if err := m.GuardNamespace(id, opts.Namespace); err != nil {
		return 0, err
	}
	conn, err := m.getConn(id)
	if err != nil {
		return 0, err
	}

	cfg := conn.impersonatedRestConfig(actingUser, GroupAdmins)
	client, err := restClientFor(cfg)
	if err != nil {
		return 0, fmt.Errorf("k8s: failed to prepare exec client: %w", err)
	}

	req := client.Post().
		Resource("pods").
		Name(opts.Pod).
		Namespace(opts.Namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: opts.Container,
		Command:   opts.Command,
		Stdin:     opts.Stdin != nil,
		Stdout:    opts.Stdout != nil,
		Stderr:    opts.Stderr != nil && !opts.TTY,
		TTY:       opts.TTY,
	}, scheme.ParameterCodec)

	executor, err := spdyExecutorFor(cfg, "POST", req.URL())
	if err != nil {
		return 0, fmt.Errorf("k8s: failed to prepare exec executor: %w", err)
	}

	streamOpts := remotecommand.StreamOptions{
		Stdin:             opts.Stdin,
		Stdout:            opts.Stdout,
		Tty:               opts.TTY,
		TerminalSizeQueue: opts.TerminalSizes,
	}
	if !opts.TTY {
		streamOpts.Stderr = opts.Stderr
	}

	err = executor.StreamWithContext(ctx, streamOpts)
	if err == nil {
		return 0, nil
	}

	var codeErr utilexec.CodeExitError
	if errors.As(err, &codeErr) {
		// The command started and exited with code != 0: not a transport/RBAC
		// failure, it is the normal outcome of the command that ran.
		return codeErr.Code, nil
	}
	return 0, fmt.Errorf("k8s: failed to run command in %s/%s: %w", opts.Namespace, opts.Pod, err)
}

// spdyExecutorFor builds the SPDY executor on top of the transport client-go
// derives from cfg, instead of remotecommand.NewSPDYExecutor. The difference
// matters for the agent connector: NewSPDYExecutor (and the WebSocket executor
// alike) dial the API server with their own net.Dialer and ignore
// rest.Config.Dial, so the upgrade request would try to resolve
// agent.bayplan.internal on the host instead of going through the tunnel.
// With UpgradeTransport the round tripper borrows the dialer and the TLS
// config of the transport, which is what every other call already uses, and
// HTTPWrappersForConfig adds the bearer token and the impersonation headers to
// the upgrade request as rest.Config would.
func spdyExecutorFor(cfg *rest.Config, method string, url *url.URL) (remotecommand.Executor, error) {
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	upgrader, err := httpstreamspdy.NewRoundTripperWithConfig(httpstreamspdy.RoundTripperConfig{
		UpgradeTransport: transport,
		PingPeriod:       5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	wrapper, err := rest.HTTPWrappersForConfig(cfg, upgrader)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewSPDYExecutorForTransports(wrapper, upgrader, method, url)
}

// restClientFor builds a *rest.RESTClient pointing at the core API group (v1),
// needed to call the pods/exec subresource directly (the Clientset exposes no
// high-level method for exec).
func restClientFor(cfg *rest.Config) (*rest.RESTClient, error) {
	cfgCopy := *cfg
	cfgCopy.GroupVersion = &corev1.SchemeGroupVersion
	cfgCopy.APIPath = "/api"
	cfgCopy.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	return rest.RESTClientFor(&cfgCopy)
}

// shortRequestTimeout bounds each request of the clients that only make short
// calls and cannot be given a context (discovery, the Helm SDK).
const shortRequestTimeout = 60 * time.Second

// boundedConfig returns a copy of cfg with a per-request Timeout
// (http.Client.Timeout). Only for clients that never stream: a watch, `logs
// -f` or exec through it would be cut when the timeout expires.
func boundedConfig(cfg *rest.Config) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.Timeout = shortRequestTimeout
	return out
}

// ImpersonatedRestConfig exposes to other packages (Helm, kubectl shell) the
// rest.Config of a cluster with the user's identity, the same one the panel's
// own actions use. It carries a per-request Timeout because the Helm SDK takes
// no context and makes only short calls; the kubectl shell only uses its
// transport (rest.TransportFor), which ignores Timeout, so `logs -f` and
// watches through it are not cut. Do not build a streaming client from it.
func (m *Manager) ImpersonatedRestConfig(id int64, actingUser, actingGroup string) (*rest.Config, error) {
	conn, err := m.getConn(id)
	if err != nil {
		return nil, err
	}
	return boundedConfig(conn.impersonatedRestConfig(actingUser, actingGroup)), nil
}

// BaseRestConfig returns the technical ServiceAccount's rest.Config (without
// impersonation), for READS that do not go through the informer cache (e.g.
// listing Helm releases, which live in Secrets). The panel's reads always go
// with the SA; the impersonated identity is reserved for writing.
func (m *Manager) BaseRestConfig(id int64) (*rest.Config, error) {
	conn, err := m.getConn(id)
	if err != nil {
		return nil, err
	}
	cfg := conn.restConfig
	// Same per-request bound as ImpersonatedRestConfig: Helm reads only.
	return boundedConfig(&cfg), nil
}
