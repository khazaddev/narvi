package ports

import (
	"reflect"
	"testing"
)

// TestSupportedSourceControlHosts pins SupportedSourceControlHosts' own
// exact contract (audit-remediation batch B3 round 2, finding #7): every
// reposource.CheckRepoHost call site in this codebase (app/imagebuild.
// Builder.resolveRepoSHAs and app/sessionactor's own warm-boot repo-access
// gate, imageresolve.go's repoAccessAllowedForSpawn -- and, as of this same
// batch, contractdrift.go's checkContractDriftForRepo and pushpr.go's
// createPRBestEffort) now calls THIS function rather than naming
// GitHubSourceControlHost directly, so there is exactly one place in the
// codebase where the allowed-host set is decided. This test exists so a
// future edit widening (or narrowing) that set is a conscious, visible
// change to this one function -- caught here -- rather than a change
// silently made to only one of several call sites.
func TestSupportedSourceControlHosts(t *testing.T) {
	got := SupportedSourceControlHosts()
	want := []string{GitHubSourceControlHost}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SupportedSourceControlHosts() = %v, want %v", got, want)
	}

	// Every call site passes this slice on, verbatim, to
	// reposource.CheckRepoHost -- proving it is non-empty and contains only
	// github.com today guards against an accidental empty-allowlist regression
	// (which would deny every repo url outright) as well as an unnoticed
	// widening.
	if len(got) != 1 {
		t.Fatalf("len(SupportedSourceControlHosts()) = %d, want exactly 1 (github.com only, until a second SourceControl adapter is actually wired)", len(got))
	}
	if got[0] != "github.com" {
		t.Errorf("SupportedSourceControlHosts()[0] = %q, want %q", got[0], "github.com")
	}
}
