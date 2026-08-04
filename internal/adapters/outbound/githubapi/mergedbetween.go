// This file (mergedbetween.go) implements ports.SourceControl.
// ListMergedBetween (Step 50, "release PR review", §15.2): "list PRs
// merged since the last release point, check review + CI-at-merge-SHA +
// reverts for each". For a release PR itself, spec.BaseRef/HeadRef are
// simply that PR's own base/head branches -- exactly what a real
// `git compare BaseRef...HeadRef` names -- so this answers "which
// already-individually-reviewed PRs does this release PR bundle".
//
// # Discovering constituent PRs
//
// GitHub's REST API has no single endpoint that lists "PRs merged in
// this ref range" directly. This adapter derives it from the SAME
// compare API GetPullRequestDiff's content-negotiated diff endpoint
// already targets (GET .../compare/{base}...{head}, here requested with
// GitHub's default JSON shape instead), which returns every commit
// reachable from HeadRef but not BaseRef. Each commit's own message is
// matched against GitHub's two well-known, default merge-commit-message
// shapes:
//
//   - "Merge pull request #N from ..." (the default "Create a merge
//     commit" strategy).
//   - "<title> (#N)" as the first line (the default "Squash and merge"
//     strategy).
//
// KNOWN, ACCEPTED LIMITATION: a PR merged via GitHub's third strategy,
// "Rebase and merge", leaves no reliable back-reference to its own PR
// number in any commit message at all (rebased commits keep their
// ORIGINAL messages verbatim) -- GitHub's own authoritative answer for
// "which PR(s) does this commit belong to" is a dedicated per-commit
// endpoint (GET /commits/{sha}/pulls), which this adapter does NOT call
// per commit here (a release PR can bundle hundreds of commits; calling
// an extra endpoint for every single one, on top of the several already
// made per DISCOVERED PR below, was judged too expensive for what this
// Step needs). A rebase-merged constituent PR is therefore silently
// absent from this manifest today -- a real, honestly-documented gap,
// not a correctness bug this file pretends not to have.
//
// # Per-PR fields, and how each is actually determined
//
// See buildMergedPR's own doc comment for the full per-field sourcing
// (reviews, CI-at-merge-SHA via combined status + check-runs, admin-
// override inference via branch protection, revert detection via one
// batch-wide search query, changed-path prefixes, and the manual-
// conflict-resolution heuristic) -- each documented at its own call site
// below rather than restated here.
//
// # Cost
//
// This is a genuinely expensive port method: one compare call, one
// branch-protection call (cached per unique base branch), one revert-
// search call (once for the whole batch), plus up to six further calls
// PER discovered constituent PR (detail, reviews, combined status,
// check-runs, files, commits). §15.2 itself names this cost's own
// justification: "fully mechanizable... a compliance check, not a code
// review" -- there is no cheaper way to answer these specific, per-PR
// factual questions through GitHub's REST API as it exists today.
// maxConstituentPRs below bounds the worst case, matching this
// codebase's own "bounded from day one" discipline (§21.1) rather than
// leaving an unbounded release PR free to make an unbounded number of
// outbound calls.

package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// maxConstituentPRs bounds how many discovered constituent PRs
// ListMergedBetween ever builds full detail for -- a release PR
// bundling more than this many PRs still returns a (silently truncated)
// manifest over the FIRST maxConstituentPRs discovered, in the compare
// API's own commit order, rather than making an unbounded number of
// outbound calls. Chosen generously for a realistic release cut while
// keeping worst-case cost bounded (this file's own top doc comment: up
// to ~6 further calls per PR).
const maxConstituentPRs = 100

// maxRevertSearchResults bounds how many revert-titled merged PRs
// fetchReverts (below) considers per repo, per ListMergedBetween call --
// GitHub's Search API itself caps a single page at 100 results; this
// adapter never paginates beyond that one page (a repo with more than
// 100 revert-titled PRs total is a real, if unusual, case this file
// accepts as a known limitation rather than an unbounded search).
const maxRevertSearchResults = 100

