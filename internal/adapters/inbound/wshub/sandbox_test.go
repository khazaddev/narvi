//go:build integration

// Integration tests proving internal/adapters/inbound/wshub's sandbox
// handshake (§6.1) against a REAL Postgres instance -- gated behind the
// "integration" build tag, matching internal/app/sessionactor's own
// testcontainers-Postgres conventions exactly. Run via `make
// test-integration`.
package wshub_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/coder/websocket"

	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// baseHeaders returns a fresh, valid set of the 3 sandbox-ws handshake
// headers (§6.1) -- fresh every call, since callers mutate their own copy.
func baseHeaders() map[string]string {
	return map[string]string{
		"Authorization": "Bearer some-token",
		"X-Sandbox-ID":  "sbx-test",
		"X-Sandbox-Gen": "1",
	}
}

func headersWithout(key string) map[string]string {
	h := baseHeaders()
	delete(h, key)
	return h
}

func headersOverride(key, val string) map[string]string {
	h := baseHeaders()
	h[key] = val
	return h
}

// doHandshake issues a plain (non-upgrading) GET against wsURL's http(s)
// equivalent + path + query, with the given headers, and returns the
// response status code. Every one of this Step's own rejection statuses
// (400/401/404/403/410/503/500) is returned BEFORE websocket.Accept ever
// runs (see sandbox.go's own doc comment on that ordering), so a plain
// http.Client request -- no WS upgrade headers needed -- is sufficient to
// observe every one of them.
func doHandshake(t *testing.T, wsURL, path, query string, headers map[string]string) int {
	t.Helper()

	url := "http" + wsURL[len("ws"):] + path
	if query != "" {
		url += "?" + query
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestSandboxHandler_HandshakeRejections is table-driven over every
// rejection this Step's own handshake table names that does NOT depend on
// a specific sandbox-row DB state (those get their own dedicated tests
// below): missing/malformed `type`, malformed session id, missing/
// malformed Authorization, missing X-Sandbox-ID, missing/malformed
// X-Sandbox-Gen.
func TestSandboxHandler_HandshakeRejections(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	createTestSandbox(ctx, t, pool, sessionID) // gen 1, Pending

	registry := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = registry.Shutdown() })
	sandboxStore := narvipg.NewSandboxStore(pool)

	server, wsURL := newTestServer(registry, sandboxStore, platform.DefaultTimeouts())
	t.Cleanup(server.Close)

	validPath := "/sessions/" + sessionID.String() + "/ws"

	tests := []struct {
		name       string
		path       string
		query      string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "missing type query param",
			path:       validPath,
			query:      "",
			headers:    baseHeaders(),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed type query param (client, not yet implemented)",
			path:       validPath,
			query:      "type=client",
			headers:    baseHeaders(),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed session id",
			path:       "/sessions/not-a-valid-uuid/ws",
			query:      "type=sandbox",
			headers:    baseHeaders(),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing authorization header",
			path:       validPath,
			query:      "type=sandbox",
			headers:    headersWithout("Authorization"),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed authorization scheme",
			path:       validPath,
			query:      "type=sandbox",
			headers:    headersOverride("Authorization", "Basic abc123"),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "empty bearer token",
			path:       validPath,
			query:      "type=sandbox",
			headers:    headersOverride("Authorization", "Bearer "),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing X-Sandbox-ID",
			path:       validPath,
			query:      "type=sandbox",
			headers:    headersWithout("X-Sandbox-ID"),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing X-Sandbox-Gen",
			path:       validPath,
			query:      "type=sandbox",
			headers:    headersWithout("X-Sandbox-Gen"),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed X-Sandbox-Gen",
			path:       validPath,
			query:      "type=sandbox",
			headers:    headersOverride("X-Sandbox-Gen", "not-a-number"),
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := doHandshake(t, wsURL, tc.path, tc.query, tc.headers)
			if got != tc.wantStatus {
				t.Errorf("status = %d, want %d", got, tc.wantStatus)
			}
		})
	}
}

