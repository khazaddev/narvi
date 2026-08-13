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
		got := reviewtriage.NewDecisionRecord(decision, cfg, decision.Depth, reviewtriage.Provenance{})
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

		got := reviewtriage.NewDecisionRecord(lightDecision, cfg, floored, reviewtriage.Provenance{})
		if got.Depth != "deep" {
			t.Errorf("Depth = %q, want deep", got.Depth)
		}
		if !got.Floored {
			t.Error("Floored = false, want true (final depth differs from the fresh decision)")
		}
	})
}
