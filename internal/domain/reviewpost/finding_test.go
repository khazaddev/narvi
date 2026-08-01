package reviewpost_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func coverageKind() *reviewpost.SentinelKind {
	k := reviewpost.SentinelKindCoverage
	return &k
}

func docsDriftKind() *reviewpost.SentinelKind {
	k := reviewpost.SentinelKindDocsDrift
	return &k
}

func intPtr(n int) *int { return &n }

// TestComputeFindingIdentity_SurvivesLineShift is Step 48's own explicitly
// required test: a finding re-reported at a SHIFTED line number must hash
// identically -- Line is deliberately excluded from identity (finding.go's
// own doc comment).
func TestComputeFindingIdentity_SurvivesLineShift(t *testing.T) {
	kind := coverageKind()
	a := reviewpost.ComputeFindingIdentity(kind, "internal/foo/bar.go", "Missing test coverage for the error path.")
	b := reviewpost.ComputeFindingIdentity(kind, "internal/foo/bar.go", "Missing test coverage for the error path.")

	if a != b {
		t.Fatalf("ComputeFindingIdentity() not stable for identical (kind, path, description): %q != %q", a, b)
	}

	// The whole point: Line is not even a parameter of this function, so a
	// caller re-reporting the SAME finding at a shifted line number (the
	// caller's own FindingInput.Line differs) computes the SAME identity,
	// by construction -- there is no way to make this function's own
	// output differ by varying only a line number, since it never
	// receives one.
}

func TestComputeFindingIdentity_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		kindA     *reviewpost.SentinelKind
		pathA     string
		descA     string
		kindB     *reviewpost.SentinelKind
		pathB     string
		descB     string
		wantEqual bool
	}{
		{
			name:  "identical inputs match",
			kindA: coverageKind(), pathA: "a/b.go", descA: "missing coverage",
			kindB: coverageKind(), pathB: "a/b.go", descB: "missing coverage",
			wantEqual: true,
		},
		{
			name:  "different kind does not match",
			kindA: coverageKind(), pathA: "a/b.go", descA: "missing coverage",
			kindB: docsDriftKind(), pathB: "a/b.go", descB: "missing coverage",
			wantEqual: false,
		},
		{
			name:  "nil kind vs non-nil kind does not match",
			kindA: nil, pathA: "a/b.go", descA: "missing coverage",
			kindB: coverageKind(), pathB: "a/b.go", descB: "missing coverage",
			wantEqual: false,
		},
		{
			name:  "different file path does not match (rejects file-path-independent scheme)",
			kindA: coverageKind(), pathA: "a/b.go", descA: "missing error-path test",
			kindB: coverageKind(), pathB: "c/d.go", descB: "missing error-path test",
			wantEqual: false,
		},
		{
			name:  "different description does not match",
			kindA: coverageKind(), pathA: "a/b.go", descA: "missing coverage for X",
			kindB: coverageKind(), pathB: "a/b.go", descB: "missing coverage for Y",
			wantEqual: false,
		},
		{
			name:  "leading ./ and cleaned path normalize identically",
			kindA: coverageKind(), pathA: "./a/b.go", descA: "missing coverage",
			kindB: coverageKind(), pathB: "a/b.go", descB: "missing coverage",
			wantEqual: true,
		},
		{
			name:  "double slash normalizes identically",
			kindA: coverageKind(), pathA: "a//b.go", descA: "missing coverage",
			kindB: coverageKind(), pathB: "a/b.go", descB: "missing coverage",
			wantEqual: true,
		},
		{
			name:  "description whitespace/case normalizes identically",
			kindA: coverageKind(), pathA: "a/b.go", descA: "  Missing   Coverage  ",
			kindB: coverageKind(), pathB: "a/b.go", descB: "missing coverage",
			wantEqual: true,
		},
		{
			name:  "description trailing punctuation is NOT stripped (whitespace-level, not paraphrase-level, an accepted residual)",
			kindA: coverageKind(), pathA: "a/b.go", descA: "missing coverage.",
			kindB: coverageKind(), pathB: "a/b.go", descB: "missing coverage",
			wantEqual: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := reviewpost.ComputeFindingIdentity(tt.kindA, tt.pathA, tt.descA)
			b := reviewpost.ComputeFindingIdentity(tt.kindB, tt.pathB, tt.descB)
			if (a == b) != tt.wantEqual {
				t.Errorf("ComputeFindingIdentity(%v,%q,%q)=%q vs (%v,%q,%q)=%q: equal=%v, want %v",
					tt.kindA, tt.pathA, tt.descA, a, tt.kindB, tt.pathB, tt.descB, b, a == b, tt.wantEqual)
			}
		})
	}
}

