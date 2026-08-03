package handoff_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/handoff"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func TestRenderComment_TableDriven(t *testing.T) {
	line := 42
	tests := []struct {
		name     string
		findings []reviewpost.Finding
		wantAll  []string
	}{
		{
			name: "contract drift only, no line",
			findings: []reviewpost.Finding{
				reviewpost.BuildFinding(handoff.ContractDriftFinding("acme/widgets")),
			},
			wantAll: []string{"Handoff readiness", "acme/widgets", "contracts/api/*"},
		},
		{
			name: "todo with a line number is rendered as file:line",
			findings: []reviewpost.Finding{
				reviewpost.BuildFinding(handoff.TODOFindingInput(handoff.TODOFinding{
					FilePath: "apps/web/src/api.ts", Line: line, Text: "// TODO: wire real backend",
				})),
			},
			wantAll: []string{"apps/web/src/api.ts:42", "wire real backend"},
		},
		{
			name: "both kinds together",
			findings: []reviewpost.Finding{
				reviewpost.BuildFinding(handoff.ContractDriftFinding("acme/widgets")),
				reviewpost.BuildFinding(handoff.TODOFindingInput(handoff.TODOFinding{
					FilePath: "a.ts", Line: 1, Text: "// TODO: x",
				})),
			},
			wantAll: []string{"acme/widgets", "a.ts:1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handoff.RenderComment(tt.findings)
			for _, want := range tt.wantAll {
				if !strings.Contains(got, want) {
					t.Errorf("RenderComment() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestRenderComment_NeverMentionsIdentityHash pins the SAME "internal
// reconciliation key, not something a PR reader needs to see" discipline
// reviewpost.RenderVerdictComment's own doc comment already establishes:
// a finding's IdentityHash must never leak into the rendered comment.
func TestRenderComment_NeverMentionsIdentityHash(t *testing.T) {
	f := reviewpost.BuildFinding(handoff.ContractDriftFinding("acme/widgets"))
	got := handoff.RenderComment([]reviewpost.Finding{f})
	if strings.Contains(got, f.IdentityHash) {
		t.Errorf("RenderComment() leaked IdentityHash %q into: %q", f.IdentityHash, got)
	}
}
