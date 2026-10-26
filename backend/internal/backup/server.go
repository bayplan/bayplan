package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/settings"
)

// Backups of Bayplan's OWN database (users, settings, audit), not of a
// managed cluster. It is a plain `pg_dump --format=custom`: the
// standard format any DBA can inspect with pg_restore, and it covers
// everything (users, assignments, clusters, settings, the audit log) without a
// list of tables to keep in sync with the migrations.
//
// The encryption key is NOT in the dump, on purpose: the dump carries the
// ciphertexts, and keeping the key apart is what makes a leaked backup
// useless. Restoring needs both.

// ServerBackupFolder is the folder under the target's prefix.
const ServerBackupFolder = "bayplan-server"

// ErrPgDumpMissing is returned when the image does not carry pg_dump (a binary
// built outside the official image).
var ErrPgDumpMissing = errors.New("pg_dump not found in the server's PATH")

// ServerRun is a server_backup_runs row.
type ServerRun struct {
	ID         int64      `json:"id"`
	Trigger    string     `json:"trigger"`
	TargetName string     `json:"targetName"`
	Status     string     `json:"status"`
	ObjectKey  string     `json:"objectKey,omitempty"`
	SizeBytes  int64      `json:"sizeBytes"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// ServerBackups schedules and runs the database dumps.
type ServerBackups struct {
	pool        *pgxpool.Pool
	store       *Store
	settings    *settings.Store
	databaseURL string
	logger      *slog.Logger
	mu          sync.Mutex // one dump at a time
}

// NewServerBackups builds the service. databaseURL is the one the server
// connects with (the embedded one in all-in-one mode).
func NewServerBackups(pool *pgxpool.Pool, store *Store, st *settings.Store, databaseURL string, logger *slog.Logger) *ServerBackups {
	return &ServerBackups{pool: pool, store: store, settings: st, databaseURL: databaseURL, logger: logger}
}

// Available reports whether pg_dump is present.
func (b *ServerBackups) Available() bool {
	_, err := exec.LookPath("pg_dump")
	return err == nil
}

// ValidateSchedule normalizes and checks a schedule.
func ValidateSchedule(c *settings.ServerBackup) error {
	if !c.Enabled {
		return nil
	}
	if c.TargetID <= 0 {
		return errors.New("choose a backup target")
	}
	if c.IntervalHours < 1 || c.IntervalHours > 24*30 {
		return errors.New("intervalHours must be between 1 and 720")
	}
	if c.Keep < 1 || c.Keep > 365 {
		return errors.New("keep must be between 1 and 365")
	}
	return nil
}

// Due reports whether a scheduled run is due given the last run's start.
func Due(c settings.ServerBackup, last *time.Time, now time.Time) bool {
	if !c.Enabled || c.IntervalHours <= 0 || c.TargetID <= 0 {
		return false
	}
	return last == nil || !now.Before(last.Add(time.Duration(c.IntervalHours)*time.Hour))
}

// Start launches the scheduler (checked every minute, like the cluster backups).
func (b *ServerBackups) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				var c settings.ServerBackup
				if err := b.settings.Get(ctx, settings.KeyServerBackup, &c); err != nil {
					continue
				}
				last, err := b.lastStart(ctx)
				if err != nil {
					b.logger.Error("server backup: failed to read history", "error", err)
					continue
				}
				if Due(c, last, now) {
					if _, err := b.Run(ctx, "schedule"); err != nil {
						b.logger.Error("server backup failed", "error", err)
					}
				}
			}
		}
	}()
}

func (b *ServerBackups) lastStart(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := b.pool.QueryRow(ctx, `SELECT max(started_at) FROM server_backup_runs`).Scan(&t)
	return t, err
}

// Runs returns the latest runs.
func (b *ServerBackups) Runs(ctx context.Context, limit int) ([]ServerRun, error) {
	rows, err := b.pool.Query(ctx, `SELECT id, trigger, target_name, status, COALESCE(object_key, ''), COALESCE(size_bytes, 0), COALESCE(error, ''), started_at, finished_at
		FROM server_backup_runs ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ServerRun, error) {
		var r ServerRun
		err := row.Scan(&r.ID, &r.Trigger, &r.TargetName, &r.Status, &r.ObjectKey, &r.SizeBytes, &r.Error, &r.StartedAt, &r.FinishedAt)
		return r, err
	})
}

