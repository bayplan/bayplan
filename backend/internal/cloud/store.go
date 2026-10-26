package cloud

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
	ErrNotFound  = errors.New("cloud: credential not found")
	ErrDuplicate = errors.New("cloud: a credential with that name already exists")
	// ErrInUse: there are clusters registered with the credential (FK RESTRICT).
	ErrInUse = errors.New("cloud: the credential has clusters attached; delete them or move them to another credential first")
)

// Store persists cloud_credentials with the secret encrypted by the keyring.
type Store struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
}

func NewStore(pool *pgxpool.Pool, keyring *crypto.Keyring) *Store {
	return &Store{pool: pool, keyring: keyring}
}

const listColumns = `c.id, c.name, c.provider, c.config, c.created_by, c.created_at, c.updated_at,
	(SELECT count(*) FROM clusters k WHERE k.cloud_credential_id = c.id)`

func scanListRow(row pgx.Row) (Credential, error) {
	var c Credential
	var createdBy *string
	var cfg []byte
	if err := row.Scan(&c.ID, &c.Name, &c.Provider, &cfg, &createdBy, &c.CreatedAt, &c.UpdatedAt, &c.ClusterCount); err != nil {
		return Credential{}, err
	}
	if err := json.Unmarshal(cfg, &c.Config); err != nil {
		return Credential{}, fmt.Errorf("cloud: corrupt config in credential %d: %w", c.ID, err)
	}
	if createdBy != nil {
		c.CreatedBy = *createdBy
	}
	return c, nil
}

// List returns every credential WITHOUT its secret.
func (s *Store) List(ctx context.Context) ([]Credential, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+listColumns+` FROM cloud_credentials c ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Credential{}
	for rows.Next() {
		c, err := scanListRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get loads a credential WITH the secret decrypted, to use it.
func (s *Store) Get(ctx context.Context, id int64) (*Credential, error) {
	var secretEnc []byte
	var keyID string
	row := s.pool.QueryRow(ctx, `SELECT `+listColumns+`, c.secret_encrypted, c.key_id FROM cloud_credentials c WHERE c.id = $1`, id)
	var c Credential
	var createdBy *string
	var cfg []byte
	err := row.Scan(&c.ID, &c.Name, &c.Provider, &cfg, &createdBy, &c.CreatedAt, &c.UpdatedAt, &c.ClusterCount, &secretEnc, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cfg, &c.Config); err != nil {
		return nil, fmt.Errorf("cloud: corrupt config in credential %d: %w", id, err)
	}
	if createdBy != nil {
		c.CreatedBy = *createdBy
	}
	plain, err := s.keyring.Decrypt(crypto.KeyID(keyID), secretEnc)
	if err != nil {
		return nil, fmt.Errorf("cloud: failed decrypting credential %q: %w", c.Name, err)
	}
	if err := json.Unmarshal(plain, &c.Secret); err != nil {
		return nil, fmt.Errorf("cloud: corrupt secret in credential %d: %w", id, err)
	}
	return &c, nil
}

func (s *Store) encrypt(sec Secret) ([]byte, string, error) {
	plain, err := json.Marshal(sec)
	if err != nil {
		return nil, "", err
	}
	keyID, enc, err := s.keyring.Encrypt(plain)
	return enc, keyID, err
}

// Create inserts the credential (secret encrypted) and returns its id.
func (s *Store) Create(ctx context.Context, c *Credential) (int64, error) {
	enc, keyID, err := s.encrypt(c.Secret)
	if err != nil {
		return 0, err
	}
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO cloud_credentials (name, provider, config, secret_encrypted, key_id, created_by)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')) RETURNING id`,
		c.Name, string(c.Provider), cfg, enc, keyID, c.CreatedBy).Scan(&id)
	if isUniqueViolation(err) {
		return 0, ErrDuplicate
	}
	return id, err
}

// Update replaces name, config and secret (the caller has already merged what
// changes over what was there).
func (s *Store) Update(ctx context.Context, c *Credential) error {
	enc, keyID, err := s.encrypt(c.Secret)
	if err != nil {
		return err
	}
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE cloud_credentials SET name = $2, config = $3, secret_encrypted = $4, key_id = $5, updated_at = now() WHERE id = $1`,
		c.ID, c.Name, cfg, enc, keyID)
	if isUniqueViolation(err) {
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

// Delete removes the credential; fails with ErrInUse if clusters use it.
func (s *Store) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM cloud_credentials WHERE id = $1`, id)
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

// ClustersUsing returns, by reference (Ref.Key), the id of every cluster
// already registered with the credential. Used to mark "already added" when
// discovering.
func (s *Store) ClustersUsing(ctx context.Context, credID int64) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, cloud_ref FROM clusters WHERE cloud_credential_id = $1`, credID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var ref Ref
		if err := json.Unmarshal(raw, &ref); err != nil {
			continue
		}
		out[ref.Key()] = id
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
