package platform_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/platform"
)

// TestCorrelationIDMiddleware covers the four behaviors specified for
// PR-03: minting when the header is absent, reuse when present,
// retrievability from the request context inside the wrapped handler, and
// the response header being set to the same value used downstream.
func TestCorrelationIDMiddleware(t *testing.T) {
	t.Run("mints when header absent", func(t *testing.T) {
		var seenInContext string
		var sawInContext bool

		handler := platform.CorrelationIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenInContext, sawInContext = platform.CorrelationIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !sawInContext {
			t.Fatal("correlation id not found in request context")
		}
		if seenInContext == "" {
			t.Fatal("minted correlation id is empty")
		}

		gotHeader := rec.Header().Get(platform.CorrelationIDHeader)
		if gotHeader == "" {
			t.Fatal("response header not set")
		}
		if gotHeader != seenInContext {
			t.Fatalf("response header = %q, want it to match context value %q", gotHeader, seenInContext)
		}
	})

	t.Run("reuses incoming header value when present", func(t *testing.T) {
		const incoming = "test-correlation-id-123"

		var seenInContext string

		handler := platform.CorrelationIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenInContext, _ = platform.CorrelationIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(platform.CorrelationIDHeader, incoming)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if seenInContext != incoming {
			t.Fatalf("context correlation id = %q, want %q (incoming header)", seenInContext, incoming)
		}
		if got := rec.Header().Get(platform.CorrelationIDHeader); got != incoming {
			t.Fatalf("response header = %q, want %q", got, incoming)
		}
	})

	t.Run("distinct requests without a header mint distinct ids", func(t *testing.T) {
		var first, second string

		handler := platform.CorrelationIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req1)
		first = rec1.Header().Get(platform.CorrelationIDHeader)

		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)
		second = rec2.Header().Get(platform.CorrelationIDHeader)

		if first == "" || second == "" {
			t.Fatal("expected both requests to mint a non-empty correlation id")
		}
		if first == second {
			t.Fatalf("expected distinct minted ids, got %q twice", first)
		}
	})
}

// TestWithCorrelationIDContextHelpers covers the plain context helpers
// (WithCorrelationID/CorrelationIDFromContext) independent of the HTTP
// middleware, plus the analogous session-id and sandbox-gen helpers and the
// "absent" case for each.
func TestWithCorrelationIDContextHelpers(t *testing.T) {
	ctx := t.Context()

	if _, ok := platform.CorrelationIDFromContext(ctx); ok {
		t.Fatal("expected no correlation id in a bare context")
	}
	ctx2 := platform.WithCorrelationID(ctx, "abc123")
	got, ok := platform.CorrelationIDFromContext(ctx2)
	if !ok || got != "abc123" {
		t.Fatalf("CorrelationIDFromContext() = (%q, %v), want (\"abc123\", true)", got, ok)
	}

	if _, ok := platform.SessionIDFromContext(ctx); ok {
		t.Fatal("expected no session id in a bare context")
	}
	ctx3 := platform.WithSessionID(ctx, "sess-1")
	gotSession, ok := platform.SessionIDFromContext(ctx3)
	if !ok || gotSession != "sess-1" {
		t.Fatalf("SessionIDFromContext() = (%q, %v), want (\"sess-1\", true)", gotSession, ok)
	}

	if _, ok := platform.SandboxGenFromContext(ctx); ok {
		t.Fatal("expected no sandbox gen in a bare context")
	}
	ctx4 := platform.WithSandboxGen(ctx, 7)
	gotGen, ok := platform.SandboxGenFromContext(ctx4)
	if !ok || gotGen != 7 {
		t.Fatalf("SandboxGenFromContext() = (%d, %v), want (7, true)", gotGen, ok)
	}
}