var (
	// mergeCommitPRNumberRE matches GitHub's default "Create a merge
	// commit" strategy's own first-line message shape.
	mergeCommitPRNumberRE = regexp.MustCompile(`^Merge pull request #(\d+) from `)
	// squashMergePRNumberRE matches GitHub's default "Squash and merge"
	// strategy's own first-line message shape.
	squashMergePRNumberRE = regexp.MustCompile(`\(#(\d+)\)\s*$`)
)

// compareCommitResponse is the subset of one entry in GitHub's real GET
// /repos/{owner}/{repo}/compare/{base}...{head} response's own "commits"
// array this adapter needs (https://docs.github.com/rest/commits/commits#compare-two-commits).
type compareCommitResponse struct {
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// compareResponse is the subset of GitHub's real compare-two-commits
// response shape this adapter needs. TotalCommits is GitHub's own
// reported total commit count for the whole range -- distinct from
// len(Commits), which GitHub caps at 250 regardless of TotalCommits;
// this adapter logs no warning on a mismatch (it has no logger) but
// documents it here as a KNOWN LIMITATION: a range spanning more than
// 250 commits may silently miss constituent PRs whose own merge/squash
// commit fell outside the first 250 returned.
type compareResponse struct {
	TotalCommits int                     `json:"total_commits"`
	Commits      []compareCommitResponse `json:"commits"`
}

// extractCandidatePRNumbers derives every candidate constituent PR
// number from commits, in the SAME order compare returned them
// (chronological, oldest first, per GitHub's own documented behavior) --
// see this file's own top doc comment for the two message shapes
// matched, and the known rebase-merge gap. Deduplicated (a real PR
// number never legitimately repeats in this list, but nothing prevents
// defending against it cheaply).
func extractCandidatePRNumbers(commits []compareCommitResponse) []int {
	seen := make(map[int]bool)
	var numbers []int
	for _, c := range commits {
		firstLine := c.Commit.Message
		if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
			firstLine = firstLine[:idx]
		}

		var numStr string
		if m := mergeCommitPRNumberRE.FindStringSubmatch(firstLine); m != nil {
			numStr = m[1]
		} else if m := squashMergePRNumberRE.FindStringSubmatch(firstLine); m != nil {
			numStr = m[1]
		} else {
			continue
		}

		n, err := strconv.Atoi(numStr)
		if err != nil || n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		numbers = append(numbers, n)
	}
	return numbers
}

// mergedPRDetailResponse is the subset of GitHub's real GET
// /repos/{owner}/{repo}/pulls/{number} response shape buildMergedPR
// needs, beyond what pullRequestResponse (adapter.go) already decodes --
// a SEPARATE, narrower struct (rather than extending pullRequestResponse
// itself) since this file's own needs (Merged/MergedAt/MergeCommitSHA)
// are specific to an already-merged PR and irrelevant to every other
// caller of that shared type.
type mergedPRDetailResponse struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	Merged         bool    `json:"merged"`
	MergedAt       *string `json:"merged_at"`
	MergeCommitSHA string  `json:"merge_commit_sha"`
	Base           struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// fetchMergedPRDetail fetches number's own detail via GET
// /repos/{owner}/{repo}/pulls/{number}. Returns ok=false (nil error) when
// GitHub reports this PR as NOT actually merged (Merged == false) -- a
// real, expected outcome for a candidate number extractCandidatePRNumbers
// mis-extracted (e.g. a coincidental "(#123)" substring in an unrelated
// commit message) or for a PR later force-pushed/closed without merging;
// never treated as an error, simply excluded from the manifest.
func (a *Adapter) fetchMergedPRDetail(ctx context.Context, owner, repo string, number int, token string) (mergedPRDetailResponse, bool, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return mergedPRDetailResponse{}, false, fmt.Errorf("githubapi: fetch merged pr detail: %w", err)
	}
	var parsed mergedPRDetailResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return mergedPRDetailResponse{}, false, fmt.Errorf("githubapi: decode merged pr detail: %w", err)
	}
	if !parsed.Merged {
		return mergedPRDetailResponse{}, false, nil
	}
	return parsed, true, nil
}

// reviewItemResponse is the subset of one entry in GitHub's real GET
// /repos/{owner}/{repo}/pulls/{number}/reviews response this adapter
// needs (https://docs.github.com/rest/pulls/reviews#list-reviews-for-a-pull-request).
type reviewItemResponse struct {
	State string `json:"state"`
}

