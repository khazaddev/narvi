//go:build integration

package httpapi_test

// This file proves classifiertemplates.go's own two new endpoints (audit
// finding M5, completeness) -- POST /api/intent-templates/preview and
// POST /api/intent-templates -- against a real Postgres instance,
// mirroring this package's own established rig (newTestRig,
// createUserWithRole, rig.doJSON) exactly like members_integration_test.go
// does.
//
// migrations/000033_intent_classifier.up.sql seeds exactly one real
// prompt_templates row ("intent_classifier_system", with the one real
// "{{surface}}" placeholder) -- every test below either edits THAT row
// (the "existing name" cases) or upserts a wholly new name whose text has
// no placeholder at all (the "fresh name" case, see classifiertemplates.
// go's own top doc comment on why a name knownTemplateVars has no entry
// for is accepted as long as its own template text references no
// placeholder).

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

const seededTemplateName = "intent_classifier_system"

type intentTemplatePreviewResponseForTest struct {
	Assembled string `json:"assembled"`
}

type intentTemplateDTOForTest struct {
	Name      string    `json:"name"`
	Template  string    `json:"template"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// promptTemplateRowForTest fetches name's current (template, updated_at)
// directly from Postgres -- used to prove a rejected request left the row
// completely untouched, and to prove an accepted one really did bump it.
func promptTemplateRowForTest(ctx context.Context, t *testing.T, r testRig, name string) (template string, updatedAt time.Time, found bool) {
	t.Helper()
	row := r.pool.QueryRow(ctx, `SELECT template, updated_at FROM prompt_templates WHERE name = $1`, name)
	if err := row.Scan(&template, &updatedAt); err != nil {
		return "", time.Time{}, false
	}
	return template, updatedAt, true
}

// --- authorize()'s admin-only gate ---

// TestIntentTemplatesRoutes_NonAdmin_Returns403 proves both endpoints
// reject a viewer/member/maintainer caller with 403 -- §13.3's own row 6
// ("prompt-template activation: admin only"), rendered via
// authz.ActionActivatePromptTemplate exactly like every other admin-only
// route in this package.
func TestIntentTemplatesRoutes_NonAdmin_Returns403(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	roles := []sqlcgen.UserRole{sqlcgen.UserRoleMember, sqlcgen.UserRoleMaintainer, sqlcgen.UserRoleViewer}
	for _, role := range roles {
		_, token := createUserWithRole(ctx, t, rig, role)

		endpoints := []struct {
			name string
			path string
			body []byte
		}{
			{"Preview", "/api/intent-templates/preview", []byte(`{"name":"` + seededTemplateName + `","template":"hi","vars":{}}`)},
			{"Upsert", "/api/intent-templates", []byte(`{"name":"` + seededTemplateName + `","template":"hi"}`)},
		}
		for _, ep := range endpoints {
			t.Run(string(role)+"/"+ep.name, func(t *testing.T) {
				status := rig.doJSON(t, http.MethodPost, ep.path, ep.body, nil, token)
				if status != http.StatusForbidden {
					t.Errorf("status = %d, want %d (role=%s)", status, http.StatusForbidden, role)
				}
			})
		}
	}
}

// --- PreviewIntentTemplate ---

// TestPreviewIntentTemplate_UnknownPlaceholder_Returns400 proves a draft
// template referencing a placeholder outside its own name's allowed set
// (knownTemplateVars[seededTemplateName] is only {"surface"}) is rejected
// with 400 and the *intentdomain.UnknownPlaceholderError detail -- and
// that this never touches Postgres at all: the seeded row is left
// completely untouched.
func TestPreviewIntentTemplate_UnknownPlaceholder_Returns400(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	beforeTemplate, beforeUpdatedAt, ok := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !ok {
		t.Fatalf("seeded row %q not found before test", seededTemplateName)
	}

	body := []byte(`{"name":"` + seededTemplateName + `","template":"Hello {{bogus}}!","vars":{"bogus":"x"}}`)
	var errBody errorResponseForTest
	status := rig.doJSON(t, http.MethodPost, "/api/intent-templates/preview", body, &errBody, adminToken)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if errBody.Error == "" {
		t.Error("expected a non-empty error message")
	}

	afterTemplate, afterUpdatedAt, ok := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !ok {
		t.Fatalf("seeded row %q disappeared after test", seededTemplateName)
	}
	if afterTemplate != beforeTemplate || !afterUpdatedAt.Equal(beforeUpdatedAt) {
		t.Error("preview must never write to Postgres, but the seeded row changed")
	}
}

// TestPreviewIntentTemplate_Valid_ReturnsAssembledPrompt proves the plain
// success path: a draft template referencing only allowed placeholders,
// with a real preview value supplied for each, assembles to the exact
// expected string -- and, again, never touches the seeded row.
func TestPreviewIntentTemplate_Valid_ReturnsAssembledPrompt(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	beforeTemplate, _, ok := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !ok {
		t.Fatalf("seeded row %q not found before test", seededTemplateName)
	}

	body := []byte(`{"name":"` + seededTemplateName + `","template":"Hello from {{surface}}!","vars":{"surface":"slack"}}`)
	var got intentTemplatePreviewResponseForTest
	status := rig.doJSON(t, http.MethodPost, "/api/intent-templates/preview", body, &got, adminToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if want := "Hello from slack!"; got.Assembled != want {
		t.Errorf("Assembled = %q, want %q", got.Assembled, want)
	}

	afterTemplate, _, ok := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !ok {
		t.Fatalf("seeded row %q disappeared after test", seededTemplateName)
	}
	if afterTemplate != beforeTemplate {
		t.Error("preview must never write to Postgres, but the seeded row's template text changed")
	}
}

// --- UpsertIntentTemplate ---

// TestUpsertIntentTemplate_UnknownPlaceholder_Returns400_NoWrite proves
// ValidateTemplate runs, and rejects, BEFORE templates.Upsert is ever
// called -- the seeded row is left byte-for-byte untouched (template AND
// updated_at) after a 400.
func TestUpsertIntentTemplate_UnknownPlaceholder_Returns400_NoWrite(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	beforeTemplate, beforeUpdatedAt, ok := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !ok {
		t.Fatalf("seeded row %q not found before test", seededTemplateName)
	}

	body := []byte(`{"name":"` + seededTemplateName + `","template":"Hello {{bogus}}!"}`)
	var errBody errorResponseForTest
	status := rig.doJSON(t, http.MethodPost, "/api/intent-templates", body, &errBody, adminToken)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if errBody.Error == "" {
		t.Error("expected a non-empty error message")
	}

	afterTemplate, afterUpdatedAt, ok := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !ok {
		t.Fatalf("seeded row %q disappeared after test", seededTemplateName)
	}
	if afterTemplate != beforeTemplate {
		t.Errorf("template = %q, want unchanged %q (rejected request must never write)", afterTemplate, beforeTemplate)
	}
	if !afterUpdatedAt.Equal(beforeUpdatedAt) {
		t.Errorf("updated_at changed (%v -> %v), want unchanged (rejected request must never write)", beforeUpdatedAt, afterUpdatedAt)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'prompt_template.upserted'`).Scan(&count); err != nil {
		t.Fatalf("count audit log rows: %v", err)
	}
	if count != 0 {
		t.Errorf("audit log rows = %d, want 0 (rejected request must never audit-log either)", count)
	}
}

