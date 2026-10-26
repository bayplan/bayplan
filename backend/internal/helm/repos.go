// Package helm wraps the Helm 3 SDK to install, upgrade, roll back and
// uninstall releases on the managed clusters, always with the user's
// impersonated identity (same pattern as internal/k8s/write.go), and to browse
// chart catalogs (HTTP repos with index.yaml and OCI registries).
//
// Helm stores each release's state as Secrets in the release namespace
// ("secrets" driver): there is nothing to persist in our DB except the
// repositories. That also means a release installed with `helm` from a
// terminal shows up here just the same, and vice versa.
package helm

import (
	"context"
	"errors"
	"fmt"
	"github.com/bayplan/bayplan/internal/privdir"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"

	"github.com/bayplan/bayplan/internal/crypto"
)

// ErrRepoNotFound is returned when the requested repository does not exist.
var ErrRepoNotFound = errors.New("helm: repository not found")

// indexTTL is how long a repo's downloaded index.yaml is reused before
// requesting it again. Large indexes (Bitnami) weigh megabytes; refreshing on
// every search would be slow and impolite to the repo.
const indexTTL = 10 * time.Minute

// Repo is a chart repository as stored (encrypted password).
type Repo struct {
	ID                int64
	Name              string
	URL               string
	Username          string
	PasswordEncrypted []byte
	KeyID             string
	InsecureSkipTLS   bool
	CreatedAt         time.Time
}

// IsOCI reports whether the repo is an OCI registry (no index.yaml: charts
// are named by reference and versions are tags).
func (r Repo) IsOCI() bool { return strings.HasPrefix(r.URL, "oci://") }

// ChartEntry is a catalog entry as the UI sees it.
type ChartEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Version     string   `json:"version"`
	AppVersion  string   `json:"appVersion,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Home        string   `json:"home,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	// Versions lists ALL available versions, newest to oldest, for the
	// install selector.
	Versions []string `json:"versions"`
}

// Repos manages the helm_repos table and the index cache.
type Repos struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
	// cacheDir is where the Helm SDK downloads indexes and charts. Under the
	// temp directory: the container runs with a read-only filesystem except
	// /tmp.
	cacheDir string

	mu      sync.Mutex
	indexes map[int64]*cachedIndex

	pruneMu    sync.Mutex
	lastPruned time.Time
}

// chartCacheMaxAge / pruneEvery bound the chart cache: every chart version
// ever browsed or installed lands there as a .tgz and nothing removed them,
// so a long-lived server grew /tmp (often a tmpfs, i.e. RAM) without limit.
const (
	chartCacheMaxAge = 7 * 24 * time.Hour
	pruneEvery       = 6 * time.Hour
)

// maybePruneCache removes downloaded chart archives older than
// chartCacheMaxAge, at most once per pruneEvery. Indexes are left alone (they
// are refreshed in place and there is one per repo); a chart deleted here is
// simply downloaded again the next time.
func (r *Repos) maybePruneCache(now time.Time) {
	r.pruneMu.Lock()
	if now.Sub(r.lastPruned) < pruneEvery {
		r.pruneMu.Unlock()
		return
	}
	r.lastPruned = now
	r.pruneMu.Unlock()
	pruneOld(filepath.Join(r.cacheDir, "repository"), now.Add(-chartCacheMaxAge))
}

// pruneOld deletes the *.tgz/*.prov files directly under dir modified before
// cutoff. Errors are ignored: it is housekeeping, never a reason to fail.
func pruneOld(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".tgz") && !strings.HasSuffix(name, ".prov") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

type cachedIndex struct {
	index     *repo.IndexFile
	fetchedAt time.Time
}

func NewRepos(pool *pgxpool.Pool, keyring *crypto.Keyring) (*Repos, error) {
	dir := filepath.Join(os.TempDir(), "bayplan-helm")
	if err := privdir.Ensure(dir); err != nil {
		return nil, fmt.Errorf("helm: %w", err)
	}
	r := &Repos{pool: pool, keyring: keyring, cacheDir: dir, indexes: map[int64]*cachedIndex{}}
	r.maybePruneCache(time.Now())
	return r, nil
}

