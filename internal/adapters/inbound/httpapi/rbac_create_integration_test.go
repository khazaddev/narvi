//go:build integration

package httpapi_test

// This file closes the M11 audit finding (completeness): CreateSession
// (create.go) authorizes via authz.ActionCreateSession, Resource{} -- a
// pure role gate, no resource-ownership dimension -- and CreateTurn
// (turn.go) authorizes via authz.ActionPromptSession, Resource{OwnedOrJoined:
// ...} -- an additional resource-ownership dimension on top of the same
// role gate. Yet, before this fix, NEITHER endpoint had a single test
// asserting a 403 anywhere in this package (only TestCreateSession_NoAuth
// existed, a 401/no-auth-cookie-at-all case -- an entirely different
// check: no credentials presented, vs. a real, authenticated caller whose
// role/ownership the matrix denies). Mirrors
// planapprove_integration_test.go's own exact precedent
// (TestApprovePlan_Viewer_NotOwnerOrParticipant_Returns403/
// TestApprovePlan_NonOwnerNonParticipantMember_Returns403): createUserWithRole/
// createSessionForUser/rig.doJSON, reused as-is (same httpapi_test
// package).
//
// Per internal/domain/authz/authorize.go's own matrix (read directly, not
// assumed from the audit-fix plan's own prose): ActionCreateSession allows
// {admin,maintainer,member} unconditionally and has no allowIfOwned set at
// all (row 2's own "creating a session has no ownership concept -- there
// is no pre-existing resource yet to own" reasoning, action.go) -- so
// VIEWER is the ONLY role ever denied it, regardless of any session.
// ActionPromptSession allows {admin,maintainer} unconditionally, plus
// {member} ONLY when Resource.OwnedOrJoined is true -- so, distinctly from
// CreateSession, a plain member who neither created nor joined the target
// session is ALSO denied (viewer is denied unconditionally here too, same
// as CreateSession) -- exactly mirroring ActionApprovePlan's identical
// two-role-excluded-cases shape.
import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestCreateSession_Viewer_Returns403 proves the viewer role -- the ONLY
// role domain/authz's own matrix excludes from ActionCreateSession -- gets
// a real 403 from POST /api/sessions, and that the denied request created
// no session row at all.
func TestCreateSession_Viewer_Returns403(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	viewer, viewerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, viewerToken)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}

	if got := rig.sessionCountForUser(ctx, t, viewer.ID); got != 0 {
		t.Errorf("session count for viewer = %d, want 0 (a 403 must never create a session)", got)
	}
}

// TestCreateTurn_Viewer_Returns403 proves CreateTurn's own identical
// viewer exclusion (ActionPromptSession's own matrix row also excludes
// viewer entirely, regardless of ownership) on a session the viewer does
// not even own -- also proving the "no turn created" half.
func TestCreateTurn_Viewer_Returns403(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, viewerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)

	body := []byte(`{"prompt": "do the thing", "modelId": null, "planMode": false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/turns", body, nil, viewerToken)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("len(turns) = %d, want 0 (a 403 must never create a turn)", len(turns))
	}
}

// TestCreateTurn_NonOwnerNonParticipantMember_Returns403 proves
// ActionPromptSession's own "own/joined" carve-out for member: a plain
// member who neither created nor joined the target session is denied --
// the ONE extra dimension CreateTurn has that CreateSession does not --
// exactly mirroring planapprove_integration_test.go's own
// TestApprovePlan_NonOwnerNonParticipantMember_Returns403 for the sibling
// action, and proving the same "no turn created" invariant.
func TestCreateTurn_NonOwnerNonParticipantMember_Returns403(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, outsiderToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)

	body := []byte(`{"prompt": "do the thing", "modelId": null, "planMode": false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/turns", body, nil, outsiderToken)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("len(turns) = %d, want 0 (a 403 must never create a turn)", len(turns))
	}
}