// fetchHasApprovingReview reports whether number carries at least one
// review with state APPROVED -- GitHub's own review-state vocabulary
// (APPROVED/CHANGES_REQUESTED/COMMENTED/DISMISSED/PENDING), matched
// exactly as GitHub reports it.
func (a *Adapter) fetchHasApprovingReview(ctx context.Context, owner, repo string, number int, token string) (bool, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews?per_page=100", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return false, fmt.Errorf("githubapi: fetch pr reviews: %w", err)
	}
	var reviews []reviewItemResponse
	if err := json.Unmarshal(body, &reviews); err != nil {
		return false, fmt.Errorf("githubapi: decode pr reviews: %w", err)
	}
	for _, r := range reviews {
		if r.State == "APPROVED" {
			return true, nil
		}
	}
	return false, nil
}

// branchProtectionResponse is the subset of GitHub's real GET
// /repos/{owner}/{repo}/branches/{branch}/protection response this
// adapter needs -- just WHETHER an approving-review requirement is
// configured at all (https://docs.github.com/rest/branches/branch-protection).
type branchProtectionResponse struct {
	RequiredPullRequestReviews *struct {
		RequiredApprovingReviewCount int `json:"required_approving_review_count"`
	} `json:"required_pull_request_reviews"`
}

// branchRequiresApprovingReview reports whether branch's own protection
// rules require at least one approving review to merge -- the positive
// fact MergedViaAdminOverride (buildMergedPR below) needs before it will
// EVER assert "admin override": without this confirmation, a PR merging
// with no approving review is equally explained by "this branch simply
// has no review requirement configured", which is not an override of
// anything.
//
// A 404 (GitHub's own documented response for an UNPROTECTED branch --
// https://docs.github.com/rest/branches/branch-protection#get-branch-protection)
// is a definitive, legitimate "no" -- protection genuinely does not
// exist here. Any OTHER failure (403: this token lacks admin rights to
// view protection settings -- GitHub REQUIRES admin/maintain permission
// for this specific endpoint; a transport/timeout error; any other
// non-2xx) is INDETERMINATE, not a "no" -- also returned as false, per
// this function's own doc comment: MergedViaAdminOverride must never be
// asserted without a POSITIVE confirmation, so "could not determine"
// and "confirmed unprotected" collapse to the SAME safe, non-accusatory
// answer here (unlike ports.SourceControl.CheckRepoAccess, which must
// keep these two cases distinguishable for a DIFFERENT, access-control
// purpose -- there is no analogous caller-visible distinction this
// function's own one real caller needs).
func (a *Adapter) branchRequiresApprovingReview(ctx context.Context, owner, repo, branch, token string) bool {
	path := fmt.Sprintf("%s/repos/%s/%s/branches/%s/protection", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return false
	}
	var parsed branchProtectionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	return parsed.RequiredPullRequestReviews != nil
}

// combinedStatusResponse is the subset of GitHub's real GET
// /repos/{owner}/{repo}/commits/{ref}/status response this adapter needs
// (https://docs.github.com/rest/commits/statuses#get-the-combined-status-for-a-specific-reference)
// -- GitHub's own LEGACY Status API surface (statuses created via POST
// .../statuses), state is one of "success"/"pending"/"failure"/"error".
type combinedStatusResponse struct {
	State string `json:"state"`
}

// checkRunsResponse is the subset of GitHub's real GET
// /repos/{owner}/{repo}/commits/{ref}/check-runs response this adapter
// needs (https://docs.github.com/rest/checks/runs#list-check-runs-for-a-git-reference)
// -- GitHub's own NEWER Checks API surface (what GitHub Actions and most
// modern CI integrations report through); conclusion is nil while a
// check is still in progress.
type checkRunsResponse struct {
	CheckRuns []struct {
		Conclusion *string `json:"conclusion"`
	} `json:"check_runs"`
}

// ciFailureConclusions is the set of GitHub Checks API "conclusion"
// values this adapter treats as a genuine failure signal -- "neutral"
// and "success" are NOT failure signals; "skipped" is also not (a check
// that opted out of running says nothing about whether the code is
// broken).
var ciFailureConclusions = map[string]bool{
	"failure":         true,
	"timed_out":       true,
	"action_required": true,
}

