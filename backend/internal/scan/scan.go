// Package scan runs Trivy on the server to look for vulnerabilities in the
// images running in the clusters. The binary is optional (like kubectl):
// without it the feature is disabled and the UI says so. Trivy downloads its
// vulnerability database to the cache the first time (it needs egress to
// ghcr.io) and refreshes it on its own.
package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bayplan/bayplan/internal/privdir"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const scanTimeout = 10 * time.Minute

var ErrUnavailable = errors.New("trivy is not available on the server")

// Runner knows the binary and its cache.
type Runner struct {
	path     string
	cacheDir string
	version  string
	mu       sync.Mutex // Trivy does not handle two simultaneous scans over the same cache well while downloading the DB
}

// New locates trivy (empty path = "trivy" in the PATH) and prepares the cache
// in $TMPDIR/bayplan-trivy (the container is read-only except /tmp).
func New(path string) *Runner {
	if path == "" {
		path = "trivy"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return &Runner{}
	}
	cache := filepath.Join(os.TempDir(), "bayplan-trivy")
	// The cache is also trivy's HOME: a directory someone else controls
	// could feed it a poisoned vulnerability DB, so scanning is off instead.
	if err := privdir.Ensure(cache); err != nil {
		return &Runner{}
	}
	r := &Runner{path: resolved, cacheDir: cache}
	if out, err := exec.Command(resolved, "--version").Output(); err == nil {
		if line, _, ok := strings.Cut(string(out), "\n"); ok {
			r.version = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		}
	}
	return r
}

func (r *Runner) Available() bool { return r.path != "" }
func (r *Runner) Version() string { return r.version }

// Vulnerability is a CVE trimmed to what the UI shows.
type Vulnerability struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed,omitempty"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	Target    string `json:"target"` // layer/OS or dependency file
}

// Report is the result of a scan.
type Report struct {
	Image           string          `json:"image"`
	OS              string          `json:"os,omitempty"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
	Counts          Counts          `json:"counts"`
}

// Counts per severity and how many have a fix.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
	Fixable  int `json:"fixable"`
}

// Credentials are those of the image's private registry, if any.
type Credentials struct {
	Username, Password string
	Insecure           bool
}

// trivyOutput is the subset of trivy's JSON that is read.
type trivyOutput struct {
	ArtifactName string `json:"ArtifactName"`
	Metadata     struct {
		OS *struct {
			Family string `json:"Family"`
			Name   string `json:"Name"`
		} `json:"OS"`
	} `json:"Metadata"`
	Results []struct {
		Target          string `json:"Target"`
		Vulnerabilities []struct {
			VulnerabilityID  string `json:"VulnerabilityID"`
			PkgName          string `json:"PkgName"`
			InstalledVersion string `json:"InstalledVersion"`
			FixedVersion     string `json:"FixedVersion"`
			Severity         string `json:"Severity"`
			Title            string `json:"Title"`
			PrimaryURL       string `json:"PrimaryURL"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

// Scan runs `trivy image` on the reference. Vulnerabilities only (no secrets
// or misconfig: they take time and the question here is "which CVEs are
// running?").
func (r *Runner) Scan(ctx context.Context, image string, creds *Credentials) (Report, error) {
	if !r.Available() {
		return Report{}, ErrUnavailable
	}
	if err := validateImageRef(image); err != nil {
		return Report{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	args := []string{"image", "--format", "json", "--scanners", "vuln", "--quiet", "--timeout", "9m", "--cache-dir", r.cacheDir, "--"}
	args = append(args, image)
	cmd := exec.CommandContext(ctx, r.path, args...)
	env := []string{"HOME=" + r.cacheDir, "PATH=" + filepath.Dir(r.path), "TRIVY_CACHE_DIR=" + r.cacheDir, "TRIVY_NO_PROGRESS=true"}
	// TRIVY_*_DB_REPOSITORY: air-gapped installs point Trivy at a mirror of
	// its vulnerability databases (deploy/airgap).
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "TRIVY_DB_REPOSITORY", "TRIVY_JAVA_DB_REPOSITORY"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if creds != nil {
		env = append(env, "TRIVY_USERNAME="+creds.Username, "TRIVY_PASSWORD="+creds.Password)
		if creds.Insecure {
			env = append(env, "TRIVY_INSECURE=true")
		}
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "FATAL"); i >= 0 {
			msg = strings.TrimSpace(msg[i+5:])
		}
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return Report{}, errors.New(msg)
	}
	var out trivyOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return Report{}, fmt.Errorf("invalid trivy output: %w", err)
	}
	rep := Report{Image: image, Vulnerabilities: []Vulnerability{}}
	if out.Metadata.OS != nil {
		rep.OS = strings.TrimSpace(out.Metadata.OS.Family + " " + out.Metadata.OS.Name)
	}
	for _, res := range out.Results {
		for _, v := range res.Vulnerabilities {
			sev := strings.ToUpper(v.Severity)
			rep.Vulnerabilities = append(rep.Vulnerabilities, Vulnerability{
				ID: v.VulnerabilityID, Severity: sev, Package: v.PkgName, Installed: v.InstalledVersion, Fixed: v.FixedVersion,
				Title: v.Title, URL: v.PrimaryURL, Target: res.Target,
			})
			switch sev {
			case "CRITICAL":
				rep.Counts.Critical++
			case "HIGH":
				rep.Counts.High++
			case "MEDIUM":
				rep.Counts.Medium++
			case "LOW":
				rep.Counts.Low++
			default:
				rep.Counts.Unknown++
			}
			if v.FixedVersion != "" {
				rep.Counts.Fixable++
			}
		}
	}
	sort.SliceStable(rep.Vulnerabilities, func(i, j int) bool {
		return severityRank(rep.Vulnerabilities[i].Severity) > severityRank(rep.Vulnerabilities[j].Severity)
	})
	return rep, nil
}

func severityRank(s string) int {
	switch s {
	case "CRITICAL":
		return 4
	case "HIGH":
		return 3
	case "MEDIUM":
		return 2
	case "LOW":
		return 1
	}
	return 0
}

// validateImageRef rejects what does not look like an image reference: the
// string goes to a subprocess (after "--", but better not to pass anything
// odd).
func validateImageRef(image string) error {
	if image == "" || len(image) > 512 || strings.HasPrefix(image, "-") {
		return errors.New("invalid image reference")
	}
	for _, c := range image {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("./:@-_", c)) {
			return fmt.Errorf("invalid image reference: character %q", c)
		}
	}
	return nil
}

// RegistryHost returns the registry host of a reference ("ghcr.io/x/y:1" →
// "ghcr.io"; "nginx" → "docker.io"), to look up credentials.
func RegistryHost(image string) string {
	first, _, _ := strings.Cut(image, "/")
	if !strings.Contains(image, "/") || (!strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost") {
		return "docker.io"
	}
	return first
}