// TestUpsertIntentTemplate_FreshName_CreatesNewRow proves the REAL schema
// behavior (an audit finding: the plan's own summary imagined "a new
// version is created, the old one disabled," which prompt_templates has
// no columns to support at all -- see classifiertemplates.go's own doc
// comment): a template name with NO existing row, and text with no
// placeholder at all (knownTemplateVars has no entry for this brand-new
// name, so its own allowed-vars set is empty -- a template with zero
// placeholders trivially satisfies that empty set), creates exactly one
// new row.
func TestUpsertIntentTemplate_FreshName_CreatesNewRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	admin, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	const freshName = "batch18_test_template"
	if _, _, found := promptTemplateRowForTest(ctx, t, rig, freshName); found {
		t.Fatalf("precondition failed: %q already exists", freshName)
	}

	body := []byte(`{"name":"` + freshName + `","template":"Just plain text, no placeholders."}`)
	var got intentTemplateDTOForTest
	status := rig.doJSON(t, http.MethodPost, "/api/intent-templates", body, &got, adminToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Name != freshName {
		t.Errorf("Name = %q, want %q", got.Name, freshName)
	}
	if want := "Just plain text, no placeholders."; got.Template != want {
		t.Errorf("Template = %q, want %q", got.Template, want)
	}

	template, _, found := promptTemplateRowForTest(ctx, t, rig, freshName)
	if !found {
		t.Fatal("no row was created in Postgres")
	}
	if template != "Just plain text, no placeholders." {
		t.Errorf("persisted template = %q, want the exact text submitted", template)
	}

	var count int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = 'prompt_template.upserted' AND actor_user_id = $1 AND resource_id = $2`,
		admin.ID, freshName,
	).Scan(&count); err != nil {
		t.Fatalf("count audit log rows: %v", err)
	}
	if count != 1 {
		t.Errorf("audit log rows = %d, want 1", count)
	}
}

// TestUpsertIntentTemplate_ExistingName_OverwritesInPlace proves the OTHER
// half of the same real schema behavior: re-upserting the ALREADY-seeded
// name overwrites its template text and bumps updated_at IN PLACE -- one
// row, no second row, no version history of any kind.
func TestUpsertIntentTemplate_ExistingName_OverwritesInPlace(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	beforeTemplate, beforeUpdatedAt, ok := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !ok {
		t.Fatalf("seeded row %q not found before test", seededTemplateName)
	}

	// A small, real delay -- not a sleep-as-synchronization crutch, just
	// enough to guarantee Postgres' own now() advances between the two
	// timestamps this test compares (a same-microsecond collision would
	// otherwise be a real, if unlikely, source of flakiness here).
	time.Sleep(10 * time.Millisecond)

	newTemplateText := "A brand new system prompt using {{surface}}."
	body := []byte(`{"name":"` + seededTemplateName + `","template":"` + newTemplateText + `"}`)
	var got intentTemplateDTOForTest
	status := rig.doJSON(t, http.MethodPost, "/api/intent-templates", body, &got, adminToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Name != seededTemplateName {
		t.Errorf("Name = %q, want %q", got.Name, seededTemplateName)
	}
	if got.Template != newTemplateText {
		t.Errorf("Template = %q, want %q", got.Template, newTemplateText)
	}

	afterTemplate, afterUpdatedAt, found := promptTemplateRowForTest(ctx, t, rig, seededTemplateName)
	if !found {
		t.Fatal("seeded row disappeared after upsert")
	}
	if afterTemplate == beforeTemplate {
		t.Error("template text did not change")
	}
	if afterTemplate != newTemplateText {
		t.Errorf("persisted template = %q, want %q", afterTemplate, newTemplateText)
	}
	if !afterUpdatedAt.After(beforeUpdatedAt) {
		t.Errorf("updated_at = %v, want strictly after %v", afterUpdatedAt, beforeUpdatedAt)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM prompt_templates WHERE name = $1`, seededTemplateName).Scan(&count); err != nil {
		t.Fatalf("count rows for seeded name: %v", err)
	}
	if count != 1 {
		t.Errorf("rows for %q = %d, want exactly 1 (overwrite in place, never a second row/version)", seededTemplateName, count)
	}
}

