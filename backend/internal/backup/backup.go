// Package backup backs up a cluster's CONFIGURATION (manifests cleaned of
// state, see k8s.DumpResources) to an S3-compatible bucket, with scheduling
// and history, and allows restoring individual objects with server-side apply.
// It does not copy volumes: that is Velero, and this is said here instead of
// pretending a tar of YAML is a full backup.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Target is a backup_targets row. SecretAccessKey is only filled in when
// loading with Store.Target.
type Target struct {
	ID              int64
	Name            string
	Endpoint        string // empty = AWS S3 by region
	Region          string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
	InsecureSkipTLS bool
	ScheduleCount   int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Validate normalizes and checks the target.
func (t *Target) Validate(requireSecret bool) error {
	t.Name = strings.TrimSpace(t.Name)
	t.Endpoint = strings.TrimRight(strings.TrimSpace(t.Endpoint), "/")
	t.Region = strings.TrimSpace(t.Region)
	t.Bucket = strings.TrimSpace(t.Bucket)
	t.Prefix = strings.Trim(strings.TrimSpace(t.Prefix), "/")
	t.AccessKeyID = strings.TrimSpace(t.AccessKeyID)
	if !nameRE.MatchString(t.Name) {
		return errors.New("the name must be a lowercase DNS label")
	}
	if t.Bucket == "" {
		return errors.New("missing bucket")
	}
	if t.Region == "" {
		t.Region = "us-east-1"
	}
	if t.Endpoint != "" && !strings.HasPrefix(t.Endpoint, "https://") && !strings.HasPrefix(t.Endpoint, "http://") {
		return errors.New("the endpoint must start with https:// (or http:// in a lab)")
	}
	if t.AccessKeyID == "" {
		return errors.New("missing access key id")
	}
	if requireSecret && t.SecretAccessKey == "" {
		return errors.New("missing secret access key")
	}
	return nil
}

// Schedule is a backup_schedules row.
type Schedule struct {
	ID             int64
	Name           string
	ClusterID      int64
	TargetID       int64
	TargetName     string
	Namespaces     []string
	IncludeSecrets bool
	IntervalHours  int
	Retention      int
	Enabled        bool
	LastRunAt      *time.Time
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Validate checks the schedule.
func (s *Schedule) Validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if !nameRE.MatchString(s.Name) {
		return errors.New("the name must be a lowercase DNS label (it goes into the bucket path)")
	}
	if s.TargetID == 0 {
		return errors.New("choose a target")
	}
	if s.IntervalHours < 0 || s.IntervalHours > 24*31 {
		return errors.New("interval out of range (0 = manual only, max. 744 h)")
	}
	if s.Retention < 1 || s.Retention > 365 {
		return errors.New("retention must be between 1 and 365 copies")
	}
	for _, ns := range s.Namespaces {
		if !nameRE.MatchString(ns) {
			return fmt.Errorf("invalid namespace %q", ns)
		}
	}
	return nil
}

// Due reports whether it is time to run it.
func (s Schedule) Due(now time.Time) bool {
	if !s.Enabled || s.IntervalHours == 0 {
		return false
	}
	if s.LastRunAt == nil {
		return true
	}
	return now.Sub(*s.LastRunAt) >= time.Duration(s.IntervalHours)*time.Hour
}

// Run is a backup_runs row.
type Run struct {
	ID         int64             `json:"id"`
	ScheduleID int64             `json:"scheduleId"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	Status     string            `json:"status"`
	Trigger    string            `json:"trigger"`
	ObjectKey  string            `json:"objectKey,omitempty"`
	SizeBytes  int64             `json:"sizeBytes"`
	Objects    int               `json:"objects"`
	Skipped    map[string]string `json:"skipped,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// Manifest is the index that goes inside the archive (manifest.json): what
// the UI shows when restoring without having to open each YAML.
type Manifest struct {
	Version   int               `json:"version"`
	Cluster   string            `json:"cluster"`
	ClusterID int64             `json:"clusterId"`
	Schedule  string            `json:"schedule"`
	CreatedAt time.Time         `json:"createdAt"`
	Secrets   bool              `json:"includeSecrets"`
	Items     []ManifestItem    `json:"items"`
	Skipped   map[string]string `json:"skipped,omitempty"`
}

// ManifestItem is an object of the archive and its path inside the tar.
type ManifestItem struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Path      string `json:"path"`
}

// Item is an object with its YAML (input when packing, output when reading).
type Item struct {
	Kind, Namespace, Name, YAML string
}

// itemPath is the path of an object inside the tar: <namespace|_cluster>/<kind>/<name>.yaml.
func itemPath(it Item) string {
	ns := it.Namespace
	if ns == "" {
		ns = "_cluster"
	}
	return path.Join(ns, it.Kind, it.Name+".yaml")
}

// Pack creates the tar.gz with manifest.json first (so the index can be read
// without decompressing everything) and one YAML per object.
func Pack(m Manifest, items []Item) ([]byte, error) {
	m.Version = 1
	m.Items = make([]ManifestItem, 0, len(items))
	for _, it := range items {
		m.Items = append(m.Items, ManifestItem{Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, Path: itemPath(it)})
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: m.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	idx, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := write("manifest.json", idx); err != nil {
		return nil, err
	}
	for _, it := range items {
		if err := write(itemPath(it), []byte(it.YAML)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// maxArchiveBytes bounds what is decompressed in memory when restoring.
const maxArchiveBytes = 256 << 20

// Unpack reads the archive: the index and, if `want` is not nil, only the
// YAML of those paths (restoring a few objects must not load hundreds).
func Unpack(r io.Reader, want map[string]bool) (Manifest, map[string]string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("invalid archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxArchiveBytes))
	var m Manifest
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, nil, fmt.Errorf("invalid archive: %w", err)
		}
		if h.Name == "manifest.json" {
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return Manifest{}, nil, fmt.Errorf("invalid manifest.json: %w", err)
			}
			continue
		}
		if want != nil && !want[h.Name] {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return Manifest{}, nil, err
		}
		files[h.Name] = string(b)
	}
	if m.Version == 0 {
		return Manifest{}, nil, errors.New("the archive has no manifest.json")
	}
	return m, files, nil
}