// fetchCIConclusion determines a constituent PR's own CI conclusion AT
// mergeSHA -- THE COMMIT THAT ACTUALLY MERGED, never the PR's current/
// latest head (§15.2's own central requirement: "a force-push after
// approval can hide a run that was red when it actually merged"). Reads
// BOTH of GitHub's two independent CI surfaces -- the legacy combined-
// Status API and the newer Checks API -- since a repo may report through
// either or both depending on how its CI is configured (a repo using
// ONLY GitHub Actions reports EXCLUSIVELY through check-runs; the
// combined-status endpoint alone would silently show no signal at all
// for such a repo). Any confirmed failure signal from EITHER surface
// wins; otherwise any confirmed success signal from either wins;
// otherwise (no signal from either, or either call itself failed)
// review.CIConclusionUnknown -- see review.CIConclusionUnknown's own doc
// comment (internal/domain/review/manifestcheck.go) for why this is
// deliberately NOT treated as a failure.
func (a *Adapter) fetchCIConclusion(ctx context.Context, owner, repo, mergeSHA, token string) ports.CIConclusion {
	sawFailure := false
	sawSuccess := false

	statusPath := fmt.Sprintf("%s/repos/%s/%s/commits/%s/status", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(mergeSHA))
	if body, err := a.doGet(ctx, statusPath, token); err == nil {
		var status combinedStatusResponse
		if json.Unmarshal(body, &status) == nil {
			switch status.State {
			case "failure", "error":
				sawFailure = true
			case "success":
				sawSuccess = true
			}
		}
	}

	checksPath := fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(mergeSHA))
	if body, err := a.doGet(ctx, checksPath, token); err == nil {
		var runs checkRunsResponse
		if json.Unmarshal(body, &runs) == nil {
			for _, r := range runs.CheckRuns {
				if r.Conclusion == nil {
					continue
				}
				if ciFailureConclusions[*r.Conclusion] {
					sawFailure = true
				} else if *r.Conclusion == "success" || *r.Conclusion == "neutral" {
					sawSuccess = true
				}
			}
		}
	}

	switch {
	case sawFailure:
		return ports.CIConclusionFailure
	case sawSuccess:
		return ports.CIConclusionSuccess
	default:
		return ports.CIConclusionUnknown
	}
}

// pullFileResponse is the subset of one entry in GitHub's real GET
// /repos/{owner}/{repo}/pulls/{number}/files response this adapter needs
// (https://docs.github.com/rest/pulls/pulls#list-pull-requests-files).
type pullFileResponse struct {
	Filename string `json:"filename"`
}

// fetchChangedPathPrefixes fetches number's own changed files (first
// page, per_page=100 -- see maxConstituentPRs' own sibling bound;
// unlike that constant this is not separately named, since a single
// PR's own file count beyond 100 is rare enough that a bounded first
// page is an acceptable, honestly-scoped approximation for §15.3's own
// "same subsystem" signal, which does not need exhaustive coverage to be
// useful) and reduces each to its own top-level path prefix
// (changedPathPrefix below). Deduplicated.
func (a *Adapter) fetchChangedPathPrefixes(ctx context.Context, owner, repo string, number int, token string) ([]string, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: fetch pr files: %w", err)
	}
	var files []pullFileResponse
	if err := json.Unmarshal(body, &files); err != nil {
		return nil, fmt.Errorf("githubapi: decode pr files: %w", err)
	}

	seen := make(map[string]bool, len(files))
	var prefixes []string
	for _, f := range files {
		prefix := changedPathPrefix(f.Filename)
		if prefix == "" || seen[prefix] {
			continue
		}
		seen[prefix] = true
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// changedPathPrefix reduces filename to its own top-level path prefix --
// the first two "/"-delimited segments when at least two exist (e.g.
// "internal/domain/review/verdict.go" -> "internal/domain"), or the
// single segment/whole filename otherwise (e.g. "README.md" ->
// "README.md", "docs/x.md" -> "docs/x.md" since it has only two
// segments total... wait: "docs/x.md" splits into ["docs","x.md"], two
// segments, both taken -> "docs/x.md"). Depth 2 mirrors this codebase's
// own repo-layout granularity (§1: "internal/domain", "internal/app",
// "internal/adapters/inbound", ...) more usefully than depth 1 alone
// (which would collapse everything under "internal" into one prefix,
// far too coarse to mean "same subsystem").
func changedPathPrefix(filename string) string {
	if filename == "" {
		return ""
	}
	parts := strings.SplitN(filename, "/", 3)
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return parts[0] + "/" + parts[1]
	}
}

