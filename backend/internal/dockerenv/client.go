// Package dockerenv manages Docker/Podman environments connected through an
// agent: the agent runs on the host with the Docker socket mounted and exposes
// it through the tunnel; here we speak the Docker Engine API (HTTP+JSON) over
// that tunnel. Raw HTTP with our own types is used instead of the Docker SDK:
// the SDK drags in half the moby module for a dozen endpoints, and the types
// the UI needs are small.
//
// Podman exposes the same API on its compatible socket, so it works the same.
package dockerenv

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// apiVersion is the minimum Engine API version requested: 1.41 (Docker 20.10)
// is supported by everything in use, Podman 4+ included.
const apiVersion = "v1.41"

var (
	ErrNotFound = errors.New("docker: not found")
	ErrConflict = errors.New("docker: conflict")
)

// APIError is an error returned by the daemon, with its message.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %s (%d)", e.Message, e.Status) }

// DialFunc opens the connection to the daemon (through the agent's tunnel).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Client talks to a specific daemon.
type Client struct {
	http *http.Client
	dial DialFunc
}

func NewClient(dial DialFunc) *Client {
	return &Client{
		dial: dial,
		http: &http.Client{Transport: &http.Transport{
			DialContext:           dial,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}},
	}
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	resp, err := c.raw(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// drainClose reads what is left of the body (bounded) before closing it, so
// that the keep-alive connection through the tunnel can be reused instead of
// opening a new one for every request.
func drainClose(b io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(b, 256<<10))
	_ = b.Close()
}

// raw performs the request and translates the daemon's error codes. The
// caller closes the body.
func (c *Client) raw(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	u := "http://docker/" + apiVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: failed to talk to the daemon: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer drainClose(resp.Body)
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&msg)
		if msg.Message == "" {
			msg.Message = resp.Status
		}
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, fmt.Errorf("%w: %s", ErrNotFound, msg.Message)
		case http.StatusConflict:
			return nil, fmt.Errorf("%w: %s", ErrConflict, msg.Message)
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg.Message}
	}
	return resp, nil
}

// --- Types (subset of the Engine API, in camelCase for the UI) ---

type Info struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	ServerVersion     string `json:"serverVersion"`
	OperatingSystem   string `json:"operatingSystem"`
	OSType            string `json:"osType"`
	Architecture      string `json:"architecture"`
	KernelVersion     string `json:"kernelVersion"`
	NCPU              int    `json:"cpus"`
	MemTotal          int64  `json:"memTotalBytes"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"containersRunning"`
	ContainersPaused  int    `json:"containersPaused"`
	ContainersStopped int    `json:"containersStopped"`
	Images            int    `json:"images"`
	Driver            string `json:"storageDriver"`
	Swarm             bool   `json:"swarm"`
	Podman            bool   `json:"podman"`
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var raw struct {
		ID, Name, ServerVersion, OperatingSystem, OSType, Architecture, KernelVersion, Driver string
		NCPU                                                                                  int
		MemTotal                                                                              int64
		Containers, ContainersRunning, ContainersPaused, ContainersStopped, Images            int
		Swarm                                                                                 struct{ LocalNodeState string }
	}
	if err := c.do(ctx, http.MethodGet, "/info", nil, nil, &raw); err != nil {
		return Info{}, err
	}
	return Info{
		ID: raw.ID, Name: raw.Name, ServerVersion: raw.ServerVersion, OperatingSystem: raw.OperatingSystem, OSType: raw.OSType,
		Architecture: raw.Architecture, KernelVersion: raw.KernelVersion, NCPU: raw.NCPU, MemTotal: raw.MemTotal,
		Containers: raw.Containers, ContainersRunning: raw.ContainersRunning, ContainersPaused: raw.ContainersPaused,
		ContainersStopped: raw.ContainersStopped, Images: raw.Images, Driver: raw.Driver,
		Swarm:  raw.Swarm.LocalNodeState == "active",
		Podman: strings.Contains(strings.ToLower(raw.ServerVersion+raw.Name), "podman") || strings.Contains(strings.ToLower(raw.OperatingSystem), "podman"),
	}, nil
}

