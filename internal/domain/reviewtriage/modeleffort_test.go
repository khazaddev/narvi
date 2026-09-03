package reviewtriage_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/reviewtriage"
)

func TestModelAndEffort(t *testing.T) {
	t.Run("light path never sets model or effort", func(t *testing.T) {
		model, effort := reviewtriage.ModelAndEffort(reviewtriage.DepthLight, "anthropic/claude-frontier")
		if model != nil || effort != nil {
			t.Fatalf("ModelAndEffort(light) = (%v, %v), want (nil, nil)", model, effort)
		}
	})

	t.Run("deep path forces high effort even with no configured model", func(t *testing.T) {
		model, effort := reviewtriage.ModelAndEffort(reviewtriage.DepthDeep, "")
		if model != nil {
			t.Errorf("model = %v, want nil (unconfigured)", model)
		}
		if effort == nil || *effort != reviewtriage.EffortHigh {
			t.Errorf("effort = %v, want %q", effort, reviewtriage.EffortHigh)
		}
	})

	t.Run("deep path uses the configured model when set", func(t *testing.T) {
		model, effort := reviewtriage.ModelAndEffort(reviewtriage.DepthDeep, "anthropic/claude-frontier")
		if model == nil || *model != "anthropic/claude-frontier" {
			t.Errorf("model = %v, want anthropic/claude-frontier", model)
		}
		if effort == nil || *effort != reviewtriage.EffortHigh {
			t.Errorf("effort = %v, want %q", effort, reviewtriage.EffortHigh)
		}
	})
}
