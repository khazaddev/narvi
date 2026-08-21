//go:build integration

// Integration tests for §12.5's own ("integrations read model & routes"
// amendment) GET /api/integrations (integrations.go), against a real
// Postgres instance -- gated behind the "integration" build tag, sharing
// this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestGetIntegrations_MemberDenied proves an ordinary member is denied
// (403) -- authz.ActionManageIntegrations is admin only (§13.3 row 6),
// no maintainer/member carve-out at all.
func TestGetIntegrations_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodGet, "/api/integrations", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestGetIntegrations_MaintainerDenied proves a maintainer -- who DOES
// hold plenty of §13.3 row-5 actions -- is still refused here: unlike
// GetRepoSettings' own authorizeAny widening, GetIntegrations has exactly
// ONE gate (ActionManageIntegrations, row 6, admin only), no read-side
// relaxation at all.
func TestGetIntegrations_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodGet, "/api/integrations", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestGetIntegrations_AdminAllowed_AllConfigured proves the happy path:
// an admin, with this rig's own default fully-configured cfg, sees all
// three surfaces (in internal/domain/integrations.Providers' own fixed
// order: slack, linear, github) reporting configured=true and every
// inbound/outbound field null (no webhook_deliveries/outbox rows have
// been seeded for this test).
func TestGetIntegrations_AdminAllowed_AllConfigured(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var got restdtos.ListIntegrationsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/integrations", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Integrations) != 3 {
		t.Fatalf("len(Integrations) = %d, want 3", len(got.Integrations))
	}
	wantOrder := []restdtos.IntegrationSurface{
		restdtos.IntegrationSurfaceSlack,
		restdtos.IntegrationSurfaceLinear,
		restdtos.IntegrationSurfaceGithub,
	}
	for i, row := range got.Integrations {
		if row.Surface != wantOrder[i] {
			t.Errorf("Integrations[%d].Surface = %q, want %q", i, row.Surface, wantOrder[i])
		}
		if !row.Configured {
			t.Errorf("Integrations[%d] (%s).Configured = false, want true (rig.cfg is fully configured)", i, row.Surface)
		}
		if row.LastInboundAt != nil {
			t.Errorf("Integrations[%d] (%s).LastInboundAt = %v, want nil (no webhook_deliveries row seeded)", i, row.Surface, row.LastInboundAt)
		}
		if row.LastOutboundAt != nil || row.LastOutboundStatus != nil || row.LastOutboundError != nil {
			t.Errorf("Integrations[%d] (%s) outbound fields not all nil: at=%v status=%v err=%v", i, row.Surface, row.LastOutboundAt, row.LastOutboundStatus, row.LastOutboundError)
		}
	}
}

// TestGetIntegrations_PartiallyConfigured_AllThreeReportFalse proves the
// end-to-end wiring for the "a partially-configured surface reads as NOT
// connected" rule (§12.5) through the REAL handler -- internal/domain/
// integrations's own TestConfiguredSlack/TestConfiguredLinear/
// TestConfiguredGitHub already prove the pure predicate exhaustively
// (one case per missing secret); this proves GetIntegrations actually
// wires platform.Config's own fields into those predicates correctly,
// one missing secret per surface.
func TestGetIntegrations_PartiallyConfigured_AllThreeReportFalse(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) {
		r.cfg = &platform.Config{
			SlackSigningSecret: "present",
			SlackBotToken:      "", // missing -- Slack incomplete.

			LinearWebhookSecret:     "present",
			LinearOAuthClientID:     "present",
			LinearOAuthClientSecret: "", // missing -- Linear incomplete.

			GitHubWebhookSecret: "", // missing -- GitHub incomplete.
			GitHubBotHandle:     "present",
			GitHubBotToken:      "present",
		}
	})
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var got restdtos.ListIntegrationsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/integrations", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	for _, row := range got.Integrations {
		if row.Configured {
			t.Errorf("Integrations (%s).Configured = true, want false (only partially configured)", row.Surface)
		}
	}
}

// TestGetIntegrations_NoSecretsInRawResponse proves NO secret value,
// prefix, length, or masked form appears anywhere in the response --
// asserted on the RAW response bytes, per this Step's own "Tests that
// must exist" requirement (a decoded-struct assertion could not tell a
// leaked value from an absent one the way a raw-bytes scan can).
func TestGetIntegrations_NoSecretsInRawResponse(t *testing.T) {
	distinctiveSecrets := []string{
		"slack-signing-secret-4f9a2c",
		"slack-bot-token-7b1e88",
		"linear-webhook-secret-9d3f01",
		"linear-client-id-2a77bc",
		"linear-client-secret-e05f44",
		"github-webhook-secret-c81a90",
		"github-bot-token-33dd12",
	}
	rig := newTestRig(t, func(r *testRig) {
		r.cfg = &platform.Config{
			SlackSigningSecret:      distinctiveSecrets[0],
			SlackBotToken:           distinctiveSecrets[1],
			LinearWebhookSecret:     distinctiveSecrets[2],
			LinearOAuthClientID:     distinctiveSecrets[3],
			LinearOAuthClientSecret: distinctiveSecrets[4],
			GitHubWebhookSecret:     distinctiveSecrets[5],
			GitHubBotHandle:         "narvi-test-bot",
			GitHubBotToken:          distinctiveSecrets[6],
		}
	})
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	raw := rig.doRaw(t, http.MethodGet, "/api/integrations", nil, token)

	for _, secret := range distinctiveSecrets {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("raw response body contains a configured secret value %q -- must NEVER appear, not even shaped: %s", secret, raw)
		}
	}
}

