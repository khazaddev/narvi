package ports

import "context"

// GitHubSourceControlHost is the ONLY source-control host this codebase's
// one real SourceControl implementation (internal/adapters/outbound/
// githubapi) ever actually queries -- production wiring always talks to
// GitHub's own real API (api.github.com) regardless of what host a
// session's own repo URL names. Shared (audit-remediation batch B3) by
// every caller that must reject a repo URL naming a host this
// deployment's configured adapter cannot serve: app/imagebuild.Builder.
// resolveRepoSHAs (before ever calling ResolveBranchSHA -- otherwise a
// GitLab/other repo URL's owner/repo path silently resolves against
// GitHub's real API for a coincidentally-matching path) and app/
// sessionactor's own warm-boot repo-access gate (imageresolve.go's
// repoAccessAllowedForSpawn, before ever deriving an owner/repo or
// spending a CheckRepoAccess call on it). Both used to hand-roll this
// same check independently (one had it, the other didn't) -- unified here
// since both are, structurally, asking the exact same question: "does
// this URL name the host our one wired SourceControl adapter actually
// talks to?" via reposource.CheckRepoHost.
//
// Update this (or make it configurable per-adapter) the day a second
// SourceControl implementation (e.g. GitLab) is actually wired -- see the
// SourceControl interface's own doc comment on why this port
// intentionally stays adapter-agnostic in its signatures; this one const
// is a deliberate, narrow, explicitly-called-out exception, not a
// precedent for leaking GitHub specifics into this port more broadly.
const GitHubSourceControlHost = "github.com"

// SupportedSourceControlHosts returns the full allowlist every
// reposource.CheckRepoHost call site in this codebase must use -- audit-
// remediation batch B3 round 2 (finding #7: two independently-maintained
// call sites -- app/imagebuild.Builder.resolveRepoSHAs and app/
// sessionactor's own warm-boot repo-access gate, imageresolve.go's
// repoAccessAllowedForSpawn -- happened to reference the SAME
// GitHubSourceControlHost constant today, but nothing structurally
// prevented one of them from being edited to accept an extra host (e.g. a
// future GitLab rollout, or a careless copy-paste) without the other
// following along, silently reopening exactly the drift this whole
// constant exists to close. Both call sites now call THIS function --
// never GitHubSourceControlHost directly -- so there is exactly one place
// in the codebase where "which hosts can this deployment's wired
// SourceControl actually serve" is decided; a future second adapter (e.g.
// GitLab) is wired by changing this one function's own return value, and
// every caller picks it up identically, by construction, not by
// convention.
func SupportedSourceControlHosts() []string {
	return []string{GitHubSourceControlHost}
}

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

// ResolveBranchSHASpec is what SourceControl.ResolveBranchSHA (Step 26,
// "image builds") needs to resolve a repo's current commit SHA for
// fingerprinting (§10 Phase 2: "fingerprint = repo SHAs + runtime
// version"). Owner/Repo are the same generic source-control concepts
// CreatePRSpec already uses, not GitHub-specific field names.
type ResolveBranchSHASpec struct {
	// Owner is the repo owner/organization.
	Owner string
	// Repo is the repo name (without owner prefix).
	Repo string
	// Branch is the branch to resolve. Empty means "the repo's own
	// default branch" -- mirroring how a null/empty
	// SessionConfigReposElem.Branch is already handled elsewhere for this
	// session's own repos (domain/gitstate's "checkout session branch,
	// create from base if absent" semantics): a not-yet-existing session
	// branch does not exist to resolve a SHA for in the first place, so
	// this always resolves the repo's OWN default branch's current SHA
	// instead -- the branch a fresh session branch would be created FROM.
	Branch string
	// Token is the same plaintext, decrypted OAuth access token shape
	// CreatePRSpec.Token already uses. Never logged by any caller or
	// implementation of this port.
	Token string
}