// EnvSettings returns the SDK settings with the cache pointing at our
// directory, not at $HOME (which is not writable in the container).
func (r *Repos) EnvSettings() *cli.EnvSettings {
	s := cli.New()
	s.RepositoryCache = filepath.Join(r.cacheDir, "repository")
	s.RepositoryConfig = filepath.Join(r.cacheDir, "repositories.yaml")
	s.RegistryConfig = filepath.Join(r.cacheDir, "registry.json")
	return s
}

const repoColumns = `id, name, url, username, password_encrypted, key_id, insecure_skip_tls, created_at`

func scanRepo(row pgx.Row) (Repo, error) {
	var rp Repo
	var username, keyID *string
	if err := row.Scan(&rp.ID, &rp.Name, &rp.URL, &username, &rp.PasswordEncrypted, &keyID, &rp.InsecureSkipTLS, &rp.CreatedAt); err != nil {
		return Repo{}, err
	}
	if username != nil {
		rp.Username = *username
	}
	if keyID != nil {
		rp.KeyID = *keyID
	}
	return rp, nil
}

func (r *Repos) List(ctx context.Context) ([]Repo, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+repoColumns+` FROM helm_repos ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("helm: failed to list repos: %w", err)
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		rp, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("helm: failed to read repo: %w", err)
		}
		out = append(out, rp)
	}
	return out, rows.Err()
}

func (r *Repos) Get(ctx context.Context, id int64) (Repo, error) {
	rp, err := scanRepo(r.pool.QueryRow(ctx, `SELECT `+repoColumns+` FROM helm_repos WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Repo{}, ErrRepoNotFound
	}
	if err != nil {
		return Repo{}, fmt.Errorf("helm: failed to read repo %d: %w", id, err)
	}
	return rp, nil
}

// Create persists a repo. The password is encrypted here.
func (r *Repos) Create(ctx context.Context, name, url, username, password string, insecure bool) (Repo, error) {
	var enc []byte
	var keyID *string
	if password != "" {
		k, ct, err := r.keyring.Encrypt([]byte(password))
		if err != nil {
			return Repo{}, err
		}
		enc = ct
		s := string(k)
		keyID = &s
	}
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO helm_repos (name, url, username, password_encrypted, key_id, insecure_skip_tls)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6) RETURNING id`,
		name, url, username, enc, keyID, insecure,
	).Scan(&id)
	if err != nil {
		return Repo{}, err
	}
	return r.Get(ctx, id)
}

func (r *Repos) Delete(ctx context.Context, id int64) (Repo, error) {
	rp, err := r.Get(ctx, id)
	if err != nil {
		return Repo{}, err
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM helm_repos WHERE id = $1`, id); err != nil {
		return Repo{}, fmt.Errorf("helm: failed to delete repo: %w", err)
	}
	r.mu.Lock()
	delete(r.indexes, id)
	r.mu.Unlock()
	return rp, nil
}

// Entry translates a Repo into the SDK's repo.Entry, with the password decrypted.
func (r *Repos) Entry(rp Repo) (*repo.Entry, error) {
	e := &repo.Entry{Name: fmt.Sprintf("repo-%d", rp.ID), URL: rp.URL, Username: rp.Username, InsecureSkipTLSverify: rp.InsecureSkipTLS}
	if len(rp.PasswordEncrypted) > 0 {
		pw, err := r.keyring.Decrypt(crypto.KeyID(rp.KeyID), rp.PasswordEncrypted)
		if err != nil {
			return nil, fmt.Errorf("helm: failed to decrypt credential of repo %q: %w", rp.Name, err)
		}
		e.Password = string(pw)
	}
	return e, nil
}

