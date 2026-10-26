package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/backup"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/settings"
)

// BackupHandler: S3 targets (global admin), schedules and runs per cluster
// (admin on the cluster: a backup may carry the Secrets).
type BackupHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager
	Svc    *backup.Service
	// Server and Settings: backups of Bayplan's own database (global admin).
	Server   *backup.ServerBackups
	Settings *settings.Store
}

type targetResponse struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Endpoint        string    `json:"endpoint,omitempty"`
	Region          string    `json:"region"`
	Bucket          string    `json:"bucket"`
	Prefix          string    `json:"prefix,omitempty"`
	AccessKeyID     string    `json:"accessKeyId"`
	PathStyle       bool      `json:"pathStyle"`
	InsecureSkipTLS bool      `json:"insecureSkipTLS"`
	ScheduleCount   int       `json:"scheduleCount"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func toTargetResponse(t backup.Target) targetResponse {
	return targetResponse{ID: t.ID, Name: t.Name, Endpoint: t.Endpoint, Region: t.Region, Bucket: t.Bucket, Prefix: t.Prefix, AccessKeyID: t.AccessKeyID,
		PathStyle: t.PathStyle, InsecureSkipTLS: t.InsecureSkipTLS, ScheduleCount: t.ScheduleCount, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

type targetInput struct {
	Name            *string `json:"name"`
	Endpoint        *string `json:"endpoint"`
	Region          *string `json:"region"`
	Bucket          *string `json:"bucket"`
	Prefix          *string `json:"prefix"`
	AccessKeyID     *string `json:"accessKeyId"`
	SecretAccessKey *string `json:"secretAccessKey"`
	PathStyle       *bool   `json:"pathStyle"`
	InsecureSkipTLS *bool   `json:"insecureSkipTLS"`
}

func (in *targetInput) applyTo(t *backup.Target) {
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Endpoint != nil {
		t.Endpoint = *in.Endpoint
	}
	if in.Region != nil {
		t.Region = *in.Region
	}
	if in.Bucket != nil {
		t.Bucket = *in.Bucket
	}
	if in.Prefix != nil {
		t.Prefix = *in.Prefix
	}
	if in.AccessKeyID != nil {
		t.AccessKeyID = *in.AccessKeyID
	}
	if in.SecretAccessKey != nil && *in.SecretAccessKey != "" {
		t.SecretAccessKey = strings.TrimSpace(*in.SecretAccessKey)
	}
	if in.PathStyle != nil {
		t.PathStyle = *in.PathStyle
	}
	if in.InsecureSkipTLS != nil {
		t.InsecureSkipTLS = *in.InsecureSkipTLS
	}
}

func (h *BackupHandler) ListTargets(w http.ResponseWriter, r *http.Request) {
	list, err := h.Svc.Store.Targets(r.Context())
	if err != nil {
		h.Logger.Error("backup.targets: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A cluster admin picks a target by name when creating a schedule; the
	// access key id and the endpoint are the platform admin's business.
	role, _ := middleware.RoleFromContext(r.Context())
	out := make([]targetResponse, 0, len(list))
	for _, t := range list {
		resp := toTargetResponse(t)
		if !role.IsAdmin() {
			resp.AccessKeyID, resp.Endpoint, resp.Region, resp.Bucket, resp.Prefix = "", "", "", "", ""
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *BackupHandler) CreateTarget(w http.ResponseWriter, r *http.Request) {
	var in targetInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var t backup.Target
	in.applyTo(&t)
	if err := t.Validate(true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := backup.Validate(r.Context(), &t); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	id, err := h.Svc.Store.CreateTarget(r.Context(), &t)
	if errors.Is(err, backup.ErrDuplicate) {
		http.Error(w, "a target with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		h.Logger.Error("backup.targets: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "backup.target.create", 0, t.Name, nil, map[string]any{"bucket": t.Bucket, "endpoint": t.Endpoint})
	created, _ := h.Svc.Store.Target(r.Context(), id)
	writeJSON(w, http.StatusCreated, toTargetResponse(*created))
}

func (h *BackupHandler) UpdateTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in targetInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	t, err := h.Svc.Store.Target(r.Context(), id)
	if errors.Is(err, backup.ErrNotFound) {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	in.applyTo(t)
	if err := t.Validate(true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := backup.Validate(r.Context(), t); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := h.Svc.Store.UpdateTarget(r.Context(), t); err != nil {
		if errors.Is(err, backup.ErrDuplicate) {
			http.Error(w, "a target with that name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "backup.target.update", 0, t.Name, nil, map[string]any{"bucket": t.Bucket})
	writeJSON(w, http.StatusOK, toTargetResponse(*t))
}

func (h *BackupHandler) DeleteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	t, err := h.Svc.Store.Target(r.Context(), id)
	if errors.Is(err, backup.ErrNotFound) {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if h.Settings != nil {
		var sb settings.ServerBackup
		if err := h.Settings.Get(r.Context(), settings.KeyServerBackup, &sb); err == nil && sb.TargetID == id {
			http.Error(w, "the target holds the backups of Bayplan itself: choose another one there first", http.StatusConflict)
			return
		}
	}
	switch err := h.Svc.Store.DeleteTarget(r.Context(), id); {
	case errors.Is(err, backup.ErrInUse):
		http.Error(w, "the target has schedules: delete them first", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "backup.target.delete", 0, t.Name, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

type scheduleResponse struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	ClusterID      int64      `json:"clusterId"`
	TargetID       int64      `json:"targetId"`
	TargetName     string     `json:"targetName"`
	Namespaces     []string   `json:"namespaces"`
	IncludeSecrets bool       `json:"includeSecrets"`
	IntervalHours  int        `json:"intervalHours"`
	Retention      int        `json:"retention"`
	Enabled        bool       `json:"enabled"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
	CreatedBy      string     `json:"createdBy,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func toScheduleResponse(s backup.Schedule) scheduleResponse {
	return scheduleResponse{ID: s.ID, Name: s.Name, ClusterID: s.ClusterID, TargetID: s.TargetID, TargetName: s.TargetName, Namespaces: s.Namespaces,
		IncludeSecrets: s.IncludeSecrets, IntervalHours: s.IntervalHours, Retention: s.Retention, Enabled: s.Enabled, LastRunAt: s.LastRunAt, CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt}
}

type scheduleInput struct {
	Name           *string   `json:"name"`
	TargetID       *int64    `json:"targetId"`
	Namespaces     *[]string `json:"namespaces"`
	IncludeSecrets *bool     `json:"includeSecrets"`
	IntervalHours  *int      `json:"intervalHours"`
	Retention      *int      `json:"retention"`
	Enabled        *bool     `json:"enabled"`
}

func (in *scheduleInput) applyTo(s *backup.Schedule) {
	if in.Name != nil {
		s.Name = *in.Name
	}
	if in.TargetID != nil {
		s.TargetID = *in.TargetID
	}
	if in.Namespaces != nil {
		s.Namespaces = *in.Namespaces
	}
	if in.IncludeSecrets != nil {
		s.IncludeSecrets = *in.IncludeSecrets
	}
	if in.IntervalHours != nil {
		s.IntervalHours = *in.IntervalHours
	}
	if in.Retention != nil {
		s.Retention = *in.Retention
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
}

func (h *BackupHandler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	list, err := h.Svc.Store.SchedulesForCluster(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]scheduleResponse, 0, len(list))
	for _, s := range list {
		out = append(out, toScheduleResponse(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *BackupHandler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in scheduleInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	sess, _ := middleware.SessionFromContext(r.Context())
	s := backup.Schedule{ClusterID: id, IntervalHours: 24, Retention: 14, Enabled: true, Namespaces: []string{}}
	if sess != nil {
		s.CreatedBy = sess.Username
	}
	in.applyTo(&s)
	if err := s.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sid, err := h.Svc.Store.CreateSchedule(r.Context(), &s)
	switch {
	case errors.Is(err, backup.ErrDuplicate):
		http.Error(w, "a schedule with that name already exists in this cluster", http.StatusConflict)
		return
	case errors.Is(err, backup.ErrNotFound):
		http.Error(w, "target not found", http.StatusBadRequest)
		return
	case err != nil:
		h.Logger.Error("backup.schedules: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "backup.schedule.create", id, s.Name, nil, map[string]any{"target": s.TargetID, "namespaces": s.Namespaces, "includeSecrets": s.IncludeSecrets, "intervalHours": s.IntervalHours})
	created, _ := h.Svc.Store.Schedule(r.Context(), sid)
	writeJSON(w, http.StatusCreated, toScheduleResponse(*created))
}

func (h *BackupHandler) scheduleFor(w http.ResponseWriter, r *http.Request) (*backup.Schedule, bool) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return nil, false
	}
	sid, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return nil, false
	}
	s, err := h.Svc.Store.Schedule(r.Context(), sid)
	if errors.Is(err, backup.ErrNotFound) || (err == nil && s.ClusterID != id) {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return s, true
}

func (h *BackupHandler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scheduleFor(w, r)
	if !ok {
		return
	}
	var in scheduleInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	in.applyTo(s)
	if err := s.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Svc.Store.UpdateSchedule(r.Context(), s); err != nil {
		if errors.Is(err, backup.ErrDuplicate) {
			http.Error(w, "a schedule with that name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "backup.schedule.update", s.ClusterID, s.Name, nil, map[string]any{"enabled": s.Enabled, "intervalHours": s.IntervalHours})
	updated, _ := h.Svc.Store.Schedule(r.Context(), s.ID)
	writeJSON(w, http.StatusOK, toScheduleResponse(*updated))
}

func (h *BackupHandler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scheduleFor(w, r)
	if !ok {
		return
	}
	if err := h.Svc.Store.DeleteSchedule(r.Context(), s.ID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "backup.schedule.delete", s.ClusterID, s.Name, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

// RunNow runs the backup synchronously (an export from the informer cache plus
// an upload takes seconds) and returns the run.
func (h *BackupHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scheduleFor(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	run, err := h.Svc.Run(ctx, *s, "manual")
	h.audit(r, "backup.run", s.ClusterID, s.Name, err, map[string]any{"objects": run.Objects, "sizeBytes": run.SizeBytes, "key": run.ObjectKey})
	if err != nil && run.ID == 0 {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, run)
}

func (h *BackupHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	runs, err := h.Svc.Store.RunsForCluster(r.Context(), id, limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (h *BackupHandler) runFor(w http.ResponseWriter, r *http.Request) (*backup.Run, *backup.Schedule, bool) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return nil, nil, false
	}
	rid, err := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return nil, nil, false
	}
	run, err := h.Svc.Store.Run(r.Context(), rid)
	if errors.Is(err, backup.ErrNotFound) {
		http.Error(w, "run not found", http.StatusNotFound)
		return nil, nil, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, nil, false
	}
	s, err := h.Svc.Store.Schedule(r.Context(), run.ScheduleID)
	if err != nil || s.ClusterID != id {
		http.Error(w, "run not found", http.StatusNotFound)
		return nil, nil, false
	}
	return run, s, true
}

// Download serves the tar.gz. It may carry Secrets: it is audited.
func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	run, s, ok := h.runFor(w, r)
	if !ok {
		return
	}
	data, err := h.Svc.Archive(r.Context(), *run)
	h.audit(r, "backup.download", s.ClusterID, s.Name, err, map[string]any{"key": run.ObjectKey, "includeSecrets": s.IncludeSecrets})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	name := fmt.Sprintf("bayplan-%s-%s.tar.gz", s.Name, run.StartedAt.UTC().Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// Index returns the archive index (which objects it contains).
func (h *BackupHandler) Index(w http.ResponseWriter, r *http.Request) {
	run, _, ok := h.runFor(w, r)
	if !ok {
		return
	}
	m, err := h.Svc.Index(r.Context(), *run)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// Restore applies the chosen objects with the user's identity (admin on the
// cluster, admins group).
func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	run, s, ok := h.runFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Paths  []string `json:"paths"`
		DryRun bool     `json:"dryRun"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || len(in.Paths) == 0 {
		http.Error(w, "specify the paths to restore", http.StatusBadRequest)
		return
	}
	sess, _ := middleware.SessionFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	results, err := h.Svc.Restore(ctx, *run, in.Paths, sess.Username, k8s.GroupAdmins, in.DryRun)
	if !in.DryRun {
		h.audit(r, "backup.restore", s.ClusterID, s.Name, err, map[string]any{"key": run.ObjectKey, "paths": len(in.Paths), "applied": len(results)})
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": results, "dryRun": in.DryRun})
}

func (h *BackupHandler) audit(r *http.Request, action string, clusterID int64, name string, err error, detail map[string]any) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var uid *int64
	var uname string
	if sess != nil {
		uid, uname = &sess.UserID, sess.Username
	}
	result := audit.ResultSuccess
	if err != nil {
		result = audit.ResultFailure
		if detail == nil {
			detail = map[string]any{}
		}
		detail["error"] = err.Error()
	}
	cluster := ""
	if clusterID != 0 {
		cluster = h.K8s.DisplayName(clusterID)
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{UserID: uid, ActorUsername: uname, IP: middleware.ClientIP(r), Action: action, Cluster: cluster, Resource: name, Result: result, Detail: detail})
}
