// Package gitops syncs manifests from a git repository to a cluster: it clones
// (depth 1) the branch, reads the YAML files under the path and applies them
// with server-side apply through k8s.Manager.Apply, with an identity of its own
// per source (bayplan:gitops:<name>) and the editors group. It is "lightweight"
// GitOps: periodic or by webhook, without prune or
// health.
package gitops

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bayplan/bayplan/internal/netguard"
	"github.com/bayplan/bayplan/internal/privdir"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"

	"github.com/bayplan/bayplan/internal/crypto"
	"github.com/bayplan/bayplan/internal/k8s"
)

var ErrSourceNotFound = errors.New("gitops: source not found")

// maxManifestBytes bounds the total YAML read from a repo in one sync. A huge
// repo must not turn into hundreds of MB in memory.
const maxManifestBytes = 8 << 20

// cloneTimeout bounds the clone: an unreachable repo cannot leave the
// reconciler goroutine hanging.
const cloneTimeout = 2 * time.Minute

// Source is a GitOps source as stored.
type Source struct {
	ID        int64  `json:"id"`
	ClusterID int64  `json:"clusterId"`
	Name      string `json:"name"`
	RepoURL   string `json:"repoUrl"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	Namespace string `json:"namespace,omitempty"`
	Username  string `json:"username,omitempty"`
	HasToken  bool   `json:"hasToken"`
	// AuthType: https (token) or ssh (private key in token_encrypted).
	AuthType        string          `json:"authType"`
	HasHostKey      bool            `json:"hasHostKey"`
	Prune           bool            `json:"prune"`
	Kustomize       bool            `json:"kustomize"`
	LastPruned      json.RawMessage `json:"lastPruned,omitempty"`
	IntervalSeconds int             `json:"intervalSeconds"`
	Enabled         bool            `json:"enabled"`
	LastSyncAt      *time.Time      `json:"lastSyncAt,omitempty"`
	LastCommit      string          `json:"lastCommit,omitempty"`
	LastStatus      string          `json:"lastStatus"`
	LastError       string          `json:"lastError,omitempty"`
	LastApplied     json.RawMessage `json:"lastApplied,omitempty"`
	CreatedBy       string          `json:"createdBy,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`

	tokenEncrypted []byte
	keyID          string
	sshKnownHosts  string
}

// Reconciler keeps one goroutine per enabled source and runs the syncs
// (periodic, by webhook or manual).
type Reconciler struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
	k8s     *k8s.Manager
	logger  *slog.Logger
	workDir string

	// base is the server's lifetime (set by Start): the periodic loops stop
	// and an in-flight scheduled sync is cancelled on shutdown.
	base context.Context

	mu      sync.Mutex
	loops   map[int64]chan struct{} // stop channel per source
	running map[int64]bool          // sync in progress per source
}

func NewReconciler(pool *pgxpool.Pool, keyring *crypto.Keyring, mgr *k8s.Manager, logger *slog.Logger) (*Reconciler, error) {
	installGuardedTransports()
	dir := filepath.Join(os.TempDir(), "bayplan-gitops")
	if err := privdir.Ensure(dir); err != nil {
		return nil, fmt.Errorf("gitops: %w", err)
	}
	return &Reconciler{pool: pool, keyring: keyring, k8s: mgr, logger: logger, workDir: dir, base: context.Background(), loops: map[int64]chan struct{}{}, running: map[int64]bool{}}, nil
}

const sourceColumns = `id, cluster_id, name, repo_url, branch, path, namespace, username, token_encrypted, key_id, auth_type, ssh_known_hosts, prune, kustomize, last_pruned,
	interval_seconds, enabled, last_sync_at, last_commit, last_status, last_error, last_applied, created_by, created_at`

