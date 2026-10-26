// Package db manages the Postgres connection and the execution of migrations
// embedded in the binary (so the Quadlet deployment is a single artifact, with
// no manual migration step).
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// appName identifies the application's connections in pg_stat_activity and,
// more importantly, in the detail of the audit entries written by the DB
// triggers (migration 0006): it is the only thing that tells a change made by
// the backend from one made by hand with psql, because inet_client_addr() is
// NULL when the connection comes in through a unix socket. The client reports
// it, so it counts as visibility, not as forensic proof.
const appName = "bayplan"

// Connect opens the pgx connection pool used by the rest of the app for
// normal queries (more efficient than database/sql for concurrent use).
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: invalid DATABASE_URL: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams["application_name"] == "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = appName
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: could not create the pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping failed: %w", err)
	}
	return pool, nil
}

// WaitReady waits until the database accepts connections, up to timeout. On a
// first install (the chart's StatefulSet, Compose without depends_on) the
// server usually starts before Postgres and the DNS name or the port is not
// there yet; exiting meant a couple of restarts that read as a broken
// install. A configuration error (bad URL, a password or database Postgres
// rejects) still fails at once: only "nobody answered" is waited for.
func WaitReady(ctx context.Context, databaseURL string, timeout time.Duration, logger *slog.Logger) error {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("db: invalid DATABASE_URL: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := pgx.ConnectConfig(attemptCtx, cfg)
		cancel()
		if err == nil {
			_ = conn.Close(ctx)
			return nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return fmt.Errorf("db: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("db: not reachable after %s: %w", timeout, err)
		}
		if attempt == 0 {
			logger.Warn("waiting for the database", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Migrate applies pending migrations using goose over a database/sql
// connection independent of the pgx pool (goose requires the stdlib driver).
func Migrate(databaseURL string) error {
	// Dedicated application_name for migrations: sql.Open does not allow
	// passing RuntimeParams, so the ConnConfig is registered and opened by the
	// name stdlib returns. It serves to tell, in the audit trail of the DB
	// triggers, what startup/goose touched from what an operator touched with
	// psql.
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("db: invalid DATABASE_URL: %w", err)
	}
	cfg.RuntimeParams["application_name"] = appName + "-migrate"

	sqlDB, err := sql.Open("pgx", stdlib.RegisterConnConfig(cfg))
	if err != nil {
		return fmt.Errorf("db: could not open connection for migrations: %w", err)
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("db: invalid goose dialect: %w", err)
	}

	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return fmt.Errorf("db: failed to apply migrations: %w", err)
	}
	return nil
}

// Ping checks that the database responds; used by /readyz.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	return pool.Ping(ctx)
}

// EnsureAuditLogPartitions creates the monthly audit_log partition for the
// current month and the next one if they do not exist yet (see migration
// 0004). It deliberately does NOT invoke the deletion of old partitions: that
// destructive part of retention lives only in the external job documented in
// deploy/README.md, so that a bug in the binary can never purge audit data by
// accident on every startup/deployment.
func EnsureAuditLogPartitions(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `SELECT audit_log_ensure_future_partitions()`); err != nil {
		return fmt.Errorf("db: failed to ensure audit_log partitions: %w", err)
	}
	return nil
}

var _ = stdlib.GetDefaultDriver // forces registration of the pgx/stdlib driver ("pgx")
