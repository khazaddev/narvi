package httpapi

// This file (planfollowupprivate_test.go) enforces §23.4/§18.6's own
// structural requirement: plan_followup classification is excluded from
// public routes BY CONSTRUCTION -- private to internal/app/, never
// registered on httpapi/wshub. "Structural" here means a routing-level
// property, not a Go-visibility one: intentclassifier.Service.
// ClassifyPlanFollowup IS exported (createTurnLocked, an ordinary,
// already-authorized function in THIS package, calls it directly, turn.go)
// -- what must never exist is a dedicated HTTP/WS ROUTE whose job is to
// expose classification itself to a caller.
//
// No pre-existing test enforced this for the FIRST classifier category
// (review-vs-request/plan-vs-build, §8.3) at the time this Step was
// written -- confirmed by direct search of this codebase. This file is
// therefore the first concrete, automated enforcement of §18.6's rule,
// covering both categories at once (neither is ever routed), rather than
// copying a prior test that did not exist.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRootForTest walks up from this test file's own source location
// (runtime.Caller, robust regardless of `go test`'s own working directory
// convention) until it finds go.mod -- the repo root every path below is
// relative to.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked up to filesystem root without finding go.mod")
		}
		dir = parent
	}
}

// grepGoFiles reports every (path, matched-substring) pair found by
// scanning every non-test *.go file under root for any of needles.
// Non-test only: this test's OWN file (and any other _test.go file) is
// allowed to name these symbols in comments/strings without tripping the
// check meant for production wiring.
func grepGoFiles(t *testing.T, root string, needles []string) (hits []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(raw)
		for _, needle := range needles {
			if strings.Contains(content, needle) {
				hits = append(hits, path+": "+needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}

// TestPlanFollowupClassifier_NeverImportedByWshub proves internal/adapters/
// inbound/wshub -- the OTHER inbound adapter package a classification
// surface could theoretically be wired into (§23.4/§18.6: "never
// registered on httpapi/wshub") -- has NO reference to intentclassifier at
// all. wshub has no legitimate reason to import it for ANY classification
// category, so this is a zero-tolerance import-boundary check, not merely
// a plan_followup-specific one.
func TestPlanFollowupClassifier_NeverImportedByWshub(t *testing.T) {
	root := repoRootForTest(t)
	wshubDir := filepath.Join(root, "internal", "adapters", "inbound", "wshub")
	if _, err := os.Stat(wshubDir); err != nil {
		t.Fatalf("wshub package directory not found at %s: %v", wshubDir, err)
	}

	hits := grepGoFiles(t, wshubDir, []string{
		`narvi/internal/app/intentclassifier`,
		"ClassifyPlanFollowup",
	})
	if len(hits) > 0 {
		t.Errorf("internal/adapters/inbound/wshub must never reference intentclassifier (§23.4/§18.6: never registered on httpapi/wshub); found:\n%s", strings.Join(hits, "\n"))
	}
}

// TestPlanFollowupClassifier_NoDedicatedRoute proves no HTTP route
// registration anywhere in cmd/control-plane/main.go names a path that
// would expose plan_followup classification directly (a hypothetical
// "POST /api/plan-followup" or similar) -- the structural property §23.4
// requires ("excluded from public routes by construction... not merely a
// convention to remember"). Every EXISTING route that indirectly reaches
// ClassifyPlanFollowup (POST /{sessionID}/turns and its siblings) does so
// only as one internal step of an already-authorized, pre-existing
// turn-creation operation, never as a route dedicated to classification
// itself -- this test does not, and cannot, forbid that; it forbids a
// FUTURE route whose own path names the classification surface directly.
func TestPlanFollowupClassifier_NoDedicatedRoute(t *testing.T) {
	root := repoRootForTest(t)

	// Scan the DIRECTORY that owns route wiring, never one hardcoded file.
	// The wiring moved out of cmd/control-plane/main.go into the importable
	// controlplane package, and a check pinned to the old path kept passing
	// against a 16-line stub containing no routes at all -- structurally
	// unable to fail, which is worse than absent. cmd/control-plane is kept
	// in the list so a route registered back there is caught too.
	dirs := []string{
		filepath.Join(root, "controlplane"),
		filepath.Join(root, "cmd", "control-plane"),
	}

	forbidden := []string{"plan-followup", "planfollowup", "plan_followup"}
	scanned := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v -- this test is worthless if it silently scans nothing", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			scanned++
			content := strings.ToLower(string(raw))
			for _, needle := range forbidden {
				if strings.Contains(content, needle) {
					t.Errorf("%s must never register a route naming the plan_followup classification surface directly (%q found) -- §23.4: excluded from public routes by construction", path, needle)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no wiring files -- the composition root moved again and this check is now vacuous")
	}
}

// TestPlanFollowupClassifier_LivesUnderInternalApp is a cheap sanity check
// that ClassifyPlanFollowup's own defining package is under internal/app/
// -- "private to app/" (§23.4) means the code LIVES there, not merely that
// nothing calls it publicly. Reads intentclassifier's own package
// declaration indirectly by confirming planfollowup.go exists at the
// expected internal/app path (a compile-time fact this test just makes
// explicit and future-typo-proof, since the package import path itself is
// already load-bearing everywhere else in this file).
func TestPlanFollowupClassifier_LivesUnderInternalApp(t *testing.T) {
	root := repoRootForTest(t)
	path := filepath.Join(root, "internal", "app", "intentclassifier", "planfollowup.go")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected internal/app/intentclassifier/planfollowup.go to exist (§23.4: structurally private to internal/app/): %v", err)
	}
}

// TestPlanFollowupClassifier_SingleCallSite proves §23.1's own cheapest
// invariant -- "there is a single call site" -- directly: ClassifyPlanFollowup
// is called from exactly ONE non-test .go file anywhere in this repo
// outside internal/app/intentclassifier itself (today: turn.go's own
// createTurnLocked). Reuses this file's own grepGoFiles helper across the
// WHOLE repo root, then excludes hits under internal/app/intentclassifier
// -- that package's own defining file (planfollowup.go) matches the
// "ClassifyPlanFollowup(" needle too (its own func signature), which is not
// a call site at all; excluding the directory rather than special-casing
// that one file also means a hypothetical SECOND method/helper added
// inside that same package that itself calls ClassifyPlanFollowup would
// correctly stay unflagged (an internal implementation detail of the
// classifier's own package, not a new external caller).
//
// This closes two gaps at once (F4, adversarial review): §23.1's own
// single-call-site invariant had no test at all before this one, and
// §23.4's "never a public surface" guarantee only had the three
// structural/heuristic checks above it -- none of which would catch a
// smuggled SECOND call site added anywhere else in the repo, regardless of
// what that new call site is named or how it's wired (confirmed by
// mutation: adding a real dedicated HTTP route that calls
// ClassifyPlanFollowup directly left all three pre-existing tests in this
// file green).
func TestPlanFollowupClassifier_SingleCallSite(t *testing.T) {
	root := repoRootForTest(t)
	hits := grepGoFiles(t, root, []string{"ClassifyPlanFollowup("})

	excludeDir := filepath.Join(root, "internal", "app", "intentclassifier") + string(filepath.Separator)
	var callSites []string
	for _, hit := range hits {
		path := strings.SplitN(hit, ": ", 2)[0]
		if strings.HasPrefix(path, excludeDir) {
			continue
		}
		callSites = append(callSites, hit)
	}

	if len(callSites) != 1 {
		t.Fatalf("ClassifyPlanFollowup( called from %d non-test call site(s) outside internal/app/intentclassifier, want exactly 1 (§23.1's own single-call-site invariant); found:\n%s", len(callSites), strings.Join(callSites, "\n"))
	}

	const wantSuffix = "httpapi/turn.go: ClassifyPlanFollowup("
	if !strings.HasSuffix(filepath.ToSlash(callSites[0]), wantSuffix) {
		t.Errorf("the one call site = %q, want it to end with %q (createTurnLocked, turn.go)", callSites[0], wantSuffix)
	}
}
