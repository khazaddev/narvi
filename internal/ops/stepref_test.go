package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoStepRefInSource fails when any Go source under internal/, cmd/, or
// contracts/ cites an implementation "Step N" -- the scheduling-artifact
// half of the reference convention TestNoSectionRefDrift (sectionref.go)
// guards the other half of: code cites only the durable §N.M, never the
// Step whose row order happened to introduce it.
//
// docs/ is deliberately not scanned -- the plan documents legitimately
// reference their own Steps.
//
// web/src is scanned for the same reason internal/ and cmd/ are: it is
// source, it cites the plan in comments, and one of its citations was being
// rendered to operators on the analytics screen. It was originally left out
// on both axes at once (not in this root list, and .ts/.tsx not in
// CheckStepRefs's extension filter), which is why the SPA drifted while CI
// stayed green -- see stepref.go's own CheckStepRefs doc comment.
func TestNoStepRefInSource(t *testing.T) {
	root := repoRoot(t)

	refs, err := CheckStepRefs(root, []string{"internal", "cmd", "contracts", "web/src"})
	if err != nil {
		t.Fatalf("CheckStepRefs: %v", err)
	}
	for _, ref := range refs {
		t.Errorf("%s:%d cites an implementation Step: %s\n"+
			"Step numbers are a scheduling artifact (the plan's own row order) and do not belong "+
			"in code -- cite the technical-plan section (§N.M) that actually governs this behavior "+
			"instead, or describe the behavior directly with no citation if no section does.",
			ref.File, ref.Line, ref.Text)
	}
}

// TestCheckStepRefs_MutationBoundary pins the exact boundary
// stepRefPattern/narrativeStepLine draw between a plan citation (must be
// caught) and local test-scenario narration (must not be) -- see
// stepref.go's own top doc comment for the rule. Both directions are
// exercised against a real temp file, the same shape CheckStepRefs walks in
// production, rather than asserting against the regexes directly, so a
// future rewrite of either regex is honest about what it actually catches.
func TestCheckStepRefs_MutationBoundary(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantCaught bool
	}{
		{"possessive plan citation", `// Step 48's own capability restriction applies here.`, true},
		{"parenthetical plan citation", `// see the resolution table (Step 59, §29.5) for details.`, true},
		{"bare plan citation mid-sentence", `// refused because Step 76's cohort-rollout gate said no.`, true},
		{"plural plan citation", `// shared across Steps 32/33/34's own GitHub/Slack/Linear ingress.`, true},
		{"plural plan citation, range", `// design (schema/domain/contracts/RBAC only), and Steps 55-56 own it.`, true},
		{"parenthesis-form plan citation", `// AgentRuntime is the SECOND port, added this Step (17), against opencode.`, true},
		{"plural parenthesis-form plan citation", `// extending it is expected as later Steps (47, 58) find real gaps.`, true},
		{"narrative step, plain", `	// Step 1: the timer fires first, exactly like production does.`, false},
		{"narrative step, dashed", `	// --- Step 3: kill pod A. Deliberately NOT a graceful stop.`, false},
		{"narrative step, sub-lettered", `	// Step 2a: the sandbox reconnects before the deadline.`, false},
		{"identifier, not a citation", `	builtInPlanStep1ID = "00000000-0000-4000-8000-000000000031"`, false},
	}

	dir := t.TempDir()
	// CheckStepRefs walks a root's "internal" (etc.) subtree, so lay the
	// fixture out the same way: <tmp>/internal/pkg/file.go.
	pkgDir := filepath.Join(dir, "internal", "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "package pkg\n\n" + c.line + "\nfunc x() {}\n"
			path := filepath.Join(pkgDir, "fixture.go")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })

			refs, err := CheckStepRefs(dir, []string{"internal"})
			if err != nil {
				t.Fatalf("CheckStepRefs: %v", err)
			}
			got := len(refs) > 0
			if got != c.wantCaught {
				t.Errorf("line %q: caught = %v, want %v (refs = %+v)", c.line, got, c.wantCaught, refs)
			}
		})
	}
}

// TestCheckStepRefs_ScansTypeScript pins the extension half of this check's
// coverage, the half that was missing.
//
// CheckStepRefs originally walked only ".go", under roots that did not
// include web/src at all, while its doc comment claimed a clean result meant
// the tree cited only sections. It did not: the SPA had accumulated dozens of
// Step citations, one of them rendered on screen to operators, every one of
// them green in CI. Both axes are fixed, and both are pinned here -- a
// citation in a .ts or .tsx file must be caught, and the narrative-colon
// exemption must survive the crossing (TypeScript uses the same "//" marker,
// so a test author writing "// Step 1: ..." in a .ts scenario comment must
// not be flagged any more than a Go one is).
func TestCheckStepRefs_ScansTypeScript(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		line       string
		wantCaught bool
	}{
		{"citation in .ts", "fixture.ts", `// see Step 59's own resolution table.`, true},
		{"citation in .tsx", "fixture.tsx", `// Settings/Analytics (§12.2 item 5, Step 86): the same shell.`, true},
		{"narrative step in .ts is exempt", "fixture.ts", `  // Step 1: the stream emits its first frame.`, false},
		{"unscanned extension is ignored", "fixture.css", `/* Step 86's own panel styles. */`, false},
	}

	dir := t.TempDir()
	srcDir := filepath.Join(dir, "web", "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(srcDir, c.file)
			if err := os.WriteFile(path, []byte(c.line+"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })

			refs, err := CheckStepRefs(dir, []string{"web/src"})
			if err != nil {
				t.Fatalf("CheckStepRefs: %v", err)
			}
			got := len(refs) > 0
			if got != c.wantCaught {
				t.Errorf("%s line %q: caught = %v, want %v (refs = %+v)", c.file, c.line, got, c.wantCaught, refs)
			}
		})
	}
}

