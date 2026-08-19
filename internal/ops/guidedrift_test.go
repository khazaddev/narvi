package ops

import (
	"path/filepath"
	"testing"
)

func TestCheckGuideDrift(t *testing.T) {
	vocab := IntentVocabulary{
		Surfaces: map[string]bool{"web": true, "github": true},
		Targets:  map[string]bool{"review": true, "request": true},
		Modes:    map[string]bool{"plan": true, "build": true},
		Sources:  map[string]bool{"classifier": true, "explicit": true, "fallback": true},
	}
	routes := map[string]RegisteredRoute{
		"POST /api/sessions": {Method: "POST", Path: "/api/sessions"},
	}

	t.Run("no drift", func(t *testing.T) {
		guides := []SurfaceGuide{{
			Surface: "web",
			Title:   "Web Guide",
			Commands: []GuideCommand{
				{Name: "Create a session", Route: "POST /api/sessions"},
				{Name: "PR mention", Classifier: &ClassifierRef{Surface: "github", Target: "review", Mode: "build", Source: "classifier"}},
			},
		}}
		if errs := CheckGuideDrift(guides, routes, vocab); len(errs) != 0 {
			t.Errorf("CheckGuideDrift() = %v, want none", errs)
		}
	})

	t.Run("command references unregistered route", func(t *testing.T) {
		guides := []SurfaceGuide{{Surface: "web", Title: "Web Guide", Commands: []GuideCommand{
			{Name: "Ghost route", Route: "POST /api/does-not-exist"},
		}}}
		errs := CheckGuideDrift(guides, routes, vocab)
		if len(errs) != 1 {
			t.Fatalf("CheckGuideDrift() = %v, want exactly 1 error", errs)
		}
		if errs[0].Kind != "route" || errs[0].Command != "Ghost route" {
			t.Errorf("CheckGuideDrift() = %+v, want Kind=route Command=\"Ghost route\"", errs[0])
		}
	})

	t.Run("guide's own filename-derived surface is unregistered", func(t *testing.T) {
		guides := []SurfaceGuide{{Surface: "carrier-pigeon", Title: "Pigeon Guide", Commands: []GuideCommand{
			{Name: "Create a session", Route: "POST /api/sessions"},
		}}}
		errs := CheckGuideDrift(guides, routes, vocab)
		if len(errs) != 1 || errs[0].Kind != "guide-surface" {
			t.Fatalf("CheckGuideDrift() = %v, want exactly 1 guide-surface error", errs)
		}
	})

	t.Run("classifier references unregistered surface/target/mode/source independently", func(t *testing.T) {
		guides := []SurfaceGuide{{Surface: "web", Title: "Web Guide", Commands: []GuideCommand{
			{Name: "Bad surface", Classifier: &ClassifierRef{Surface: "carrier-pigeon"}},
			{Name: "Bad target", Classifier: &ClassifierRef{Surface: "github", Target: "teleport"}},
			{Name: "Bad mode", Classifier: &ClassifierRef{Surface: "github", Mode: "levitate"}},
			{Name: "Bad source", Classifier: &ClassifierRef{Surface: "github", Source: "telepathy"}},
		}}}
		errs := CheckGuideDrift(guides, routes, vocab)
		if len(errs) != 4 {
			t.Fatalf("CheckGuideDrift() = %v, want exactly 4 errors", errs)
		}
		kinds := map[string]bool{}
		for _, e := range errs {
			kinds[e.Kind] = true
		}
		for _, want := range []string{"classifier-surface", "classifier-target", "classifier-mode", "classifier-source"} {
			if !kinds[want] {
				t.Errorf("missing error kind %q in %v", want, errs)
			}
		}
	})

	t.Run("renamed route is caught the same as a never-registered one", func(t *testing.T) {
		renamedRoutes := map[string]RegisteredRoute{
			"POST /api/sessions/v2": {Method: "POST", Path: "/api/sessions/v2"},
		}
		guides := []SurfaceGuide{{Surface: "web", Title: "Web Guide", Commands: []GuideCommand{
			{Name: "Create a session", Route: "POST /api/sessions"},
		}}}
		errs := CheckGuideDrift(guides, renamedRoutes, vocab)
		if len(errs) != 1 {
			t.Fatalf("CheckGuideDrift() = %v, want exactly 1 error", errs)
		}
	})
}

// TestNoGuideDrift is Step 78's own CI-enforcing structural guard,
// TestNoMetricDrift's own direct sibling (drift_test.go): scans this
// repo's REAL cmd/control-plane route wiring and REAL internal/domain/
// intent + sqlcgen vocabulary, loads the REAL docs/guides/*.md files, and
// fails if any documented command's route or classifier binding names
// something the code does not actually implement. Runs as a plain `go
// test`, already part of `make test`/`go test -race ./...` — no separate
// CI job or workflow edit needed.
//
// Mutation-tested by hand as part of this Step's own verification (see
// the PR description): (1) documenting a command with a route that maps
// to no real endpoint makes this test fail; (2) renaming a real route in
// cmd/control-plane/main.go without updating the guide makes this test
// fail identically; (3) malforming a guide file (breaking its embedded
// JSON) makes LoadGuides itself fail the test, rather than silently
// skipping that file. All three mutations were reverted byte-identical
// afterward.
func TestNoGuideDrift(t *testing.T) {
	root := repoRoot(t)

	routes, err := ScanRegisteredRoutes(filepath.Join(root, "cmd", "control-plane"))
	if err != nil {
		t.Fatalf("ScanRegisteredRoutes: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("ScanRegisteredRoutes found zero routes -- almost certainly a scan-path bug (this repo has real chi routes), not a genuinely empty binary")
	}

	vocab, err := ScanIntentVocabulary(
		filepath.Join(root, "internal", "domain", "intent"),
		filepath.Join(root, "internal", "adapters", "outbound", "postgres", "sqlcgen"),
	)
	if err != nil {
		t.Fatalf("ScanIntentVocabulary: %v", err)
	}
	if len(vocab.Surfaces) == 0 || len(vocab.Targets) == 0 || len(vocab.Modes) == 0 || len(vocab.Sources) == 0 {
		t.Fatalf("ScanIntentVocabulary found an empty bucket -- almost certainly a scan-path bug: %+v", vocab)
	}

	guides, err := LoadGuides(filepath.Join(root, "docs", "guides"))
	if err != nil {
		t.Fatalf("LoadGuides: %v", err)
	}
	if len(guides) == 0 {
		t.Fatal("LoadGuides found zero per-surface guides")
	}

	if errs := CheckGuideDrift(guides, routes, vocab); len(errs) > 0 {
		for _, e := range errs {
			t.Error(e)
		}
	}
}

// TestGuidesCoverAllFourSurfaces is a cheap companion check, same
// rationale as TestAlertRunbooksExist (drift_test.go): §10-P6 names the
// guide as covering "web/Slack/Linear/GitHub" explicitly — a missing
// surface is a smaller version of the same "looks complete but isn't"
// hazard.
func TestGuidesCoverAllFourSurfaces(t *testing.T) {
	root := repoRoot(t)
	guides, err := LoadGuides(filepath.Join(root, "docs", "guides"))
	if err != nil {
		t.Fatalf("LoadGuides: %v", err)
	}
	got := map[string]bool{}
	for _, g := range guides {
		got[g.Surface] = true
	}
	for _, want := range []string{"web", "slack", "linear", "github"} {
		if !got[want] {
			t.Errorf("no guide found for surface %q", want)
		}
	}
}
