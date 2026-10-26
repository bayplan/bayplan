package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/bayplan/bayplan/internal/k8s"
)

const tickInterval = time.Minute

// Service runs backups (manual and scheduled) and restores.
type Service struct {
	Store  *Store
	k8s    *k8s.Manager
	logger *slog.Logger

	mu      sync.Mutex
	running map[int64]bool // schedules in progress
}

func NewService(store *Store, k8sManager *k8s.Manager, logger *slog.Logger) *Service {
	return &Service{Store: store, k8s: k8sManager, logger: logger, running: map[int64]bool{}}
}

// Start launches the scheduler: every minute it runs the schedules that are due.
func (s *Service) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.tick(ctx, now)
			}
		}
	}()
}

func (s *Service) tick(ctx context.Context, now time.Time) {
	list, err := s.Store.EnabledSchedules(ctx)
	if err != nil {
		s.logger.Error("backup: failed to list schedules", "error", err)
		return
	}
	for _, sc := range list {
		if sc.Due(now) {
			sc := sc
			go func() { _, _ = s.Run(ctx, sc, "schedule") }()
		}
	}
}

// Run performs a backup: exports, packs, uploads, applies retention and leaves
// the result in backup_runs. A schedule never runs twice at the same time.
func (s *Service) Run(ctx context.Context, sc Schedule, trigger string) (Run, error) {
	s.mu.Lock()
	if s.running[sc.ID] {
		s.mu.Unlock()
		return Run{}, errors.New("a backup is already in progress for this schedule")
	}
	s.running[sc.ID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, sc.ID)
		s.mu.Unlock()
	}()

	runID, err := s.Store.StartRun(ctx, sc.ID, trigger)
	if err != nil {
		return Run{}, err
	}
	key, size, objects, skipped, runErr := s.execute(ctx, sc, runID)
	if err := s.Store.FinishRun(ctx, runID, key, size, objects, skipped, runErr); err != nil {
		s.logger.Error("backup: failed to store result", "error", err)
	}
	if runErr != nil {
		s.logger.Warn("backup: backup failed", "schedule", sc.Name, "cluster", sc.ClusterID, "error", runErr)
	}
	r, err := s.Store.Run(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	return *r, runErr
}

func (s *Service) execute(ctx context.Context, sc Schedule, runID int64) (key string, size int64, objects int, skipped map[string]string, err error) {
	target, err := s.Store.Target(ctx, sc.TargetID)
	if err != nil {
		return "", 0, 0, nil, err
	}
	items, skipped, err := s.k8s.DumpResources(sc.ClusterID, k8s.DumpOptions{Namespaces: sc.Namespaces, IncludeSecrets: sc.IncludeSecrets})
	if err != nil {
		return "", 0, 0, skipped, err
	}
	now := time.Now().UTC()
	packed := make([]Item, 0, len(items))
	for _, it := range items {
		packed = append(packed, Item{Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, YAML: it.YAML})
	}
	data, err := Pack(Manifest{
		Cluster: s.k8s.DisplayName(sc.ClusterID), ClusterID: sc.ClusterID, Schedule: sc.Name, CreatedAt: now, Secrets: sc.IncludeSecrets, Skipped: skipped,
	}, packed)
	if err != nil {
		return "", 0, 0, skipped, err
	}
	folder := target.key(fmt.Sprintf("cluster-%d", sc.ClusterID), sc.Name)
	// Sortable date + run id: two backups in the same second (a manual one on
	// top of a scheduled one) must not overwrite each other in the bucket.
	key = fmt.Sprintf("%s/%s-%d.tar.gz", folder, now.Format("20060102T150405Z"), runID)
	if err := upload(ctx, target, key, data); err != nil {
		return "", 0, 0, skipped, fmt.Errorf("failed to upload to %s: %w", target.Bucket, err)
	}
	if deleted, err := prune(ctx, target, folder, sc.Retention); err != nil {
		s.logger.Warn("backup: failed to apply retention", "schedule", sc.Name, "error", err)
	} else if len(deleted) > 0 {
		_ = s.Store.PruneRuns(ctx, sc.ID, deleted)
	}
	return key, int64(len(data)), len(items), skipped, nil
}

// Archive downloads a run's archive.
func (s *Service) Archive(ctx context.Context, run Run) ([]byte, error) {
	if run.ObjectKey == "" {
		return nil, errors.New("this run produced no archive")
	}
	sc, err := s.Store.Schedule(ctx, run.ScheduleID)
	if err != nil {
		return nil, err
	}
	target, err := s.Store.Target(ctx, sc.TargetID)
	if err != nil {
		return nil, err
	}
	return download(ctx, target, run.ObjectKey)
}

// Index returns a run's manifest.json (what the UI shows to choose what to
// restore).
func (s *Service) Index(ctx context.Context, run Run) (Manifest, error) {
	data, err := s.Archive(ctx, run)
	if err != nil {
		return Manifest{}, err
	}
	m, _, err := Unpack(bytes.NewReader(data), map[string]bool{})
	return m, err
}

// Restore applies (server-side apply) the chosen objects of the archive with
// the user's identity. What can be restored is decided by the cluster's RBAC,
// same as the YAML editor. Masked Secrets are rejected: a Secret with
// "<redacted>" applied would overwrite the real one.
func (s *Service) Restore(ctx context.Context, run Run, paths []string, actingUser, actingGroup string, dryRun bool) ([]k8s.ApplyResult, error) {
	data, err := s.Archive(ctx, run)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	m, files, err := Unpack(bytes.NewReader(data), want)
	if err != nil {
		return nil, err
	}
	var objs []*unstructured.Unstructured
	for _, it := range m.Items {
		raw, ok := files[it.Path]
		if !ok {
			continue
		}
		if strings.Contains(raw, k8s.RedactedAnnotation) {
			return nil, fmt.Errorf("%s/%s is masked in the backup (it was made without secrets) and cannot be restored", it.Kind, it.Name)
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(raw), &obj); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", it.Path, err)
		}
		objs = append(objs, &unstructured.Unstructured{Object: obj})
	}
	if len(objs) == 0 {
		return nil, errors.New("there is nothing to restore with those paths")
	}
	sc, err := s.Store.Schedule(ctx, run.ScheduleID)
	if err != nil {
		return nil, err
	}
	return s.k8s.Apply(ctx, sc.ClusterID, objs, "", actingUser, actingGroup, dryRun)
}