// ResolveContractsFingerprintSpec is what SourceControl.
// ResolveContractsFingerprint (Step 27, "mocking + contract drift", §14.3)
// needs to fingerprint a repo's configured contracts directory at one ref.
// Owner/Repo/Token are the same generic source-control concepts
// CreatePRSpec/ResolveBranchSHASpec already use, not GitHub-specific field
// names.
type ResolveContractsFingerprintSpec struct {
	// Owner is the repo owner/organization.
	Owner string
	// Repo is the repo name (without owner prefix).
	Repo string
	// Ref is the commit SHA (or branch/tag) to resolve the contracts
	// directory listing AT -- app/sessionactor's own checkContractDrift
	// (contractdrift.go) always passes the repo's own just-resolved
	// current commit SHA here, never a branch name, so drift detection
	// compares fingerprints at precise, stable commits rather than a
	// moving branch ref.
	Ref string
	// Path is the repo-relative path to the contracts directory (e.g.
	// "contracts/api") -- the Postgres Environment row's own
	// contracts_path column (sqlcgen.Environment.ContractsPath), resolved
	// by the caller (app/sessionactor/contractdrift.go's
	// checkContractDrift).
	Path string
	// Token is the same plaintext, decrypted OAuth access token shape
	// CreatePRSpec.Token/ResolveBranchSHASpec.Token already use. Never
	// logged by any caller or implementation of this port.
	Token string
}

// CheckRepoAccessSpec is what SourceControl.CheckRepoAccess (audit fix,
// "warm-boot image access control") needs to answer a single question:
// can spec.Token read spec.Owner/spec.Repo AT ALL, independent of any
// branch or commit. Owner/Repo/Token are the same generic source-control
// concepts CreatePRSpec/ResolveBranchSHASpec already use, not GitHub-
// specific field names.
type CheckRepoAccessSpec struct {
	// Owner is the repo owner/organization.
	Owner string
	// Repo is the repo name (without owner prefix).
	Repo string
	// Token is the same plaintext, decrypted OAuth access token shape
	// CreatePRSpec.Token/ResolveBranchSHASpec.Token already use. Never
	// logged by any caller or implementation of this port.
	Token string
}

// GetFileContentSpec is what SourceControl.GetFileContent (Step 48,
// "sentinels + suggestions", §12.2 item 2) needs to read one file's own
// current content at a ref -- the apply-suggestion endpoint's own
// precondition read, before it ever attempts to validate or commit a
// finding's SuggestedFix.
type GetFileContentSpec struct {
	Owner string
	Repo  string
	// Path is the repo-relative file path (review_findings.file_path).
	Path string
	// Ref is the commit SHA/branch to read at -- the PR's own CURRENT
	// head branch, so the apply-suggestion endpoint always validates
	// against the PR's real, live state, never a stale snapshot.
	Ref string
	// Token is the ACTING maintainer's own plaintext, decrypted OAuth
	// access token (§17.4/apply-suggestion's own "using the acting
	// maintainer's own OAuth token for attribution -- not the original
	// session creator's" requirement) -- never logged.
	Token string
}

// UpdateFileContentSpec is what SourceControl.UpdateFileContent (Step 48,
// §12.2 item 2) needs to commit a new version of one file directly onto a
// branch, via the source-control host's Contents API -- the apply-
// suggestion endpoint's own write, once ValidateSuggestionApplies (pure,
// internal/domain/reviewpost) has already confirmed the patch still
// applies against the file's current content.
type UpdateFileContentSpec struct {
	Owner string
	Repo  string
	Path  string
	// Content is the file's own NEW, full content (this port's
	// implementation is never asked to apply a patch itself -- the caller
	// has already computed the resulting content).
	Content string
	// SHA is the blob SHA of the CURRENT content being replaced (GitHub's
	// Contents API requires this on every update, as its own optimistic-
	// concurrency check -- a mismatched SHA means the file changed since
	// it was last read, and the API itself rejects the write).
	SHA string
	// Branch is the branch to commit onto (the PR's own current head
	// branch).
	Branch string
	// Message is the commit message.
	Message string
	// Token is the acting maintainer's own OAuth token, exactly like
	// GetFileContentSpec.Token above -- the resulting commit is
	// attributed to THIS user, never the original session creator.
	Token string
}