// Index returns the repo's index.yaml (HTTP), from the cache if recent.
// force skips the cache ("refresh" button and validation on create).
func (r *Repos) Index(ctx context.Context, rp Repo, force bool) (*repo.IndexFile, error) {
	if rp.IsOCI() {
		return nil, errors.New("helm: an OCI registry has no index; look the chart up by name")
	}
	r.mu.Lock()
	if c, ok := r.indexes[rp.ID]; ok && !force && time.Since(c.fetchedAt) < indexTTL {
		r.mu.Unlock()
		return c.index, nil
	}
	r.mu.Unlock()

	entry, err := r.Entry(rp)
	if err != nil {
		return nil, err
	}
	settings := r.EnvSettings()
	cr, err := repo.NewChartRepository(entry, getter.All(settings))
	if err != nil {
		return nil, fmt.Errorf("helm: invalid repo: %w", err)
	}
	cr.CachePath = settings.RepositoryCache
	path, err := cr.DownloadIndexFile()
	if err != nil {
		return nil, fmt.Errorf("helm: could not download the index of %s: %w", rp.URL, err)
	}
	idx, err := repo.LoadIndexFile(path)
	if err != nil {
		return nil, fmt.Errorf("helm: invalid index: %w", err)
	}
	idx.SortEntries()
	r.mu.Lock()
	r.indexes[rp.ID] = &cachedIndex{index: idx, fetchedAt: time.Now()}
	r.mu.Unlock()
	_ = ctx
	return idx, nil
}

// Probe downloads the index of a repo not yet stored, with the plaintext
// password, to validate it before the INSERT.
func (r *Repos) Probe(ctx context.Context, rp Repo, password string) error {
	entry := &repo.Entry{Name: "probe", URL: rp.URL, Username: rp.Username, Password: password, InsecureSkipTLSverify: rp.InsecureSkipTLS}
	settings := r.EnvSettings()
	cr, err := repo.NewChartRepository(entry, getter.All(settings))
	if err != nil {
		return fmt.Errorf("invalid repository: %w", err)
	}
	cr.CachePath = settings.RepositoryCache
	if _, err := cr.DownloadIndexFile(); err != nil {
		return fmt.Errorf("could not download the index of %s: %w", rp.URL, err)
	}
	_ = ctx
	return nil
}

// Search returns the index entries whose name, description or keywords
// contain query (empty = all), one per chart with its latest version.
func (r *Repos) Search(ctx context.Context, rp Repo, query string, force bool) ([]ChartEntry, error) {
	idx, err := r.Index(ctx, rp, force)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]ChartEntry, 0, len(idx.Entries))
	for name, versions := range idx.Entries {
		if len(versions) == 0 {
			continue
		}
		latest := versions[0]
		if q != "" {
			hay := strings.ToLower(name + " " + latest.Description + " " + strings.Join(latest.Keywords, " "))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		e := ChartEntry{
			Name: name, Description: latest.Description, Version: latest.Version, AppVersion: latest.AppVersion,
			Icon: latest.Icon, Home: latest.Home, Keywords: latest.Keywords, Deprecated: latest.Deprecated,
		}
		for _, v := range versions {
			e.Versions = append(e.Versions, v.Version)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// OCITags lists the versions (tags) of a chart in an OCI registry.
func (r *Repos) OCITags(ctx context.Context, rp Repo, chart string) ([]string, error) {
	if err := ValidateChartName(chart, true); err != nil {
		return nil, err
	}
	client, err := r.registryClient(rp)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSuffix(rp.URL, "/") + "/" + chart
	tags, err := client.Tags(ref)
	if err != nil {
		return nil, fmt.Errorf("helm: failed to list tags of %s: %w", ref, err)
	}
	_ = ctx
	return tags, nil
}

// registryClient builds the OCI client with the repo's credentials. Login is
// done per host; Helm stores it in RegistryConfig (our /tmp).
func (r *Repos) registryClient(rp Repo) (*registry.Client, error) {
	settings := r.EnvSettings()
	opts := []registry.ClientOption{registry.ClientOptCredentialsFile(settings.RegistryConfig)}
	if rp.InsecureSkipTLS {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}
	client, err := registry.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("helm: failed to create OCI client: %w", err)
	}
	if rp.Username != "" {
		entry, err := r.Entry(rp)
		if err != nil {
			return nil, err
		}
		host := strings.TrimPrefix(rp.URL, "oci://")
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		if err := client.Login(host, registry.LoginOptBasicAuth(entry.Username, entry.Password), registry.LoginOptInsecure(rp.InsecureSkipTLS)); err != nil {
			return nil, fmt.Errorf("helm: failed to authenticate against %s: %w", host, err)
		}
	}
	return client, nil
}
