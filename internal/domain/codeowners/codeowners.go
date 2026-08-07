// Package codeowners parses a CODEOWNERS file and matches repo-relative
// paths against it (Step 60, "decision inbox: read model + API", §16.2:
// "CODEOWNERS teams resolve to persons through the identity graph
// (§13.2)"). Pure, no I/O (CLAUDE.md/§11) -- fetching the file's own
// content and resolving an @user/@org/team mention to a real GitHub
// account (and, for a team, to its member accounts) both require a live
// API call, which belongs to this package's one real caller,
// internal/adapters/outbound/githubapi.ResolveCodeOwners.
//
// Every rule below is verified directly against GitHub's own published
// documentation (docs.github.com/en/repositories/managing-your-
// repositorys-settings-and-features/customizing-your-repository/about-
// code-owners, fetched 2026-08-07), not assumed:
//
//   - File locations, in precedence order (first one found wins): .github/
//     CODEOWNERS, then the repository root, then docs/CODEOWNERS. This
//     package does not pick a location itself -- see ResolveCodeOwners'
//     own doc comment for the fetch-in-order loop.
//   - Pattern precedence: "the last matching pattern takes the most
//     precedence" -- Match (below) walks rules in file order and keeps
//     overwriting its own running answer on every further match, so the
//     LAST match wins, never the first/most-specific.
//   - Pattern syntax follows gitignore conventions, with three explicit,
//     documented exceptions this package deliberately never implements:
//     "!" negation, "[ ]" character-range matching, and "\#" escaping of a
//     leading "#". Implementing either of the first two would silently
//     accept a pattern GitHub itself does not honor -- worse than simply
//     not supporting the syntax at all, since it would claim a match
//     GitHub's own server-side enforcement never actually grants.
//   - An owner token is one of: "@login" (an individual GitHub user),
//     "@org/team-slug" (a team), or a bare email address associated with a
//     GitHub account.
package codeowners

import (
	"regexp"
	"strings"
)

// OwnerKind classifies one raw owner token parsed from a CODEOWNERS line --
// see the package doc comment's own "owner token" bullet for the three
// real forms GitHub recognizes.
type OwnerKind string

const (
	// OwnerKindUser is an "@login" individual-account mention.
	OwnerKindUser OwnerKind = "user"
	// OwnerKindTeam is an "@org/team-slug" mention.
	OwnerKindTeam OwnerKind = "team"
	// OwnerKindEmail is a bare email address.
	OwnerKindEmail OwnerKind = "email"
)

// OwnerRef is one raw, UNRESOLVED owner token from a CODEOWNERS line --
// resolving it to a real account (and, for a team, to its member accounts)
// is this package's one real caller's job (githubapi.ResolveCodeOwners),
// never this package's own (no I/O, per this package's own doc comment).
type OwnerRef struct {
	Kind OwnerKind
	// Login is the account login for OwnerKindUser, or the raw email
	// address for OwnerKindEmail. Empty for OwnerKindTeam (see OrgSlug/
	// TeamSlug below instead).
	Login string
	// OrgSlug/TeamSlug are OwnerKindTeam's own two halves of "@org/team-
	// slug" -- both empty for every other Kind.
	OrgSlug  string
	TeamSlug string
}

// Rule is one non-blank, non-comment CODEOWNERS line.
type Rule struct {
	// Pattern is the line's own raw gitignore-style pattern, unmodified --
	// carried through so a caller can render "the matched pattern" back to
	// a human (§16.1: "yours via CODEOWNERS · internal/app/scheduler/**").
	Pattern string
	// Owners is every owner token on this line, in file order. EMPTY is a
	// legitimate, real value -- GitHub's own documented convention for a
	// pattern with no owners is that it explicitly UN-owns whatever it
	// matches, overriding an earlier, broader pattern's own owners for
	// that path (e.g. "* @global-owner" followed by "/generated/" with no
	// owners at all). Match (below) still reports this as a genuine match,
	// just with a zero-length Owners slice -- never silently skipped as if
	// the line did not exist.
	Owners []OwnerRef
	// Line is this rule's 1-based source line number -- diagnostics only,
	// never consulted by Match's own precedence logic (file ORDER, which
	// Parse already preserves via slice order, is what "last matching
	// pattern" means -- Line is redundant with that order and exists only
	// so a caller can report a human-readable "matched at line N").
	Line int
}