// Ping checks that the daemon responds. It returns the API version.
func (c *Client) Ping(ctx context.Context) (string, error) {
	resp, err := c.raw(ctx, http.MethodGet, "/_ping", nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return resp.Header.Get("Api-Version"), nil
}

type Port struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"privatePort"`
	PublicPort  int    `json:"publicPort,omitempty"`
	Type        string `json:"type"`
}

type Container struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Command string            `json:"command"`
	Created time.Time         `json:"created"`
	State   string            `json:"state"`  // running | exited | paused | created | restarting | dead
	Status  string            `json:"status"` // "Up 3 hours (healthy)"
	Ports   []Port            `json:"ports"`
	Labels  map[string]string `json:"labels"`
	// Stack is the compose project (label com.docker.compose.project), if any.
	Stack string `json:"stack,omitempty"`
}

func (c *Client) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	var raw []struct {
		ID      string `json:"Id"`
		Names   []string
		Image   string
		Command string
		Created int64
		State   string
		Status  string
		Ports   []struct {
			IP          string `json:"IP"`
			PrivatePort int
			PublicPort  int
			Type        string
		}
		Labels map[string]string
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		// Initialized slices: in JSON a nil comes out as null and the UI expects [].
		ct := Container{ID: r.ID, Image: r.Image, Command: r.Command, Created: time.Unix(r.Created, 0), State: r.State, Status: r.Status, Labels: r.Labels, Ports: []Port{}}
		if ct.Labels == nil {
			ct.Labels = map[string]string{}
		}
		if len(r.Names) > 0 {
			ct.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		for _, p := range r.Ports {
			ct.Ports = append(ct.Ports, Port{IP: p.IP, PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Type: p.Type})
		}
		ct.Stack = r.Labels["com.docker.compose.project"]
		out = append(out, ct)
	}
	return out, nil
}

// ContainerAction runs start|stop|restart|kill|pause|unpause.
func (c *Client) ContainerAction(ctx context.Context, id, action string) error {
	switch action {
	case "start", "stop", "restart", "kill", "pause", "unpause":
	default:
		return fmt.Errorf("docker: action %q not supported", action)
	}
	q := url.Values{}
	if action == "stop" || action == "restart" {
		q.Set("t", "10")
	}
	// 304 (already in that state) is below 400, so raw does not turn it into
	// an error: it is not one for the user.
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/"+action, q, nil, nil)
}

func (c *Client) RemoveContainer(ctx context.Context, id string, force, volumes bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	if volumes {
		q.Set("v", "1")
	}
	return c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil, nil)
}

// ContainerInspect returns the inspect JSON as is (the UI shows it as the
// container's "YAML/JSON", equivalent to Kubernetes' View YAML).
func (c *Client) ContainerInspect(ctx context.Context, id string) (json.RawMessage, bool, error) {
	resp, err := c.raw(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false, err
	}
	var cfg struct {
		Config struct{ Tty bool }
	}
	_ = json.Unmarshal(b, &cfg)
	return b, cfg.Config.Tty, nil
}

// Logs opens the log stream (stdout+stderr, follow). If the container has no
// TTY, Docker multiplexes stdout/stderr with 8-byte headers: it is
// demultiplexed here to deliver plain text to the WebSocket.
func (c *Client) Logs(ctx context.Context, id string, tail int, tty bool) (io.ReadCloser, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "follow": {"1"}, "timestamps": {"0"}}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	}
	resp, err := c.raw(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil)
	if err != nil {
		return nil, err
	}
	if tty {
		return resp.Body, nil
	}
	pr, pw := io.Pipe()
	go func() {
		defer resp.Body.Close()
		pw.CloseWithError(demux(resp.Body, pw))
	}()
	return pr, nil
}

// demux unwraps Docker's multiplexed format (1-byte stream type, 3 bytes of
// padding, 4-byte big-endian length, payload).
func demux(r io.Reader, w io.Writer) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(w, r, int64(n)); err != nil {
			return err
		}
	}
}

type Image struct {
	ID       string    `json:"id"`
	Tags     []string  `json:"tags"`
	Size     int64     `json:"sizeBytes"`
	Created  time.Time `json:"created"`
	InUse    bool      `json:"inUse"`
	Dangling bool      `json:"dangling"`
}

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	var raw []struct {
		ID         string `json:"Id"`
		RepoTags   []string
		Size       int64
		Created    int64
		Containers int64
	}
	if err := c.do(ctx, http.MethodGet, "/images/json", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(raw))
	for _, r := range raw {
		tags := make([]string, 0, len(r.RepoTags))
		for _, t := range r.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		out = append(out, Image{ID: strings.TrimPrefix(r.ID, "sha256:"), Tags: tags, Size: r.Size, Created: time.Unix(r.Created, 0), InUse: r.Containers > 0, Dangling: len(tags) == 0})
	}
	return out, nil
}

// PullImage downloads an image and waits for it to finish (the daemon emits
// line-delimited JSON progress; it is consumed to the end and the last error,
// if any, is returned).
func (c *Client) PullImage(ctx context.Context, ref string) error {
	q := url.Values{"fromImage": {ref}}
	resp, err := c.raw(ctx, http.MethodPost, "/images/create", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("docker: pull of %s: %s", ref, msg.Error)
		}
	}
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	return c.do(ctx, http.MethodDelete, "/images/"+url.PathEscape(id), q, nil, nil)
}

type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Created    string            `json:"created,omitempty"`
	Labels     map[string]string `json:"labels"`
	Stack      string            `json:"stack,omitempty"`
}

func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	var raw struct {
		Volumes []struct {
			Name, Driver, Mountpoint, CreatedAt string
			Labels                              map[string]string
		}
	}
	if err := c.do(ctx, http.MethodGet, "/volumes", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(raw.Volumes))
	for _, v := range raw.Volumes {
		out = append(out, Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Created: v.CreatedAt, Labels: v.Labels, Stack: v.Labels["com.docker.compose.project"]})
	}
	return out, nil
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil, nil)
}

type Network struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Driver   string            `json:"driver"`
	Scope    string            `json:"scope"`
	Internal bool              `json:"internal"`
	Subnets  []string          `json:"subnets"`
	Labels   map[string]string `json:"labels"`
	Stack    string            `json:"stack,omitempty"`
}

func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	var raw []struct {
		ID           string `json:"Id"`
		Name, Driver string
		Scope        string
		Internal     bool
		IPAM         struct{ Config []struct{ Subnet string } }
		Labels       map[string]string
	}
	if err := c.do(ctx, http.MethodGet, "/networks", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(raw))
	for _, n := range raw {
		nw := Network{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal, Labels: n.Labels, Stack: n.Labels["com.docker.compose.project"], Subnets: []string{}}
		if nw.Labels == nil {
			nw.Labels = map[string]string{}
		}
		for _, cfg := range n.IPAM.Config {
			if cfg.Subnet != "" {
				nw.Subnets = append(nw.Subnets, cfg.Subnet)
			}
		}
		out = append(out, nw)
	}
	return out, nil
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(id), nil, nil, nil)
}

// --- Interactive exec ---

// ExecSession is an exec session with TTY over a "hijacked" connection: after
// POST /exec/{id}/start the daemon stops speaking HTTP and the connection
// becomes the process's stdin/stdout. That is why http.Client will not do: the
// connection is opened by hand through the tunnel, the request is written and
// the response is read.
type ExecSession struct {
	ID   string
	conn net.Conn
	rw   *bufio.ReadWriter
	c    *Client
}

// Exec starts `cmd` with a TTY in the container and returns the session.
func (c *Client) Exec(ctx context.Context, containerID string, cmd []string) (*ExecSession, error) {
	var created struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(containerID)+"/exec", nil, map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true, "Cmd": cmd,
	}, &created); err != nil {
		return nil, err
	}
	conn, err := c.dial(ctx, "tcp", "docker")
	if err != nil {
		return nil, fmt.Errorf("docker: failed to open connection for exec: %w", err)
	}
	body, _ := json.Marshal(map[string]any{"Detach": false, "Tty": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/"+apiVersion+"/exec/"+created.ID+"/start", strings.NewReader(string(body)))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("docker: failed to send exec/start: %w", err)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	resp, err := http.ReadResponse(rw.Reader, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("docker: failed to read exec/start response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		defer conn.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	return &ExecSession{ID: created.ID, conn: conn, rw: rw, c: c}, nil
}

// Read reads the process output (with a TTY there is no multiplexing).
func (s *ExecSession) Read(p []byte) (int, error) { return s.rw.Read(p) }

// Write sends bytes to the process stdin.
func (s *ExecSession) Write(p []byte) (int, error) {
	n, err := s.rw.Write(p)
	if err != nil {
		return n, err
	}
	return n, s.rw.Flush()
}

func (s *ExecSession) Close() error { return s.conn.Close() }

// Resize changes the TTY size (a normal HTTP request, separate from the stream).
func (s *ExecSession) Resize(ctx context.Context, cols, rows uint16) error {
	q := url.Values{"w": {strconv.Itoa(int(cols))}, "h": {strconv.Itoa(int(rows))}}
	return s.c.do(ctx, http.MethodPost, "/exec/"+s.ID+"/resize", q, nil, nil)
}

// ExitCode queries the exit code after the stream closes.
func (s *ExecSession) ExitCode(ctx context.Context) int {
	var ins struct {
		ExitCode int
	}
	if err := s.c.do(ctx, http.MethodGet, "/exec/"+s.ID+"/json", nil, nil, &ins); err != nil {
		return -1
	}
	return ins.ExitCode
}
