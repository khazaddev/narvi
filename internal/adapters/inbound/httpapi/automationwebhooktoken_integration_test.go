//go:build integration

// Integration tests for the webhook-token rotate/revoke pair (review fix:
// "webhook token has no rotation/revocation/expiry", automations.go's own
// RotateAutomationWebhookToken/RevokeAutomationWebhookToken) -- mirrors
// automations_integration_test.go's own black-box, real-HTTP-against-a-
// real-Postgres shape exactly, sharing this package's own testRig. Unlike
// that file, several tests here also drive the REAL inbound webhook
// handler (POST /webhooks/automations/{automationID}, mounted on this
// rig's own router alongside /api/automations -- see httpapi_integration_
// test.go's own newTestRig), proving end to end that a rotated/revoked
// token actually stops authenticating against it, not merely that the
// store layer's own hash no longer matches.
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

const createWebhookAutomationBody = `{"name":"webhook automation","prompt":null,"repos":[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null}],"triggerType":"webhook"}`

// doWebhookPost issues an authenticated (or not, if token == "") POST
// against automationID's own inbound webhook endpoint and returns the
// status code -- this rig's own local copy of internal/adapters/inbound/
// automationwebhook's own identical doPost helper (that package's own test
// file is a separate, self-contained testRig against its own throwaway
// pool; this one deliberately reuses the SAME rig/pool/router
// automations_integration_test.go already shares, so a rotate/revoke test
// can prove its effect against a token minted through the REAL create
// flow).
func (r testRig) doWebhookPost(t *testing.T, automationID, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/webhooks/automations/"+automationID, nil)
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do webhook request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestRotateAutomationWebhookToken_MemberDenied proves the SAME
// member/maintainer split CreateAutomation/Pause/Resume already apply
// gates this route too.
func TestRotateAutomationWebhookToken_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	_, memberToken := rig.createAuthenticatedUser(ctx, t)

	var created restdtos.CreateAutomationResponse
	if status := rig.doJSON(t, http.MethodPost, "/api/automations", []byte(createWebhookAutomationBody), &created, adminToken); status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/automations/"+created.Automation.Id+"/webhook-token", nil, nil, memberToken)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestRotateAutomationWebhookToken_NotFound proves a well-formed but
// nonexistent id is a 404, mirroring GetAutomation's own identical
// precedent.
func TestRotateAutomationWebhookToken_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/automations/00000000-0000-0000-0000-000000000000/webhook-token", nil, nil, token)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestRotateAutomationWebhookToken_NonWebhookAutomationConflict proves
// rotating a token on a manual-trigger automation (which never had a
// webhook_token_hash to begin with) is a 409, not a silent success.
func TestRotateAutomationWebhookToken_NonWebhookAutomationConflict(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var created restdtos.CreateAutomationResponse
	if status := rig.doJSON(t, http.MethodPost, "/api/automations", []byte(createManualAutomationBody), &created, token); status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/automations/"+created.Automation.Id+"/webhook-token", nil, nil, token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d", status, http.StatusConflict)
	}
}

// TestRotateAutomationWebhookToken_OldTokenStopsWorking_NewTokenWorks is
// the review fix's own central round trip: a real webhook automation's
// OLD token authenticates successfully, rotating mints a NEW plaintext
// token and immediately invalidates the old one -- the OLD token now 401s
// against the real inbound webhook endpoint, while the NEW one works.
func TestRotateAutomationWebhookToken_OldTokenStopsWorking_NewTokenWorks(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var created restdtos.CreateAutomationResponse
	if status := rig.doJSON(t, http.MethodPost, "/api/automations", []byte(createWebhookAutomationBody), &created, token); status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}
	if created.WebhookToken == nil || *created.WebhookToken == "" {
		t.Fatalf("WebhookToken = %v, want a real plaintext token", created.WebhookToken)
	}
	oldToken := *created.WebhookToken

	// The old token works, before any rotation.
	if status := rig.doWebhookPost(t, created.Automation.Id, oldToken); status != http.StatusAccepted {
		t.Fatalf("webhook POST with old token (pre-rotate) status = %d, want %d", status, http.StatusAccepted)
	}

	var rotated restdtos.RotateAutomationWebhookTokenResponse
	status := rig.doJSON(t, http.MethodPost, "/api/automations/"+created.Automation.Id+"/webhook-token", nil, &rotated, token)
	if status != http.StatusOK {
		t.Fatalf("rotate status = %d, want %d", status, http.StatusOK)
	}
	if rotated.WebhookToken == "" {
		t.Fatalf("rotated WebhookToken is empty, want a real plaintext token")
	}
	if rotated.WebhookToken == oldToken {
		t.Fatalf("rotated WebhookToken == old token, want a genuinely different value")
	}
	if rotated.Automation.Id != created.Automation.Id {
		t.Fatalf("rotated Automation.Id = %q, want %q", rotated.Automation.Id, created.Automation.Id)
	}

	// The OLD token no longer authenticates -- invalidated immediately, no
	// grace period.
	if status := rig.doWebhookPost(t, created.Automation.Id, oldToken); status != http.StatusUnauthorized {
		t.Fatalf("webhook POST with old token (post-rotate) status = %d, want %d", status, http.StatusUnauthorized)
	}

	// The NEW token authenticates successfully.
	if status := rig.doWebhookPost(t, created.Automation.Id, rotated.WebhookToken); status != http.StatusAccepted {
		t.Fatalf("webhook POST with new (rotated) token status = %d, want %d", status, http.StatusAccepted)
	}
}