func TestComputeFindingIdentity_DeterministicHexShape(t *testing.T) {
	got := reviewpost.ComputeFindingIdentity(nil, "a.go", "x")
	if len(got) != 64 {
		t.Fatalf("ComputeFindingIdentity() = %q, want a 64-char hex sha256 digest, got len %d", got, len(got))
	}
	for _, c := range got {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("ComputeFindingIdentity() = %q, contains non-hex-lowercase char %q", got, c)
		}
	}
}

func TestValidateFindingInput(t *testing.T) {
	valid := reviewpost.FindingInput{
		SentinelKind: coverageKind(),
		Severity:     review.RiskLevelMedium,
		FilePath:     "internal/foo/bar.go",
		Line:         intPtr(42),
		Description:  "Missing coverage for the error path.",
	}

	tests := []struct {
		name    string
		mutate  func(reviewpost.FindingInput) reviewpost.FindingInput
		wantErr error
	}{
		{
			name:    "valid input passes",
			mutate:  func(f reviewpost.FindingInput) reviewpost.FindingInput { return f },
			wantErr: nil,
		},
		{
			name: "nil sentinel kind is legal (ordinary risk-map finding)",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.SentinelKind = nil
				return f
			},
			wantErr: nil,
		},
		{
			name: "nil line is legal (file-level finding)",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.Line = nil
				return f
			},
			wantErr: nil,
		},
		{
			name: "unrecognized sentinel kind rejected",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				bogus := reviewpost.SentinelKind("bogus")
				f.SentinelKind = &bogus
				return f
			},
			wantErr: reviewpost.ErrInvalidSentinelKind,
		},
		{
			name: "unrecognized severity rejected",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.Severity = review.RiskLevel("bogus")
				return f
			},
			wantErr: reviewpost.ErrInvalidFindingSeverity,
		},
		{
			name: "zero-value severity rejected (fail-conservative: absent field == garbled field)",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.Severity = ""
				return f
			},
			wantErr: reviewpost.ErrInvalidFindingSeverity,
		},
		{
			name: "empty file path rejected",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.FilePath = "   "
				return f
			},
			wantErr: reviewpost.ErrEmptyFindingFilePath,
		},
		{
			name: "empty description rejected",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.Description = ""
				return f
			},
			wantErr: reviewpost.ErrEmptyFindingDescription,
		},
		{
			name: "zero line rejected (1-based)",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.Line = intPtr(0)
				return f
			},
			wantErr: reviewpost.ErrInvalidFindingLine,
		},
		{
			name: "negative line rejected",
			mutate: func(f reviewpost.FindingInput) reviewpost.FindingInput {
				f.Line = intPtr(-1)
				return f
			},
			wantErr: reviewpost.ErrInvalidFindingLine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reviewpost.ValidateFindingInput(tt.mutate(valid))
			if err != tt.wantErr {
				t.Errorf("ValidateFindingInput() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildFinding_IdentityMatchesComputeFindingIdentity(t *testing.T) {
	in := reviewpost.FindingInput{
		SentinelKind: docsDriftKind(),
		Severity:     review.RiskLevelLow,
		FilePath:     "docs/README.md",
		Line:         intPtr(3),
		Description:  "Stale example.",
	}

	f := reviewpost.BuildFinding(in)

	want := reviewpost.ComputeFindingIdentity(in.SentinelKind, in.FilePath, in.Description)
	if f.IdentityHash != want {
		t.Errorf("BuildFinding().IdentityHash = %q, want %q", f.IdentityHash, want)
	}
	if f.FilePath != in.FilePath || f.Description != in.Description || f.Severity != in.Severity {
		t.Errorf("BuildFinding() did not carry every field verbatim: got %+v, from %+v", f, in)
	}
}

func TestBuildFindings_NilForEmptyInput(t *testing.T) {
	if got := reviewpost.BuildFindings(reviewpost.VerdictInput{}); got != nil {
		t.Errorf("BuildFindings(no findings) = %v, want nil", got)
	}
}
