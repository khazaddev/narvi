package ports

import "context"

// CreatePRSpec is what SourceControl.CreatePR needs to open a pull
// request. Owner/Repo/Head/Base are generic source-control concepts, not
// GitHub API field names -- nothing GitHub-specific leaks into this
// signature even though internal/adapters/outbound/githubapi is this
// port's only real implementation today (§4.3: "SourceControl (GitHub +
// GitLab: createPR, credential minting, push specs)").
type CreatePRSpec struct {
	// Owner is the repo owner/organization.
	Owner string
	// Repo is the repo name (without owner prefix).
	Repo string
	// Head is the branch the PR is created FROM (the session's own
	// already-pushed branch).
	Head string
	// Base is the branch the PR targets.
	Base string
	// Title is the PR title.
	Title string
	// Body is the PR description.
	Body string

	// Token is the SAME plaintext, decrypted OAuth access token the
	// scm-credentials endpoint already obtained for this session's own
	// user (§8.11: "PR created with the prompting user's OAuth token") --
	// the caller that already succeeded at the push already has this
	// value in hand, so CreatePR never re-fetches/re-decrypts it itself.
	// Never logged by any caller or implementation of this port.
	Token string
}

// PRRef is what CreatePR returns on success: the created PR's own number
// and URL.
type PRRef struct {
	Number int
	URL    string
}

// SourceControl is the port that creates a pull request against a source-
// control host (§4.3). internal/adapters/outbound/githubapi (Step 21) is
// the first real implementation; internal/adapters/outbound/gitlabapi
// remains an untouched stub -- this port exists specifically so a future
// GitLab implementation can satisfy the SAME interface (CLAUDE.md: "don't
// couple a port to a single adapter").
type SourceControl interface {
	// CreatePR opens a pull request per spec. Errors are plain (unlike
	// SandboxProvider, this Step does not invent a typed
	// transient/permanent classification for source-control errors --
	// no caller of this port needs one yet: PR creation is not retried by
	// any circuit-breaker-style mechanism this Step builds).
	CreatePR(ctx context.Context, spec CreatePRSpec) (PRRef, error)
}