// TestCheckStepRefs_CatchesPlanDocReferences pins planDocRefPattern.
//
// The Step-worded pattern cannot see "IMPLEMENTATION_PLAN.md row 87", which is
// the same non-durable reference in different words. Widening stepRefPattern to
// `row\s+\d+` looks like the fix and is not: it matches the TECHNICAL plan's own
// §13.3 RBAC table rows ("§13.3 row 1", "row 6, admin-only") that this
// convention actively wants cited. The rule that separates them does not count
// rows at all -- source has no business naming the schedule.
//
// Both directions are exercised, because the second is what keeps this check
// from fighting its users.
func TestCheckStepRefs_CatchesPlanDocReferences(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantCaught bool
	}{
		{"row citation by filename", `// see docs/IMPLEMENTATION_PLAN.md row 87 for the requirement.`, true},
		{"bare filename, no row", `// confirmed against IMPLEMENTATION_PLAN.md before starting.`, true},
		{"inside a shipped string, not a comment", `	metric.WithDescription("... IMPLEMENTATION_PLAN.md row 77: 'false failures'")`, true},
		{"technical-plan RBAC table row", `// ActionViewAnalytics (§13.3 row 1: every role, including viewer).`, false},
		{"technical-plan row, possessive", `// reusing ActionManageMembers (row 6, admin-only) instead of a new action.`, false},
		{"an ordinary sentence about a table row", `// skip the header and read row 2 onward.`, false},
	}

	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "internal", "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "package pkg\n\n" + c.line + "\nfunc x() {}\n"
			path := filepath.Join(pkgDir, "fixture.go")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })

			refs, err := CheckStepRefs(dir, []string{"internal"})
			if err != nil {
				t.Fatalf("CheckStepRefs: %v", err)
			}
			got := len(refs) > 0
			if got != c.wantCaught {
				t.Errorf("line %q: caught = %v, want %v (refs = %+v)", c.line, got, c.wantCaught, refs)
			}
		})
	}
}

// TestCheckStepRefs_CatchesWrappedCitations pins the line-wrap blind spot.
//
// stepRefPattern matches per line. A citation whose comment wrapped between
// the word and the number — "Step" ending one line, "88)" beginning the next —
// matches neither half, so it was invisible. Comments in this repo wrap at 80
// columns constantly, so that is the ordinary way a citation survives, not an
// exotic one: ten of them were found repo-wide AFTER every single-line
// citation had been swept, three of them in web/src, which a previous sweep
// had reported as complete.
//
// The negative cases matter as much as the positive ones. The head pattern
// requires the capitalised citation form at end of line, so ordinary English
// ending in "step" before an unrelated numbered line must not trip it.
func TestCheckStepRefs_CatchesWrappedCitations(t *testing.T) {
	cases := []struct {
		name       string
		lines      []string
		wantCaught bool
	}{
		{"wrapped possessive citation", []string{`// see the resolution table (§29.5, Step`, `// 59) for the details.`}, true},
		{"wrapped plural citation", []string{`// shared across Steps`, `// 32/33/34's own ingress.`}, true},
		{"wrapped citation in a doc block", []string{`/*`, ` * built by Step`, ` * 21) and never since.`, ` */`}, true},
		{"ordinary english 'step' before a numbered line", []string{`// each one is a genuinely independent step`, `// 2 of the pipeline is where it lands.`}, false},
		{"the word alone with no number after it", []string{`// this is a narrative step`, `// describing what happens next.`}, false},
		{"number on the next line but no Step word", []string{`// bounded by the retry ceiling`, `// 5 attempts, then it stops.`}, false},
	}

	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "internal", "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "package pkg\n\n" + strings.Join(c.lines, "\n") + "\nfunc x() {}\n"
			path := filepath.Join(pkgDir, "fixture.go")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })

			refs, err := CheckStepRefs(dir, []string{"internal"})
			if err != nil {
				t.Fatalf("CheckStepRefs: %v", err)
			}
			got := len(refs) > 0
			if got != c.wantCaught {
				t.Errorf("lines %q: caught = %v, want %v (refs = %+v)", c.lines, got, c.wantCaught, refs)
			}
		})
	}
}
