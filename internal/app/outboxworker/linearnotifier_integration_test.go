//go:build integration

// This file (audit finding M13) proves linearNotifier's own four distinct
// Deliver branches -- payload decode failure, LinearInstallationStore.
// GetByOrganizationID returning pgx.ErrNoRows (no admin has connected this
// workspace), platform.DecryptToken failure, and routing payload.Success to
// CreateResponseActivity vs CreateErrorActivity -- DIRECTLY: constructed via
// NewLinearNotifier, driven via its own .Deliver, never through
// Builder/fakeNotifier (which only exercises Builder's own dispatch/retry
// logic, not linearNotifier's own internal decode/lookup/decrypt/route
// behavior at all).
//
// n.installations is a concrete *postgres.LinearInstallationStore wrapping
// a real *pgxpool.Pool -- there is no interface to fake, so the
// ErrNoRows/lookup-succeeds branches need a REAL Postgres, hence this file
// carries the "integration" build tag and reuses builder_integration_test.
// go's own newTestPool helper exactly (same package, same house style).
// The real Linear API itself is faked via httptest.NewServer, mirroring
// internal/adapters/outbound/linearapi's own activity_test.go pattern:
// point a real *linearapi.Client at a local fake HTTP server and assert
// which content type (response vs error) it posted.
package outboxworker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/outboxworker"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// linearTokenEncryptionKey is THE correct 32-byte AES-256-GCM key every
// test below constructs its own linearNotifier with, unless deliberately
// proving the decrypt-failure branch (which instead encrypts under
// linearWrongTokenEncryptionKey and decrypts with this one).
var linearTokenEncryptionKey = []byte("linear-notifier-test-key-32-byte")

// linearWrongTokenEncryptionKey is a DIFFERENT valid 32-byte key --
// encrypting under this one and decrypting under
// linearTokenEncryptionKey above reproduces a genuine
// platform.DecryptToken authentication failure (AES-GCM's own tag check),
// not merely a malformed-ciphertext error.
var linearWrongTokenEncryptionKey = []byte("a-completely-different-32-bytes!")

// seedLinearInstallation upserts a linear_installations row for
// organizationID with accessTokenEncrypted as its stored (already
// encrypted) access token -- this file's own only way to populate the
// real Postgres row linearNotifier.Deliver's GetByOrganizationID call
// looks up.
func seedLinearInstallation(ctx context.Context, t *testing.T, pool *pgxpool.Pool, organizationID string, accessTokenEncrypted []byte) sqlcgen.LinearInstallation {
	t.Helper()

	row, err := narvipg.NewLinearInstallationStore(pool).Upsert(ctx, sqlcgen.UpsertLinearInstallationParams{
		OrganizationID:       organizationID,
		AppUserID:            "app-user-1",
		AccessTokenEncrypted: accessTokenEncrypted,
		ExpiresAt:            pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("seed linear installation: %v", err)
	}
	return row
}

// linearPayload marshals a linearapi.Payload for organizationID/success --
// this file's own only outbox payload shape.
func linearPayload(t *testing.T, organizationID string, success bool) []byte {
	t.Helper()
	payload, err := json.Marshal(linearapi.Payload{
		AgentSessionID: "agent-session-1",
		OrganizationID: organizationID,
		Text:           "outcome text",
		Success:        success,
	})
	if err != nil {
		t.Fatalf("marshal linear payload: %v", err)
	}
	return payload
}

// TestLinearNotifier_Deliver_DecodeFailure_ReturnsError proves branch (1):
// a payload that is not valid JSON is a decode failure, returned as a
// real error, before ever touching the installation lookup.
func TestLinearNotifier_Deliver_DecodeFailure_ReturnsError(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"agentActivityCreate": map[string]any{"success": true}}})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)
	installations := narvipg.NewLinearInstallationStore(pool)
	notifier := outboxworker.NewLinearNotifier(client, installations, linearTokenEncryptionKey)

	err := notifier.Deliver(ctx, ports.Notification{
		Kind:    ports.NotificationKindLinear,
		Payload: []byte(`{not valid json`),
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want non-nil (malformed payload)")
	}
	if requests != 0 {
		t.Fatalf("linear API requests = %d, want 0 (must never call the API for an undecodable payload)", requests)
	}
}

// TestLinearNotifier_Deliver_InstallationNotFound_ReturnsError proves
// branch (2): no admin has connected this workspace (no
// linear_installations row for this organization_id) -- a real
// pgx.ErrNoRows from GetByOrganizationID, returned wrapped as a real
// delivery failure like any other, never silently swallowed, and never
// reaching the Linear API at all.
func TestLinearNotifier_Deliver_InstallationNotFound_ReturnsError(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"agentActivityCreate": map[string]any{"success": true}}})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)
	installations := narvipg.NewLinearInstallationStore(pool)
	notifier := outboxworker.NewLinearNotifier(client, installations, linearTokenEncryptionKey)

	err := notifier.Deliver(ctx, ports.Notification{
		Kind:    ports.NotificationKindLinear,
		Payload: linearPayload(t, "org-never-connected", true),
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want non-nil (no installation row for this organization)")
	}
	if requests != 0 {
		t.Fatalf("linear API requests = %d, want 0 (must never call the API without a resolved installation)", requests)
	}
}

