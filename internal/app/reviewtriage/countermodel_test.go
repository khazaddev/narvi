package reviewtriage_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/app/reviewtriage"
)

// allProvidersCredentialed is every test in this file's own default input
// for ResolveCounterReviewerModel's credentialedProviders parameter (B2
// fix) unless a test case is SPECIFICALLY about that parameter's own
// gating behavior (TestResolveCounterReviewerModel_CredentialGating below)
// -- every other case in this file exists to exercise the RANKING/
// exclusion logic in isolation, so it opts every catalog provider in,
// exactly mirroring this codebase's own "credentials fully available"
// happy path.
var allProvidersCredentialed = map[string]bool{"anthropic": true, "openai": true, "google": true}

func TestResolveCounterReviewerModel(t *testing.T) {
	tests := []struct {
		name            string
		authoringModel  string
		wantEmpty       bool
		wantNotProvider string
	}{
		{"empty authoring model (human-authored PR, the common case) returns no override", "", true, ""},
		{"malformed authoring model (no slash) returns no override", "bogus-no-slash", true, ""},
		{"anthropic authoring model opposes to a non-anthropic provider", "anthropic/claude-opus-4-5", false, "anthropic"},
		{"openai authoring model opposes to a non-openai provider", "openai/gpt-5.4", false, "openai"},
		{"google authoring model opposes to a non-google provider", "google/gemini-2.5-flash", false, "google"},
		{"unrecognized provider in authoring model still opposes (no provider to skip)", "unknown-provider/some-model", false, ""},
		{"mixed-case authoring provider still excludes its own family (B10)", "Anthropic/claude-opus-4-5", false, "anthropic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewtriage.ResolveCounterReviewerModel(tt.authoringModel, allProvidersCredentialed)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("ResolveCounterReviewerModel(%q) = %q, want empty (no override)", tt.authoringModel, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("ResolveCounterReviewerModel(%q) = \"\", want a real provider/model override", tt.authoringModel)
			}
			provider, model, ok := strings.Cut(got, "/")
			if !ok || provider == "" || model == "" {
				t.Fatalf("ResolveCounterReviewerModel(%q) = %q, want a \"provider/model\" shaped string", tt.authoringModel, got)
			}
			if tt.wantNotProvider != "" && provider == tt.wantNotProvider {
				t.Errorf("ResolveCounterReviewerModel(%q) = %q, provider must NEVER match the authoring family %q", tt.authoringModel, got, tt.wantNotProvider)
			}
		})
	}
}

// TestResolveCounterReviewerModel_CredentialGating is B2's own regression
// test: "prefer no pin over guessing when the opposing provider is not
// known-credentialed". §25.1's own credential injection is best-effort
// and per-session configured -- counterReviewerProviderPreference's fixed
// 3-provider list only names which providers the MECHANISM supports,
// never which ones are actually usable for a given session -- so a
// candidate provider absent from credentialedProviders must be skipped
// exactly like an excluded authoring-family match, never guessed at.
func TestResolveCounterReviewerModel_CredentialGating(t *testing.T) {
	tests := []struct {
		name                  string
		authoringModel        string
		credentialedProviders map[string]bool
		want                  string
	}{
		{
			name:                  "opposing provider not credentialed falls through to the next credentialed preference provider",
			authoringModel:        "anthropic/claude-opus-4-5",
			credentialedProviders: map[string]bool{"google": true}, // openai (preference order's first non-anthropic entry) deliberately absent
			want:                  "google/gemini-3-pro-preview",
		},
		{
			name:                  "no preference provider credentialed at all returns no override, never a guess",
			authoringModel:        "anthropic/claude-opus-4-5",
			credentialedProviders: map[string]bool{},
			want:                  "",
		},
		{
			name:                  "nil credentialedProviders degrades identically to an empty set (Go nil-map read is always false)",
			authoringModel:        "anthropic/claude-opus-4-5",
			credentialedProviders: nil,
			want:                  "",
		},
		{
			name:                  "a credentialedProviders entry for the EXCLUDED authoring family alone still yields no override",
			authoringModel:        "anthropic/claude-opus-4-5",
			credentialedProviders: map[string]bool{"anthropic": true},
			want:                  "",
		},
		{
			name:                  "every preference provider credentialed resolves the normal, highest-priority pick",
			authoringModel:        "anthropic/claude-opus-4-5",
			credentialedProviders: allProvidersCredentialed,
			want:                  "openai/gpt-5.6-fast",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewtriage.ResolveCounterReviewerModel(tt.authoringModel, tt.credentialedProviders)
			if got != tt.want {
				t.Errorf("ResolveCounterReviewerModel(%q, %v) = %q, want %q", tt.authoringModel, tt.credentialedProviders, got, tt.want)
			}
		})
	}
}

