package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/codeowners"
)

// codeownersCandidatePaths is every location GitHub itself checks for a
// CODEOWNERS file, IN PRECEDENCE ORDER -- GitHub's own docs: "GitHub
// searches for a file called CODEOWNERS... GitHub will search for them in
// [.github/, root, docs/] order and use the first one it finds"
// (https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners,
// fetched 2026-08-07, this Step's own design phase -- verified directly,
// not assumed).
var codeownersCandidatePaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// simpleUserResponse is the subset of GitHub's real "Simple User" shape
// this adapter needs -- returned both by GET /users/{username}
// (https://docs.github.com/rest/users/users#get-a-user, fetched
// 2026-08-07) and embedded in each element of GET /orgs/{org}/teams/
// {team_slug}/members's own array response (https://docs.github.com/rest/teams/members#list-team-members,
// fetched 2026-08-07): the stable numeric account id plus the current
// login.
type simpleUserResponse struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// ResolveCodeOwners implements ports.SourceControl (Step 60, "decision
// inbox: read model + API", §16.2): fetches the repo's own CODEOWNERS
// file (the first of codeownersCandidatePaths' own three GitHub-
// documented locations that actually exists at spec.Ref), parses and
// matches it via internal/domain/codeowners (pure, no I/O -- this
// adapter's own job is exactly the I/O that package cannot do itself:
// fetching the file, and resolving an "@login"/"@org/team-slug"/email
// token into a real, identity-graph-resolvable account), and returns one
// ports.Owner per (matched input path, resolved account) pair.
//
// A repo with NO CODEOWNERS file at any of the three candidate locations
// returns (nil, nil) -- a legitimate, common outcome, never an error,
// mirroring GetFileContent's own established exists/err-are-independent-
// signals discipline (fetchFileContent, adapter.go).
//
// Team resolution ("@org/team-slug" -> its member accounts) calls GitHub's
// real, documented "List team members" endpoint -- first page,
// per_page=100, mirroring mergedbetween.go's own established "an
// honestly-scoped approximation" precedent for every other list endpoint
// in this package that could, in principle, paginate further. Both
// individual-account and team-member resolution are cached for the
// lifetime of THIS ONE CALL ONLY (the two maps built below, never a
// package-level or cross-call cache -- this adapter holds no state of its
// own between calls, matching every other method in this package): the
// same login or team commonly recurs across several of spec.Paths' own
// matched rules (e.g. behind a single catch-all "*" pattern), and this
// avoids re-resolving it once per path.
//
// A single owner token that fails to resolve (e.g. an account renamed or
// deleted since the CODEOWNERS line was written) is skipped, best-effort
// -- it never fails the whole call, mirroring this package's own
// established "one bad sub-fetch degrades gracefully, never aborts the
// batch" discipline (mergedbetween.go's buildMergedPR).
func (a *Adapter) ResolveCodeOwners(ctx context.Context, spec ports.ResolveCodeOwnersSpec) ([]ports.Owner, error) {
	content, found, err := a.fetchCodeownersContent(ctx, spec.Owner, spec.Repo, spec.Ref, spec.Token)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	matcher := codeowners.Compile(codeowners.Parse(content))

	userCache := map[string]ports.Owner{}
	teamCache := map[string][]ports.Owner{}

	var result []ports.Owner
	for _, path := range spec.Paths {
		rule, ok := matcher.Match(path)
		if !ok {
			continue
		}
		for _, ref := range rule.Owners {
			switch ref.Kind {
			case codeowners.OwnerKindEmail:
				result = append(result, ports.Owner{Email: ref.Login, Path: path, Pattern: rule.Pattern})

			case codeowners.OwnerKindUser:
				owner, err := a.resolveCodeOwnerUser(ctx, ref.Login, spec.Token, userCache)
				if err != nil {
					continue
				}
				owner.Path, owner.Pattern = path, rule.Pattern
				result = append(result, owner)

			case codeowners.OwnerKindTeam:
				members, err := a.resolveCodeOwnerTeam(ctx, ref.OrgSlug, ref.TeamSlug, spec.Token, teamCache)
				if err != nil {
					continue
				}
				for _, m := range members {
					m.Path, m.Pattern = path, rule.Pattern
					result = append(result, m)
				}
			}
		}
	}

	return result, nil
}

// fetchCodeownersContent tries each of codeownersCandidatePaths in order,
// returning the first one that exists -- see ResolveCodeOwners' own doc
// comment. A genuine fetch failure (as opposed to a 404, which
// fetchFileContent already reports as exists=false, err=nil) on any
// candidate aborts the whole search and is propagated -- an
// indeterminate "could this location even be checked" must never be
// silently treated the same as "confirmed absent" the way it would be if
// this function pressed on to the next candidate after a real error.
func (a *Adapter) fetchCodeownersContent(ctx context.Context, owner, repo, ref, token string) (content string, found bool, err error) {
	for _, candidate := range codeownersCandidatePaths {
		c, _, exists, fetchErr := a.fetchFileContent(ctx, owner, repo, candidate, ref, token)
		if fetchErr != nil {
			return "", false, fmt.Errorf("githubapi: resolve code owners: fetch %s: %w", candidate, fetchErr)
		}
		if exists {
			return c, true, nil
		}
	}
	return "", false, nil
}

// resolveCodeOwnerUser resolves a "@login" OwnerRef to a real GitHub
// account via GET /users/{username} -- cache is this ONE ResolveCodeOwners
// call's own login->Owner memoization (see that method's own doc
// comment). The cached/returned ports.Owner never carries Path/Pattern
// (both are the CALLER's own per-match context, filled in after this
// function returns) -- caching a value that already had them set would
// silently leak the FIRST match's own Path/Pattern onto every later
// lookup of the same login.
func (a *Adapter) resolveCodeOwnerUser(ctx context.Context, login, token string, cache map[string]ports.Owner) (ports.Owner, error) {
	if o, ok := cache[login]; ok {
		return o, nil
	}

	path := fmt.Sprintf("%s/users/%s", a.apiBaseURL, url.PathEscape(login))
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return ports.Owner{}, fmt.Errorf("githubapi: resolve code owner user %q: %w", login, err)
	}
	var parsed simpleUserResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ports.Owner{}, fmt.Errorf("githubapi: decode user response for %q: %w", login, err)
	}

	owner := ports.Owner{ExternalID: strconv.FormatInt(parsed.ID, 10), Login: parsed.Login}
	cache[login] = owner
	return owner, nil
}

// resolveCodeOwnerTeam resolves an "@org/team-slug" OwnerRef to every
// member account via GET /orgs/{org}/teams/{team_slug}/members -- cache is
// keyed on "org/team-slug", this ONE call's own memoization. Mirrors
// resolveCodeOwnerUser's own "never cache Path/Pattern" discipline.
func (a *Adapter) resolveCodeOwnerTeam(ctx context.Context, org, team, token string, cache map[string][]ports.Owner) ([]ports.Owner, error) {
	key := org + "/" + team
	if members, ok := cache[key]; ok {
		return members, nil
	}

	path := fmt.Sprintf("%s/orgs/%s/teams/%s/members?per_page=100", a.apiBaseURL, url.PathEscape(org), url.PathEscape(team))
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: resolve code owner team %q: %w", key, err)
	}
	var parsed []simpleUserResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("githubapi: decode team members response for %q: %w", key, err)
	}

	members := make([]ports.Owner, len(parsed))
	for i, m := range parsed {
		members[i] = ports.Owner{ExternalID: strconv.FormatInt(m.ID, 10), Login: m.Login, TeamSlug: key}
	}
	cache[key] = members
	return members, nil
}
