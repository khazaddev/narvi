//go:build integration

// Integration tests for Step 63's own §22.4 lifecycle REST surface
// (falsepositivepatterns.go), against a real Postgres instance -- gated
// behind the "integration" build tag, sharing this package's own testRig
// (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// seedFalsePositivePattern inserts one active pattern directly via the
// real store -- mirrors this package's own established "seed via the
// store, exercise the HTTP route" convention (e.g. reviewfindings_
// integration_test.go).
func seedFalsePositivePattern(ctx context.Context, t *testing.T, rig testRig, repoFullName, reason string, commentID int64) sqlcgen.ReviewFalsePositivePattern {
	t.Helper()
	// commentType is fixed to "issue_comment" here -- none of this file's
	// own tests exercise comment-type-collision behavior (that is
	// falsepositivecapture_integration_test.go's own job); this helper
	// just needs SOME valid, non-empty value for the NOT NULL column.
	row, _, err := rig.falsePositivePatterns.Upsert(ctx, repoFullName, commentID, "issue_comment", reason, pgtype.UUID{})
	if err != nil {
		t.Fatalf("seed false-positive pattern: %v", err)
	}
	return row
}

func TestListFalsePositivePatterns_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	const repoFullName = "acme/fp-list-repo"
	rig.markRepoKnown(ctx, t, repoFullName)
	seedFalsePositivePattern(ctx, t, rig, repoFullName, "first taught pattern", 810001)
	seedFalsePositivePattern(ctx, t, rig, repoFullName, "second taught pattern", 810002)

	var got restdtos.ListFalsePositivePatternsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/fp-list-repo/false-positive-patterns", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Patterns) != 2 {
		t.Fatalf("len(Patterns) = %d, want 2", len(got.Patterns))
	}
}

func TestListFalsePositivePatterns_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/fp-list-repo/false-positive-patterns", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (member holds no false-positive-pattern action, §13.3 row 5)", status, http.StatusForbidden)
	}
}

func TestListFalsePositivePatterns_ViewerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/fp-list-repo/false-positive-patterns", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestListFalsePositivePatterns_IncludesRetired proves the audit view
// (§22.4) shows EVERY pattern, active or retired -- distinct from the
// advisory-injection read (ListActive), which excludes retired rows.
func TestListFalsePositivePatterns_IncludesRetired(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	admin, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	const repoFullName = "acme/fp-list-retired-repo"
	rig.markRepoKnown(ctx, t, repoFullName)
	row := seedFalsePositivePattern(ctx, t, rig, repoFullName, "will be retired", 810003)
	if _, err := rig.falsePositivePatterns.Retire(ctx, row.ID, admin.ID, repoFullName); err != nil {
		t.Fatalf("retire: %v", err)
	}

	var got restdtos.ListFalsePositivePatternsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/fp-list-retired-repo/false-positive-patterns", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Patterns) != 1 {
		t.Fatalf("len(Patterns) = %d, want 1 (the retired row must still be listed)", len(got.Patterns))
	}
	if got.Patterns[0].RetiredAt == nil {
		t.Error("Patterns[0].RetiredAt = nil, want a real timestamp")
	}
}

func TestRetireFalsePositivePattern_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	const repoFullName = "acme/fp-retire-repo"
	rig.markRepoKnown(ctx, t, repoFullName)
	row := seedFalsePositivePattern(ctx, t, rig, repoFullName, "to retire", 810004)

	var got restdtos.FalsePositivePattern
	status := rig.doJSON(t, http.MethodPost, fmt.Sprintf("/api/repos/acme/fp-retire-repo/false-positive-patterns/%s/retire", row.ID.String()), nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.RetiredAt == nil {
		t.Error("RetiredAt = nil, want a real timestamp")
	}

	fresh, err := rig.falsePositivePatterns.Get(ctx, row.ID, repoFullName)
	if err != nil {
		t.Fatalf("Get after retire: %v", err)
	}
	if !fresh.RetiredAt.Valid {
		t.Error("RetiredAt.Valid = false in the database after a successful retire call")
	}
}

func TestRetireFalsePositivePattern_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	const repoFullName = "acme/fp-retire-denied-repo"
	row := seedFalsePositivePattern(ctx, t, rig, repoFullName, "member must not retire this", 810005)

	status := rig.doJSON(t, http.MethodPost, fmt.Sprintf("/api/repos/acme/fp-retire-denied-repo/false-positive-patterns/%s/retire", row.ID.String()), nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}

	fresh, err := rig.falsePositivePatterns.Get(ctx, row.ID, repoFullName)
	if err != nil {
		t.Fatalf("Get after denied retire: %v", err)
	}
	if fresh.RetiredAt.Valid {
		t.Error("RetiredAt.Valid = true after a DENIED retire attempt -- must remain active")
	}
}

