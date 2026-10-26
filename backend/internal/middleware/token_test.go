package middleware

import (
	"testing"

	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/session"
)

func TestCapRoleReadOnlyToken(t *testing.T) {
	ro := &session.Session{ViaToken: true, ReadOnly: true}
	full := &session.Session{ViaToken: true}
	if capRole(ro, rbac.RoleAdmin) != rbac.RoleReadonly || capRole(ro, rbac.RoleEditor) != rbac.RoleReadonly {
		t.Fatal("a read-only token caps admin and editor to readonly")
	}
	if capRole(ro, rbac.RoleReadonly) != rbac.RoleReadonly {
		t.Fatal("readonly stays the same")
	}
	if capRole(full, rbac.RoleAdmin) != rbac.RoleAdmin {
		t.Fatal("a full token keeps the user's role")
	}
}