func scanSource(row pgx.Row) (Source, error) {
	var s Source
	var ns, user, keyID, lastCommit, lastErr, createdBy, knownHosts *string
	var applied, pruned []byte
	if err := row.Scan(&s.ID, &s.ClusterID, &s.Name, &s.RepoURL, &s.Branch, &s.Path, &ns, &user, &s.tokenEncrypted, &keyID,
		&s.AuthType, &knownHosts, &s.Prune, &s.Kustomize, &pruned,
		&s.IntervalSeconds, &s.Enabled, &s.LastSyncAt, &lastCommit, &s.LastStatus, &lastErr, &applied, &createdBy, &s.CreatedAt); err != nil {
		return Source{}, err
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	s.Namespace, s.Username, s.keyID, s.LastCommit, s.LastError, s.CreatedBy = deref(ns), deref(user), deref(keyID), deref(lastCommit), deref(lastErr), deref(createdBy)
	s.HasToken = len(s.tokenEncrypted) > 0
	s.sshKnownHosts = deref(knownHosts)
	s.HasHostKey = strings.TrimSpace(s.sshKnownHosts) != ""
	if len(applied) > 0 {
		s.LastApplied = json.RawMessage(applied)
	}
	if len(pruned) > 0 {
		s.LastPruned = json.RawMessage(pruned)
	}
	return s, nil
}

func (r *Reconciler) List(ctx context.Context, clusterID int64) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sourceColumns+` FROM gitops_sources WHERE cluster_id = $1 ORDER BY name`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("gitops: failed to list sources: %w", err)
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("gitops: failed to read source: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Reconciler) Get(ctx context.Context, id int64) (Source, error) {
	s, err := scanSource(r.pool.QueryRow(ctx, `SELECT `+sourceColumns+` FROM gitops_sources WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrSourceNotFound
	}
	if err != nil {
		return Source{}, fmt.Errorf("gitops: failed to read source %d: %w", id, err)
	}
	return s, nil
}

// CreateInput holds the fields of a new source.
type CreateInput struct {
	ClusterID       int64
	Name            string
	RepoURL         string
	Branch          string
	Path            string
	Namespace       string
	Username        string
	Token           string // HTTPS token or SSH private key (PEM) depending on AuthType
	IntervalSeconds int
	CreatedBy       string
	AuthType        string
	SSHKnownHosts   string
	Prune           bool
	Kustomize       bool
}

// Create stores the source and also returns the webhook token IN THE CLEAR,
// the only time it can be seen (only its hash is stored).
func (r *Reconciler) Create(ctx context.Context, in CreateInput) (Source, string, error) {
	var enc []byte
	var keyID *string
	if in.Token != "" {
		k, ct, err := r.keyring.Encrypt([]byte(in.Token))
		if err != nil {
			return Source{}, "", err
		}
		enc = ct
		s := string(k)
		keyID = &s
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Source{}, "", err
	}
	webhookToken := base64.RawURLEncoding.EncodeToString(raw)
	// Encrypted in addition to the hash: HMAC verification (GitHub/Bitbucket)
	// needs the cleartext token as the secret.
	whKeyID, whEnc, err := r.keyring.Encrypt([]byte(webhookToken))
	if err != nil {
		return Source{}, "", err
	}
	var id int64
	err = r.pool.QueryRow(ctx,
		`INSERT INTO gitops_sources (cluster_id, name, repo_url, branch, path, namespace, username, token_encrypted, key_id, interval_seconds, webhook_token_hash, webhook_token_encrypted, webhook_key_id, created_by,
		   auth_type, ssh_known_hosts, prune, kustomize)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''), $17, $18) RETURNING id`,
		in.ClusterID, in.Name, in.RepoURL, in.Branch, in.Path, in.Namespace, in.Username, enc, keyID, in.IntervalSeconds, hashToken(webhookToken), whEnc, string(whKeyID), in.CreatedBy,
		in.AuthType, strings.TrimSpace(in.SSHKnownHosts), in.Prune, in.Kustomize,
	).Scan(&id)
	if err != nil {
		return Source{}, "", err
	}
	s, err := r.Get(ctx, id)
	if err != nil {
		return Source{}, "", err
	}
	r.startLoop(s)
	return s, webhookToken, nil
}

// SetEnabled pauses or resumes a source.
func (r *Reconciler) SetEnabled(ctx context.Context, id int64, enabled bool) (Source, error) {
	if _, err := r.pool.Exec(ctx, `UPDATE gitops_sources SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled); err != nil {
		return Source{}, fmt.Errorf("gitops: failed to update source: %w", err)
	}
	s, err := r.Get(ctx, id)
	if err != nil {
		return Source{}, err
	}
	if enabled {
		r.startLoop(s)
	} else {
		r.stopLoop(id)
	}
	return s, nil
}

