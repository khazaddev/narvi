//go:build integration

// Integration tests for §4.1.2's own amendment ("PR preview links at the
// latest PR commit", exposure amendment) GET/PUT
// /api/repos/{owner}/{repo}/preview-config (previewconfig.go), against a
// real Postgres instance -- gated behind the "integration" build tag,
// sharing this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/platform"
)

// TestPutPreviewConfig_MaintainerDenied proves a maintainer is refused
// (403) -- authz.ActionConfigurePreviewLinks is admin only (§13.3 row 6),
// per this Step's own "Tests that must exist" requirement.
func TestPutPreviewConfig_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	body := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview.acme.dev","orgSlug":"acme"}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/preview-config", body, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestGetPreviewConfig_MaintainerDenied mirrors TestPutPreviewConfig_
// MaintainerDenied for GET -- this endpoint has no authorizeAny-style
// read-side relaxation the way GetRepoSettings does (see
// previewconfig.go's own doc comment for why: a credential-adjacent
// surface, not an ordinary policy toggle).
func TestGetPreviewConfig_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/widgets/preview-config", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestPutPreviewConfig_AdminAllowed proves the happy path: an admin sets
// all three fields, and the response reflects endpointTemplate/orgSlug
// verbatim plus a non-nil maskedDispatchKey -- never the real key.
func TestPutPreviewConfig_AdminAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	body := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview.acme.dev","orgSlug":"acme","dispatchKey":"rwx-dispatch-key-abc123"}`)
	var got restdtos.PreviewConfig
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/preview-config", body, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.RepoFullName != "acme/widgets" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/widgets")
	}
	if got.EndpointTemplate == nil || *got.EndpointTemplate != "https://{{prNumber}}.preview.acme.dev" {
		t.Errorf("EndpointTemplate = %v, want the submitted value", got.EndpointTemplate)
	}
	if got.OrgSlug == nil || *got.OrgSlug != "acme" {
		t.Errorf("OrgSlug = %v, want \"acme\"", got.OrgSlug)
	}
	if got.MaskedDispatchKey == nil || *got.MaskedDispatchKey == "" {
		t.Fatal("MaskedDispatchKey = nil, want the fixed placeholder (a key was set)")
	}
	if *got.MaskedDispatchKey == "rwx-dispatch-key-abc123" {
		t.Error("MaskedDispatchKey equals the real submitted key -- must be the fixed placeholder, never the real value")
	}

	// GET reflects the same state back.
	var reGet restdtos.PreviewConfig
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/widgets/preview-config", nil, &reGet, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if reGet.MaskedDispatchKey == nil {
		t.Error("GET MaskedDispatchKey = nil, want the fixed placeholder (still configured)")
	}
}

// TestPutPreviewConfig_UnknownRepo404 proves an unknown repo 404s, per
// this Step's own "Tests that must exist" requirement -- resolveKnownRepo
// is reused unchanged from reposettings.go.
func TestPutPreviewConfig_UnknownRepo404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	// Deliberately NOT calling rig.markRepoKnown.

	body := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview.acme.dev","orgSlug":"acme"}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/never-onboarded/preview-config", body, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestGetPreviewConfig_UnknownRepo404 mirrors the PUT case for GET.
func TestGetPreviewConfig_UnknownRepo404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/never-onboarded/preview-config", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestGetPreviewConfig_NoRowYet_NullFields proves a repo with no
// repo_settings row at all renders every field null, never a 404 --
// mirrors GetRepoSettings' own identical "no row yet is not an error
// condition" precedent (reposettings.go).
func TestGetPreviewConfig_NoRowYet_NullFields(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/never-configured")

	var got restdtos.PreviewConfig
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/never-configured/preview-config", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.RepoFullName != "acme/never-configured" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/never-configured")
	}
	if got.EndpointTemplate != nil || got.OrgSlug != nil || got.MaskedDispatchKey != nil {
		t.Errorf("expected every field null for a never-configured repo, got endpointTemplate=%v orgSlug=%v maskedDispatchKey=%v", got.EndpointTemplate, got.OrgSlug, got.MaskedDispatchKey)
	}
}

// TestPutPreviewConfig_AbsentDispatchKeyLeavesStoredValueUnchanged proves
// "absent means unchanged" for dispatchKey -- the one place on this
// surface partial-state semantics are correct, per this Step's own
// "Tests that must exist" requirement. First PUT sets a real key; a
// SECOND PUT that omits dispatchKey entirely (only endpointTemplate/
// orgSlug, rotated to new values) must leave the stored key intact --
// proven both via the DTO's own maskedDispatchKey staying non-nil AND by
// reading the raw stored row directly (the ONLY way to actually confirm
// the underlying value survived, since the wire response never reveals
// it).
func TestPutPreviewConfig_AbsentDispatchKeyLeavesStoredValueUnchanged(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	firstBody := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview.acme.dev","orgSlug":"acme","dispatchKey":"original-dispatch-key"}`)
	if status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/preview-config", firstBody, nil, token); status != http.StatusOK {
		t.Fatalf("first PUT status = %d, want %d", status, http.StatusOK)
	}

	// Second PUT: rotates endpointTemplate/orgSlug, omits dispatchKey
	// entirely.
	secondBody := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview-v2.acme.dev","orgSlug":"acme-v2"}`)
	var got restdtos.PreviewConfig
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/preview-config", secondBody, &got, token)
	if status != http.StatusOK {
		t.Fatalf("second PUT status = %d, want %d", status, http.StatusOK)
	}
	if got.EndpointTemplate == nil || *got.EndpointTemplate != "https://{{prNumber}}.preview-v2.acme.dev" {
		t.Errorf("EndpointTemplate = %v, want the rotated value", got.EndpointTemplate)
	}
	if got.MaskedDispatchKey == nil {
		t.Error("MaskedDispatchKey = nil after omitting dispatchKey -- want the placeholder still present (unchanged)")
	}

	// Confirm the RAW stored value truly survived unchanged (not just the
	// masked placeholder, which would still render even if the real value
	// had been silently blanked to a different non-empty string).
	settings, err := rig.repoSettings.Get(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("read back repo settings: %v", err)
	}
	if settings.RwxPreviewDispatchKey == nil || *settings.RwxPreviewDispatchKey != "original-dispatch-key" {
		t.Errorf("stored RwxPreviewDispatchKey = %v, want \"original-dispatch-key\" (unchanged)", settings.RwxPreviewDispatchKey)
	}
}