// RegisterPRStackSpec is what SourceControl.RegisterPRStack (Step 48,
// §17.2/§17.6) needs to register an already-open origin+fix PR pair as a
// real GitHub stack, once both exist.
type RegisterPRStackSpec struct {
	Owner string
	Repo  string
	// PRNumbers is bottom-to-top (§17.2: "pull_requests: [originPR,
	// fixPR]") -- always exactly two elements for this Step's one real
	// producer (§17.6: "the one pair, not an N-deep producer"), though
	// this port itself places no length restriction of its own.
	PRNumbers []int
	Token     string
}

// CreateBranchSpec is what SourceControl.CreateBranch (Step 48 confirmed-
// finding fix, §17.2) needs to create a brand-new branch ref pointing at
// an already-known commit SHA -- the sentinel-auto-fix flow's own fix for
// giving a fix child session an upstream branch DISTINCT from the origin
// PR's own head branch to check out and push to (see
// internal/app/outboxworker/sentinelautofix.go's own Deliver doc comment
// for the full "why": without this, the fix session's repos[].branch was
// the origin's own literal head-branch name, so its clone/checkout/push
// all targeted that SAME branch -- silently fast-forwarding the still-
// open origin PR with an unreviewed commit, and dooming the eventual fix-
// PR CreatePR call to Head == Base, which GitHub rejects outright).
type CreateBranchSpec struct {
	Owner string
	Repo  string
	// Branch is the NEW branch name to create (refs/heads/<Branch>).
	Branch string
	// SHA is the commit this new branch is created FROM -- the caller's
	// own already-resolved value (e.g. ResolveBranchSHA's own first return
	// value), never re-resolved by this method itself.
	SHA string
	// Token is the same plaintext, decrypted OAuth/bot access token shape
	// every other spec in this file already uses. Never logged.
	Token string
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

	// ResolveBranchSHA returns spec.Branch's current commit SHA (or the
	// repo's own default branch's, if spec.Branch is empty) -- Step 26's
	// ("image builds") own real, control-plane-side fingerprint input,
	// resolved directly via the source-control host's API rather than
	// waiting for a sandbox to report anything back (a deliberate design
	// decision: the control plane resolves SHAs itself, independently, see
	// internal/adapters/outbound/githubapi's own implementation doc
	// comment). Errors are plain, exactly like CreatePR above -- no
	// caller of this port retries or trips a circuit-breaker on a
	// resolution failure; a failure here means the caller falls back to
	// the base image for this spawn (§10 Phase 2: "always fall back to
	// base image on any miss -- never block a session"), never a fatal
	// condition.
	//
	// The second return, resolvedBranch, is spec.Branch verbatim when
	// non-empty, or the repo's own real default branch name (the SAME
	// name this call already resolved internally to pick which ref to
	// read the SHA at) when spec.Branch was empty -- audit finding F5's
	// follow-up: a caller that keys any per-branch state (e.g.
	// sessionactor/contractdrift.go's contract-drift snapshot key) off
	// spec.Branch directly would otherwise treat a session left with no
	// explicit branch and one that explicitly names the repo's actual
	// default branch as two different branches, even though they resolve
	// to the exact same ref -- splitting what should be one branch's
	// tracked state into two.
	ResolveBranchSHA(ctx context.Context, spec ResolveBranchSHASpec) (sha string, resolvedBranch string, err error)

	// ResolveContractsFingerprint fingerprints spec.Path's directory
	// listing at spec.Ref (Step 27, "mocking + contract drift", §14.3).
	// exists=false, err=nil means "no directory exists at that path/ref"
	// -- a legitimate, expected outcome (most repos/refs have no
	// contracts directory at all), NOT an error: callers MUST be able to
	// tell that apart from "the API call itself failed" (exists=false,
	// err!=nil never happens -- on any real failure, fingerprint is ""
	// and exists is false, but err is the one and only signal a caller
	// checks first). On success (err == nil), exists and fingerprint
	// together are authoritative: exists=true means fingerprint is
	// contractdrift.Fingerprint's own real, non-guessed output over the
	// directory's actual current contents.
	ResolveContractsFingerprint(ctx context.Context, spec ResolveContractsFingerprintSpec) (fingerprint string, exists bool, err error)

	// CheckRepoAccess reports whether spec.Token can read spec.Owner/
	// spec.Repo at all, independent of any branch/commit -- the audit fix
	// ("warm-boot image access control") that gates
	// app/sessionactor.resolveAndSetImage (imageresolve.go) before it will
	// either mint a pending image_builds row or warm-hit an already-ready
	// one for a given repo set: neither may happen unless the SPAWNING
	// session's own creator can currently read every one of its repos.
	//
	// (true, nil) means definitively yes. (false, nil) means definitively
	// no (e.g. a real GitHub 404, or a 403 that is NOT itself a rate-limit/
	// abuse-detection response, for this token against this repo) -- a
	// legitimate, expected answer, not a failure; callers use this to deny
	// warm-boot for exactly this spawn, exactly like ResolveContracts
	// Fingerprint's own exists=false, err=nil is a legitimate "no
	// directory" answer, not an error. err != nil means the check itself
	// could not be completed (network/timeout/5xx, OR a 403 that GitHub's
	// own real rate-limit/abuse-detection mechanism produced -- see
	// internal/adapters/outbound/githubapi.isRateLimitedResponse: GitHub
	// returns 403 for both a genuine denial and a rate-limited request, so
	// an implementation MUST distinguish them rather than reporting every
	// 403 as a definitive deny) -- callers MUST NOT treat err != nil as a
	// definitive "no": see resolveAndSetImage's own doc comment for why a
	// genuine "cannot read this repo" and "could not determine access
	// right now" must never collapse into the same on-disk consequence
	// for every session in the fleet.
	CheckRepoAccess(ctx context.Context, spec CheckRepoAccessSpec) (bool, error)

	// GetFileContent reads spec.Path's own content at spec.Ref (Step 48,
	// §12.2 item 2). exists=false, err=nil means the file does not exist
	// at that ref -- a legitimate answer (the apply-suggestion endpoint
	// treats this as "stale: the file this finding named no longer
	// exists"), never conflated with a genuine API failure (err != nil),
	// mirroring ResolveContractsFingerprint's own identical
	// exists/err-are-independent-signals discipline above.
	GetFileContent(ctx context.Context, spec GetFileContentSpec) (content, sha string, exists bool, err error)

	// UpdateFileContent commits spec.Content onto spec.Branch at spec.Path,
	// replacing the blob spec.SHA names (Step 48, §12.2 item 2) -- returns
	// the new commit's own SHA. Errors are plain, exactly like CreatePR/
	// ResolveBranchSHA above -- a stale spec.SHA (the file changed since
	// it was read) is one real, expected way this call fails; the caller
	// (reviewfindings.go's own ApplySuggestion handler) surfaces that as a
	// 409, never retries or auto-resolves.
	UpdateFileContent(ctx context.Context, spec UpdateFileContentSpec) (commitSHA string, err error)

	// RegisterPRStack groups spec.PRNumbers into a real GitHub stack (Step
	// 48, §17.2/§17.6) -- a SECOND call, made only after every named PR
	// already exists (this port never creates a PR itself here). Per
	// §17.2's own explicit design: a 404 or any other failure from this
	// call is meant to be logged and otherwise IGNORED by the caller --
	// this method itself still returns a plain error either way (the
	// ignoring is the caller's own policy decision, pushpr.go's
	// createSentinelFixPRBestEffort, not something this port silently
	// swallows on the caller's behalf).
	RegisterPRStack(ctx context.Context, spec RegisterPRStackSpec) error

	// CreateBranch creates a new branch ref (refs/heads/spec.Branch)
	// pointing at spec.SHA (Step 48 confirmed-finding fix, §17.2) --
	// IDEMPOTENT: a spec.Branch that already exists is treated as success,
	// never an error, since this method's own one real caller (the
	// sentinel-auto-fix notifier) may be redelivered for the SAME claim
	// before the branch's own creation is durably recorded anywhere else,
	// and re-observing an already-created ref is exactly the outcome an
	// idempotent retry must produce. Errors are plain, exactly like
	// CreatePR/ResolveBranchSHA above -- no caller of this port retries or
	// trips a circuit-breaker on a creation failure beyond the outbox
	// worker's own existing backoff/retry machinery.
	CreateBranch(ctx context.Context, spec CreateBranchSpec) error
}
