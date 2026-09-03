package turn_test

import (
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/domain/turn"
)

func boolPtr(b bool) *bool { return &b }

// TestResolveEpistemicCheckEnabled pins §20.4's precedence -- "session
// override wins when set, global default otherwise" -- in both
// directions (an override can flip the global default either way), plus
// the no-override passthrough.
func TestResolveEpistemicCheckEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		platformDefault bool
		sessionOverride *bool
		want            bool
	}{
		{"no override, global off", false, nil, false},
		{"no override, global on", true, nil, true},
		{"override true beats global false", false, boolPtr(true), true},
		{"override false beats global true", true, boolPtr(false), false},
		{"override true, global also true", true, boolPtr(true), true},
		{"override false, global also false", false, boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := turn.ResolveEpistemicCheckEnabled(tt.platformDefault, tt.sessionOverride)
			if got != tt.want {
				t.Errorf("ResolveEpistemicCheckEnabled(%v, %v) = %v, want %v", tt.platformDefault, tt.sessionOverride, got, tt.want)
			}
		})
	}
}

// TestShouldInjectEpistemicPreamble pins §20.3's exclusion -- a
// plan_mode=true turn NEVER gets the preamble, regardless of how the
// enable/disable flag resolved -- as the exhaustive 2x2 truth table.
func TestShouldInjectEpistemicPreamble(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		checkEnabled bool
		planMode     bool
		want         bool
	}{
		{"enabled, build turn -> inject", true, false, true},
		{"disabled, build turn -> no inject", false, false, false},
		{"enabled, plan turn -> STILL excluded (§20.3)", true, true, false},
		{"disabled, plan turn -> no inject", false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := turn.ShouldInjectEpistemicPreamble(tt.checkEnabled, tt.planMode)
			if got != tt.want {
				t.Errorf("ShouldInjectEpistemicPreamble(%v, %v) = %v, want %v", tt.checkEnabled, tt.planMode, got, tt.want)
			}
		})
	}
}

// TestMaybeInjectEpistemicPreamble is F6's own shared-gate test
// (adversarial review): exhaustive over the SAME resolve/exclude axes
// TestResolveEpistemicCheckEnabled/TestShouldInjectEpistemicPreamble
// already pin individually, but exercised end-to-end through the single
// composed entry point every raw turn-insert call site now uses -- this is
// the ONE test standing between a future edit to MaybeInjectEpistemicPreamble
// and a silent regression on every one of its callers at once.
func TestMaybeInjectEpistemicPreamble(t *testing.T) {
	t.Parallel()

	const prompt = "please build the thing"
	preamble := turn.RenderEpistemicPreamble()

	tests := []struct {
		name            string
		platformDefault bool
		sessionOverride *bool
		planMode        bool
		want            string
	}{
		{"platform off, no override, build turn -> no-op", false, nil, false, prompt},
		{"platform on, no override, build turn -> injected", true, nil, false, preamble + prompt},
		{"platform off, override true, build turn -> injected", false, boolPtr(true), false, preamble + prompt},
		{"platform on, override false, build turn -> no-op", true, boolPtr(false), false, prompt},
		{"platform on, no override, PLAN-MODE turn -> STILL excluded (§20.3)", true, nil, true, prompt},
		{"platform on, override true, PLAN-MODE turn -> STILL excluded (§20.3)", true, boolPtr(true), true, prompt},
		{"platform off, no override, plan-mode turn -> no-op", false, nil, true, prompt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := turn.MaybeInjectEpistemicPreamble(tt.platformDefault, tt.sessionOverride, tt.planMode, prompt)
			if got != tt.want {
				t.Errorf("MaybeInjectEpistemicPreamble(%v, %v, %v, prompt) = %q, want %q", tt.platformDefault, tt.sessionOverride, tt.planMode, got, tt.want)
			}
		})
	}
}

// TestMaybeInjectEpistemicPreamble_PrependsNeverAppends pins §20.1's own
// "preceded -- prepended, never appended" requirement directly against the
// composed entry point, independent of the table above (which already
// proves this indirectly via string equality, but not by NAME).
func TestMaybeInjectEpistemicPreamble_PrependsNeverAppends(t *testing.T) {
	t.Parallel()

	const prompt = "distinctive-marker-prompt-text"
	got := turn.MaybeInjectEpistemicPreamble(true, nil, false, prompt)

	if !strings.HasPrefix(got, turn.RenderEpistemicPreamble()) {
		t.Fatalf("MaybeInjectEpistemicPreamble(...) = %q, want it to START with the preamble text", got)
	}
	if !strings.HasSuffix(got, prompt) {
		t.Fatalf("MaybeInjectEpistemicPreamble(...) = %q, want it to END with the caller's own prompt text", got)
	}
}

