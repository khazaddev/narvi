package reviewtriage_test

import (
	"reflect"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
)

func TestNewDecisionRecord(t *testing.T) {
	sig := reviewtriage.Signals{ChangedPaths: []string{"migrations/x.sql"}}
	cfg := reviewtriage.DefaultConfig()
	decision := reviewtriage.Decide(sig, cfg)

	t.Run("not floored", func(t *testing.T) {
		got := reviewtriage.NewDecisionRecord(decision, cfg, decision.Depth, reviewtriage.Provenance{}, nil, nil)
		want := reviewtriage.DecisionRecord{
			Depth:                "deep",
			Reason:               string(reviewtriage.ReasonSensitiveGlob),
			MatchedSensitiveTags: []string{"migrations"},
			ChangedLines:         0,
			DistinctRoots:        1,
			Mode:                 "auto",
			Floored:              false,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("NewDecisionRecord() = %#v, want %#v", got, want)
		}
	})

	t.Run("floored by a higher-ranked prior depth", func(t *testing.T) {
		lightSig := reviewtriage.Signals{ChangedPaths: []string{"internal/app/foo/a.go"}}
		lightDecision := reviewtriage.Decide(lightSig, cfg)
		floored := reviewtriage.Floor(lightDecision.Depth, reviewtriage.DepthDeep)

		got := reviewtriage.NewDecisionRecord(lightDecision, cfg, floored, reviewtriage.Provenance{}, nil, nil)
		if got.Depth != "deep" {
			t.Errorf("Depth = %q, want deep", got.Depth)
		}
		if !got.Floored {
			t.Error("Floored = false, want true (final depth differs from the fresh decision)")
		}
	})

	// D4 (nice-to-have adversarial-review fix): ResolvedModelID/
	// ResolvedEffort record ModelAndEffort's own actual output for THIS
	// turn -- both nil in, both empty out (the light path, or an
	// unconfigured deep-tier model); both set in, both recorded verbatim
	// out.
	t.Run("resolved model/effort recorded verbatim", func(t *testing.T) {
		modelID := "anthropic/claude-frontier"
		effort := reviewtriage.EffortHigh
		got := reviewtriage.NewDecisionRecord(decision, cfg, decision.Depth, reviewtriage.Provenance{}, &modelID, &effort)
		if got.ResolvedModelID != modelID {
			t.Errorf("ResolvedModelID = %q, want %q", got.ResolvedModelID, modelID)
		}
		if got.ResolvedEffort != effort {
			t.Errorf("ResolvedEffort = %q, want %q", got.ResolvedEffort, effort)
		}
	})

	t.Run("nil resolved model/effort records as empty, never a nil-pointer panic", func(t *testing.T) {
		got := reviewtriage.NewDecisionRecord(decision, cfg, decision.Depth, reviewtriage.Provenance{}, nil, nil)
		if got.ResolvedModelID != "" || got.ResolvedEffort != "" {
			t.Errorf("ResolvedModelID/ResolvedEffort = %q/%q, want both empty", got.ResolvedModelID, got.ResolvedEffort)
		}
	})
}
