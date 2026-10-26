package registries

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/crypto"
)

var (
	ErrNotFound  = errors.New("registries: not found")
	ErrDuplicate = errors.New("registries: already exists")
)

// Store persists registries (password encrypted with the keyring) and bindings.
type Store struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
}

func NewStore(pool *pgxpool.Pool, keyring *crypto.Keyring) *Store {
	return &Store{pool: pool, keyring: keyring}
}

const registryColumns = `r.id, r.name, r.server, r.username, COALESCE(r.email, ''), r.insecure_skip_tls, r.created_at, r.updated_at,
	(SELECT count(*) FROM registry_bindings b WHERE b.registry_id = r.id)`

func scanRegistry(row pgx.Row, dst *Registry, extra ...any) error {
	args := []any{&dst.ID, &dst.Name, &dst.Server, &dst.Username, &dst.Email, &dst.InsecureSkipTLS, &dst.CreatedAt, &dst.UpdatedAt, &dst.BindingCount}
	return row.Scan(append(args, extra...)...)
}

// List returns the registries WITHOUT password.
func (s *Store) List(ctx context.Context) ([]Registry, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+registryColumns+` FROM image_registries r ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Registry{}
	for rows.Next() {
		var r Registry
		if err := scanRegistry(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get loads a registry WITH the password decrypted.
func (s *Store) Get(ctx context.Context, id int64) (*Registry, error) {
	var r Registry
	var enc []byte
	var keyID string
	err := scanRegistry(s.pool.QueryRow(ctx, `SELECT `+registryColumns+`, r.password_encrypted, r.key_id FROM image_registries r WHERE r.id = $1`, id), &r, &enc, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	pw, err := s.keyring.Decrypt(crypto.KeyID(keyID), enc)
	if err != nil {
		return nil, fmt.Errorf("registries: failed to decrypt %q: %w", r.Name, err)
	}
	r.Password = string(pw)
	return &r, nil
}

func (s *Store) Create(ctx context.Context, r *Registry) (int64, error) {
	keyID, enc, err := s.keyring.Encrypt([]byte(r.Password))
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO image_registries (name, server, username, password_encrypted, key_id, email, insecure_skip_tls)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7) RETURNING id`,
		r.Name, r.Server, r.Username, enc, keyID, r.Email, r.InsecureSkipTLS).Scan(&id)
	if isUniqueViolation(err) {
		return 0, ErrDuplicate
	}
	return id, err
}

// Update replaces everything except the name (it is part of the Secret name
// in every cluster; changing it would leave orphan Secrets).
func (s *Store) Update(ctx context.Context, r *Registry) error {
	keyID, enc, err := s.keyring.Encrypt([]byte(r.Password))
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE image_registries SET server = $2, username = $3, password_encrypted = $4, key_id = $5, email = NULLIF($6, ''), insecure_skip_tls = $7, updated_at = now() WHERE id = $1`,
		r.ID, r.Server, r.Username, enc, keyID, r.Email, r.InsecureSkipTLS)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM image_registries WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const bindingColumns = `b.id, b.registry_id, r.name, r.server, b.cluster_id, b.namespace, b.attach_default_sa, b.last_synced_at, COALESCE(b.last_error, ''), COALESCE(b.created_by, ''), b.created_at`
const bindingFrom = ` FROM registry_bindings b JOIN image_registries r ON r.id = b.registry_id`

func scanBinding(row pgx.Row) (Binding, error) {
	var b Binding
	err := row.Scan(&b.ID, &b.RegistryID, &b.RegistryName, &b.Server, &b.ClusterID, &b.Namespace, &b.AttachDefaultSA, &b.LastSyncedAt, &b.LastError, &b.CreatedBy, &b.CreatedAt)
	return b, err
}

func (s *Store) bindings(ctx context.Context, where string, args ...any) ([]Binding, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+bindingColumns+bindingFrom+where+` ORDER BY r.name, b.namespace`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BindingsForCluster lists a cluster's bindings.
func (s *Store) BindingsForCluster(ctx context.Context, clusterID int64) ([]Binding, error) {
	return s.bindings(ctx, ` WHERE b.cluster_id = $1`, clusterID)
}

// BindingsForRegistry lists a registry's bindings (to resync on rotation).
func (s *Store) BindingsForRegistry(ctx context.Context, registryID int64) ([]Binding, error) {
	return s.bindings(ctx, ` WHERE b.registry_id = $1`, registryID)
}

// AllBindings lists all of them (periodic resync).
func (s *Store) AllBindings(ctx context.Context) ([]Binding, error) {
	return s.bindings(ctx, ``)
}

func (s *Store) Binding(ctx context.Context, id int64) (*Binding, error) {
	b, err := scanBinding(s.pool.QueryRow(ctx, `SELECT `+bindingColumns+bindingFrom+` WHERE b.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) CreateBinding(ctx context.Context, b *Binding) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO registry_bindings (registry_id, cluster_id, namespace, attach_default_sa, created_by) VALUES ($1, $2, $3, $4, NULLIF($5, '')) RETURNING id`,
		b.RegistryID, b.ClusterID, b.Namespace, b.AttachDefaultSA, b.CreatedBy).Scan(&id)
	if isUniqueViolation(err) {
		return 0, ErrDuplicate
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) DeleteBinding(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM registry_bindings WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus stores the result of the last sync.
func (s *Store) SetStatus(ctx context.Context, id int64, syncErr error) error {
	var errText *string
	if syncErr != nil {
		e := syncErr.Error()
		if len(e) > 2000 {
			e = e[:2000]
		}
		errText = &e
	}
	_, err := s.pool.Exec(ctx, `UPDATE registry_bindings SET last_synced_at = CASE WHEN $2::text IS NULL THEN now() ELSE last_synced_at END, last_error = $2 WHERE id = $1`, id, errText)
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