// pullCommitResponse is the subset of one entry in GitHub's real GET
// /repos/{owner}/{repo}/pulls/{number}/commits response this adapter
// needs (https://docs.github.com/rest/commits/commits#list-commits) --
// just each commit's own parent count.
type pullCommitResponse struct {
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

// fetchHadManualConflictResolution infers whether landing number
// required manually resolving a conflict against its base -- §15.3's own
// third OR-condition. GitHub's REST API has NO direct "this merge
// required manual conflict resolution" flag; this is a HEURISTIC, not a
// confirmed fact, and documented as such: a merge commit appearing
// WITHIN a PR's own commit history (a commit with 2+ parents, listed by
// GET .../pulls/{number}/commits, first page, per_page=100) means a
// contributor or GitHub's own "Resolve conflicts" web UI merged the base
// branch into the PR's branch at some point -- the standard way a
// conflict against a moving base gets resolved without a full rebase.
// KNOWN IMPRECISION: a merge commit can also appear from a routine
// "merge main into my branch" that never actually conflicted, so this
// can over-report; conversely, a conflict resolved via `git rebase`
// (which produces no merge commit at all) is invisible to this check, so
// it can also under-report. Accepted as the best available mechanical
// signal per this file's own top doc comment on cost -- a true, provably
// correct signal is not obtainable through GitHub's REST API here.
func (a *Adapter) fetchHadManualConflictResolution(ctx context.Context, owner, repo string, number int, token string) (bool, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/commits?per_page=100", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return false, fmt.Errorf("githubapi: fetch pr commits: %w", err)
	}
	var commits []pullCommitResponse
	if err := json.Unmarshal(body, &commits); err != nil {
		return false, fmt.Errorf("githubapi: decode pr commits: %w", err)
	}
	for _, c := range commits {
		if len(c.Parents) >= 2 {
			return true, nil
		}
	}
	return false, nil
}

// searchIssueItemResponse is the subset of one entry in GitHub's real GET
// /search/issues response's own "items" array this adapter needs
// (https://docs.github.com/rest/search/search#search-issues-and-pull-requests).
// Number is this revert candidate's OWN pull request number -- needed to
// fetch ITS OWN review state (fetchHasApprovingReview), a second call
// this adapter only ever makes for an item that already matched the
// revert-title convention (extractRevertedTitle), never for the whole
// search page.
type searchIssueItemResponse struct {
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	ClosedAt *string `json:"closed_at"`
}

// searchIssuesResponse is the subset of GitHub's real search-issues
// response shape this adapter needs.
type searchIssuesResponse struct {
	Items []searchIssueItemResponse `json:"items"`
}

// revertInfo is what fetchReverts (below) reports finding for one
// original PR title.
type revertInfo struct {
	revertedAt time.Time
	reviewed   bool
}

