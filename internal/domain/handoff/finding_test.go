package handoff_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/handoff"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestContractDriftFinding_NeverCarriesASentinelKind pins doc.go's own
// design call #1: a handoff finding must NEVER be eligible for the
// unrelated sentinel-auto-fix flow (reviewpost's own hasSentinelFinding
// checks exactly this field). A regression here would silently make
// every future scoped-session PR with contract drift ALSO a candidate
// for spawning a sentinel-auto-fix child session on any repo with
// sentinel_autofix_enabled on -- exactly the "no child session for
// handoff" scope violation this Step's own brief forbids.
func TestContractDriftFinding_NeverCarriesASentinelKind(t *testing.T) {
	in := handoff.ContractDriftFinding("acme/widgets")
	if in.SentinelKind != nil {
		t.Fatalf("ContractDriftFinding: SentinelKind = %v, want nil", in.SentinelKind)
	}
}

func TestTODOFindingInput_NeverCarriesASentinelKind(t *testing.T) {
	in := handoff.TODOFindingInput(handoff.TODOFinding{FilePath: "a.ts", Line: 3, Text: "// TODO: x"})
	if in.SentinelKind != nil {
		t.Fatalf("TODOFindingInput: SentinelKind = %v, want nil", in.SentinelKind)
	}
}

func TestContractDriftFinding_ValidAndMediumSeverity(t *testing.T) {
	in := handoff.ContractDriftFinding("acme/widgets")
	if err := reviewpost.ValidateFindingInput(in); err != nil {
		t.Fatalf("ValidateFindingInput(ContractDriftFinding(...)) = %v, want nil", err)
	}
	if in.Severity != review.RiskLevelMedium {
		t.Errorf("ContractDriftFinding severity = %q, want %q", in.Severity, review.RiskLevelMedium)
	}
	if !strings.Contains(in.Description, "acme/widgets") {
		t.Errorf("ContractDriftFinding description %q does not mention repo", in.Description)
	}
}

func TestTODOFindingInput_ValidAndLowSeverity(t *testing.T) {
	f := handoff.TODOFinding{FilePath: "apps/web/src/api.ts", Line: 42, Text: "// TODO: wire real backend"}
	in := handoff.TODOFindingInput(f)
	if err := reviewpost.ValidateFindingInput(in); err != nil {
		t.Fatalf("ValidateFindingInput(TODOFindingInput(...)) = %v, want nil", err)
	}
	if in.Severity != review.RiskLevelLow {
		t.Errorf("TODOFindingInput severity = %q, want %q", in.Severity, review.RiskLevelLow)
	}
	if in.FilePath != f.FilePath {
		t.Errorf("TODOFindingInput FilePath = %q, want %q", in.FilePath, f.FilePath)
	}
	if in.Line == nil || *in.Line != f.Line {
		t.Errorf("TODOFindingInput Line = %v, want %d", in.Line, f.Line)
	}
	if !strings.Contains(in.Description, f.Text) {
		t.Errorf("TODOFindingInput description %q does not contain marker text %q", in.Description, f.Text)
	}
}

func TestBuildFindingInputs_TableDriven(t *testing.T) {
	todo1 := handoff.TODOFinding{FilePath: "a.ts", Line: 1, Text: "// TODO: a"}
	todo2 := handoff.TODOFinding{FilePath: "b.ts", Line: 2, Text: "// TODO: b"}

	tests := []struct {
		name            string
		repoFullName    string
		contractDrifted bool
		todos           []handoff.TODOFinding
		wantCount       int
	}{
		{"nothing to report", "acme/widgets", false, nil, 0},
		{"only contract drift", "acme/widgets", true, nil, 1},
		{"only todos", "acme/widgets", false, []handoff.TODOFinding{todo1, todo2}, 2},
		{"both", "acme/widgets", true, []handoff.TODOFinding{todo1}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handoff.BuildFindingInputs(tt.repoFullName, tt.contractDrifted, tt.todos)
			if len(got) != tt.wantCount {
				t.Fatalf("BuildFindingInputs() returned %d findings, want %d", len(got), tt.wantCount)
			}
			for _, in := range got {
				if err := reviewpost.ValidateFindingInput(in); err != nil {
					t.Errorf("BuildFindingInputs() produced an invalid FindingInput: %v", err)
				}
			}
		})
	}
}
