package reposource

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
)

// Sentinel errors the validators below can return, each naming a distinct
// reason a candidate value is rejected, wrapped by a per-field typed error
// (InvalidRepoNameError, InvalidRepoURLError, InvalidRemoteNameError,
// InvalidRefError) so callers/tests can tell them apart via errors.Is
// while still getting the offending value via errors.As -- matching the
// sentinel-error house style used in internal/domain/{sandbox,turn,
// gitstate,environment}.
var (
	// ErrRepoNameCharset means a repo name contains a byte outside
	// `[a-zA-Z0-9_.-]` -- most importantly, this already excludes "/",
	// which rules out any multi-segment path traversal (e.g. "../etc")
	// outright.
	ErrRepoNameCharset = errors.New("reposource: repo name contains a character outside [a-zA-Z0-9_.-]")

	// ErrRepoNameDotSegment means a repo name is exactly "." or ".." --
	// both pass ErrRepoNameCharset's own charset check (both are composed
	// entirely of allowed characters) and must therefore be rejected as
	// their own explicit rule, not assumed to already be caught by the
	// charset above.
	ErrRepoNameDotSegment = errors.New(`reposource: repo name is exactly "." or ".."`)

	// ErrRemoteNameCharset means a remote name contains a byte outside
	// `[a-zA-Z0-9_.-]` -- the SAME charset ErrRepoNameCharset enforces,
	// deliberately: a git remote name (e.g. "origin", "upstream", "fork")
	// is conceptually a bare identifier, never a path or a URL, so this
	// one allowlist rule rejects "/" (no filesystem path, no path-based
	// redirection to a rogue destination repo) and ":" (no scheme
	// delimiter, so no "ext::"/"fd::"/alternate-transport string) in a
	// single step -- the same allowlist reasoning ValidateRepoURL already
	// uses for the URL field, applied here to the remote field.
	ErrRemoteNameCharset = errors.New("reposource: remote name contains a character outside [a-zA-Z0-9_.-]")

	// ErrRemoteNameDotSegment means a remote name is exactly "." or ".." --
	// both pass ErrRemoteNameCharset's own charset check (both are
	// composed entirely of allowed characters), and both are otherwise
	// meaningful to git as relative filesystem paths (the current or
	// parent directory of `git push`'s `-C <dir>`) if ever reached as a
	// remote positional argument, so both must be rejected as their own
	// explicit rule here too, exactly mirroring ErrRepoNameDotSegment's
	// own reasoning.
	ErrRemoteNameDotSegment = errors.New(`reposource: remote name is exactly "." or ".."`)

	// ErrURLNotParseable means the candidate URL failed net/url.Parse
	// outright.
	ErrURLNotParseable = errors.New("reposource: repo url does not parse as a URL")

	// ErrURLSchemeNotHTTPS means the candidate URL parsed, but its scheme
	// is not exactly "https". This is an ALLOWLIST rule, not a denylist:
	// every non-https scheme is rejected in one rule, including every git
	// alternate-transport ("ext::", "fd::", ...) and any bare
	// "-"-prefixed string (which never parses with Scheme == "https" at
	// all) -- no attempt is made to enumerate "known bad" schemes one at
	// a time.
	ErrURLSchemeNotHTTPS = errors.New(`reposource: repo url scheme is not "https"`)

	// ErrURLNoHost means the candidate URL parsed with Scheme == "https"
	// but an empty Host.
	ErrURLNoHost = errors.New("reposource: repo url has no host")

	// ErrRefEmpty means a branch name was the empty string.
	ErrRefEmpty = errors.New("reposource: branch/remote name is empty")

	// ErrRefDashPrefix means a branch name begins with "-" -- the exact
	// shape that lets git's own option parser interpret a trailing
	// positional argument (e.g. `git push <remote> <branch>`) as a FLAG
	// instead of a plain ref name (e.g. "--receive-pack=<cmd>",
	// "--upload-pack=<cmd>"). This is the non-negotiable rejection rule
	// this validator exists for.
	ErrRefDashPrefix = errors.New(`reposource: branch/remote name begins with "-"`)

	// ErrRefControlChar means a branch name contains a byte below 0x20 or
	// equal to 0x7f (a control character, including newlines) -- rejected
	// defensively; no legitimate git ref name needs one.
	ErrRefControlChar = errors.New("reposource: branch/remote name contains a control character")
)

