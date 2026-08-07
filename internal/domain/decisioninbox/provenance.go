package decisioninbox

import "fmt"

// ProvenanceKind is one of the three ways a PR can be "yours" (§16.1: "how
// it reached the user -- directly assigned vs requested reviewer vs via
// CODEOWNERS").
type ProvenanceKind string

const (
	// ProvenanceDirect means the user is a direct GitHub assignee.
	ProvenanceDirect ProvenanceKind = "assigned_directly"
	// ProvenanceRequestedReviewer means the user was requested as a
	// reviewer (individually, or via a team they belong to that GitHub
	// itself resolves for search purposes -- see githubapi.
	// ListOpenPRsForUser's own doc comment).
	ProvenanceRequestedReviewer ProvenanceKind = "requested_reviewer"
	// ProvenanceCodeOwners means the user (or a team they belong to)
	// matches a CODEOWNERS pattern covering at least one of the PR's
	// changed files.
	ProvenanceCodeOwners ProvenanceKind = "codeowners"
)

// Provenance is one row's own assignment-provenance field (§16.1: "a
// first-class field, not a UI nicety") -- printed on every ready_to_merge/
// needs_review row so "a queue whose origin the user can't trust" (§16.1's
// own words) never happens.
type Provenance struct {
	Kind ProvenanceKind
	// RepoFullName is set for ProvenanceRequestedReviewer only -- the
	// mockup's own rendered shape ("requested reviewer · acme/payroll-
	// api", docs/design/mockups.html).
	RepoFullName string
	// Pattern is set for ProvenanceCodeOwners only -- the winning
	// CODEOWNERS pattern (codeowners.Rule.Pattern), rendered verbatim
	// (mockup: "yours via CODEOWNERS · internal/app/scheduler/**").
	Pattern string
}

// ProvenanceInput bundles every raw signal ResolveProvenance needs --
// built by the app-layer aggregator from one already-fetched OpenPR plus
// its own ResolveCodeOwners lookup for that PR's changed files.
type ProvenanceInput struct {
	DirectlyAssigned  bool
	RequestedReviewer bool
	RepoFullName      string
	CodeOwnerMatch    bool
	CodeOwnerPattern  string
}

// ResolveProvenance picks ONE Provenance from in's possibly-multiple true
// signals, in a fixed precedence: a direct assignment is the most
// deliberate, explicit signal a human put THIS specific person on THIS
// specific PR, so it wins over a requested-review (which a team's own
// membership can trigger automatically) or a CODEOWNERS match (which is
// itself often WHY a review got requested in the first place, and would
// otherwise double-report the same underlying fact under a different
// label). ok=false means none of in's three signals held -- the row
// should never have been considered "assigned to this user" in the first
// place; the app-layer aggregator treats this as a defensive sanity
// check, never a row it renders.
func ResolveProvenance(in ProvenanceInput) (Provenance, bool) {
	switch {
	case in.DirectlyAssigned:
		return Provenance{Kind: ProvenanceDirect}, true
	case in.RequestedReviewer:
		return Provenance{Kind: ProvenanceRequestedReviewer, RepoFullName: in.RepoFullName}, true
	case in.CodeOwnerMatch:
		return Provenance{Kind: ProvenanceCodeOwners, Pattern: in.CodeOwnerPattern}, true
	default:
		return Provenance{}, false
	}
}

// Describe renders p exactly matching the mockup's own literal phrasing
// (docs/design/mockups.html, the qwhy spans under each qrow) -- the ONE
// place this exact copy is decided; a future UI never re-derives it from
// Kind/Pattern/RepoFullName separately.
func (p Provenance) Describe() string {
	switch p.Kind {
	case ProvenanceDirect:
		return "assigned to you directly"
	case ProvenanceRequestedReviewer:
		if p.RepoFullName != "" {
			return fmt.Sprintf("requested reviewer · %s", p.RepoFullName)
		}
		return "requested reviewer"
	case ProvenanceCodeOwners:
		if p.Pattern != "" {
			return fmt.Sprintf("yours via CODEOWNERS · %s", p.Pattern)
		}
		return "yours via CODEOWNERS"
	default:
		return "assigned to you"
	}
}