// Parse reads a CODEOWNERS file's raw content into an ordered slice of
// Rule, one per non-blank, non-comment line, in FILE order (never
// reordered) -- Match's own "last matching pattern wins" precedence
// depends on this order being preserved exactly as it appeared in the
// file. A malformed line (a pattern with a token that is neither a valid
// "@login", "@org/team", nor something that looks like an email address)
// is skipped entirely, never partially parsed into a Rule with a bogus
// owner -- fail-conservative: a caller un-derstands "this line named zero
// resolvable owners" identically whether the line was blank, a comment,
// or genuinely malformed, and treats it the same way Match already treats
// a legitimate zero-owner line (this package draws no distinction between
// them; only a caller inspecting Rule.Owners for emptiness could, and none
// today needs to).
func Parse(content string) []Rule {
	lines := strings.Split(content, "\n")
	rules := make([]Rule, 0, len(lines))

	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pattern := fields[0]

		var owners []OwnerRef
		for _, tok := range fields[1:] {
			if ref, ok := parseOwnerToken(tok); ok {
				owners = append(owners, ref)
			}
			// An unparseable token is skipped (fail-conservative, see this
			// function's own doc comment) rather than aborting the whole
			// line -- GitHub's own parser is similarly line-tolerant, and a
			// single bad token on an otherwise-good line should not lose
			// every OTHER real owner on that same line.
		}

		rules = append(rules, Rule{Pattern: pattern, Owners: owners, Line: lineNo})
	}

	return rules
}

// parseOwnerToken classifies one whitespace-delimited owner token.
func parseOwnerToken(tok string) (OwnerRef, bool) {
	switch {
	case strings.HasPrefix(tok, "@"):
		body := strings.TrimPrefix(tok, "@")
		if body == "" {
			return OwnerRef{}, false
		}
		if org, team, ok := strings.Cut(body, "/"); ok {
			if org == "" || team == "" {
				return OwnerRef{}, false
			}
			return OwnerRef{Kind: OwnerKindTeam, OrgSlug: org, TeamSlug: team}, true
		}
		return OwnerRef{Kind: OwnerKindUser, Login: body}, true

	case strings.Contains(tok, "@"):
		// A bare email address -- deliberately a loose heuristic (contains
		// "@", was not itself prefixed with "@") rather than a strict RFC
		// 5322 validator: CODEOWNERS' own real-world usage is exactly this
		// loose (GitHub's own docs give plain examples like
		// "docs@example.com" with no further validation described), and
		// this package's one real caller (githubapi.ResolveCodeOwners)
		// treats an email OwnerRef as an opaque string to match against
		// the identity graph's own identities.email column regardless.
		return OwnerRef{Kind: OwnerKindEmail, Login: tok}, true

	default:
		return OwnerRef{}, false
	}
}

// Matcher is rules (Parse's own output), pre-compiled once via Compile --
// the type that actually answers Match calls. A Matcher holds no shared,
// package-level state (audit note: an earlier draft of this package cached
// compiled patterns in a package-level map with no synchronization at all,
// which `go test -race` correctly flagged as a data race the moment two
// Match calls ran concurrently -- a real bug this codebase's own "go test
// -race always on" convention exists specifically to catch before it ships
// rather than after). A Matcher is an ordinary, request-scoped value: build
// one via Compile, use it for every path lookup within that one
// ResolveCodeOwners call, then discard it -- never shared across
// goroutines, so it needs no mutex of its own either.
type Matcher struct {
	rules    []Rule
	compiled []*regexp.Regexp // parallel to rules, same index
}

