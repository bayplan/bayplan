package scan

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("scan: image has no scan")

// Record is an image_scans row (the report only when requesting the detail).
type Record struct {
	Image          string     `json:"image"`
	Status         string     `json:"status"`
	ScannerVersion string     `json:"scannerVersion,omitempty"`
	ScannedAt      *time.Time `json:"scannedAt,omitempty"`
	Counts         Counts     `json:"counts"`
	Error          string     `json:"error,omitempty"`
	RequestedBy    string     `json:"requestedBy,omitempty"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	Report         *Report    `json:"report,omitempty"`
}

// Store persists the scans.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cols = `image, status, COALESCE(scanner_version, ''), scanned_at, critical, high, medium, low, unknown, fixable, COALESCE(error, ''), COALESCE(requested_by, ''), updated_at`

func scanRecord(row pgx.Row, withReport bool) (Record, error) {
	var r Record
	var report []byte
	args := []any{&r.Image, &r.Status, &r.ScannerVersion, &r.ScannedAt, &r.Counts.Critical, &r.Counts.High, &r.Counts.Medium, &r.Counts.Low, &r.Counts.Unknown, &r.Counts.Fixable, &r.Error, &r.RequestedBy, &r.UpdatedAt}
	if withReport {
		args = append(args, &report)
	}
	if err := row.Scan(args...); err != nil {
		return Record{}, err
	}
	if withReport && len(report) > 0 {
		var rep Report
		if json.Unmarshal(report, &rep) == nil {
			r.Report = &rep
		}
	}
	return r, nil
}

// ForImages returns the existing records for a list of images.
func (s *Store) ForImages(ctx context.Context, images []string) (map[string]Record, error) {
	if len(images) == 0 {
		return map[string]Record{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM image_scans WHERE image = ANY($1)`, images)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Record{}
	for rows.Next() {
		r, err := scanRecord(rows, false)
		if err != nil {
			return nil, err
		}
		out[r.Image] = r
	}
	return out, rows.Err()
}

// Get returns the record with its report.
func (s *Store) Get(ctx context.Context, image string) (*Record, error) {
	r, err := scanRecord(s.pool.QueryRow(ctx, `SELECT `+cols+`, report FROM image_scans WHERE image = $1`, image), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Enqueue marks an image as pending. It returns false if it was already
// queued or being scanned, or if it was scanned less than `minAge` ago and
// force is not set.
func (s *Store) Enqueue(ctx context.Context, image, requestedBy string, minAge time.Duration, force bool) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO image_scans (image, status, requested_by) VALUES ($1, 'queued', NULLIF($2, ''))
		 ON CONFLICT (image) DO UPDATE SET status = 'queued', requested_by = EXCLUDED.requested_by, updated_at = now()
		 WHERE image_scans.status IN ('ok', 'error')
		   AND ($3 OR image_scans.scanned_at IS NULL OR image_scans.scanned_at < now() - $4::interval)`,
		image, requestedBy, force, minAge.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// NextQueued takes the next pending image and marks it as running.
func (s *Store) NextQueued(ctx context.Context) (string, bool, error) {
	var image string
	err := s.pool.QueryRow(ctx,
		`UPDATE image_scans SET status = 'running', updated_at = now()
		 WHERE image = (SELECT image FROM image_scans WHERE status = 'queued' ORDER BY updated_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		 RETURNING image`).Scan(&image)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return image, err == nil, err
}

// Finish stores the result of a scan.
func (s *Store) Finish(ctx context.Context, image, version string, rep Report, scanErr error) error {
	if scanErr != nil {
		msg := scanErr.Error()
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
		_, err := s.pool.Exec(ctx, `UPDATE image_scans SET status = 'error', scanner_version = $2, scanned_at = now(), error = $3, updated_at = now() WHERE image = $1`, image, version, msg)
		return err
	}
	report, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE image_scans SET status = 'ok', scanner_version = $2, scanned_at = now(), critical = $3, high = $4, medium = $5, low = $6, unknown = $7, fixable = $8, report = $9, error = NULL, updated_at = now() WHERE image = $1`,
		image, version, rep.Counts.Critical, rep.Counts.High, rep.Counts.Medium, rep.Counts.Low, rep.Counts.Unknown, rep.Counts.Fixable, report)
	return err
}

// Requeue puts one image back in the queue (a scan interrupted by shutdown:
// it is not a failure of the image).
func (s *Store) Requeue(ctx context.Context, image string) error {
	_, err := s.pool.Exec(ctx, `UPDATE image_scans SET status = 'queued' WHERE image = $1 AND status = 'running'`, image)
	return err
}

// ResetStale returns to the queue whatever was left "running" by a previous process.
func (s *Store) ResetStale(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE image_scans SET status = 'queued' WHERE status = 'running'`)
	return err
}

// Queued counts what is pending.
func (s *Store) Queued(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM image_scans WHERE status IN ('queued', 'running')`).Scan(&n)
	return n, err
}

// StaleImages returns images scanned more than `age` ago (to rescan
// periodically: the CVE database changes even if the image does not).
func (s *Store) StaleImages(ctx context.Context, age time.Duration, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT image FROM image_scans WHERE status = 'ok' AND scanned_at < now() - $1::interval ORDER BY scanned_at LIMIT $2`, age.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var img string
		if err := rows.Scan(&img); err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}
