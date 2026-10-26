// Package embeddedpg runs PostgreSQL as a child process of the server for the
// all-in-one mode: `docker run -v bayplan-data:/data ghcr.io/bayplan/bayplan`
// with no DATABASE_URL, so the panel starts with a single command and no
// external database. The image carries the PostgreSQL
// binaries; the chart, Compose and Quadlet keep pointing at a separate
// Postgres, which is still what production should use.
//
// It is the same PostgreSQL the rest of the code expects (migrations,
// partitioned audit_log, FOR UPDATE SKIP LOCKED), not a different storage
// engine: the price of not having two persistence layers is a second process
// inside the container.
package embeddedpg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/bayplan/bayplan/internal/privdir"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	dbName = "bayplan"
	dbUser = "bayplan"
)

// SocketDir is where the server listens. Only a Unix socket, never TCP
// (empty listen_addresses), in a 0700 directory: only processes with the server's
// own UID reach it, which is why local authentication is "trust". It lives in
// the container's /tmp and not in the volume because some volume drivers
// (NFS, SMB) do not support sockets; `docker exec … bayplan reencrypt` shares
// /tmp with the server and finds it there.
var SocketDir = filepath.Join(os.TempDir(), "bayplan-pg")

// Server is a running (or attached-to) embedded PostgreSQL.
type Server struct {
	URL  string
	cmd  *exec.Cmd
	done chan error
}

// URL returns the connection string for the embedded database.
func URL() string {
	return fmt.Sprintf("postgres://%s@/%s?host=%s&sslmode=disable", dbUser, dbName, SocketDir)
}

