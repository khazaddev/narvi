package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScanIntentVocabulary_ExtractsPrefixedStringConstants proves the
// scanner's own core job across both scan roots: every Target/Mode/
// RecordSource-prefixed string constant under intentRoot lands in its own
// bucket by VALUE (never by identifier name), every SessionSpawnSource-
// prefixed one under sqlcgenRoot lands in Surfaces, and an unrelated
// constant (wrong prefix, or a non-string value) is excluded from every
// bucket.
func TestScanIntentVocabulary_ExtractsPrefixedStringConstants(t *testing.T) {
	intentDir := t.TempDir()
	intentSrc := `package intent

const (
	TargetReview  = "review"
	TargetRequest = "request"
)

const (
	ModePlan  = "plan"
	ModeBuild = "build"
)

const (
	RecordSourceClassifier = "classifier"
	RecordSourceExplicit   = "explicit"
	RecordSourceFallback   = "fallback"
)

// ConfidenceHigh is deliberately a DIFFERENT prefix -- must never land in
// any of the three buckets above.
const ConfidenceHigh = "high"

// MaxReasoningLength is deliberately a non-string constant -- must never
// land in any bucket even though its own name starts with nothing that
// matches, proving int/iota-shaped constants are silently skipped.
const MaxReasoningLength = 2000
`
	if err := os.WriteFile(filepath.Join(intentDir, "rubric.go"), []byte(intentSrc), 0o644); err != nil {
		t.Fatalf("write rubric.go: %v", err)
	}

	sqlcDir := t.TempDir()
	sqlcSrc := `package sqlcgen

type SessionSpawnSource string

const (
	SessionSpawnSourceWeb    SessionSpawnSource = "web"
	SessionSpawnSourceSlack  SessionSpawnSource = "slack"
	SessionSpawnSourceLinear SessionSpawnSource = "linear"
	SessionSpawnSourceGithub SessionSpawnSource = "github"
)

// SessionStatusReady is a DIFFERENT enum entirely -- must never land in
// Surfaces.
const SessionStatusReady = "ready"
`
	if err := os.WriteFile(filepath.Join(sqlcDir, "models.go"), []byte(sqlcSrc), 0o644); err != nil {
		t.Fatalf("write models.go: %v", err)
	}

	got, err := ScanIntentVocabulary(intentDir, sqlcDir)
	if err != nil {
		t.Fatalf("ScanIntentVocabulary: %v", err)
	}

	for _, want := range []string{"review", "request"} {
		if !got.Targets[want] {
			t.Errorf("missing target %q in %v", want, got.Targets)
		}
	}
	for _, want := range []string{"plan", "build"} {
		if !got.Modes[want] {
			t.Errorf("missing mode %q in %v", want, got.Modes)
		}
	}
	for _, want := range []string{"classifier", "explicit", "fallback"} {
		if !got.Sources[want] {
			t.Errorf("missing record source %q in %v", want, got.Sources)
		}
	}
	for _, want := range []string{"web", "slack", "linear", "github"} {
		if !got.Surfaces[want] {
			t.Errorf("missing surface %q in %v", want, got.Surfaces)
		}
	}

	if got.Targets["high"] || got.Modes["high"] || got.Sources["high"] {
		t.Errorf("ConfidenceHigh's value leaked into a bucket its own prefix does not belong to: %+v", got)
	}
	if len(got.Surfaces) != 4 {
		t.Errorf("Surfaces = %v, want exactly the 4 real spawn sources (SessionStatusReady must be excluded)", got.Surfaces)
	}
}

// TestScanIntentVocabulary_SkipsTestFiles mirrors this package's other two
// scanners' identical convention.
func TestScanIntentVocabulary_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	src := `package intent

const TargetTestOnly = "test_only"
`
	if err := os.WriteFile(filepath.Join(dir, "rubric_test.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write rubric_test.go: %v", err)
	}
	got, err := ScanIntentVocabulary(dir, dir)
	if err != nil {
		t.Fatalf("ScanIntentVocabulary: %v", err)
	}
	if got.Targets["test_only"] {
		t.Error("a _test.go file's own constant must be skipped, not counted as registered")
	}
}