// bareIdentifierCharset matches exactly the characters a "bare identifier"
// field -- one that is conceptually a single name, never a multi-segment
// path, URL, or transport string -- is allowed to contain. Both
// ValidateRepoName (a repo name) and ValidateRemoteName (a git remote
// name, e.g. "origin"/"upstream"/"fork") share this SAME charset, rather
// than each declaring its own regex literal: deliberately excludes "/",
// "\", ":", "[", "]", and every other shell/glob/path-separator/scheme-
// delimiter special character, so nothing on the right of a "/" (a path
// segment) or a ":" (a URL scheme or git alternate-transport prefix like
// "ext::"/"fd::") is ever reachable through either field at all.
var bareIdentifierCharset = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// isDotSegment reports whether value is exactly the literal string "." or
// "..". Both pass bareIdentifierCharset's own charset check (each is
// composed entirely of allowed characters), so callers that must reject
// them (ValidateRepoName, ValidateRemoteName) need this explicit check
// too, rather than assuming the charset check alone already covers it. A
// regex that is even slightly too permissive here is exactly the class of
// near-miss this package exists to not repeat.
func isDotSegment(value string) bool {
	return value == "." || value == ".."
}

// ValidateRepoName validates a candidate repo name (sessionconfig.
// SessionConfig.Repos[].Name / sandboxws.Push.Repos[].Name) before it is
// used to build a filesystem path (filepath.Join(workspaceDir, name)) or
// reaches a git subprocess's argument list. Rejects:
//
//  1. any byte outside [a-zA-Z0-9_.-] (ErrRepoNameCharset) -- this alone
//     already rejects any name containing "/", ruling out multi-segment
//     traversal;
//  2. the literal strings "." and ".." (ErrRepoNameDotSegment), explicitly
//     -- both pass rule 1's charset check (each is composed entirely of
//     characters that charset allows), so they need their own explicit
//     check rather than being assumed already caught by rule 1. A regex
//     that is even slightly too permissive here is exactly the class of
//     near-miss this check exists to not repeat.
func ValidateRepoName(name string) error {
	if !bareIdentifierCharset.MatchString(name) {
		return &InvalidRepoNameError{Name: name, Reason: ErrRepoNameCharset}
	}
	if isDotSegment(name) {
		return &InvalidRepoNameError{Name: name, Reason: ErrRepoNameDotSegment}
	}
	return nil
}

// InvalidRepoNameError reports a single repo name ValidateRepoName
// rejected, and why.
type InvalidRepoNameError struct {
	// Name is the offending repo name, verbatim.
	Name string
	// Reason is one of ErrRepoNameCharset or ErrRepoNameDotSegment -- the
	// base sentinel this error unwraps to.
	Reason error
}

func (e *InvalidRepoNameError) Error() string {
	return fmt.Sprintf("reposource: invalid repo name %q: %s", e.Name, e.Reason)
}

func (e *InvalidRepoNameError) Unwrap() error { return e.Reason }

// ValidateRepoURL validates a candidate repo clone URL (sessionconfig.
// SessionConfig.Repos[].Url) before it reaches a git subprocess's
// argument list as a trailing positional argument. Requires the URL to:
//
//  1. parse at all, via net/url.Parse (ErrURLNotParseable);
//  2. have Scheme == "https" exactly (ErrURLSchemeNotHTTPS) -- an
//     ALLOWLIST, not a denylist: this one rule rejects every git
//     alternate transport ("ext::", "fd::", "file://", "ssh://", plain
//     "http://", ...) and any bare "-"-prefixed string (which git's own
//     option parser would otherwise read as a flag, not a URL) in one
//     step, with no need to enumerate "known bad" schemes/transports one
//     at a time -- this codebase's own credential-helper design (§5.2:
//     "scoped https+host only") already assumes https-only remotes, so
//     this loses no real capability;
//  3. have a non-empty Host (ErrURLNoHost).
func ValidateRepoURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return &InvalidRepoURLError{URL: rawURL, Reason: ErrURLNotParseable}
	}
	if parsed.Scheme != "https" {
		return &InvalidRepoURLError{URL: rawURL, Reason: ErrURLSchemeNotHTTPS}
	}
	if parsed.Host == "" {
		return &InvalidRepoURLError{URL: rawURL, Reason: ErrURLNoHost}
	}
	return nil
}

// InvalidRepoURLError reports a single repo URL ValidateRepoURL rejected,
// and why.
type InvalidRepoURLError struct {
	// URL is the offending URL, verbatim.
	URL string
	// Reason is one of ErrURLNotParseable, ErrURLSchemeNotHTTPS, or
	// ErrURLNoHost -- the base sentinel this error unwraps to.
	Reason error
}

func (e *InvalidRepoURLError) Error() string {
	return fmt.Sprintf("reposource: invalid repo url %q: %s", e.URL, e.Reason)
}

func (e *InvalidRepoURLError) Unwrap() error { return e.Reason }