// TestResolveCounterReviewerModel_RanksByContextWindowThenCost_NotAlphabetical
// is B3's own regression test: pins a LITERAL, hand-computed model ID from
// the embedded catalog snapshot (internal/app/modelcatalog/snapshot.json),
// so this genuinely exercises rankCounterReviewerCandidates' own
// ContextWindow-desc/Cost.Output-desc/ID-asc ranking, rather than merely
// asserting "some non-blank string came back" (this file's own pre-
// existing wantNotProvider-only cases, which would pass unchanged whether
// the ranking were sensible or pure alphabetical). "openai/gpt-5.4" as the
// authoring model excludes the openai family, leaving anthropic first in
// counterReviewerProviderPreference: among anthropic's own Reasoning-
// capable models, "claude-fable-5" has the LARGEST ContextWindow (tied
// with several claude-opus-4-*/claude-sonnet-* entries at 1,000,000) and,
// among THOSE, the highest Cost.Output (tied with claude-opus-4-8-fast/
// claude-opus-5-fast at 50), winning the final ID-ascending tie-break --
// coincidentally the SAME model the old pure-alphabetical ranking would
// also have picked (its name happens to sort first too), so a second case
// below pins a provider where the two rankings genuinely diverge.
func TestResolveCounterReviewerModel_RanksByContextWindowThenCost_NotAlphabetical(t *testing.T) {
	got := reviewtriage.ResolveCounterReviewerModel("openai/gpt-5.4", allProvidersCredentialed)
	want := "anthropic/claude-fable-5"
	if got != want {
		t.Errorf("ResolveCounterReviewerModel(%q) = %q, want %q", "openai/gpt-5.4", got, want)
	}
}

// TestResolveCounterReviewerModel_RanksByContextWindowThenCost_DivergesFromAlphabetical
// pins the ONE case in this catalog snapshot where the new ContextWindow/
// Cost ranking and the OLD pure-alphabetical ranking genuinely disagree:
// excluding anthropic leaves openai first in preference order, and
// alphabetically "gpt-5.3-codex-spark" would have won (its own name
// simply sorts first) despite a narrow 128,000-token ContextWindow --
// nowhere near openai's own catalog maximum of 1,050,000. Under the new
// ranking, the largest-ContextWindow tier (1,050,000, many entries) is
// resolved by its own highest Cost.Output (60, tied between "gpt-5.6-fast"
// and "gpt-5.6-sol-fast"), and "gpt-5.6-fast" wins the final ID-ascending
// tie-break. If this ever regresses back to a bare alphabetically-first
// pick, this test fails with "gpt-5.3-codex-spark" instead.
func TestResolveCounterReviewerModel_RanksByContextWindowThenCost_DivergesFromAlphabetical(t *testing.T) {
	got := reviewtriage.ResolveCounterReviewerModel("anthropic/claude-opus-4-5", allProvidersCredentialed)
	want := "openai/gpt-5.6-fast"
	if got != want {
		t.Errorf("ResolveCounterReviewerModel(%q) = %q, want %q (alphabetical would have wrongly picked openai/gpt-5.3-codex-spark, a 128k-context variant, over this catalog's own 1.05M-context/highest-cost tier)", "anthropic/claude-opus-4-5", got, want)
	}
}

// TestResolveCounterReviewerModel_Deterministic pins that repeated calls
// with the same input always return the identical result -- this
// function must never depend on map/slice iteration order (pickReasoningOrFirst's
// own doc comment).
func TestResolveCounterReviewerModel_Deterministic(t *testing.T) {
	first := reviewtriage.ResolveCounterReviewerModel("anthropic/claude-opus-4-5", allProvidersCredentialed)
	for i := 0; i < 20; i++ {
		if got := reviewtriage.ResolveCounterReviewerModel("anthropic/claude-opus-4-5", allProvidersCredentialed); got != first {
			t.Fatalf("ResolveCounterReviewerModel is non-deterministic: call %d = %q, first call = %q", i, got, first)
		}
	}
}