func (r *Reconciler) Delete(ctx context.Context, id int64) (Source, error) {
	s, err := r.Get(ctx, id)
	if err != nil {
		return Source{}, err
	}
	r.stopLoop(id)
	if _, err := r.pool.Exec(ctx, `DELETE FROM gitops_sources WHERE id = $1`, id); err != nil {
		return Source{}, fmt.Errorf("gitops: failed to delete source: %w", err)
	}
	return s, nil
}

// VerifyWebhook authenticates a webhook call in two ways, depending on what
// the git provider allows:
//   - token in a header (X-Webhook-Token, or X-Gitlab-Token on GitLab): its
//     hash is compared with the stored one;
//   - HMAC-SHA256 signature of the body (X-Hub-Signature-256 on GitHub, also
//     Gitea/Bitbucket), using the token as the secret: it is recomputed with
//     the decrypted token.
//
// Never via query string: it would end up in the reverse proxy's access logs.
func (r *Reconciler) VerifyWebhook(ctx context.Context, id int64, headerToken, signature string, body []byte) (bool, error) {
	var hash string
	var enc []byte
	var keyID *string
	err := r.pool.QueryRow(ctx, `SELECT webhook_token_hash, webhook_token_encrypted, webhook_key_id FROM gitops_sources WHERE id = $1`, id).Scan(&hash, &enc, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrSourceNotFound
	}
	if err != nil {
		return false, err
	}
	if headerToken != "" {
		return subtle.ConstantTimeCompare([]byte(hashToken(headerToken)), []byte(hash)) == 1, nil
	}
	if signature == "" || len(enc) == 0 || keyID == nil {
		return false, nil
	}
	secret, err := r.keyring.Decrypt(crypto.KeyID(*keyID), enc)
	if err != nil {
		return false, err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(signature)), []byte(want)), nil
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// Start launches the loops of every enabled source (when the binary starts).
func (r *Reconciler) Start(ctx context.Context) error {
	r.mu.Lock()
	r.base = ctx
	r.mu.Unlock()
	rows, err := r.pool.Query(ctx, `SELECT `+sourceColumns+` FROM gitops_sources WHERE enabled = TRUE`)
	if err != nil {
		return fmt.Errorf("gitops: failed to list sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return err
		}
		r.startLoop(s)
	}
	return rows.Err()
}

func (r *Reconciler) startLoop(s Source) {
	r.mu.Lock()
	if old, ok := r.loops[s.ID]; ok {
		close(old)
	}
	stop := make(chan struct{})
	r.loops[s.ID] = stop
	base := r.base
	r.mu.Unlock()

	go func() {
		// First sync shortly after startup (not immediately: the cluster's
		// informer may still be syncing) and then every interval.
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-base.Done():
				return
			case <-timer.C:
				r.Sync(base, s.ID, "schedule")
				timer.Reset(time.Duration(s.IntervalSeconds) * time.Second)
			}
		}
	}()
}

func (r *Reconciler) stopLoop(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.loops[id]; ok {
		close(stop)
		delete(r.loops, id)
	}
}

// SyncResult is what a manual sync returns.
type SyncResult struct {
	Commit  string            `json:"commit"`
	Applied []k8s.ApplyResult `json:"applied"`
	Pruned  []k8s.ObjectRef   `json:"pruned,omitempty"`
	Skipped bool              `json:"skipped"` // a sync was already in progress
}