// TestRetireFalsePositivePattern_AlreadyRetired_Conflict proves the
// guarded UPDATE's own WHERE retired_at IS NULL clause surfaces as a 409,
// distinct from a plain 404 -- CLAUDE.md/§11's "guarded UPDATE... for
// cross-writer transitions" rule, exercised end to end through the real
// HTTP route.
func TestRetireFalsePositivePattern_AlreadyRetired_Conflict(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	const repoFullName = "acme/fp-retire-twice-repo"
	rig.markRepoKnown(ctx, t, repoFullName)
	row := seedFalsePositivePattern(ctx, t, rig, repoFullName, "retired twice", 810006)

	first := rig.doJSON(t, http.MethodPost, fmt.Sprintf("/api/repos/acme/fp-retire-twice-repo/false-positive-patterns/%s/retire", row.ID.String()), nil, nil, token)
	if first != http.StatusOK {
		t.Fatalf("first retire status = %d, want %d", first, http.StatusOK)
	}

	second := rig.doJSON(t, http.MethodPost, fmt.Sprintf("/api/repos/acme/fp-retire-twice-repo/false-positive-patterns/%s/retire", row.ID.String()), nil, nil, token)
	if second != http.StatusConflict {
		t.Errorf("second retire status = %d, want %d", second, http.StatusConflict)
	}
}

func TestRetireFalsePositivePattern_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/fp-nonexistent-repo/false-positive-patterns/00000000-0000-0000-0000-000000000000/retire", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestRetireFalsePositivePattern_CrossRepo_NotFound is Fix D's own
// regression proof: this handler's own doc comment promises a 404 "if no
// pattern with this id exists in this repo at all" -- before this fix,
// that was unreachable, because the underlying Get/Retire store methods
// and SQL queries were keyed on the pattern UUID ALONE, with no
// repo_full_name predicate anywhere. A pattern seeded under one repo,
// retired through a DIFFERENT repo's own URL, must now 404 (matching the
// documented contract) instead of succeeding and silently mutating the
// wrong repo's audit trail.
func TestRetireFalsePositivePattern_CrossRepo_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	const realRepo = "acme/fp-crossrepo-real-repo"
	const otherRepo = "acme/fp-crossrepo-other-repo"
	row := seedFalsePositivePattern(ctx, t, rig, realRepo, "belongs to realRepo only", 810008)

	status := rig.doJSON(t, http.MethodPost, fmt.Sprintf("/api/repos/acme/fp-crossrepo-other-repo/false-positive-patterns/%s/retire", row.ID.String()), nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d -- a pattern belonging to %q must 404 when retired through %q's own URL, never succeed", status, http.StatusNotFound, realRepo, otherRepo)
	}

	fresh, err := rig.falsePositivePatterns.Get(ctx, row.ID, realRepo)
	if err != nil {
		t.Fatalf("Get (via the real repo) after cross-repo retire attempt: %v", err)
	}
	if fresh.RetiredAt.Valid {
		t.Error("RetiredAt.Valid = true after a CROSS-REPO retire attempt -- must remain active, never mutated via the wrong repo's URL")
	}
}

// TestFalsePositivePatternStore_IncrementHitCount is the store-level
// proof for §22.4's own hit-count bookkeeping -- internal/app/
// reviewcontext's own unit tests already prove FetchFalsePositivePatterns
// calls IncrementHitCount with the right ids (via a fake); this test
// proves the REAL SQL behind it actually persists hit_count/last_hit_at
// correctly against a real Postgres instance.
func TestFalsePositivePatternStore_IncrementHitCount(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	const repoFullName = "acme/fp-hitcount-repo"
	row := seedFalsePositivePattern(ctx, t, rig, repoFullName, "hit-counted pattern", 810007)

	before, err := rig.falsePositivePatterns.Get(ctx, row.ID, repoFullName)
	if err != nil {
		t.Fatalf("Get before increment: %v", err)
	}
	if before.HitCount != 0 || before.LastHitAt.Valid {
		t.Fatalf("before increment: HitCount=%d LastHitAt.Valid=%v, want 0/false", before.HitCount, before.LastHitAt.Valid)
	}

	if err := rig.falsePositivePatterns.IncrementHitCount(ctx, []pgtype.UUID{row.ID}); err != nil {
		t.Fatalf("IncrementHitCount: %v", err)
	}

	after, err := rig.falsePositivePatterns.Get(ctx, row.ID, repoFullName)
	if err != nil {
		t.Fatalf("Get after increment: %v", err)
	}
	if after.HitCount != 1 {
		t.Errorf("HitCount = %d, want 1", after.HitCount)
	}
	if !after.LastHitAt.Valid {
		t.Error("LastHitAt.Valid = false after increment, want true")
	}

	// A second increment must ADD, never reset.
	if err := rig.falsePositivePatterns.IncrementHitCount(ctx, []pgtype.UUID{row.ID}); err != nil {
		t.Fatalf("second IncrementHitCount: %v", err)
	}
	twice, err := rig.falsePositivePatterns.Get(ctx, row.ID, repoFullName)
	if err != nil {
		t.Fatalf("Get after second increment: %v", err)
	}
	if twice.HitCount != 2 {
		t.Errorf("HitCount after second increment = %d, want 2", twice.HitCount)
	}
}
