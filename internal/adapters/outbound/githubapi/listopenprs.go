// This file (listopenprs.go) implements ports.SourceControl.
// ListOpenPRsForUser (Step 60, "decision inbox: read model + API", §16.2):
// "review state, CI at head SHA, labels, assignees/reviewers" for every
// open PR a user is involved in.
//
// # Resolving a login from spec.GitHubExternalID
//
// This codebase's own identity graph (§13.2) stores ONLY the stable,
// numeric GitHub account id (identities.external_id, populated from the
// OAuth /user response's own "id" field at sign-in,
// internal/adapters/inbound/auth/callback.go) -- never the account's
// LOGIN. Verified directly before writing this file (grepped the whole
// codebase for github_login/GitHubLogin/githubUsername and every schema
// file): the login is used ONLY transiently, at OAuth callback time (for
// the allowlist org-membership check and as a display-name fallback), and
// is never persisted anywhere. GitHub's own search qualifiers this file
// needs (assignee:, review-requested:) require a LOGIN, not a numeric id
// -- so this method's own first call resolves one via GitHub's real,
// documented "Get a user using their ID" endpoint (GET /user/{account_id},
// https://docs.github.com/rest/users/users#get-a-user-using-their-id,
// fetched 2026-08-07: "This method takes their durable user ID instead of
// their login, which can change over time"), exactly the durable-id-to-
// current-login resolution this endpoint exists for.
//
// # Discovering candidate PRs
//
// GitHub's Search API supports exactly the qualifiers this method needs,
// verified directly against GitHub's own documentation (fetched
// 2026-08-07): "assignee:USERNAME" and "review-requested:USERNAME".
// review-requested's own documented behavior already folds in team-based
// requests: "If the requested person is on a team that is requested for
// review, then review requests for that team will also appear in the
// search results" -- so this ONE query surfaces both an individually-
// requested reviewer AND one reached via team membership, with no
// separate team-membership lookup needed for DISCOVERY (ResolveCodeOwners
// is still needed separately to determine WHETHER a given match came via
// a CODEOWNERS pattern specifically, for §16.1's own provenance
// requirement -- that is an ENRICHMENT of an already-discovered PR, not a
// second discovery mechanism; see the app-layer aggregator).
//
// This deliberately does NOT run an "author:" query: §16.1's own three
// assignment-provenance paths are "directly, as requested reviewer, or
// via CODEOWNERS" -- authorship on its own is not one of them (a PR the
// user AUTHORED but is neither assigned to nor requested to review is not
// "assigned to the user" in the sense this Step's taxonomy means; the
// decision inbox is about decisions ADDRESSED to the user, not a list of
// their own outgoing work).
//
// This also does NOT scope the search to a configured set of Narvi-
// managed repos/orgs -- there is no such registry anywhere in this
// codebase today (repo_settings rows only exist for repos an admin has
// already touched a toggle for, not a canonical "every repo Narvi
// manages" list). Running the user's own token unscoped means the result
// is exactly "every open PR on GitHub this person can see and is
// assigned/requested on" -- a known, honestly-scoped choice: a user whose
// GitHub account also touches repos outside anything Narvi cares about
// would see those PRs too. The cost is a UX nicety gap (a few
// out-of-scope rows), never a correctness/security one -- every row still
// goes through the SAME server-side re-validation before any action is
// taken (§16.2, §5.2), so an out-of-scope row is inert, not exploitable.
//
// # Cost
//
// Two Search API calls (30 req/min budget -- see mergedbetween.go's own
// identical caution) plus up to five further ordinary REST calls (detail,
// reviews, CI's own two surfaces, changed files) PER discovered candidate
// PR, bounded by maxOpenPRsForUser -- the SAME "genuinely expensive, no
// cheaper way through GitHub's REST API as it exists today" cost class
// mergedbetween.go's own top doc comment already accepts for
// ListMergedBetween, amortized here by §16.2's own short-TTL cache at the
// app layer rather than by this adapter itself (which holds no state
// between calls, matching every other method in this package).