// Start initializes the cluster under dataDir/postgres the first time and
// starts it. If a server is already answering on the socket (a maintenance
// subcommand run with `docker exec` next to the running server), it attaches
// to it instead and Stop does nothing.
func Start(ctx context.Context, dataDir string, logger *slog.Logger) (*Server, error) {
	// Before attaching to whatever answers on the socket: with "trust" auth,
	// a directory planted by another user in a shared /tmp (the binary run on
	// a host, not in the image) would hand our data to their server.
	if err := ensurePrivateDir(SocketDir); err != nil {
		return nil, err
	}
	if ping(ctx, URL()) == nil {
		return &Server{URL: URL()}, nil
	}

	initdb, err := exec.LookPath("initdb")
	if err != nil {
		return nil, errors.New("embedded postgres: initdb not found; this binary is not running in the official image, so set DATABASE_URL")
	}
	postgres, err := exec.LookPath("postgres")
	if err != nil {
		return nil, errors.New("embedded postgres: postgres not found; set DATABASE_URL")
	}

	pgdata := filepath.Join(dataDir, "postgres")
	if err := os.MkdirAll(pgdata, 0o700); err != nil {
		return nil, fmt.Errorf("embedded postgres: %w (is %s a writable volume?)", err, dataDir)
	}

	binMajor, err := binaryMajor(postgres)
	if err != nil {
		return nil, err
	}
	pgVersion, err := os.ReadFile(filepath.Join(pgdata, "PG_VERSION"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		logger.Info("embedded postgres: initializing data directory", "dir", pgdata, "version", binMajor)
		// C locale: ICU/glibc collation changes between versions are the usual
		// cause of corrupt indexes after an image upgrade; the panel sorts in
		// the frontend anyway.
		out, err := exec.CommandContext(ctx, initdb, "-D", pgdata, "-U", dbUser, "-E", "UTF8", "--locale=C",
			"--auth-local=trust", "--auth-host=reject", "--no-instructions").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("embedded postgres: initdb failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
	case err != nil:
		return nil, fmt.Errorf("embedded postgres: %w", err)
	default:
		if err := checkMajor(strings.TrimSpace(string(pgVersion)), binMajor); err != nil {
			return nil, err
		}
	}

	// log_min_messages=log keeps startup, shutdown and crash lines but not
	// the ERRORs of failed statements: the server already reports those
	// through pgx, and goose's probe for its table on the first start would
	// otherwise read as a failure. No checkpoint lines every 5 minutes either.
	cmd := exec.Command(postgres, "-D", pgdata, "-k", SocketDir,
		"-c", "listen_addresses=", "-c", "unix_socket_permissions=0700",
		"-c", "log_min_messages=log", "-c", "log_checkpoints=off")
	// Its own process group: a Ctrl+C in a terminal must not reach postgres
	// before the server has closed its pool; Stop sends the signal itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	go forwardLogs(pr, logger)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("embedded postgres: %w", err)
	}
	s := &Server{URL: URL(), cmd: cmd, done: make(chan error, 1)}
	go func() { s.done <- cmd.Wait(); pw.Close() }()

	if err := s.waitReady(ctx); err != nil {
		_ = s.Stop()
		return nil, err
	}
	if err := ensureDatabase(ctx); err != nil {
		_ = s.Stop()
		return nil, err
	}
	logger.Info("embedded postgres: ready", "dir", pgdata, "version", binMajor)
	return s, nil
}

func (s *Server) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute) // crash recovery of a big WAL can take a while
	maint := maintenanceURL()
	for {
		select {
		case err := <-s.done:
			s.done <- err
			return fmt.Errorf("embedded postgres exited during startup: %v (see the postgres log lines above)", err)
		default:
		}
		if ping(ctx, maint) == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("embedded postgres: not ready after 2 minutes")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Stop shuts postgres down with a "fast" shutdown (SIGINT: rolls back open
// transactions and checkpoints), waiting for it so that the container does not
// exit with the data directory mid-write.
func (s *Server) Stop() error {
	if s == nil || s.cmd == nil {
		return nil
	}
	_ = s.cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-s.done:
		return nil
	case <-time.After(30 * time.Second):
		_ = s.cmd.Process.Kill()
		return errors.New("embedded postgres: did not stop in 30 s, killed")
	}
}

// ensurePrivateDir creates dir 0700 or accepts an existing one only if it is a
// real directory (not a symlink) owned by our UID and closed to everyone else.
func ensurePrivateDir(dir string) error {
	if err := privdir.Ensure(dir); err != nil {
		return fmt.Errorf("embedded postgres: %w", err)
	}
	return nil
}

func maintenanceURL() string {
	return fmt.Sprintf("postgres://%s@/postgres?host=%s&sslmode=disable", dbUser, SocketDir)
}

func ping(ctx context.Context, url string) error {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(cctx, url)
	if err != nil {
		return err
	}
	return conn.Close(ctx)
}

func ensureDatabase(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, maintenanceURL())
	if err != nil {
		return fmt.Errorf("embedded postgres: %w", err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists); err != nil {
		return fmt.Errorf("embedded postgres: %w", err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		return fmt.Errorf("embedded postgres: failed to create database: %w", err)
	}
	return nil
}

var versionRe = regexp.MustCompile(`\(PostgreSQL\) (\d+)`)

func binaryMajor(postgres string) (string, error) {
	out, err := exec.Command(postgres, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("embedded postgres: %s --version: %w", postgres, err)
	}
	return parseMajor(string(out))
}

// parseMajor extracts the major version from `postgres --version`
// ("postgres (PostgreSQL) 18.6").
func parseMajor(versionOutput string) (string, error) {
	m := versionRe.FindStringSubmatch(versionOutput)
	if m == nil {
		return "", fmt.Errorf("embedded postgres: unrecognized version %q", strings.TrimSpace(versionOutput))
	}
	return m[1], nil
}

// checkMajor refuses to start a data directory created by another major
// version: postgres would refuse too, but with a message that does not say
// what to do.
func checkMajor(dataMajor, binMajor string) error {
	if dataMajor == binMajor {
		return nil
	}
	d, _ := strconv.Atoi(dataMajor)
	b, _ := strconv.Atoi(binMajor)
	if d > b {
		return fmt.Errorf("embedded postgres: the data was written by PostgreSQL %s and this image carries %s; use the image you were running before (downgrades are not supported)", dataMajor, binMajor)
	}
	return fmt.Errorf("embedded postgres: the data was written by PostgreSQL %s and this image carries %s; follow \"Upgrading the embedded PostgreSQL\" in deploy/README.md (backup with the previous image, restore with this one)", dataMajor, binMajor)
}

// forwardLogs passes postgres' own log lines to the server's logger so that
// they end up in `docker logs` in the same format.
func forwardLogs(r io.Reader, logger *slog.Logger) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		// The FATALs while starting up are our own readiness probes.
		starting := strings.Contains(line, "not yet accepting connections") || strings.Contains(line, "is starting up")
		if (strings.Contains(line, "FATAL:") && !starting) || strings.Contains(line, "PANIC:") {
			logger.Error(line, "component", "postgres")
			continue
		}
		logger.Info(line, "component", "postgres")
	}
}