// Compile pre-compiles every rule's own Pattern exactly once -- rules is
// assumed to already be in file order (Parse's own output, unmodified);
// Compile never sorts or otherwise reorders it, and neither does Match
// below (Match's own "last matching pattern wins" precedence depends on
// that order being preserved exactly).
func Compile(rules []Rule) *Matcher {
	m := &Matcher{rules: rules, compiled: make([]*regexp.Regexp, len(rules))}
	for i, r := range rules {
		m.compiled[i] = compilePattern(r.Pattern)
	}
	return m
}

// Match returns the LAST rule whose Pattern matches path -- the package
// doc comment's own "last matching pattern takes the most precedence"
// rule, verified against GitHub's own documentation. ok=false means no
// rule matches path at all (an unowned path -- a legitimate, common
// outcome for any repo that does not carry a catch-all "*" pattern),
// never an error.
//
// path is a repo-relative FILE path (no leading "/"), exactly the shape a
// PR's own changed-files listing already provides (githubapi's own
// pullFileResponse.Filename) -- Match itself is agnostic to whether path
// names a file or directory (CODEOWNERS patterns, like gitignore's, treat
// a directory pattern as implicitly covering everything beneath it; see
// compilePattern's own doc comment), but every real caller in this
// codebase only ever matches concrete file paths, never a bare directory
// name on its own.
func (m *Matcher) Match(path string) (Rule, bool) {
	var (
		winner Rule
		found  bool
	)
	for i, re := range m.compiled {
		if re.MatchString(path) {
			winner = m.rules[i]
			found = true
		}
	}
	return winner, found
}

// compilePattern translates one CODEOWNERS/gitignore-style pattern into a
// Go regexp matching a repo-relative path, per the package doc comment's
// own verified-against-GitHub's-docs precedence rules:
//
//   - A pattern containing "/" ANYWHERE other than a single trailing
//     slash (leading OR in the middle) is ANCHORED to the repo root --
//     matches only that exact relative path, never at another depth.
//   - A pattern with NO "/" (or only a single TRAILING "/") is
//     UNANCHORED -- matches at any depth, exactly like a plain gitignore
//     basename pattern.
//   - A trailing "/" marks a directory pattern -- matches the named
//     directory itself and everything beneath it (GitHub's own docs
//     example: "/apps/ @doctocat" owns the apps/ directory, "its files,
//     and all its subdirectories"). This package applies that SAME
//     "matches this and everything beneath" allowance uniformly, even to
//     a pattern with no trailing slash -- harmless for an ordinary file
//     pattern (a real file path is never itself a prefix of a longer real
//     file path the way a directory name is), and necessary for a
//     directory pattern written without the trailing slash.
//   - "*" matches any run of characters EXCEPT "/" (one path segment).
//   - "?" matches exactly one character except "/".
//   - "**" matches across segment boundaries: "**/" at the start of a
//     (sub)pattern means "zero or more full directory levels"; a trailing
//     "/**" means "everything beneath this point"; "**" used any other
//     way (defensively) degrades to the same "match anything" behavior
//     rather than a hard parse error -- a malformed "**" placement should
//     never make an entire CODEOWNERS rule silently inert.
//   - "[" / "]" / "!" are NOT treated as metacharacters at all (regexp.
//     QuoteMeta'd like any other literal character) -- GitHub's own docs
//     state CODEOWNERS supports neither character-range matching nor
//     negation, unlike ordinary gitignore; treating them as literal text
//     (which real CODEOWNERS patterns essentially never contain) is the
//     correct, conservative behavior for the one documented gitignore
//     dialect that excludes them.
//
// # The "matches this and everything beneath" suffix, and its one exception
//
// The trailing "(?:/.*)?$" appended below implements "matches this and
// everything beneath" (see the bullet above) -- applied uniformly
// regardless of an explicit trailing slash, EXCEPT when the pattern has
// no explicit trailing slash AND its own last translated token is a bare,
// segment-unconstrained "*" (single star, not "**"). That exception
// exists because appending the suffix is only ever "harmless" (this
// package's own established reasoning: "a real file path is never itself
// a prefix of a longer real file path the way a directory name is") when
// the pattern's own last matched character(s) are a FIXED literal --
// which pins the match to one exact name, indistinguishable from a file.
// A bare trailing "*" instead matches ANY run of non-"/" characters with
// NO further constraint, so "docs/*" without this exception compiles to
// "^docs/[^/]*(?:/.*)?$", which matches "docs/build-app/troubleshooting.md"
// by letting "[^/]*" consume the "build-app" DIRECTORY NAME and the
// suffix consume the rest -- GitHub's own documentation gives this exact
// path as the canonical NON-match for "docs/*" (§60 review finding C2):
// a bare trailing "*" stays within the one path segment it is written in,
// exactly like plain gitignore's own "never crosses a /" rule for "*",
// with no implicit "everything beneath" extension layered on top merely
// because nothing follows it in the pattern. A pattern that explicitly
// ends in "/" (dirOnly) is unaffected -- that trailing slash is itself an
// explicit, deliberate "this names a directory" signal, never an
// inference this package is making on the author's behalf.
func compilePattern(pattern string) *regexp.Regexp {
	dirOnly := strings.HasSuffix(pattern, "/")
	core := strings.TrimSuffix(pattern, "/")
	anchored := strings.Contains(core, "/")
	core = strings.TrimPrefix(core, "/")

	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	b.WriteString(translatePatternBody(core))
	if dirOnly || !endsInBareStar(core) {
		b.WriteString("(?:/.*)?$")
	} else {
		b.WriteString("$")
	}

	return regexp.MustCompile(b.String())
}