// Sync clones, applies and persists the result. trigger is informational
// (schedule | webhook | manual). Only one sync per source at a time.
func (r *Reconciler) Sync(ctx context.Context, id int64, trigger string) (SyncResult, error) {
	r.mu.Lock()
	if r.running[id] {
		r.mu.Unlock()
		return SyncResult{Skipped: true}, nil
	}
	r.running[id] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, id)
		r.mu.Unlock()
	}()

	s, err := r.Get(ctx, id)
	if err != nil {
		return SyncResult{}, err
	}
	res, err := r.syncOnce(ctx, s)
	status, errText := "synced", ""
	if err != nil {
		status, errText = "error", err.Error()
		r.logger.Warn("gitops: sync failed", "source", s.Name, "trigger", trigger, "error", err)
	} else {
		r.logger.Info("gitops: sync ok", "source", s.Name, "trigger", trigger, "commit", res.Commit, "objects", len(res.Applied))
	}
	applied, _ := json.Marshal(res.Applied)
	pruned, _ := json.Marshal(res.Pruned)
	if _, uerr := r.pool.Exec(ctx,
		`UPDATE gitops_sources SET last_sync_at = now(), last_commit = NULLIF($2, ''), last_status = $3, last_error = NULLIF($4, ''), last_applied = $5, last_pruned = $6 WHERE id = $1`,
		id, res.Commit, status, errText, applied, pruned,
	); uerr != nil {
		r.logger.Error("gitops: failed to save state", "error", uerr)
	}
	return res, err
}

func (r *Reconciler) syncOnce(ctx context.Context, s Source) (SyncResult, error) {
	dir, err := os.MkdirTemp(r.workDir, fmt.Sprintf("src-%d-", s.ID))
	if err != nil {
		return SyncResult{}, fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)

	cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	opts := &git.CloneOptions{URL: s.RepoURL, Depth: 1, SingleBranch: true, ReferenceName: plumbing.NewBranchReferenceName(s.Branch), Tags: git.NoTags}
	if s.HasToken {
		tok, err := r.keyring.Decrypt(crypto.KeyID(s.keyID), s.tokenEncrypted)
		if err != nil {
			return SyncResult{}, fmt.Errorf("failed to decrypt credential: %w", err)
		}
		if s.AuthType == AuthSSH {
			opts.Auth, err = sshAuth(s.Username, string(tok), s.sshKnownHosts, dir)
			if err != nil {
				return SyncResult{}, err
			}
		} else {
			user := s.Username
			if user == "" {
				user = "git" // GitHub/GitLab accept any username with a token
			}
			opts.Auth = &githttp.BasicAuth{Username: user, Password: string(tok)}
		}
	} else if s.AuthType == AuthSSH {
		return SyncResult{}, errors.New("an SSH source needs a private key")
	}
	repo, err := git.PlainCloneContext(cctx, dir, false, opts)
	if err != nil {
		return SyncResult{}, fmt.Errorf("failed to clone %s (%s): %w", s.RepoURL, s.Branch, err)
	}
	head, err := repo.Head()
	if err != nil {
		return SyncResult{}, fmt.Errorf("failed to read HEAD: %w", err)
	}
	commit := head.Hash().String()

	var objs []*unstructured.Unstructured
	if s.Kustomize {
		objs, err = buildKustomize(dir, s.Path)
	} else {
		objs, err = loadManifests(dir, s.Path)
	}
	if err != nil {
		return SyncResult{Commit: commit}, err
	}
	if len(objs) == 0 {
		return SyncResult{Commit: commit}, fmt.Errorf("no YAML manifests in %q", s.Path)
	}
	labelObjects(objs, s.ID)
	actor := "gitops:" + s.Name
	applied, err := r.k8s.Apply(ctx, s.ClusterID, objs, s.Namespace, actor, k8s.GroupEditors, false)
	if err != nil {
		return SyncResult{Commit: commit, Applied: applied}, err
	}
	res := SyncResult{Commit: commit, Applied: applied}
	if !s.Prune {
		return res, nil
	}
	// Prune only after a COMPLETE successful apply: if the apply failed halfway,
	// what was not applied must not be deleted for not being in the list.
	existing, err := r.k8s.ListByLabel(ctx, s.ClusterID, SourceLabel+"="+strconv.FormatInt(s.ID, 10))
	if err != nil {
		return res, fmt.Errorf("prune: %w", err)
	}
	for _, ref := range PruneCandidates(existing, applied) {
		if err := r.k8s.DeleteResource(ctx, s.ClusterID, ref.KindKey, ref.Namespace, ref.Name, actor, k8s.GroupAdmins); err != nil && !errors.Is(err, k8s.ErrResourceNotFound) {
			return res, fmt.Errorf("prune %s: %w", ref, err)
		}
		res.Pruned = append(res.Pruned, ref)
	}
	return res, nil
}

