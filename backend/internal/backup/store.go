package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/crypto"
)

var (
	ErrNotFound  = errors.New("backup: not found")
	ErrDuplicate = errors.New("backup: already exists")
	ErrInUse     = errors.New("backup: the target has schedules; delete them first")
)

// Store persists targets, schedules and runs.
type Store struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
}

func NewStore(pool *pgxpool.Pool, keyring *crypto.Keyring) *Store {
	return &Store{pool: pool, keyring: keyring}
}

const targetColumns = `t.id, t.name, COALESCE(t.endpoint, ''), t.region, t.bucket, t.prefix, t.access_key_id, t.path_style, t.insecure_skip_tls, t.created_at, t.updated_at,
	(SELECT count(*) FROM backup_schedules s WHERE s.target_id = t.id)`

func scanTarget(row pgx.Row, dst *Target, extra ...any) error {
	args := []any{&dst.ID, &dst.Name, &dst.Endpoint, &dst.Region, &dst.Bucket, &dst.Prefix, &dst.AccessKeyID, &dst.PathStyle, &dst.InsecureSkipTLS, &dst.CreatedAt, &dst.UpdatedAt, &dst.ScheduleCount}
	return row.Scan(append(args, extra...)...)
}

func (s *Store) Targets(ctx context.Context) ([]Target, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+targetColumns+` FROM backup_targets t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		if err := scanTarget(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Target loads a target WITH the secret decrypted.
func (s *Store) Target(ctx context.Context, id int64) (*Target, error) {
	var t Target
	var enc []byte
	var keyID string
	err := scanTarget(s.pool.QueryRow(ctx, `SELECT `+targetColumns+`, t.secret_encrypted, t.key_id FROM backup_targets t WHERE t.id = $1`, id), &t, &enc, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sec, err := s.keyring.Decrypt(crypto.KeyID(keyID), enc)
	if err != nil {
		return nil, fmt.Errorf("backup: failed to decrypt target %q: %w", t.Name, err)
	}
	t.SecretAccessKey = string(sec)
	return &t, nil
}

func (s *Store) CreateTarget(ctx context.Context, t *Target) (int64, error) {
	keyID, enc, err := s.keyring.Encrypt([]byte(t.SecretAccessKey))
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO backup_targets (name, endpoint, region, bucket, prefix, access_key_id, secret_encrypted, key_id, path_style, insecure_skip_tls)
		 VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		t.Name, t.Endpoint, t.Region, t.Bucket, t.Prefix, t.AccessKeyID, enc, keyID, t.PathStyle, t.InsecureSkipTLS).Scan(&id)
	if isUnique(err) {
		return 0, ErrDuplicate
	}
	return id, err
}

func (s *Store) UpdateTarget(ctx context.Context, t *Target) error {
	keyID, enc, err := s.keyring.Encrypt([]byte(t.SecretAccessKey))
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE backup_targets SET name = $2, endpoint = NULLIF($3, ''), region = $4, bucket = $5, prefix = $6, access_key_id = $7, secret_encrypted = $8, key_id = $9, path_style = $10, insecure_skip_tls = $11, updated_at = now() WHERE id = $1`,
		t.ID, t.Name, t.Endpoint, t.Region, t.Bucket, t.Prefix, t.AccessKeyID, enc, keyID, t.PathStyle, t.InsecureSkipTLS)
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteTarget(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM backup_targets WHERE id = $1`, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrInUse
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const scheduleColumns = `s.id, s.name, s.cluster_id, s.target_id, t.name, s.namespaces, s.include_secrets, s.interval_hours, s.retention, s.enabled, s.last_run_at, COALESCE(s.created_by, ''), s.created_at, s.updated_at`
const scheduleFrom = ` FROM backup_schedules s JOIN backup_targets t ON t.id = s.target_id`

func scanSchedule(row pgx.Row) (Schedule, error) {
	var sc Schedule
	var ns []byte
	if err := row.Scan(&sc.ID, &sc.Name, &sc.ClusterID, &sc.TargetID, &sc.TargetName, &ns, &sc.IncludeSecrets, &sc.IntervalHours, &sc.Retention, &sc.Enabled, &sc.LastRunAt, &sc.CreatedBy, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
		return Schedule{}, err
	}
	_ = json.Unmarshal(ns, &sc.Namespaces)
	if sc.Namespaces == nil {
		sc.Namespaces = []string{}
	}
	return sc, nil
}

func (s *Store) schedules(ctx context.Context, where string, args ...any) ([]Schedule, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+scheduleColumns+scheduleFrom+where+` ORDER BY s.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) SchedulesForCluster(ctx context.Context, clusterID int64) ([]Schedule, error) {
	return s.schedules(ctx, ` WHERE s.cluster_id = $1`, clusterID)
}