// TestListPromptTemplates_MemberDenied proves the real server-side gate:
// a member (below authz.ActionActivatePromptTemplate's admin-only floor,
// the SAME gate preview/upsert above already use) gets a genuine 403 from
// the list endpoint itself.
func TestListPromptTemplates_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	status := rig.doJSON(t, http.MethodGet, "/api/intent-templates", nil, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestListPromptTemplates_AdminAllowed_RendersSeededRow proves an admin
// sees the migration-seeded "intent_classifier_system" row back through
// the NEW /contracts-generated restdtos.PromptTemplate shape -- proving
// this list endpoint and the pre-existing hand-written upsert response
// (intentTemplateDTOForTest above) describe the SAME wire shape, even
// though only one of the two is contracts-generated.
func TestListPromptTemplates_AdminAllowed_RendersSeededRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var resp restdtos.ListPromptTemplatesResponse
	status := rig.doJSON(t, http.MethodGet, "/api/intent-templates", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	found := false
	for _, tpl := range resp.PromptTemplates {
		if tpl.Name != seededTemplateName {
			continue
		}
		found = true
		if tpl.Template == "" {
			t.Errorf("Template is empty for seeded row %q", seededTemplateName)
		}
	}
	if !found {
		t.Fatalf("seeded template %q not present in response: %+v", seededTemplateName, resp.PromptTemplates)
	}
}