// fetchReverts runs ONE Search API query for the whole batch --
// "repo:{owner}/{repo} type:pr is:merged in:title Revert" -- rather than
// one query per constituent PR, deliberately: GitHub's Search API is
// rate-limited far more aggressively than the rest of the REST API (30
// requests/minute, authenticated, vs. 5000/hour for ordinary endpoints
// -- https://docs.github.com/rest/search/search#rate-limit), and a
// release PR can easily bundle more constituent PRs than that per-minute
// budget would allow if this were one search per PR. Every returned
// item's own title is matched against GitHub's own default revert-PR
// title convention -- `Revert "<original title>"` -- produced by both
// GitHub's web UI "Revert" button and the `gh pr revert`-equivalent flow;
// a PR reverted by some OTHER means (a hand-authored revert PR with a
// differently-worded title) is a known, accepted miss, exactly like
// GetPullRequestDiff's own truncation and this file's own rebase-merge
// gap are.
//
// The revert PR's OWN review state (fetchHasApprovingReview, reused) is
// what determines RevertReviewed -- "whether that revert was itself
// reviewed" (§15.2).
func (a *Adapter) fetchReverts(ctx context.Context, owner, repo, token string) (map[string]revertInfo, error) {
	query := fmt.Sprintf("repo:%s/%s type:pr is:merged in:title Revert", owner, repo)
	path := fmt.Sprintf("%s/search/issues?q=%s&per_page=%d", a.apiBaseURL, url.QueryEscape(query), maxRevertSearchResults)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: search revert prs: %w", err)
	}
	var parsed searchIssuesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("githubapi: decode revert search results: %w", err)
	}

	reverts := make(map[string]revertInfo, len(parsed.Items))
	for _, item := range parsed.Items {
		originalTitle, ok := extractRevertedTitle(item.Title)
		if !ok {
			continue
		}
		var closedAt time.Time
		if item.ClosedAt != nil {
			if t, err := time.Parse(time.RFC3339, *item.ClosedAt); err == nil {
				closedAt = t
			}
		}
		// The revert PR's own review state -- reused fetchHasApprovingReview,
		// exactly like an ordinary constituent PR's own review state is
		// determined. Best-effort: a failure here leaves reviewed=false for
		// THIS revert -- accepted rather than excluding the whole revert
		// finding, since "was this PR reverted at all" (the PRIMARY fact) is
		// already confirmed by the title match above; only the SECONDARY
		// "was the revert itself reviewed" fact degrades on a sub-fetch
		// failure, mirroring buildMergedPR's own identical CI/files/commits
		// degrade-one-field discipline rather than excluding the whole PR.
		reviewed, _ := a.fetchHasApprovingReview(ctx, owner, repo, item.Number, token)
		reverts[originalTitle] = revertInfo{revertedAt: closedAt, reviewed: reviewed}
	}
	return reverts, nil
}

// revertTitlePrefix and revertTitleSuffix bracket GitHub's own default
// revert-PR title shape: `Revert "<original title>"`.
const (
	revertTitlePrefix = `Revert "`
	revertTitleSuffix = `"`
)

// extractRevertedTitle extracts the original PR title from a revert
// PR's own title, per GitHub's own default `Revert "<original title>"`
// convention -- ok=false when title does not match that shape at all.
func extractRevertedTitle(title string) (string, bool) {
	if !strings.HasPrefix(title, revertTitlePrefix) || !strings.HasSuffix(title, revertTitleSuffix) {
		return "", false
	}
	inner := title[len(revertTitlePrefix) : len(title)-len(revertTitleSuffix)]
	if inner == "" {
		return "", false
	}
	return inner, true
}

