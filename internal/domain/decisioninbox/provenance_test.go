package decisioninbox_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/decisioninbox"
)

func TestResolveProvenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   decisioninbox.ProvenanceInput
		want decisioninbox.Provenance
		ok   bool
	}{
		{
			name: "direct assignment wins over everything else",
			in: decisioninbox.ProvenanceInput{
				DirectlyAssigned: true, RequestedReviewer: true, RepoFullName: "acme/payroll-api",
				CodeOwnerMatch: true, CodeOwnerPattern: "internal/app/scheduler/**",
			},
			want: decisioninbox.Provenance{Kind: decisioninbox.ProvenanceDirect},
			ok:   true,
		},
		{
			name: "requested reviewer wins over codeowners",
			in: decisioninbox.ProvenanceInput{
				RequestedReviewer: true, RepoFullName: "acme/payroll-api",
				CodeOwnerMatch: true, CodeOwnerPattern: "internal/app/scheduler/**",
			},
			want: decisioninbox.Provenance{Kind: decisioninbox.ProvenanceRequestedReviewer, RepoFullName: "acme/payroll-api"},
			ok:   true,
		},
		{
			name: "codeowners alone",
			in:   decisioninbox.ProvenanceInput{CodeOwnerMatch: true, CodeOwnerPattern: "internal/app/scheduler/**"},
			want: decisioninbox.Provenance{Kind: decisioninbox.ProvenanceCodeOwners, Pattern: "internal/app/scheduler/**"},
			ok:   true,
		},
		{
			name: "nothing set is not ok",
			in:   decisioninbox.ProvenanceInput{},
			want: decisioninbox.Provenance{},
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := decisioninbox.ResolveProvenance(tc.in)
			if ok != tc.ok {
				t.Fatalf("ResolveProvenance() ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("ResolveProvenance() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProvenance_Describe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    decisioninbox.Provenance
		want string
	}{
		{"direct", decisioninbox.Provenance{Kind: decisioninbox.ProvenanceDirect}, "assigned to you directly"},
		{
			"requested reviewer with repo",
			decisioninbox.Provenance{Kind: decisioninbox.ProvenanceRequestedReviewer, RepoFullName: "acme/payroll-api"},
			"requested reviewer · acme/payroll-api",
		},
		{
			"requested reviewer without repo",
			decisioninbox.Provenance{Kind: decisioninbox.ProvenanceRequestedReviewer},
			"requested reviewer",
		},
		{
			"codeowners with pattern",
			decisioninbox.Provenance{Kind: decisioninbox.ProvenanceCodeOwners, Pattern: "internal/app/scheduler/**"},
			"yours via CODEOWNERS · internal/app/scheduler/**",
		},
		{
			"codeowners without pattern",
			decisioninbox.Provenance{Kind: decisioninbox.ProvenanceCodeOwners},
			"yours via CODEOWNERS",
		},
		{"unrecognized kind fails to a generic description", decisioninbox.Provenance{Kind: "bogus"}, "assigned to you"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.p.Describe(); got != tc.want {
				t.Errorf("Describe() = %q, want %q", got, tc.want)
			}
		})
	}
}