var errLinkLocal = errors.New("the repository URL points to a link-local address (cloud metadata); not allowed")

// RejectLinkLocal rejects URLs (http, ssh or scp-like) whose host is a
// link-local IP. The check at connect time is done by guardedDialer (a URL
// with a DNS name may resolve to 169.254.169.254).
func RejectLinkLocal(raw string) error {
	host := raw
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.Index(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if strings.HasPrefix(host, "[") {
		if i := strings.Index(host, "]"); i >= 0 {
			host = host[1:i]
		}
	} else if i := strings.IndexAny(host, "/?#:"); i >= 0 {
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return errLinkLocal
	}
	return nil
}

// guardedDialer blocks the link-local range at connect time, like the notify
// webhooks and the registries validation do: DNS may point to the cloud
// metadata even if the URL does not look like it.
func guardedDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 30 * time.Second,
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && netguard.IsMetadata(ip) {
				return errLinkLocal
			}
			return nil
		},
	}
}

// installGuardedTransports registers http/https clients with the guarded
// dialer in go-git. go-git resolves the transport by scheme in a global
// registry, so it is done once when building the Reconciler.
func installGuardedTransports() {
	tr := &http.Transport{DialContext: guardedDialer().DialContext, Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 15 * time.Second}
	c := githttp.NewClient(&http.Client{Transport: tr})
	gitclient.InstallProtocol("http", c)
	gitclient.InstallProtocol("https", c)
}

const (
	AuthHTTPS = "https"
	AuthSSH   = "ssh"
	// SourceLabel marks every applied object with the source that manages it:
	// it is what allows prune without keeping an inventory in our DB (the
	// cluster is the source of truth for what exists).
	SourceLabel = "bayplan.io/gitops-source"
)

// labelObjects adds the source label to every object (not to pod templates:
// a Deployment carries the label, its pods do not).
func labelObjects(objs []*unstructured.Unstructured, sourceID int64) {
	for _, o := range objs {
		labels := o.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[SourceLabel] = strconv.FormatInt(sourceID, 10)
		o.SetLabels(labels)
	}
}

// PruneCandidates returns what carries the label in the cluster but was not
// just applied. Pure, for the test. It compares by kind/namespace/name; an
// object of a kind the panel does not know is not returned by ListByLabel and
// therefore is never deleted (nor could it be).
func PruneCandidates(existing []k8s.ObjectRef, applied []k8s.ApplyResult) []k8s.ObjectRef {
	keep := map[string]bool{}
	for _, a := range applied {
		keep[a.Kind+"/"+a.Namespace+"/"+a.Name] = true
	}
	var out []k8s.ObjectRef
	for _, e := range existing {
		if !keep[e.Kind+"/"+e.Namespace+"/"+e.Name] {
			out = append(out, e)
		}
	}
	return out
}