package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// maxOpenPRsForUser bounds how many discovered candidate PRs
// ListOpenPRsForUser ever builds full OpenPR detail for -- mirrors
// maxConstituentPRs' own identical "bounded from day one" reasoning
// (§21.1) rather than an unbounded per-user candidate list driving an
// unbounded number of outbound calls. A person legitimately assigned to
// or requested on more than this many open PRs at once is a real, if
// unusual, case this file accepts as a known limitation (the search
// results beyond this bound are simply never resolved into full OpenPR
// rows) rather than an unbounded worst case.
const maxOpenPRsForUser = 50

// maxOpenPRSearchResultsPerQuery mirrors maxRevertSearchResults'
// (mergedbetween.go) own identical "GitHub's Search API caps a single
// page at 100 results, and this adapter never paginates beyond that one
// page" acceptance.
const maxOpenPRSearchResultsPerQuery = 100

// openPRSearchItemResponse is the subset of one entry in GitHub's real GET
// /search/issues response's own "items" array this file needs -- a
// DIFFERENT subset than mergedbetween.go's own searchIssueItemResponse
// (which needs Title/Body/ClosedAt for revert detection): this file only
// ever needs enough to identify WHICH repo/PR a search hit names, since
// buildOpenPR (below) re-fetches everything else it needs via a full
// detail call regardless.
type openPRSearchItemResponse struct {
	Number        int    `json:"number"`
	RepositoryURL string `json:"repository_url"`
}

type openPRSearchResponse struct {
	Items []openPRSearchItemResponse `json:"items"`
}

// openPRDetailResponse is the subset of GitHub's real GET
// /repos/{owner}/{repo}/pulls/{pull_number} response shape this file
// needs, beyond what pullRequestResponse (adapter.go, H5's narrower head-
// branch-resolution need) or mergedPRDetailResponse (mergedbetween.go,
// scoped to an already-MERGED PR) already decode -- verified directly
// against GitHub's own documentation (fetched 2026-08-07): requested_
// reviewers/requested_teams/assignees/draft are real, documented fields
// on this exact response.
type openPRDetailResponse struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	HTMLURL   string `json:"html_url"`
	Draft     bool   `json:"draft"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`

	User *simpleUserResponse `json:"user"`

	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`

	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`

	Assignees          []simpleUserResponse `json:"assignees"`
	RequestedReviewers []simpleUserResponse `json:"requested_reviewers"`
	// RequestedTeams only carries Slug -- a requested team's own
	// "organization" is, on a real GitHub repo, always the SAME org that
	// owns the repo itself (an org-owned repo can only grant a team
	// permissions if that team belongs to that same org; a personal-
	// account-owned repo can carry no teams at all) -- buildOpenPR below
	// combines this Slug with the ALREADY-KNOWN repo owner rather than
	// decoding a second, nested organization object for a fact the repo
	// owner already gives it.
	RequestedTeams []struct {
		Slug string `json:"slug"`
	} `json:"requested_teams"`
}

// ListOpenPRsForUser implements ports.SourceControl -- see this file's own
// top doc comment for the full design (login resolution, candidate
// discovery, cost).
func (a *Adapter) ListOpenPRsForUser(ctx context.Context, spec ports.ListOpenPRsForUserSpec) ([]ports.OpenPR, error) {
	login, err := a.resolveLoginByID(ctx, spec.GitHubExternalID, spec.Token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: list open prs for user: resolve login: %w", err)
	}

	type prKey struct {
		owner, repo string
		number      int
	}
	seen := make(map[prKey]bool)
	var candidates []prKey

	for _, qualifier := range []string{"assignee", "review-requested"} {
		items, searchErr := a.searchOpenPRs(ctx, qualifier, login, spec.Token)
		if searchErr != nil {
			// Best-effort per-query: one qualifier's own search failing
			// (e.g. a transient 5xx) should never blank out whatever the
			// OTHER qualifier already found.
			continue
		}
		for _, item := range items {
			owner, repo, ok := splitRepositoryURL(item.RepositoryURL)
			if !ok {
				continue
			}
			key := prKey{owner: owner, repo: repo, number: item.Number}
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, key)
			if len(candidates) >= maxOpenPRsForUser {
				break
			}
		}
		if len(candidates) >= maxOpenPRsForUser {
			break
		}
	}

	result := make([]ports.OpenPR, 0, len(candidates))
	for _, c := range candidates {
		pr, ok := a.buildOpenPR(ctx, c.owner, c.repo, c.number, spec.Token)
		if !ok {
			// Best-effort per-PR, mirrors buildMergedPR's own identical
			// "a genuine sub-fetch failure excludes just this one row"
			// discipline (mergedbetween.go) -- this method reports no
			// analogous "truncated" signal (unlike ListMergedBetween)
			// since §16.2 already frames this whole read as advisory,
			// short-TTL-cached, and re-validated at action time -- an
			// occasionally-missing row here is a staleness/coverage
			// nicety, never a correctness hazard the way an incomplete
			// release-compliance manifest would be.
			continue
		}
		result = append(result, pr)
	}
	return result, nil
}

