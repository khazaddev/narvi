package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScanRegisteredRoutes_JoinsNestedRoutePrefix proves the scanner's own
// core job: a route registered inside a Route(...) group carries the
// group's own prefix, a top-level route (no Route(...) wrapper) carries
// none, and a nested group's own path is joined without a stray trailing
// slash (joinRoutePath's own "/" sub-path collapse).
func TestScanRegisteredRoutes_JoinsNestedRoutePrefix(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

type router struct{}

func (r router) Get(path string, h int)                    {}
func (r router) Post(path string, h int)                   {}
func (r router) Route(path string, fn func(router)) {}

func build() {
	rt := router{}
	rt.Get("/health", 0)
	rt.Route("/api/members", func(r router) {
		r.Get("/", 0)
		r.Post("/{userID}/role", 0)
	})
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fake.go: %v", err)
	}

	got, err := ScanRegisteredRoutes(dir)
	if err != nil {
		t.Fatalf("ScanRegisteredRoutes: %v", err)
	}

	want := []string{"GET /health", "GET /api/members", "POST /api/members/{userID}/role"}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing route %q in %v", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("found %d routes, want exactly %d: %v", len(got), len(want), got)
	}
}

// TestScanRegisteredRoutes_SkipsTestFiles mirrors
// TestScanRegisteredInstruments_SkipsTestFiles's own identical rationale:
// a route only ever registered inside a _test.go file must never be
// treated as a real, production-served route.
func TestScanRegisteredRoutes_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

type router struct{}

func (r router) Get(path string, h int) {}

func build() {
	router{}.Get("/test-only", 0)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake_test.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fake_test.go: %v", err)
	}
	got, err := ScanRegisteredRoutes(dir)
	if err != nil {
		t.Fatalf("ScanRegisteredRoutes: %v", err)
	}
	if _, ok := got["GET /test-only"]; ok {
		t.Error("a _test.go file's own route must be skipped, not counted as registered")
	}
}

// TestScanRegisteredRoutes_IgnoresNonLiteralPath mirrors
// TestScanRegisteredInstruments_IgnoresNonLiteralAndUnrelatedCalls's own
// "a computed path is silently skipped, not an error" case.
func TestScanRegisteredRoutes_IgnoresNonLiteralPath(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

type router struct{}

func (r router) Get(path string, h int) {}

func build(p string) {
	router{}.Get(p, 0)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fake.go: %v", err)
	}
	got, err := ScanRegisteredRoutes(dir)
	if err != nil {
		t.Fatalf("ScanRegisteredRoutes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("found %d routes, want 0 (non-literal path argument): %v", len(got), got)
	}
}

// TestScanRegisteredRoutes_UnrelatedSameNamedMethod documents, rather than
// hides, this scanner's own accepted trade-off (routes.go's own
// chiRouterMethods doc comment): a call to an unrelated method sharing one
// of the five common names (e.g. an outbound HTTP client's own Get) is
// recorded too. Harmless for CheckGuideDrift's own purposes (it can only
// enlarge, never shrink, the registered set) — this test exists so that
// behavior is a documented, verified property, not a silent surprise.
func TestScanRegisteredRoutes_UnrelatedSameNamedMethod(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

type httpClient struct{}

func (c httpClient) Get(url string) {}

func build() {
	// Deliberately no "://" in this literal -- path.Join/path.Clean
	// (joinRoutePath's own implementation) collapses a double slash
	// exactly like a real URL scheme's "//" would, which is irrelevant
	// noise for THIS test's own purpose (proving an unrelated same-named
	// method is still recorded at all, not proving joinRoutePath is a
	// general-purpose URL joiner).
	httpClient{}.Get("not-a-route")
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fake.go: %v", err)
	}
	got, err := ScanRegisteredRoutes(dir)
	if err != nil {
		t.Fatalf("ScanRegisteredRoutes: %v", err)
	}
	if _, ok := got["GET not-a-route"]; !ok {
		t.Errorf("expected the unrelated Get call to still be recorded (documented trade-off), got %v", got)
	}
}

func TestJoinRoutePath(t *testing.T) {
	tests := []struct {
		prefix, p, want string
	}{
		{"", "/health", "/health"},
		{"/api/members", "/", "/api/members"},
		{"/api/members", "/{userID}/role", "/api/members/{userID}/role"},
		{"", "/", "/"},
	}
	for _, tt := range tests {
		if got := joinRoutePath(tt.prefix, tt.p); got != tt.want {
			t.Errorf("joinRoutePath(%q, %q) = %q, want %q", tt.prefix, tt.p, got, tt.want)
		}
	}
}
