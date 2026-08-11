package turn_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/turn"
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

	// Order matters for the three §20.1 questions -- "in order" is part
	// of the spec, not just their mere presence.
	iAssumption := strings.Index(got, "assumption")
	iContradict := strings.Index(got, "contradict")
	iSecondLook := strings.Index(got, "second look")
	if iAssumption >= iContradict || iContradict >= iSecondLook {
		t.Errorf("the three §20.1 questions are not in the required order (assumption=%d, contradict=%d, second look=%d)", iAssumption, iContradict, iSecondLook)
	}
}