// resolveLoginByID resolves externalID (a numeric GitHub account id, as a
// decimal string -- ports.ListOpenPRsForUserSpec.GitHubExternalID's own
// doc comment) to that account's CURRENT login, via GET
// /user/{account_id} -- see this file's own top doc comment.
func (a *Adapter) resolveLoginByID(ctx context.Context, externalID, token string) (string, error) {
	path := fmt.Sprintf("%s/user/%s", a.apiBaseURL, url.PathEscape(externalID))
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return "", err
	}
	var parsed simpleUserResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("githubapi: decode user-by-id response: %w", err)
	}
	if parsed.Login == "" {
		return "", fmt.Errorf("githubapi: user id %s reported an empty login", externalID)
	}
	return parsed.Login, nil
}

// searchOpenPRs runs "is:pr is:open <qualifier>:<login>" -- see this
// file's own top doc comment for why review-requested alone (no separate
// team-review-requested query) already covers team-based requests too.
func (a *Adapter) searchOpenPRs(ctx context.Context, qualifier, login, token string) ([]openPRSearchItemResponse, error) {
	query := fmt.Sprintf("is:pr is:open %s:%s", qualifier, login)
	path := fmt.Sprintf("%s/search/issues?q=%s&per_page=%d", a.apiBaseURL, url.QueryEscape(query), maxOpenPRSearchResultsPerQuery)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: search open prs (%s): %w", qualifier, err)
	}
	var parsed openPRSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("githubapi: decode open-pr search results (%s): %w", qualifier, err)
	}
	return parsed.Items, nil
}

