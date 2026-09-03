//go:build integration

package httpapi_test

// This file proves GET /api/me (me.go): the "who am I" endpoint the web
// UI's sign-in view (§12.2 item 7) reads for its identity auto-link panel
// and already-signed-in state. memberDTOForTest/errorResponseForTest are
// members_integration_test.go's own local wire-shape mirrors, reused as-is
// since this file lives in the SAME httpapi_test package (createUserWithRole
// is planapprove_integration_test.go's own shared helper, likewise reused).

import (
	"context"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestGetMe_RequiresAuth proves the route is gated behind auth.Middleware
// exactly like every other route in this package -- no narvi_auth_session
// cookie at all gets the SAME generic 401 body every other rejected route
// in this package returns.
func TestGetMe_RequiresAuth(t *testing.T) {
	rig := newTestRig(t)

	var got errorResponseForTest
	status := rig.doJSON(t, http.MethodGet, "/api/me", nil, &got, "")
	if status != http.StatusUnauthorized {
		t.Errorf("GET /api/me with no auth cookie: status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// TestGetMe_ReturnsOwnProfile proves the response is the CALLER's own row
// -- id/email/displayName/role match what createUserWithRole created, and
// identities carries the github identity that same helper always links
// (so the sign-in view's "github verified" chip has real data to render).
func TestGetMe_ReturnsOwnProfile(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	user, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	var got memberDTOForTest
	status := rig.doJSON(t, http.MethodGet, "/api/me", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/me: status = %d, want %d", status, http.StatusOK)
	}

	if got.ID != user.ID.String() {
		t.Errorf("GET /api/me: id = %q, want %q (the caller's own id)", got.ID, user.ID.String())
	}
	if got.Email != user.PrimaryEmail {
		t.Errorf("GET /api/me: email = %q, want %q", got.Email, user.PrimaryEmail)
	}
	if got.Role != string(sqlcgen.UserRoleMember) {
		t.Errorf("GET /api/me: role = %q, want %q", got.Role, sqlcgen.UserRoleMember)
	}
	if got.Disabled {
		t.Errorf("GET /api/me: disabled = true, want false")
	}
	if len(got.Identities) != 1 || got.Identities[0].Provider != "github" {
		t.Fatalf("GET /api/me: identities = %+v, want exactly one github identity", got.Identities)
	}
}

// TestGetMe_ViewerCanRead proves §13.3 row 1's own "everyone, including
// viewer" -- mirrors modelcatalog_integration_test.go's own identically
// named/shaped test for the SAME matrix row. Unlike /api/me/chatgpt-link
// (ActionLinkChatGPTAccount's own admin/maintainer+owned-member row), a
// viewer must NOT be forbidden here.
func TestGetMe_ViewerCanRead(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	_, viewerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	var got memberDTOForTest
	status := rig.doJSON(t, http.MethodGet, "/api/me", nil, &got, viewerToken)
	if status != http.StatusOK {
		t.Fatalf("GET /api/me as viewer: status = %d, want %d", status, http.StatusOK)
	}
	if got.Role != string(sqlcgen.UserRoleViewer) {
		t.Errorf("GET /api/me as viewer: role = %q, want %q", got.Role, sqlcgen.UserRoleViewer)
	}
}

// TestGetMe_IsSelfScoped_NotAnotherUsersRow proves two different callers
// each get back their OWN row -- this endpoint takes no id of any kind
// (path param or otherwise); it can only ever answer "who is THIS
// request's own bearer cookie", never "show me user X" (that is GET
// /api/members's own admin-only job, a structurally different route).
func TestGetMe_IsSelfScoped_NotAnotherUsersRow(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	userA, tokenA := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)
	userB, tokenB := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	var gotA memberDTOForTest
	if status := rig.doJSON(t, http.MethodGet, "/api/me", nil, &gotA, tokenA); status != http.StatusOK {
		t.Fatalf("GET /api/me (caller A): status = %d, want %d", status, http.StatusOK)
	}
	var gotB memberDTOForTest
	if status := rig.doJSON(t, http.MethodGet, "/api/me", nil, &gotB, tokenB); status != http.StatusOK {
		t.Fatalf("GET /api/me (caller B): status = %d, want %d", status, http.StatusOK)
	}

	if gotA.ID != userA.ID.String() || gotA.ID == userB.ID.String() {
		t.Errorf("GET /api/me (caller A): id = %q, want %q (never B's %q)", gotA.ID, userA.ID.String(), userB.ID.String())
	}
	if gotB.ID != userB.ID.String() || gotB.ID == userA.ID.String() {
		t.Errorf("GET /api/me (caller B): id = %q, want %q (never A's %q)", gotB.ID, userB.ID.String(), userA.ID.String())
	}
}