// sshAuth builds go-git's SSH authentication: PEM private key (no passphrase)
// and host verification with the stored known_hosts line, which is MANDATORY:
// without it a MITM of the git server would serve manifests that would be
// applied to the cluster with the GitOps identity. The convenience of not
// pasting GitHub's key is not worth that.
func sshAuth(user, pem, knownHosts, dir string) (transport.AuthMethod, error) {
	if user == "" {
		user = "git"
	}
	auth, err := gitssh.NewPublicKeys(user, []byte(pem), "")
	if err != nil {
		return nil, fmt.Errorf("invalid SSH key: %w", err)
	}
	if strings.TrimSpace(knownHosts) == "" {
		return nil, errors.New("an SSH source needs the git server's known_hosts line (ssh-keyscan <host>)")
	}
	// go-git can only read known_hosts from a file.
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(knownHosts)+"\n"), 0o600); err != nil {
		return nil, err
	}
	cb, err := gitssh.NewKnownHostsCallback(path)
	if err != nil {
		return nil, fmt.Errorf("invalid known_hosts: %w", err)
	}
	// The host key callback sees the remote address already resolved: it is
	// the place to apply the link-local block on SSH too.
	auth.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if tcp, ok := remote.(*net.TCPAddr); ok && netguard.IsMetadata(tcp.IP) {
			return errLinkLocal
		}
		return cb(hostname, remote, key)
	}
	return auth, nil
}

// buildKustomize runs Kustomize on path (which must contain
// kustomization.yaml) and returns the resulting objects. With the default load
// restrictions (nothing outside the directory) and no plugins: anything that
// is not pure YAML does not run on the server.
func buildKustomize(root, sub string) ([]*unstructured.Unstructured, error) {
	base, err := resolveInside(root, sub)
	if err != nil {
		return nil, err
	}
	// Kustomize reads whatever the repo files tell it to (configMapGenerator
	// files, patches…): a symlink pointing outside the clone would be an
	// arbitrary read of the server. Any symlink is rejected before building.
	if err := rejectSymlinks(base); err != nil {
		return nil, err
	}
	if err := rejectRemoteKustomize(base, base, 0); err != nil {
		return nil, err
	}
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsRootOnly
	res, err := krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), base)
	if err != nil {
		return nil, fmt.Errorf("kustomize build %q: %w", sub, err)
	}
	out, err := res.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("kustomize build %q: %w", sub, err)
	}
	if len(out) > maxManifestBytes {
		return nil, fmt.Errorf("%w: too many manifests (more than %d MiB)", k8s.ErrInvalidManifest, maxManifestBytes>>20)
	}
	return k8s.ParseManifests(string(out))
}

// loadManifests reads every *.yaml / *.yml under path (recursive, stable
// order by name) and parses them with the same function as the YAML editor.
// Hidden files and directories such as .git are skipped.
func loadManifests(root, sub string) ([]*unstructured.Unstructured, error) {
	base, err := resolveInside(root, sub)
	if err != nil {
		return nil, err
	}
	var files []string
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A symlink in the repo may point to /proc/self/environ or to a file
		// on the server: it is neither followed nor read. WalkDir does not
		// descend into linked directories, but it does list them, and a
		// linked file would be read as-is.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != base {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if (ext == ".yaml" || ext == ".yml") && !strings.HasPrefix(d.Name(), ".") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read %q: %w", sub, err)
	}
	sort.Strings(files)
	var total int64
	var out []*unstructured.Unstructured
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > maxManifestBytes {
			return nil, fmt.Errorf("%w: too many manifests (more than %d MiB)", k8s.ErrInvalidManifest, maxManifestBytes>>20)
		}
		// A YAML file that is not a manifest (values.yaml, CI config) is
		// ignored instead of aborting the whole sync.
		objs, err := k8s.ParseManifests(string(data))
		if err != nil {
			continue
		}
		out = append(out, objs...)
	}
	return out, nil
}

