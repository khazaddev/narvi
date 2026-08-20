// Package reviewcontext implements §8.2's ("review sessions", §8.2) own
// inline-pre-fetched-context assembly: given a PR identity (owner/repo/
// number) and its own reviewing event's current head, fetch the PR's
// current diff and (when present) its GitHub-native stack context
// (§17.6), and hand back a internal/domain/review.PreFetchedContext ready
// for that pure package's own RenderTurnPrompt to fold into a review turn's
// prompt text.
//
// This package is the one seam every review-session trigger path shares --
// internal/adapters/inbound/github's own @mention (issue_comment/
// pull_request_review_comment) and label-retrigger lanes, and internal/
// adapters/inbound/httpapi's own manual re-review REST button -- so the
// "fetch diff, fetch/derive stack, degrade gracefully on either failure"
// sequencing exists in exactly one place, never re-implemented per ingress
// adapter. Living under internal/app (not internal/domain, since this
// package does real I/O: two outbound GitHub API calls) mirrors internal/
// app/actorauthz's own identical "shared app-layer helper several inbound
// adapters call" precedent.
//
// # Why a new app-layer package, not internal/adapters/inbound/github itself
//
// The obvious-looking alternative -- putting Fetch directly in internal/
// adapters/inbound/github and having httpapi's own manual-retrigger
// handler call it from there -- does not compile: that package's own
// coalesce.go already imports internal/adapters/inbound/httpapi (for
// CreateSessionOnTx/CreateTurnForBot, its own doc comment), so httpapi
// importing github back would be a compile-time import cycle. This
// package is the shared seam BELOW both inbound adapters instead, exactly
// where a capability two siblings both need, that neither package can
// depend on the other for, belongs.
//
// # Why Fetcher references githubapi's concrete types directly, not a
// # internal/app/ports abstraction
//
// This is a deliberate, narrow exception to this codebase's usual "app
// packages depend on ports, never a concrete adapter" discipline (CLAUDE.md:
// "don't couple a port to a single adapter") -- justified the SAME way
// internal/adapters/inbound/github's own PullRequestResolver interface
// (headresolve.go) already justifies an identical choice for GetPullRequest
// itself: "this need is specific to this one ingress adapter['s own review-
// session concern], not a general 'source control' operation another
// adapter would ever implement independently, so it does not belong in
// internal/app/ports." Diff-fetching and GitHub-native stack context are
// GitHub-specific capabilities with no GitLab (ports.SourceControl's own
// second, still-stubbed implementation) equivalent designed anywhere in
// this plan today -- inventing a portable abstraction for a capability
// exactly one adapter implements would be speculative scope, not the
// "second adapter every port must anticipate" CLAUDE.md actually asks for.
//
// # Best-effort by design
//
// Every fetch this package makes is best-effort: a diff-fetch failure or a
// stack-resolution failure degrades to a zero value for that ONE piece
// (logged, never propagated as an error) rather than failing the caller's
// own review-turn creation. §8.2's own pre-fetch requirement is a
// CONVENIENCE ("the agent must not need to run `gh pr diff` repeatedly"),
// not a correctness-critical precondition for the turn to exist at all --
// an agent that receives no pre-fetched diff can still fall back to
// shelling out itself, exactly the (slower, but functional) behavior this
// feature exists to make unnecessary in the common case, not the ONLY
// path. Fetch therefore never returns an error of its own.
package reviewcontext
