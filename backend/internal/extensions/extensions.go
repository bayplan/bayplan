// Package extensions manages the links to external tools shown in each
// cluster's sidebar ("UI extensions"). It is deliberately NOT a plugin system:
// the external tool (Grafana, Kibana, Harbor…) already has its own UI
// and what the operator wants is to reach it from the context of the cluster
// they are looking at, not to reimplement it here.
package extensions

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ModeLink  = "link"  // opens the URL in another tab
	ModeEmbed = "embed" // renders it in an iframe inside the panel

	maxNameLen = 60
	maxURLLen  = 2048
)

var ErrNotFound = errors.New("extensions: not found")

// Extension is a ui_extensions row. ClusterID nil = every cluster.
type Extension struct {
	ID        int64     `json:"id"`
	ClusterID *int64    `json:"clusterId,omitempty"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Mode      string    `json:"mode"`
	MinRole   rbac.Role `json:"minRole"`
	SortOrder int       `json:"sortOrder"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Input is what create/edit accept.
type Input struct {
	ClusterID *int64    `json:"clusterId"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Mode      string    `json:"mode"`
	MinRole   rbac.Role `json:"minRole"`
	SortOrder int       `json:"sortOrder"`
}

// Placeholders the frontend substitutes when composing the URL. They are
// validated here so that a {typo} does not end up literally in the link.
var placeholders = map[string]bool{"{cluster}": true, "{clusterId}": true, "{namespace}": true}

// Validate normalizes and checks an Input. It is pure: the URL must be an
// absolute http(s) one (a javascript: in a sidebar link would be XSS served by
// the panel itself) and the placeholders must come from the known list.
func Validate(in *Input) error {
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	if in.Name == "" || len(in.Name) > maxNameLen {
		return fmt.Errorf("name is required (max. %d characters)", maxNameLen)
	}
	if in.URL == "" || len(in.URL) > maxURLLen {
		return fmt.Errorf("url is required (max. %d characters)", maxURLLen)
	}
	// Placeholders are not valid URL (the braces); they are removed to parse.
	probe := in.URL
	for i := strings.Index(probe, "{"); i >= 0; i = strings.Index(probe, "{") {
		j := strings.Index(probe[i:], "}")
		if j < 0 {
			return errors.New("url: unclosed { brace")
		}
		ph := probe[i : i+j+1]
		if !placeholders[ph] {
			return fmt.Errorf("url: unknown placeholder %s (valid: {cluster}, {clusterId}, {namespace})", ph)
		}
		probe = probe[:i] + "x" + probe[i+j+1:]
	}
	u, err := url.Parse(probe)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be absolute and http or https")
	}
	switch in.Mode {
	case "":
		in.Mode = ModeLink
	case ModeLink, ModeEmbed:
	default:
		return errors.New("mode must be link or embed")
	}
	if in.MinRole == "" {
		in.MinRole = rbac.RoleReadonly
	}
	if !in.MinRole.Valid() {
		return errors.New("minRole must be readonly, editor or admin")
	}
	if in.SortOrder < -1000 || in.SortOrder > 1000 {
		return errors.New("sortOrder out of range")
	}
	return nil
}

// Expand substitutes the placeholders. The frontend actually does this (it
// has the namespace filter at hand); this copy exists for the test and for
// whoever consumes the API from outside.
func Expand(raw string, clusterID int64, clusterName, namespace string) string {
	r := strings.NewReplacer(
		"{cluster}", url.PathEscape(clusterName),
		"{clusterId}", fmt.Sprint(clusterID),
		"{namespace}", url.PathEscape(namespace),
	)
	return r.Replace(raw)
}

// Store is the access to ui_extensions.
type Store struct{ DB *pgxpool.Pool }

const cols = "id, cluster_id, name, url, mode, min_role, sort_order, created_at, updated_at"

func scan(row pgx.Row) (Extension, error) {
	var e Extension
	err := row.Scan(&e.ID, &e.ClusterID, &e.Name, &e.URL, &e.Mode, &e.MinRole, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

func collect(rows pgx.Rows) ([]Extension, error) {
	defer rows.Close()
	out := []Extension{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// List returns all of them (admin).
func (s *Store) List(ctx context.Context) ([]Extension, error) {
	rows, err := s.DB.Query(ctx, "SELECT "+cols+" FROM ui_extensions ORDER BY sort_order, name, id")
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// ListForCluster returns the cluster's ones plus the global ones, already
// filtered by the user's role on that cluster.
func (s *Store) ListForCluster(ctx context.Context, clusterID int64, role rbac.Role) ([]Extension, error) {
	rows, err := s.DB.Query(ctx, "SELECT "+cols+" FROM ui_extensions WHERE cluster_id IS NULL OR cluster_id = $1 ORDER BY sort_order, name, id", clusterID)
	if err != nil {
		return nil, err
	}
	all, err := collect(rows)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, e := range all {
		if role.AtLeast(e.MinRole) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Extension, error) {
	e, err := scan(s.DB.QueryRow(ctx, "SELECT "+cols+" FROM ui_extensions WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Extension{}, ErrNotFound
	}
	return e, err
}

func (s *Store) Create(ctx context.Context, in Input) (Extension, error) {
	return scan(s.DB.QueryRow(ctx,
		"INSERT INTO ui_extensions (cluster_id, name, url, mode, min_role, sort_order) VALUES ($1,$2,$3,$4,$5,$6) RETURNING "+cols,
		in.ClusterID, in.Name, in.URL, in.Mode, in.MinRole, in.SortOrder))
}

func (s *Store) Update(ctx context.Context, id int64, in Input) (Extension, error) {
	e, err := scan(s.DB.QueryRow(ctx,
		"UPDATE ui_extensions SET cluster_id=$2, name=$3, url=$4, mode=$5, min_role=$6, sort_order=$7, updated_at=now() WHERE id=$1 RETURNING "+cols,
		id, in.ClusterID, in.Name, in.URL, in.Mode, in.MinRole, in.SortOrder))
	if errors.Is(err, pgx.ErrNoRows) {
		return Extension{}, ErrNotFound
	}
	return e, err
}

func (s *Store) Delete(ctx context.Context, id int64) (Extension, error) {
	e, err := scan(s.DB.QueryRow(ctx, "DELETE FROM ui_extensions WHERE id=$1 RETURNING "+cols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Extension{}, ErrNotFound
	}
	return e, err
}