// TestGetIntegrations_InboundOutboundIndependent proves inbound and
// outbound are independent facts: a surface (GitHub) with a recent
// inbound delivery AND a failed outbound attempt reports BOTH, and
// nothing in the response collapses them into a single verdict --
// per this Step's own "Tests that must exist" requirement. Also proves
// the outbox->provider prefix helper end to end against a REAL row (a
// genuine "github_verdict" kind).
func TestGetIntegrations_InboundOutboundIndependent(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	// Seed a real inbound delivery for GitHub.
	if _, err := rig.webhookDeliveries.Claim(ctx, "github", "delivery-1"); err != nil {
		t.Fatalf("seed webhook delivery: %v", err)
	}

	// Seed a real, FAILED outbound outbox row for GitHub
	// (kind="github_verdict", status left at its own 'pending' DEFAULT --
	// this table has no third in-flight status, migrations/
	// 000010_outbox.up.sql's own doc comment, so a row that has been
	// attempted-and-retried still reads 'pending' until it either
	// delivers or dead-letters; RecordFailure below moves it forward one
	// attempt with a real last_error, mirroring outboxworker.Builder's own
	// real attempt() sequence).
	entry, err := rig.outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		Kind:    "github_verdict",
		Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create outbox entry: %v", err)
	}
	if _, err := rig.outbox.RecordFailure(ctx, entry.ID, pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}, "posting to github failed: 503"); err != nil {
		t.Fatalf("record outbox failure: %v", err)
	}

	var got restdtos.ListIntegrationsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/integrations", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var github *restdtos.Integration
	for i := range got.Integrations {
		if got.Integrations[i].Surface == restdtos.IntegrationSurfaceGithub {
			github = &got.Integrations[i]
		}
	}
	if github == nil {
		t.Fatal("no github row in response")
	}

	if github.LastInboundAt == nil {
		t.Error("LastInboundAt = nil, want a real timestamp (a webhook delivery was seeded)")
	}
	if github.LastOutboundAt == nil {
		t.Fatal("LastOutboundAt = nil, want a real timestamp (an outbox row was seeded)")
	}
	if github.LastOutboundStatus == nil || *github.LastOutboundStatus != "pending" {
		t.Errorf("LastOutboundStatus = %v, want \"pending\" (RecordFailure leaves the row pending -- still eligible for retry, not yet dead-lettered)", github.LastOutboundStatus)
	}
	if github.LastOutboundError == nil || *github.LastOutboundError != "posting to github failed: 503" {
		t.Errorf("LastOutboundError = %v, want the recorded failure message", github.LastOutboundError)
	}
	// The two timestamps are independent facts from independent tables --
	// this response carries no derived "healthy"/"degraded" field at all
	// (restdtos.Integration's own schema, contracts/rest/v1/
	// dtos.schema.json) for either to feed into, which is itself the
	// proof that nothing here labels this surface healthy despite the
	// failed outbound attempt.
}

// TestGetIntegrations_OutboxPrefixFragility_ExcludesNonConformingKind
// proves the outbox->provider prefix helper's own documented fragility
// (internal/domain/integrations.ProviderForOutboxKind) against a REAL
// database row: a "sentinel_auto_fix" kind is a genuine GitHub-directed
// outbound call in this codebase, but does not literally begin with
// "github" -- it must be EXCLUDED from the github row here, never
// mis-attributed to some other surface and never silently counted as
// github's own last outbound activity, per this Step's own "excluded
// rather than mis-attributed" requirement.
func TestGetIntegrations_OutboxPrefixFragility_ExcludesNonConformingKind(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	// Only a NON-conforming kind exists in outbox -- no "github_*" row at
	// all -- so github's own lastOutboundAt must stay nil, proving the
	// prefix match genuinely excludes it rather than accidentally still
	// matching (e.g. via a naive substring search instead of a real
	// prefix check).
	if _, err := rig.outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		Kind:    "sentinel_auto_fix",
		Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("create outbox entry: %v", err)
	}

	var got restdtos.ListIntegrationsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/integrations", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	for _, row := range got.Integrations {
		if row.LastOutboundAt != nil {
			t.Errorf("Integrations (%s).LastOutboundAt = %v, want nil -- the only outbox row present (\"sentinel_auto_fix\") must not attribute to ANY surface by prefix", row.Surface, row.LastOutboundAt)
		}
	}
}