// TestRenderEpistemicPreamble_Deterministic pins purity/determinism (§11):
// two calls produce byte-identical output, with no hidden clock/random
// input to vary it.
func TestRenderEpistemicPreamble_Deterministic(t *testing.T) {
	t.Parallel()

	a := turn.RenderEpistemicPreamble()
	b := turn.RenderEpistemicPreamble()
	if a != b {
		t.Fatalf("RenderEpistemicPreamble() not deterministic:\na = %q\nb = %q", a, b)
	}
	if a == "" {
		t.Fatal("RenderEpistemicPreamble() = \"\", want non-empty preamble text")
	}
}

// TestRenderEpistemicPreamble_ContainsRequiredElements pins §20.1's own
// required content, element by element, so a future edit that accidentally
// drops one (e.g. rewording away the explicit proceed-bias, or one of the
// three ordered questions) fails a test instead of silently shipping a
// preamble that no longer satisfies the spec.
func TestRenderEpistemicPreamble_ContainsRequiredElements(t *testing.T) {
	t.Parallel()

	got := turn.RenderEpistemicPreamble()

	requiredSubstrings := []string{
		// §20.1: "whether anything about the action rests on an
		// unverified assumption, contradicts something already observed
		// in the session, or is otherwise worth a second look" -- in
		// order.
		"assumption",
		"contradict",
		"second look",
		// The two-tier taxonomy, by name.
		"MINOR",
		"STRONG",
		// §20.1: "worth a heads-up ... not worth stopping for; the agent
		// proceeds".
		"not worth stopping",
		// §20.1: "worth stopping for; the agent surfaces the concern
		// instead of acting, and waits for the user".
		"Do NOT take the action",
		// §20.1: the bias itself, stated explicitly in the text (not left
		// to the model's own judgment).
		"DELIBERATELY BIASED TOWARD PROCEEDING",
		// F4 (adversarial review): §20.1's own THIRD tier -- "silence for
		// anything that doesn't rise to either" MINOR or STRONG -- pinned
		// by its own trigger condition and its own required action, not
		// merely the word "silence" in the abstract. Both substrings sit
		// inside the SAME operative sentence as "DELIBERATELY BIASED
		// TOWARD PROCEEDING" above; a rewrite that keeps the bias slogan
		// but drops the silence tier (exactly the mutation this test's own
		// docstring below describes) must fail here.
		"if nothing rises to either tier",
		"say NOTHING",
		// §20.1: "worth stopping for" is defined by THIS package, not
		// deferred to the model -- the taxonomy's own preamble line makes
		// this explicit ("not by your own sense of how cautious to be").
		// Pinned separately from the forbiddenSubstrings check below: this
		// asserts the REQUIRED disclaiming clause is present, that check
		// asserts no rewrite ever hands the decision back regardless.
		"not by your own sense of how cautious to be",
		// §20.2: the structured-signal reporting instructions, never
		// prompt-only.
		"POST " + turn.EpistemicOutcomeToolURLPlaceholder,
		"Authorization: Bearer " + turn.EpistemicOutcomeToolBearerPlaceholder,
		"X-Sandbox-Gen: " + turn.EpistemicOutcomeToolGenPlaceholder,
		`"none" | "minor" | "strong"`,
	}
	for _, want := range requiredSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("RenderEpistemicPreamble() missing required substring %q\nfull text:\n%s", want, got)
		}
	}

	// F4 (adversarial review): the previous version of this test asserted
	// ONLY the required substrings above -- which a mutation replacing the
	// whole operative sentence with "This check is DELIBERATELY BIASED
	// TOWARD PROCEEDING. Use your own judgment about how cautious to be."
	// passed cleanly (the bias slogan survives verbatim; nothing here used
	// to check for its own negation). That rewrite substitutes back in
	// exactly what §20.1 forbids -- "not left to the model's own judgment
	// of 'how cautious to be'" -- so it must be an explicit forbidden
	// substring, not just an absent required one: a required-substrings
	// list can only ever catch a DELETION, never an ADDITION that coexists
	// peacefully alongside everything already required above (as this
	// exact mutation does, since it keeps the bias slogan intact).
	forbiddenSubstrings := []string{
		"own judgment",
	}
	for _, forbidden := range forbiddenSubstrings {
		if strings.Contains(got, forbidden) {
			t.Errorf("RenderEpistemicPreamble() contains forbidden substring %q -- this hands the proceed-bias decision back to the model's own judgment, exactly what §20.1 says is \"not left to the model's own judgment of 'how cautious to be'\"\nfull text:\n%s", forbidden, got)
		}
	}

	// Order matters for the three §20.1 questions -- "in order" is part
	// of the spec, not just their mere presence.
	iAssumption := strings.Index(got, "assumption")
	iContradict := strings.Index(got, "contradict")
	iSecondLook := strings.Index(got, "second look")
	if iAssumption >= iContradict || iContradict >= iSecondLook {
		t.Errorf("the three §20.1 questions are not in the required order (assumption=%d, contradict=%d, second look=%d)", iAssumption, iContradict, iSecondLook)
	}
}
