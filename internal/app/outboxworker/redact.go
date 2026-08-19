package outboxworker

import "regexp"

// credentialsPattern matches a leading "[scheme://]user:password@" prefix
// so it can be redacted regardless of whether the surrounding string is a
// validly-parseable URL -- byte-for-byte the SAME pattern
// internal/adapters/outbound/modal/errors.go's own credentialsPattern
// uses (redactURLCredentials there, for the identical reason: an error
// string is not guaranteed to have gone through url.Parse successfully
// before it was ever formatted into a message, so url.URL.Redacted alone
// is not a safe universal guard). Duplicated here rather than imported:
// modal is a sibling outbound provider adapter with no dependency
// relationship to this package, and exporting a single private helper
// cross-package purely to share four lines of regexp would be a stranger
// coupling than the duplication itself -- this mirrors this codebase's
// own established precedent for exactly this tradeoff (pushpr.go's own
// top comment on parseOwnerRepo notes the OPPOSITE case -- consolidating
// into internal/domain/reposource -- specifically because THAT helper had
// a natural, domain-owned home to move into; no such shared home exists
// for "redact credentials from an arbitrary error string").
var credentialsPattern = regexp.MustCompile(`^(([a-zA-Z][a-zA-Z0-9+.-]*://)?[^/:@]*:)[^/@]*(@)`)

// redactURLCredentials replaces the password component of a leading
// userinfo prefix ("user:PASSWORD@...") with "xxxxx", leaving the rest of
// raw untouched. raw need not be a validly-parseable URL. A raw value with
// no userinfo prefix is returned unchanged.
//
// Applied to a delivery error string before it is ever placed on a log
// line (recordFailure's own dead-letter branch, builder.go): this
// package's own notifier adapters (slackapi/linearapi/githubapi/rwx/
// objstore) authenticate via HTTP headers, never a credential-bearing
// URL, under this codebase's own current wiring -- but a delivery target
// is still, in the general case, a URL a future notifier kind could
// legitimately embed a token or webhook secret in (e.g. a Slack incoming-
// webhook-style integration), and net/http's own *url.Error wraps the
// REQUEST URL verbatim into Error() on any transport-level failure. This
// costs nothing to apply defensively now, closing off that leak class
// before any notifier actually needs it to be safe -- the same
// "costs nothing to close off the same leak class" reasoning
// modal.InvalidBaseURLError's own doc comment already states for BaseURL,
// which similarly is not EXPECTED to carry credentials either.
func redactURLCredentials(raw string) string {
	return credentialsPattern.ReplaceAllString(raw, "${1}xxxxx${3}")
}