// TestLinearNotifier_Deliver_DecryptFailure_ReturnsError proves branch
// (3): the installation lookup itself SUCCEEDS (a real row exists), but
// its stored access_token_encrypted was encrypted under a DIFFERENT key
// than this notifier's own tokenEncryptionKey -- a genuine
// platform.DecryptToken authentication failure (AES-GCM's own tag check),
// not just a malformed ciphertext -- returned as a real error, never
// reaching the Linear API.
func TestLinearNotifier_Deliver_DecryptFailure_ReturnsError(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	ciphertextUnderWrongKey, err := platform.EncryptToken(linearWrongTokenEncryptionKey, []byte("plaintext-access-token"))
	if err != nil {
		t.Fatalf("encrypt token under wrong key: %v", err)
	}
	seedLinearInstallation(ctx, t, pool, "org-mismatched-key", ciphertextUnderWrongKey)

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"agentActivityCreate": map[string]any{"success": true}}})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)
	installations := narvipg.NewLinearInstallationStore(pool)
	// Deliberately constructed with the CORRECT key -- the mismatch lives
	// entirely in how the stored ciphertext above was encrypted.
	notifier := outboxworker.NewLinearNotifier(client, installations, linearTokenEncryptionKey)

	err = notifier.Deliver(ctx, ports.Notification{
		Kind:    ports.NotificationKindLinear,
		Payload: linearPayload(t, "org-mismatched-key", true),
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want non-nil (ciphertext encrypted under a different key)")
	}
	if requests != 0 {
		t.Fatalf("linear API requests = %d, want 0 (must never call the API with a token that failed to decrypt)", requests)
	}
}

// linearActivityRequest captures one agentActivityCreate GraphQL call's
// own relevant fields -- this file's own assertion shape for branch (4).
type linearActivityRequest struct {
	auth        string
	contentType string
	body        string
}

// captureLinearActivityServer starts a fake Linear GraphQL API recording
// every agentActivityCreate call it receives (mirrors linearapi's own
// activity_test.go decode-and-assert pattern) and always responds with a
// successful mutation result.
func captureLinearActivityServer(t *testing.T, calls *[]linearActivityRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var gotBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		variables, _ := gotBody["variables"].(map[string]any)
		input, _ := variables["input"].(map[string]any)
		content, _ := input["content"].(map[string]any)

		*calls = append(*calls, linearActivityRequest{
			auth:        r.Header.Get("Authorization"),
			contentType: fmt.Sprintf("%v", content["type"]),
			body:        fmt.Sprintf("%v", content["body"]),
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"agentActivityCreate": map[string]any{"success": true}},
		})
	}))
}

// TestLinearNotifier_Deliver_Success_RoutesToResponseActivity proves
// branch (4)'s success side: payload.Success == true routes to
// CreateResponseActivity -- a "response"-typed AgentActivity -- using the
// FRESHLY decrypted access token from the real installation row.
func TestLinearNotifier_Deliver_Success_RoutesToResponseActivity(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	const plaintextToken = "real-linear-access-token"
	ciphertext, err := platform.EncryptToken(linearTokenEncryptionKey, []byte(plaintextToken))
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	seedLinearInstallation(ctx, t, pool, "org-success", ciphertext)

	var calls []linearActivityRequest
	server := captureLinearActivityServer(t, &calls)
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)
	installations := narvipg.NewLinearInstallationStore(pool)
	notifier := outboxworker.NewLinearNotifier(client, installations, linearTokenEncryptionKey)

	err = notifier.Deliver(ctx, ports.Notification{
		Kind:    ports.NotificationKindLinear,
		Payload: linearPayload(t, "org-success", true),
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}

	if len(calls) != 1 {
		t.Fatalf("linear API calls = %d, want exactly 1", len(calls))
	}
	got := calls[0]
	if got.auth != "Bearer "+plaintextToken {
		t.Errorf("Authorization = %q, want %q", got.auth, "Bearer "+plaintextToken)
	}
	if got.contentType != "response" {
		t.Errorf("content.type = %q, want %q (payload.Success == true must route to CreateResponseActivity)", got.contentType, "response")
	}
	if got.body != "outcome text" {
		t.Errorf("content.body = %q, want %q", got.body, "outcome text")
	}
}

// TestLinearNotifier_Deliver_Failure_RoutesToErrorActivity is
// TestLinearNotifier_Deliver_Success_RoutesToResponseActivity's mirror:
// payload.Success == false routes to CreateErrorActivity -- an
// "error"-typed AgentActivity.
func TestLinearNotifier_Deliver_Failure_RoutesToErrorActivity(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	ciphertext, err := platform.EncryptToken(linearTokenEncryptionKey, []byte("real-linear-access-token"))
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	seedLinearInstallation(ctx, t, pool, "org-failure", ciphertext)

	var calls []linearActivityRequest
	server := captureLinearActivityServer(t, &calls)
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)
	installations := narvipg.NewLinearInstallationStore(pool)
	notifier := outboxworker.NewLinearNotifier(client, installations, linearTokenEncryptionKey)

	err = notifier.Deliver(ctx, ports.Notification{
		Kind:    ports.NotificationKindLinear,
		Payload: linearPayload(t, "org-failure", false),
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}

	if len(calls) != 1 {
		t.Fatalf("linear API calls = %d, want exactly 1", len(calls))
	}
	if got := calls[0].contentType; got != "error" {
		t.Errorf("content.type = %q, want %q (payload.Success == false must route to CreateErrorActivity)", got, "error")
	}
}
