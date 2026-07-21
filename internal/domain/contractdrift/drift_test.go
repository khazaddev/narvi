package contractdrift_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/contractdrift"
)

// TestHasDrifted covers every row of HasDrifted's own doc-comment truth
// table, table-driven, plus the adversarial "repo changed AND contract
// changed -> false" case explicitly named as the easiest row to get
// backwards.
func TestHasDrifted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		previous contractdrift.Snapshot
		current  contractdrift.Snapshot
		want     bool
	}{
		{
			name:     "first sighting: no prior snapshot recorded -> false",
			previous: contractdrift.Snapshot{RepoSHA: "", ContractsFingerprint: ""},
			current:  contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			want:     false,
		},
		{
			name:     "first sighting takes priority even if current has no contracts dir either",
			previous: contractdrift.Snapshot{RepoSHA: "", ContractsFingerprint: ""},
			current:  contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: ""},
			want:     false,
		},
		{
			name:     "no contracts dir at current ref -> false",
			previous: contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			current:  contractdrift.Snapshot{RepoSHA: "sha-2", ContractsFingerprint: ""},
			want:     false,
		},
		{
			// Isolates row 1 (previous.RepoSHA == "") from row 4's
			// equality check: previous's fingerprint is non-empty here
			// (unlike the two "first sighting" cases above, which both
			// pair previous.RepoSHA=="" with previous.ContractsFingerprint
			// == ""), so if row 1 were ever deleted, row 4 would compare
			// "fp-1" == "fp-1" and wrongly return true. Row 1 must fire
			// first, independent of what the rest of previous looks like.
			name:     "first sighting fires even when previous carries a stale non-empty fingerprint",
			previous: contractdrift.Snapshot{RepoSHA: "", ContractsFingerprint: "fp-1"},
			current:  contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			want:     false,
		},
		{
			// Isolates row 2 (current.ContractsFingerprint == "") from
			// row 4: previous.ContractsFingerprint is ALSO "" here (a
			// repo that has never had a contracts directory), so if row 2
			// were ever deleted, row 4 would compare "" == "" and wrongly
			// return true even though there is still no contract to have
			// drifted from. Mirrors
			// TestCheckContractDrift_NoContractsDirectory_FingerprintStoredEmptyNeverDrifts
			// (contractdrift_integration_test.go) at the unit level.
			name:     "no contracts dir at current ref, previous fingerprint also empty -> false",
			previous: contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: ""},
			current:  contractdrift.Snapshot{RepoSHA: "sha-2", ContractsFingerprint: ""},
			want:     false,
		},
		{
			name:     "repo unchanged -> false (can't have drifted)",
			previous: contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			current:  contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			want:     false,
		},
		{
			name:     "repo unchanged, even if fingerprint field somehow differs -> false",
			previous: contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			current:  contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-2"},
			want:     false,
		},
		{
			name:     "repo changed, contract fingerprint UNCHANGED -> true (the actual drift signal)",
			previous: contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			current:  contractdrift.Snapshot{RepoSHA: "sha-2", ContractsFingerprint: "fp-1"},
			want:     true,
		},
		{
			name:     "adversarial: repo changed AND contract fingerprint ALSO changed -> false (properly updated together, NOT drift)",
			previous: contractdrift.Snapshot{RepoSHA: "sha-1", ContractsFingerprint: "fp-1"},
			current:  contractdrift.Snapshot{RepoSHA: "sha-2", ContractsFingerprint: "fp-2"},
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := contractdrift.HasDrifted(tc.previous, tc.current)
			if got != tc.want {
				t.Errorf("HasDrifted(%+v, %+v) = %v, want %v", tc.previous, tc.current, got, tc.want)
			}
		})
	}
}