// endsInBareStar reports whether core -- a pattern with any leading "/"
// and trailing "/" already stripped by compilePattern -- ends in a single,
// segment-unconstrained "*" wildcard: a trailing "*" that is not itself
// the tail of a "**" (compilePattern's own doc comment above explains why
// this specific shape is the one exception to the "matches this and
// everything beneath" suffix).
func endsInBareStar(core string) bool {
	return strings.HasSuffix(core, "*") && !strings.HasSuffix(core, "**")
}

// translatePatternBody translates core (a pattern with any leading "/"
// and trailing "/" already stripped by compilePattern) into a regex
// fragment, character by character -- see compilePattern's own doc
// comment for the full rule set this implements.
func translatePatternBody(core string) string {
	var b strings.Builder
	runes := []rune(core)
	for i := 0; i < len(runes); {
		c := runes[i]
		switch {
		case c == '*' && i+1 < len(runes) && runes[i+1] == '*':
			leftBoundary := i == 0 || runes[i-1] == '/'
			afterStars := i + 2
			rightBoundary := afterStars == len(runes) || runes[afterStars] == '/'

			switch {
			case leftBoundary && rightBoundary && afterStars < len(runes):
				// "**/" -- zero or more full directory levels, consuming
				// the trailing slash itself so the following literal
				// segment does not see a doubled separator.
				b.WriteString("(?:.*/)?")
				i = afterStars + 1
			case leftBoundary && afterStars == len(runes):
				// A trailing "**" (with nothing after it) -- match
				// everything from here on.
				b.WriteString(".*")
				i = afterStars
			default:
				// "**" not in a clean segment position (e.g. "a**b") --
				// defensive fallback per this function's own doc comment:
				// degrade to "match anything" rather than a hard error.
				b.WriteString(".*")
				i += 2
			}

		case c == '*':
			b.WriteString("[^/]*")
			i++

		case c == '?':
			b.WriteString("[^/]")
			i++

		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	return b.String()
}