// TestPutPreviewConfig_EmptyStringClearsDispatchKey proves the explicit
// clear path: dispatchKey="" removes the stored key, per this Step's own
// "test it" instruction for the CLEAR mechanism.
func TestPutPreviewConfig_EmptyStringClearsDispatchKey(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	firstBody := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview.acme.dev","orgSlug":"acme","dispatchKey":"to-be-cleared"}`)
	if status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/preview-config", firstBody, nil, token); status != http.StatusOK {
		t.Fatalf("first PUT status = %d, want %d", status, http.StatusOK)
	}

	clearBody := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview.acme.dev","orgSlug":"acme","dispatchKey":""}`)
	var got restdtos.PreviewConfig
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/preview-config", clearBody, &got, token)
	if status != http.StatusOK {
		t.Fatalf("clear PUT status = %d, want %d", status, http.StatusOK)
	}
	if got.MaskedDispatchKey != nil {
		t.Errorf("MaskedDispatchKey = %v after clearing, want nil", got.MaskedDispatchKey)
	}

	settings, err := rig.repoSettings.Get(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("read back repo settings: %v", err)
	}
	if settings.RwxPreviewDispatchKey != nil && *settings.RwxPreviewDispatchKey != "" {
		t.Errorf("stored RwxPreviewDispatchKey = %v, want nil/empty (cleared)", settings.RwxPreviewDispatchKey)
	}
	// endpointTemplate/orgSlug are untouched by the clear.
	if settings.RwxPreviewEndpointTemplate == nil || *settings.RwxPreviewEndpointTemplate != "https://{{prNumber}}.preview.acme.dev" {
		t.Errorf("stored RwxPreviewEndpointTemplate = %v, want unchanged", settings.RwxPreviewEndpointTemplate)
	}
}

// TestPreviewConfig_DispatchKeyNeverInAnyResponse proves the real
// dispatch key never appears in ANY response -- asserted on RAW bytes,
// across both the PUT that set it and the subsequent GET -- per this
// Step's own "dispatchKey never appears in any response, on any route,
// including the repo-settings read" requirement (the repo-settings/
// GetRepoSettings half is structurally impossible to violate here since
// that DTO carries no preview-config fields at all -- this proves the
// half that IS reachable: this endpoint's own two routes).
func TestPreviewConfig_DispatchKeyNeverInAnyResponse(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	const realKey = "super-secret-rwx-dispatch-key-zz9x"
	body := []byte(`{"endpointTemplate":"https://{{prNumber}}.preview.acme.dev","orgSlug":"acme","dispatchKey":"` + realKey + `"}`)

	putRaw := rig.doRaw(t, http.MethodPut, "/api/repos/acme/widgets/preview-config", body, token)
	if bytes.Contains(putRaw, []byte(realKey)) {
		t.Errorf("PUT response body contains the real dispatch key: %s", putRaw)
	}

	getRaw := rig.doRaw(t, http.MethodGet, "/api/repos/acme/widgets/preview-config", nil, token)
	if bytes.Contains(getRaw, []byte(realKey)) {
		t.Errorf("GET response body contains the real dispatch key: %s", getRaw)
	}

	// Also confirm the repo-settings endpoint (a DIFFERENT DTO entirely,
	// RepoSettings -- reposettings.go) carries no such field at all, so
	// there is no second route this value could leak through.
	settingsRaw := rig.doRaw(t, http.MethodGet, "/api/repos/acme/widgets/settings", nil, token)
	if bytes.Contains(settingsRaw, []byte(realKey)) {
		t.Errorf("GET /settings response body contains the real dispatch key: %s", settingsRaw)
	}
	if bytes.Contains(settingsRaw, []byte("dispatchKey")) || bytes.Contains(settingsRaw, []byte("maskedDispatchKey")) {
		t.Errorf("GET /settings response mentions dispatchKey at all -- RepoSettings carries no preview-config fields: %s", settingsRaw)
	}
}

// doRaw is integrations_integration_test.go/previewconfig_integration_test.go's
// own shared helper for a raw-bytes response assertion -- doJSON
// (httpapi_integration_test.go) decodes into a caller-supplied struct,
// which cannot distinguish a leaked extra field from an absent one the
// way scanning the raw wire bytes directly can (this package's own
// existing precedent, e.g. workflowdefinitions_integration_test.go's own
// inline io.ReadAll(resp.Body) -- factored out here since this file needs
// it three times).
func (r testRig) doRaw(t *testing.T, method, path string, body []byte, token string) []byte {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, r.server.URL+path, reqBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: platform.AuthSessionCookieName, Value: token})
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return raw
}