func (s *Store) EnabledSchedules(ctx context.Context) ([]Schedule, error) {
	return s.schedules(ctx, ` WHERE s.enabled AND s.interval_hours > 0`)
}

func (s *Store) Schedule(ctx context.Context, id int64) (*Schedule, error) {
	sc, err := scanSchedule(s.pool.QueryRow(ctx, `SELECT `+scheduleColumns+scheduleFrom+` WHERE s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *Store) CreateSchedule(ctx context.Context, sc *Schedule) (int64, error) {
	ns, _ := json.Marshal(sc.Namespaces)
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO backup_schedules (name, cluster_id, target_id, namespaces, include_secrets, interval_hours, retention, enabled, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')) RETURNING id`,
		sc.Name, sc.ClusterID, sc.TargetID, ns, sc.IncludeSecrets, sc.IntervalHours, sc.Retention, sc.Enabled, sc.CreatedBy).Scan(&id)
	if isUnique(err) {
		return 0, ErrDuplicate
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) UpdateSchedule(ctx context.Context, sc *Schedule) error {
	ns, _ := json.Marshal(sc.Namespaces)
	tag, err := s.pool.Exec(ctx,
		`UPDATE backup_schedules SET name = $2, target_id = $3, namespaces = $4, include_secrets = $5, interval_hours = $6, retention = $7, enabled = $8, updated_at = now() WHERE id = $1`,
		sc.ID, sc.Name, sc.TargetID, ns, sc.IncludeSecrets, sc.IntervalHours, sc.Retention, sc.Enabled)
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSchedule(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM backup_schedules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// StartRun records the start of a run and marks last_run_at (so two
// consecutive ticks do not launch it twice).
func (s *Store) StartRun(ctx context.Context, scheduleID int64, trigger string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO backup_runs (schedule_id, trigger) VALUES ($1, $2) RETURNING id`, scheduleID, trigger).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE backup_schedules SET last_run_at = now() WHERE id = $1`, scheduleID)
	return id, err
}

// FinishRun stores the result.
func (s *Store) FinishRun(ctx context.Context, runID int64, key string, size int64, objects int, skipped map[string]string, runErr error) error {
	status := "ok"
	var errText *string
	if runErr != nil {
		status = "error"
		e := runErr.Error()
		if len(e) > 2000 {
			e = e[:2000]
		}
		errText = &e
	}
	var sk []byte
	if len(skipped) > 0 {
		sk, _ = json.Marshal(skipped)
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE backup_runs SET finished_at = now(), status = $2, object_key = NULLIF($3, ''), size_bytes = $4, objects = $5, skipped = $6, error = $7 WHERE id = $1`,
		runID, status, key, size, objects, sk, errText)
	return err
}

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var key, errText *string
	var size *int64
	var objects *int
	var sk []byte
	if err := row.Scan(&r.ID, &r.ScheduleID, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Trigger, &key, &size, &objects, &sk, &errText); err != nil {
		return Run{}, err
	}
	if key != nil {
		r.ObjectKey = *key
	}
	if size != nil {
		r.SizeBytes = *size
	}
	if objects != nil {
		r.Objects = *objects
	}
	if errText != nil {
		r.Error = *errText
	}
	if len(sk) > 0 {
		_ = json.Unmarshal(sk, &r.Skipped)
	}
	return r, nil
}

const runColumns = `id, schedule_id, started_at, finished_at, status, trigger, object_key, size_bytes, objects, skipped, error`

// RunsForCluster lists the latest runs of every schedule of the cluster.
func (s *Store) RunsForCluster(ctx context.Context, clusterID int64, limit int) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+` FROM backup_runs WHERE schedule_id IN (SELECT id FROM backup_schedules WHERE cluster_id = $1) ORDER BY started_at DESC LIMIT $2`, clusterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Run(ctx context.Context, id int64) (*Run, error) {
	r, err := scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM backup_runs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// PruneRuns removes from the history the runs whose object was deleted from
// the bucket by retention, and the failed ones older than 90 days.
func (s *Store) PruneRuns(ctx context.Context, scheduleID int64, deletedKeys []string) error {
	for _, k := range deletedKeys {
		if _, err := s.pool.Exec(ctx, `DELETE FROM backup_runs WHERE schedule_id = $1 AND object_key = $2`, scheduleID, k); err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM backup_runs WHERE schedule_id = $1 AND status = 'error' AND started_at < now() - interval '90 days'`, scheduleID)
	return err
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