// validateRef implements ValidateBranch's own rejection rule: reject
// empty, reject a leading "-" (the non-negotiable rule closing the
// argument-injection class this whole package exists for -- a branch
// reaching `git push <remote> <branch>` as a trailing positional argument
// must never be readable as an option), and reject any control character/
// newline for good measure. Branches legitimately contain "/" (e.g.
// "feature/foo"), so -- unlike ValidateRepoName/ValidateRemoteName's own
// charset allowlist -- this rule is deliberately a narrower denylist, not
// a charset allowlist. kind names the field in the returned error
// ("branch") so a caller/log line can tell which one was rejected.
func validateRef(kind, value string) error {
	if value == "" {
		return &InvalidRefError{Kind: kind, Value: value, Reason: ErrRefEmpty}
	}
	if value[0] == '-' {
		return &InvalidRefError{Kind: kind, Value: value, Reason: ErrRefDashPrefix}
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return &InvalidRefError{Kind: kind, Value: value, Reason: ErrRefControlChar}
		}
	}
	return nil
}

// ValidateBranch validates a candidate branch name (sessionconfig.
// SessionConfig.Repos[].Branch / sandboxws.Push.Repos[].Branch) before it
// reaches a git subprocess's argument list (as `--branch <branch>` for
// clone, or as a trailing positional refspec for push). Branch is
// optional/nullable upstream -- callers only invoke this when a branch
// value is actually present, never for a nil/absent branch. See
// validateRef for the exact rejection rules.
func ValidateBranch(branch string) error {
	return validateRef("branch", branch)
}

// InvalidRefError reports a single branch name ValidateBranch rejected,
// and why.
type InvalidRefError struct {
	// Kind is "branch" -- which field was being validated.
	Kind string
	// Value is the offending branch name, verbatim.
	Value string
	// Reason is one of ErrRefEmpty, ErrRefDashPrefix, or
	// ErrRefControlChar -- the base sentinel this error unwraps to.
	Reason error
}

func (e *InvalidRefError) Error() string {
	return fmt.Sprintf("reposource: invalid %s %q: %s", e.Kind, e.Value, e.Reason)
}

func (e *InvalidRefError) Unwrap() error { return e.Reason }

// ValidateRemoteName validates a candidate git remote name (sandboxws.
// Push.Repos[].Remote, session-controlled and overridable from its
// default of "origin") before it reaches `git push` as a trailing
// positional argument. Unlike ValidateBranch, this is NOT validateRef's
// permissive "reject empty/leading-dash/control-chars only" rule: a git
// remote name is conceptually a bare identifier (like "origin", "upstream",
// "fork"), never a path or a URL, so it shares ValidateRepoName's own
// stricter charset-allowlist rule instead (via the same
// bareIdentifierCharset/isDotSegment helpers, not a duplicated regex
// literal). This single allowlist closes, in one step:
//
//   - path/destination redirection -- a remote value shaped like a
//     filesystem path (containing "/") to an attacker-controlled rogue
//     bare repo, which `git push` would otherwise happily push the real
//     commit to instead of the real "origin";
//   - alternate-transport strings ("ext::", "fd::", ...) -- these all
//     require ":", already excluded by the same charset rule that
//     excludes "/", so both angles close at once, exactly mirroring how
//     ValidateRepoURL is an allowlist rather than a denylist.
//
// Rejects:
//
//  1. any byte outside [a-zA-Z0-9_.-] (ErrRemoteNameCharset);
//  2. the literal strings "." and ".." (ErrRemoteNameDotSegment),
//     explicitly -- both pass rule 1's charset check, and both are
//     otherwise meaningful to git as relative filesystem paths if ever
//     reached as a remote positional argument.
func ValidateRemoteName(remote string) error {
	if !bareIdentifierCharset.MatchString(remote) {
		return &InvalidRemoteNameError{Remote: remote, Reason: ErrRemoteNameCharset}
	}
	if isDotSegment(remote) {
		return &InvalidRemoteNameError{Remote: remote, Reason: ErrRemoteNameDotSegment}
	}
	return nil
}

// InvalidRemoteNameError reports a single remote name ValidateRemoteName
// rejected, and why.
type InvalidRemoteNameError struct {
	// Remote is the offending remote name, verbatim.
	Remote string
	// Reason is one of ErrRemoteNameCharset or ErrRemoteNameDotSegment --
	// the base sentinel this error unwraps to.
	Reason error
}

func (e *InvalidRemoteNameError) Error() string {
	return fmt.Sprintf("reposource: invalid remote name %q: %s", e.Remote, e.Reason)
}

func (e *InvalidRemoteNameError) Unwrap() error { return e.Reason }
