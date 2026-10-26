package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bayplan/bayplan/internal/backup"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/settings"
)

// Backups of Bayplan's own database (internal/backup/server.go). Global admin
// for everything, GETs included: the dump carries every user, assignment and
// cluster credential (encrypted) and the whole audit log.

type serverBackupResponse struct {
	Config    settings.ServerBackup `json:"config"`
	Runs      []backup.ServerRun    `json:"runs"`
	Available bool                  `json:"available"` // pg_dump present
}

// GetServerBackup serves GET /api/admin/server-backup.
func (h *BackupHandler) GetServerBackup(w http.ResponseWriter, r *http.Request) {
	var resp serverBackupResponse
	if err := h.Settings.Get(r.Context(), settings.KeyServerBackup, &resp.Config); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	runs, err := h.Server.Runs(r.Context(), 20)
	if err != nil {
		h.Logger.Error("server backup: failed to list runs", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp.Runs = runs
	if resp.Runs == nil {
		resp.Runs = []backup.ServerRun{}
	}
	resp.Available = h.Server.Available()
	writeJSON(w, http.StatusOK, resp)
}

// UpdateServerBackup serves PUT /api/admin/server-backup.
func (h *BackupHandler) UpdateServerBackup(w http.ResponseWriter, r *http.Request) {
	var in settings.ServerBackup
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := backup.ValidateSchedule(&in); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if in.TargetID > 0 {
		if _, err := h.Svc.Store.Target(r.Context(), in.TargetID); errors.Is(err, backup.ErrNotFound) {
			http.Error(w, "target not found", http.StatusUnprocessableEntity)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	sess, _ := middleware.SessionFromContext(r.Context())
	actor := ""
	if sess != nil {
		actor = sess.Username
	}
	if err := h.Settings.Set(r.Context(), settings.KeyServerBackup, in, actor); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "server_backup.configure", 0, "", nil, map[string]any{"enabled": in.Enabled, "targetId": in.TargetID, "intervalHours": in.IntervalHours, "keep": in.Keep})
	writeJSON(w, http.StatusOK, in)
}

// RunServerBackup serves POST /api/admin/server-backup/run: synchronous, the
// UI waits for the result (a dump of the panel's database takes seconds).
func (h *BackupHandler) RunServerBackup(w http.ResponseWriter, r *http.Request) {
	run, err := h.Server.Run(r.Context(), "manual")
	h.audit(r, "server_backup.run", 0, run.ObjectKey, err, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// DownloadServerBackup serves GET /api/admin/server-backup/download: a dump
// generated on the spot and streamed, for installations without S3. Always
// audited.
func (h *BackupHandler) DownloadServerBackup(w http.ResponseWriter, r *http.Request) {
	if !h.Server.Available() {
		http.Error(w, backup.ErrPgDumpMissing.Error(), http.StatusNotFound)
		return
	}
	name := fmt.Sprintf("bayplan-%s.dump", time.Now().UTC().Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	err := h.Server.DumpTo(r.Context(), w)
	h.audit(r, "server_backup.download", 0, name, err, nil)
	if err != nil {
		// The headers are already out: the truncated file is what tells the
		// browser, and the log and the audit entry say why.
		h.Logger.Error("server backup download failed", "error", err)
	}
}