// rejectRemoteKustomize walks the kustomizations from dir and rejects every
// resources/bases/components entry that is not a local file or directory:
// Kustomize treats any nonexistent path as a git URL and would clone it from
// the server (SSRF and third-party code in the build). Local references are
// followed recursively.
func rejectRemoteKustomize(root, dir string, depth int) error {
	if depth > 20 {
		return fmt.Errorf("%w: too many nested kustomizations", k8s.ErrInvalidManifest)
	}
	var data []byte
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			data = b
			break
		}
	}
	if data == nil {
		return nil // no kustomization: krusty will give its own error
	}
	// First a generic sweep of ALL the scalars in the file: not only
	// resources/bases/components can point outside (helmCharts.repo,
	// openapi.path, future fields). Any string that looks like a remote URL
	// invalidates the source, wherever it is.
	var tree any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return fmt.Errorf("%w: kustomization in %s: %v", k8s.ErrInvalidManifest, dir, err)
	}
	if ref, ok := findRemoteRef(tree); ok {
		return fmt.Errorf("%w: kustomize does not allow remote references (%s)", k8s.ErrInvalidManifest, ref)
	}
	var k struct {
		Resources  []string `yaml:"resources"`
		Bases      []string `yaml:"bases"`
		Components []string `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &k); err != nil {
		return fmt.Errorf("%w: kustomization in %s: %v", k8s.ErrInvalidManifest, dir, err)
	}
	for _, ref := range append(append(k.Resources, k.Bases...), k.Components...) {
		p := filepath.Clean(filepath.Join(dir, ref))
		if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
			return fmt.Errorf("%w: %s escapes the kustomize directory", k8s.ErrInvalidManifest, ref)
		}
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("%w: kustomize does not allow remote resources or nonexistent paths (%s)", k8s.ErrInvalidManifest, ref)
		}
		if st.IsDir() {
			if err := rejectRemoteKustomize(root, p, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// findRemoteRef searches a decoded YAML tree for any scalar that looks like
// a remote reference (URL with scheme, scp-like, kustomize's ?ref=, or the
// github.com/org/repo shortcut kustomize understands without a scheme). It
// returns the first one found.
func findRemoteRef(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		if looksRemote(t) {
			return t, true
		}
	case map[string]any:
		for _, x := range t {
			if r, ok := findRemoteRef(x); ok {
				return r, true
			}
		}
	case []any:
		for _, x := range t {
			if r, ok := findRemoteRef(x); ok {
				return r, true
			}
		}
	}
	return "", false
}

// Hosts kustomize accepts without a scheme (git.NewRepoSpecFromURL). A generic
// domain is no good as a criterion: label keys (app.kubernetes.io/name) have
// the same shape. Any other nonexistent path is rejected anyway by the
// existence check.
var remoteHostRe = regexp.MustCompile(`^(?:github\.com|gitlab\.com|bitbucket\.org|dev\.azure\.com|ssh\.dev\.azure\.com|visualstudio\.com)/`)

func looksRemote(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "://") || strings.HasPrefix(s, "git@") || strings.Contains(s, "?ref=") ||
		strings.HasPrefix(s, "git::") || strings.HasPrefix(s, "hg::") || remoteHostRe.MatchString(s)
}

// resolveInside returns the real path of root/sub checking, with symlinks
// already resolved, that it is still inside the clone. The prefix comparison
// includes the separator: "/tmp/src-1" must not accept "/tmp/src-10/x".
func resolveInside(root, sub string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	base, err := filepath.EvalSymlinks(filepath.Join(realRoot, sub))
	if err != nil {
		return "", fmt.Errorf("%w: path %q does not exist in the repository", k8s.ErrInvalidManifest, sub)
	}
	if base != realRoot && !strings.HasPrefix(base, realRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path %q escapes the repository", k8s.ErrInvalidManifest, sub)
	}
	return base, nil
}

// rejectSymlinks fails if there is any symbolic link under dir.
func rejectSymlinks(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			rel, _ := filepath.Rel(dir, p)
			return fmt.Errorf("%w: the repository contains a symbolic link (%s); they are not allowed with Kustomize", k8s.ErrInvalidManifest, rel)
		}
		return nil
	})
}