// TestSandboxHandler_SessionNotFound proves a session id that parses as a
// valid UUID but has no corresponding sandbox row (pgx.ErrNoRows) -> 404.
func TestSandboxHandler_SessionNotFound(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	registry := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = registry.Shutdown() })
	sandboxStore := narvipg.NewSandboxStore(pool)

	server, wsURL := newTestServer(registry, sandboxStore, platform.DefaultTimeouts())
	t.Cleanup(server.Close)

	const nonexistentID = "11111111-1111-1111-1111-111111111111"
	got := doHandshake(t, wsURL, "/sessions/"+nonexistentID+"/ws", "type=sandbox", baseHeaders())
	if got != http.StatusNotFound {
		t.Errorf("status = %d, want %d", got, http.StatusNotFound)
	}
}

// TestSandboxHandler_DeadSandboxStatus proves a sandbox in any of the 3
// dead statuses (Stopped/Stale/Failed) -> 410, even with an otherwise
// perfectly valid handshake.
func TestSandboxHandler_DeadSandboxStatus(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	createTestSandbox(ctx, t, pool, sessionID)
	moveSandboxStatus(ctx, t, pool, sessionID, sqlcgen.SandboxStatusFailed)

	registry := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = registry.Shutdown() })
	sandboxStore := narvipg.NewSandboxStore(pool)

	server, wsURL := newTestServer(registry, sandboxStore, platform.DefaultTimeouts())
	t.Cleanup(server.Close)

	got := doHandshake(t, wsURL, "/sessions/"+sessionID.String()+"/ws", "type=sandbox", baseHeaders())
	if got != http.StatusGone {
		t.Errorf("status = %d, want %d (dead sandbox status)", got, http.StatusGone)
	}
}

// TestSandboxHandler_GenMismatch proves a presented X-Sandbox-Gen that
// doesn't match the sandbox row's current gen -> 403 (§9.3 scenario #6).
func TestSandboxHandler_GenMismatch(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	createTestSandbox(ctx, t, pool, sessionID) // gen 1

	registry := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = registry.Shutdown() })
	sandboxStore := narvipg.NewSandboxStore(pool)

	server, wsURL := newTestServer(registry, sandboxStore, platform.DefaultTimeouts())
	t.Cleanup(server.Close)

	got := doHandshake(t, wsURL, "/sessions/"+sessionID.String()+"/ws", "type=sandbox",
		headersOverride("X-Sandbox-Gen", "999"))
	if got != http.StatusForbidden {
		t.Errorf("status = %d, want %d (gen mismatch)", got, http.StatusForbidden)
	}
}

// TestSandboxHandler_BadToken proves a presented bearer token that does
// NOT match a REAL stored token_hash -> 401 (distinct from the nil-hash
// "accept anything" bridge, which TestSandboxHandler_ValidHandshakeUpgrades
// below exercises).
func TestSandboxHandler_BadToken(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	createTestSandbox(ctx, t, pool, sessionID)
	setSandboxTokenHash(ctx, t, pool, sessionID, wshub.HashSandboxToken("correct-token"))

	registry := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = registry.Shutdown() })
	sandboxStore := narvipg.NewSandboxStore(pool)

	server, wsURL := newTestServer(registry, sandboxStore, platform.DefaultTimeouts())
	t.Cleanup(server.Close)

	got := doHandshake(t, wsURL, "/sessions/"+sessionID.String()+"/ws", "type=sandbox",
		headersOverride("Authorization", "Bearer wrong-token"))
	if got != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d (wrong token against a real stored hash)", got, http.StatusUnauthorized)
	}
}

// TestSandboxHandler_ValidHandshakeUpgrades proves a fully valid handshake
// reaches a real 101 upgrade -- the "any non-empty token accepted while
// token_hash is NULL" bridge (verifySandboxToken) is exercised here too,
// since no token is minted for this sandbox row.
func TestSandboxHandler_ValidHandshakeUpgrades(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	createTestSandbox(ctx, t, pool, sessionID)

	registry := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = registry.Shutdown() })
	sandboxStore := narvipg.NewSandboxStore(pool)

	server, wsURL := newTestServer(registry, sandboxStore, platform.DefaultTimeouts())
	t.Cleanup(server.Close)

	header := http.Header{}
	for k, v := range baseHeaders() {
		header.Set(k, v)
	}

	conn, resp, err := websocket.Dial(ctx, wsURL+"/sessions/"+sessionID.String()+"/ws?type=sandbox",
		&websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("Dial() error = %v, want a successful 101 upgrade", err)
	}
	defer func() { _ = conn.CloseNow() }()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("handshake response status = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}
}