// TestRevokeAutomationWebhookToken_MemberDenied proves the SAME
// member/maintainer split gates this route too.
func TestRevokeAutomationWebhookToken_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	_, memberToken := rig.createAuthenticatedUser(ctx, t)

	var created restdtos.CreateAutomationResponse
	if status := rig.doJSON(t, http.MethodPost, "/api/automations", []byte(createWebhookAutomationBody), &created, adminToken); status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	status := rig.doJSON(t, http.MethodDelete, "/api/automations/"+created.Automation.Id+"/webhook-token", nil, nil, memberToken)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestRevokeAutomationWebhookToken_NotFound proves a well-formed but
// nonexistent id is a 404.
func TestRevokeAutomationWebhookToken_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodDelete, "/api/automations/00000000-0000-0000-0000-000000000000/webhook-token", nil, nil, token)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestRevokeAutomationWebhookToken_StopsWorkingPermanentlyUntilRotated is
// the review fix's own other central round trip: revoking clears the
// automation's own webhook_token_hash entirely -- the token 401s against
// the real inbound webhook endpoint, PERMANENTLY (checked twice, proving
// this is not a one-shot/transient rejection), until a subsequent rotate
// mints a genuinely new one.
func TestRevokeAutomationWebhookToken_StopsWorkingPermanentlyUntilRotated(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var created restdtos.CreateAutomationResponse
	if status := rig.doJSON(t, http.MethodPost, "/api/automations", []byte(createWebhookAutomationBody), &created, token); status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}
	if created.WebhookToken == nil || *created.WebhookToken == "" {
		t.Fatalf("WebhookToken = %v, want a real plaintext token", created.WebhookToken)
	}
	webhookToken := *created.WebhookToken

	// The token works, before any revocation.
	if status := rig.doWebhookPost(t, created.Automation.Id, webhookToken); status != http.StatusAccepted {
		t.Fatalf("webhook POST (pre-revoke) status = %d, want %d", status, http.StatusAccepted)
	}

	var revoked restdtos.Automation
	status := rig.doJSON(t, http.MethodDelete, "/api/automations/"+created.Automation.Id+"/webhook-token", nil, &revoked, token)
	if status != http.StatusOK {
		t.Fatalf("revoke status = %d, want %d", status, http.StatusOK)
	}
	if revoked.Id != created.Automation.Id {
		t.Fatalf("revoked Automation.Id = %q, want %q", revoked.Id, created.Automation.Id)
	}

	// The token no longer authenticates -- checked TWICE, proving this is
	// a permanent state, not a one-shot/transient rejection.
	if status := rig.doWebhookPost(t, created.Automation.Id, webhookToken); status != http.StatusUnauthorized {
		t.Fatalf("webhook POST (post-revoke, 1st check) status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := rig.doWebhookPost(t, created.Automation.Id, webhookToken); status != http.StatusUnauthorized {
		t.Fatalf("webhook POST (post-revoke, 2nd check) status = %d, want %d", status, http.StatusUnauthorized)
	}

	// Only a subsequent ROTATE brings this automation back to life, with a
	// genuinely new token -- never the old (now-revoked) one.
	var rotated restdtos.RotateAutomationWebhookTokenResponse
	status = rig.doJSON(t, http.MethodPost, "/api/automations/"+created.Automation.Id+"/webhook-token", nil, &rotated, token)
	if status != http.StatusOK {
		t.Fatalf("rotate-after-revoke status = %d, want %d", status, http.StatusOK)
	}
	if rotated.WebhookToken == "" || rotated.WebhookToken == webhookToken {
		t.Fatalf("rotated WebhookToken = %q, want a real, genuinely new plaintext token", rotated.WebhookToken)
	}
	if status := rig.doWebhookPost(t, created.Automation.Id, rotated.WebhookToken); status != http.StatusAccepted {
		t.Fatalf("webhook POST with newly rotated token (post-revoke) status = %d, want %d", status, http.StatusAccepted)
	}
}