// Run dumps the database to a temporary file, uploads it to the configured
// target and applies the retention. The run is recorded before starting, so
// that two ticks never launch it twice.
func (b *ServerBackups) Run(ctx context.Context, trigger string) (ServerRun, error) {
	if !b.mu.TryLock() {
		return ServerRun{}, errors.New("a backup of the server is already running")
	}
	defer b.mu.Unlock()

	var c settings.ServerBackup
	if err := b.settings.Get(ctx, settings.KeyServerBackup, &c); err != nil {
		return ServerRun{}, err
	}
	if c.TargetID <= 0 {
		return ServerRun{}, errors.New("no backup target configured")
	}
	target, err := b.store.Target(ctx, c.TargetID)
	if err != nil {
		return ServerRun{}, fmt.Errorf("backup target: %w", err)
	}
	var id int64
	if err := b.pool.QueryRow(ctx, `INSERT INTO server_backup_runs (trigger, target_name) VALUES ($1, $2) RETURNING id`, trigger, target.Name).Scan(&id); err != nil {
		return ServerRun{}, err
	}

	now := time.Now().UTC()
	key := target.key(ServerBackupFolder, fmt.Sprintf("%s-%d.dump", now.Format("20060102T150405Z"), id))
	size, runErr := b.dumpAndUpload(ctx, target, key)
	if runErr == nil && c.Keep > 0 {
		if _, err := pruneSuffix(ctx, target, target.key(ServerBackupFolder), ".dump", c.Keep); err != nil {
			b.logger.Warn("server backup: retention failed", "error", err)
		}
	}
	status, errText := "ok", ""
	if runErr != nil {
		status, errText, key = "error", runErr.Error(), ""
	}
	if _, err := b.pool.Exec(context.WithoutCancel(ctx), `UPDATE server_backup_runs SET status = $2, object_key = NULLIF($3, ''), size_bytes = $4, error = NULLIF($5, ''), finished_at = now() WHERE id = $1`,
		id, status, key, size, errText); err != nil {
		b.logger.Error("server backup: failed to record result", "error", err)
	}
	_, _ = b.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM server_backup_runs WHERE id NOT IN (SELECT id FROM server_backup_runs ORDER BY started_at DESC LIMIT 100)`)

	runs, _ := b.Runs(ctx, 1)
	var run ServerRun
	if len(runs) == 1 {
		run = runs[0]
	}
	return run, runErr
}

func (b *ServerBackups) dumpAndUpload(ctx context.Context, target *Target, key string) (int64, error) {
	// Through a temporary file and not in memory: the audit log makes the dump
	// grow without bound, and S3 wants the length up front.
	f, err := os.CreateTemp("", "bayplan-server-*.dump")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := Dump(ctx, b.databaseURL, f); err != nil {
		return 0, err
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if err := uploadFile(ctx, target, key, f, size); err != nil {
		return 0, err
	}
	return size, nil
}

// uploadFile streams a file of known size to the target.
func uploadFile(ctx context.Context, target *Target, key string, body io.ReadSeeker, size int64) error {
	uctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	_, err := s3Client(target).PutObject(uctx, &s3.PutObjectInput{
		Bucket: aws.String(target.Bucket), Key: aws.String(key), Body: body,
		ContentLength: aws.Int64(size), ContentType: aws.String("application/octet-stream"),
	})
	if err != nil {
		return redactAWS(err)
	}
	return nil
}

// Dump writes `pg_dump --format=custom` of databaseURL to w.
func Dump(ctx context.Context, databaseURL string, w io.Writer) error {
	bin, err := exec.LookPath("pg_dump")
	if err != nil {
		return ErrPgDumpMissing
	}
	env, err := LibpqEnv(databaseURL)
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, bin, "--format=custom", "--no-owner", "--no-privileges")
	cmd.Env = append(childEnv(), env...)
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Restore loads a dump into databaseURL, which must not hold Bayplan tables
// yet: restoring over a live schema would mix two histories, and pg_restore
// --clean cannot drop the immutable audit log's partitions cleanly.
func Restore(ctx context.Context, databaseURL, file string) error {
	bin, err := exec.LookPath("pg_restore")
	if err != nil {
		return errors.New("pg_restore not found in the server's PATH")
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	var exists bool
	err = conn.QueryRow(ctx, `SELECT to_regclass('public.users') IS NOT NULL`).Scan(&exists)
	conn.Close(ctx)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("the database already has Bayplan data: restore into an empty database (a new volume in all-in-one mode)")
	}
	env, err := LibpqEnv(databaseURL)
	if err != nil {
		return err
	}
	cfg, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return err
	}
	// Only the database name in --dbname (host, user and password go in the
	// environment); without --dbname pg_restore prints SQL instead of loading.
	dbname := "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(cfg.Database) + "'"
	cmd := exec.CommandContext(ctx, bin, "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction", "--dbname", dbname, file)
	cmd.Env = append(childEnv(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var sslmodeRE = regexp.MustCompile(`(?:^|\s)sslmode=(\S+)`)

// LibpqEnv turns a connection string into PG* variables for pg_dump and
// pg_restore, so that the password does not show up in the process list as
// it would in a --dbname argument.
func LibpqEnv(databaseURL string) ([]string, error) {
	cfg, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL: %w", err)
	}
	env := []string{
		"PGHOST=" + cfg.Host,
		"PGPORT=" + strconv.Itoa(int(cfg.Port)),
		"PGUSER=" + cfg.User,
		"PGDATABASE=" + cfg.Database,
	}
	if cfg.Password != "" {
		env = append(env, "PGPASSWORD="+cfg.Password)
	}
	// pgconn turns sslmode into a tls.Config; libpq needs the original word.
	mode := ""
	if u, err := url.Parse(databaseURL); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		mode = u.Query().Get("sslmode")
	} else if m := sslmodeRE.FindStringSubmatch(databaseURL); m != nil {
		mode = m[1]
	}
	if mode != "" {
		env = append(env, "PGSSLMODE="+mode)
	}
	return env, nil
}

// DumpTo writes a dump of the server's database to w.
func (b *ServerBackups) DumpTo(ctx context.Context, w io.Writer) error {
	return Dump(ctx, b.databaseURL, w)
}

// childEnv is the environment pg_dump/pg_restore start from: what they need
// to run and find certificates, and nothing of the server's own (the
// encryption keys, DATABASE_URL, cloud credentials would otherwise be
// inherited by the child, like the trivy runner already avoids).
func childEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