// buildMergedPR assembles one ports.MergedPR for prNumber, or ok=false
// when GitHub reports it as not actually merged (fetchMergedPRDetail's
// own doc comment). requiredReviewsByBase is a per-call cache keyed on
// base branch name -- branchRequiresApprovingReview is fetched at most
// ONCE per distinct base branch across the whole ListMergedBetween call,
// never once per PR (every constituent PR of a real release almost
// always shares the SAME base branch). reverts is fetchReverts' own
// batch-wide result, looked up by this PR's own title.
//
// Sub-fetch failure handling (see this file's own top doc comment for
// the reasoning): a failure fetching this PR's OWN detail or reviews
// excludes it from the manifest entirely (ok=false, nil error -- these
// two facts are central to what this check audits, never guessed at).
// A failure fetching CI/files/commits degrades that ONE field to its own
// honest "nothing confirmed" value (CIConclusionUnknown / no path
// prefixes / HadManualConflictResolution=false) without excluding the
// PR -- none of those three defaults assert a false fact the way
// guessing HasApprovingReview would.
func (a *Adapter) buildMergedPR(ctx context.Context, owner, repo string, prNumber int, token string, requiredReviewsByBase map[string]bool, reverts map[string]revertInfo) (ports.MergedPR, bool) {
	detail, ok, err := a.fetchMergedPRDetail(ctx, owner, repo, prNumber, token)
	if err != nil || !ok {
		return ports.MergedPR{}, false
	}

	hasApproval, err := a.fetchHasApprovingReview(ctx, owner, repo, prNumber, token)
	if err != nil {
		return ports.MergedPR{}, false
	}

	adminOverride := false
	if !hasApproval {
		requiresReview, cached := requiredReviewsByBase[detail.Base.Ref]
		if !cached {
			requiresReview = a.branchRequiresApprovingReview(ctx, owner, repo, detail.Base.Ref, token)
			requiredReviewsByBase[detail.Base.Ref] = requiresReview
		}
		adminOverride = requiresReview
	}

	var mergedAt time.Time
	if detail.MergedAt != nil {
		if t, err := time.Parse(time.RFC3339, *detail.MergedAt); err == nil {
			mergedAt = t
		}
	}

	ciConclusion := ports.CIConclusionUnknown
	if detail.MergeCommitSHA != "" {
		ciConclusion = a.fetchCIConclusion(ctx, owner, repo, detail.MergeCommitSHA, token)
	}

	prefixes, err := a.fetchChangedPathPrefixes(ctx, owner, repo, prNumber, token)
	if err != nil {
		prefixes = nil
	}

	manualConflict, err := a.fetchHadManualConflictResolution(ctx, owner, repo, prNumber, token)
	if err != nil {
		manualConflict = false
	}

	labels := make([]string, len(detail.Labels))
	for i, l := range detail.Labels {
		labels[i] = l.Name
	}

	merged := ports.MergedPR{
		Number:                      detail.Number,
		Title:                       detail.Title,
		HasApprovingReview:          hasApproval,
		MergedViaAdminOverride:      adminOverride,
		CIConclusionAtMergeSHA:      ciConclusion,
		MergedAt:                    mergedAt,
		HadManualConflictResolution: manualConflict,
		ChangedPathPrefixes:         prefixes,
		Labels:                      labels,
	}

	if revert, found := reverts[detail.Title]; found {
		merged.WasReverted = true
		if !revert.revertedAt.IsZero() {
			revertedAt := revert.revertedAt
			merged.RevertedAt = &revertedAt
		}
		// The revert PR's own review state was already resolved inside
		// fetchReverts itself (a second call, made only for a title that
		// already matched the revert convention) -- see that function's
		// own doc comment.
		merged.RevertReviewed = revert.reviewed
	}

	return merged, true
}

// ListMergedBetween implements ports.SourceControl (Step 50, "release PR
// review", §15.2) -- see this file's own top doc comment for the full
// design (constituent-PR discovery, per-field sourcing, known
// limitations, and cost).
func (a *Adapter) ListMergedBetween(ctx context.Context, spec ports.ListMergedBetweenSpec) ([]ports.MergedPR, error) {
	comparePath := fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s",
		a.apiBaseURL, url.PathEscape(spec.Owner), url.PathEscape(spec.Repo),
		url.PathEscape(spec.BaseRef), url.PathEscape(spec.HeadRef))
	body, err := a.doGet(ctx, comparePath, spec.Token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: list merged between: compare: %w", err)
	}
	var compare compareResponse
	if err := json.Unmarshal(body, &compare); err != nil {
		return nil, fmt.Errorf("githubapi: list merged between: decode compare response: %w", err)
	}

	candidates := extractCandidatePRNumbers(compare.Commits)
	if len(candidates) > maxConstituentPRs {
		candidates = candidates[:maxConstituentPRs]
	}

	reverts, err := a.fetchReverts(ctx, spec.Owner, spec.Repo, spec.Token)
	if err != nil {
		// Best-effort: a failed revert search never fails this whole
		// call -- every PR simply reports WasReverted=false, a known,
		// accepted limitation (this file's own top doc comment).
		reverts = map[string]revertInfo{}
	}

	requiredReviewsByBase := make(map[string]bool)
	merged := make([]ports.MergedPR, 0, len(candidates))
	for _, number := range candidates {
		pr, ok := a.buildMergedPR(ctx, spec.Owner, spec.Repo, number, spec.Token, requiredReviewsByBase, reverts)
		if !ok {
			continue
		}
		merged = append(merged, pr)
	}
	return merged, nil
}