// splitRepositoryURL extracts owner/repo from GitHub's own search-item
// "repository_url" field, shaped "https://api.github.com/repos/{owner}/
// {repo}" (GitHub's own documented, stable shape for this field).
func splitRepositoryURL(repositoryURL string) (owner, repo string, ok bool) {
	const marker = "/repos/"
	idx := strings.Index(repositoryURL, marker)
	if idx < 0 {
		return "", "", false
	}
	rest := repositoryURL[idx+len(marker):]
	owner, repo, ok = strings.Cut(rest, "/")
	if !ok || owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

// buildOpenPR assembles one ports.OpenPR for (owner, repo, number) --
// ok=false means a genuine sub-fetch failure on the CENTRAL detail call
// (mirrors buildMergedPR's own identical "the detail fetch itself failing
// excludes the whole row" precedent); a failure on any of the other,
// SECONDARY fetches (reviews, CI, changed files) instead degrades that
// ONE field to its own honest zero-value/Unknown rather than excluding
// the PR outright -- the SAME per-field-degrade discipline buildMergedPR
// already establishes.
func (a *Adapter) buildOpenPR(ctx context.Context, owner, repo string, number int, token string) (ports.OpenPR, bool) {
	detail, err := a.fetchOpenPRDetail(ctx, owner, repo, number, token)
	if err != nil {
		return ports.OpenPR{}, false
	}

	hasApproving, hasChangesRequested := a.fetchReviewDecision(ctx, owner, repo, number, token)

	ci := ports.CIConclusionUnknown
	if detail.Head.SHA != "" {
		ci = a.fetchCIConclusion(ctx, owner, repo, detail.Head.SHA, token)
	}

	files, filesErr := a.fetchChangedFilePaths(ctx, owner, repo, number, token)
	if filesErr != nil {
		files = nil
	}

	labels := make([]string, len(detail.Labels))
	for i, l := range detail.Labels {
		labels[i] = l.Name
	}

	assignees := make([]ports.PRPerson, 0, len(detail.Assignees))
	for _, u := range detail.Assignees {
		assignees = append(assignees, ports.PRPerson{ExternalID: strconv.FormatInt(u.ID, 10), Login: u.Login})
	}
	reviewers := make([]ports.PRPerson, 0, len(detail.RequestedReviewers))
	for _, u := range detail.RequestedReviewers {
		reviewers = append(reviewers, ports.PRPerson{ExternalID: strconv.FormatInt(u.ID, 10), Login: u.Login})
	}
	teams := make([]string, 0, len(detail.RequestedTeams))
	for _, tm := range detail.RequestedTeams {
		if tm.Slug != "" {
			teams = append(teams, owner+"/"+tm.Slug)
		}
	}

	var author ports.PRPerson
	if detail.User != nil {
		author = ports.PRPerson{ExternalID: strconv.FormatInt(detail.User.ID, 10), Login: detail.User.Login}
	}

	pr := ports.OpenPR{
		Owner: owner,
		Repo:  repo,

		Number:  detail.Number,
		Title:   detail.Title,
		HTMLURL: detail.HTMLURL,

		HeadSHA: detail.Head.SHA,
		BaseRef: detail.Base.Ref,
		Draft:   detail.Draft,

		Author:             author,
		Assignees:          assignees,
		RequestedReviewers: reviewers,
		RequestedTeams:     teams,

		HasApprovingReview:  hasApproving,
		HasChangesRequested: hasChangesRequested,

		CIConclusion: ci,
		Labels:       labels,

		ChangedFiles: files,
		Additions:    detail.Additions,
		Deletions:    detail.Deletions,
	}
	if t, parseErr := time.Parse(time.RFC3339, detail.CreatedAt); parseErr == nil {
		pr.CreatedAt = t
	}
	if t, parseErr := time.Parse(time.RFC3339, detail.UpdatedAt); parseErr == nil {
		pr.UpdatedAt = t
	}

	return pr, true
}

func (a *Adapter) fetchOpenPRDetail(ctx context.Context, owner, repo string, number int, token string) (openPRDetailResponse, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return openPRDetailResponse{}, fmt.Errorf("githubapi: fetch open pr detail: %w", err)
	}
	var parsed openPRDetailResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return openPRDetailResponse{}, fmt.Errorf("githubapi: decode open pr detail: %w", err)
	}
	return parsed, nil
}

// fetchReviewDecision scans EVERY review GitHub reports for number (first
// page, per_page=100, mirroring fetchHasApprovingReview's own identical
// bound) and reports whether at least one carries state APPROVED and
// whether at least one carries state CHANGES_REQUESTED. Deliberately does
// NOT reduce to "the latest review per reviewer" (a reviewer who requested
// changes and later approved would report both flags true here) --
// mirrors fetchHasApprovingReview's own identical, already-accepted level
// of rigor in this package for the same informational, non-merge-gating
// purpose (§16.2's own Merge endpoint re-validates independently server-
// side at click time regardless of what this advisory read reports).
func (a *Adapter) fetchReviewDecision(ctx context.Context, owner, repo string, number int, token string) (hasApproving, hasChangesRequested bool) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews?per_page=100", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return false, false
	}
	var reviews []reviewItemResponse
	if err := json.Unmarshal(body, &reviews); err != nil {
		return false, false
	}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED":
			hasApproving = true
		case "CHANGES_REQUESTED":
			hasChangesRequested = true
		}
	}
	return hasApproving, hasChangesRequested
}

// fetchChangedFilePaths fetches number's own changed files (first page,
// per_page=100, mirroring fetchChangedPathPrefixes' own identical bound
// and acceptance) -- FULL filenames (unlike fetchChangedPathPrefixes'
// own reduced top-level-prefix form), since this file's own caller needs
// them for ResolveCodeOwners' own Paths input, which matches CODEOWNERS
// patterns against complete file paths, not prefixes.
func (a *Adapter) fetchChangedFilePaths(ctx context.Context, owner, repo string, number int, token string) ([]string, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: fetch open pr changed files: %w", err)
	}
	var files []pullFileResponse
	if err := json.Unmarshal(body, &files); err != nil {
		return nil, fmt.Errorf("githubapi: decode open pr changed files: %w", err)
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Filename
	}
	return paths, nil
}
