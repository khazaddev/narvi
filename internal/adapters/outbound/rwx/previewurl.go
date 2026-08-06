package rwx

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// rwxPreviewDomainSuffix is the fixed, trusted DNS suffix every genuine
// RWX friendly-preview URL's hostname must end with (§4.1.2 points 3/4) --
// named once so FriendlyPreviewURL's own rendering and its validation
// below share one unambiguous source of truth for "what counts as RWX's
// own domain."
const rwxPreviewDomainSuffix = ".rwx.run"

// FriendlyPreviewURL renders RWX's own "friendly" preview-app URL (§4.1.2
// points 3/4): "https://{endpoint}--{org-slug}.rwx.run" — always the
// latest build for that endpoint ("Use for shared PR links that
// automatically pick up new commits", RWX's own docs, fetched
// 2026-08-06), as opposed to RWX's own CANONICAL, per-build URL (pinned to
// one specific build's own task cache key — not rendered here at all:
// §4.1.2 point 4's own "no build to await inside a Deliver" reasoning
// means this adapter never has a build's cache key in hand at enqueue
// time; that is the named v2 upgrade path, not this Step's own job).
//
// endpointTemplate is the per-repo setting's own configured value
// (repo_settings.rwx_preview_endpoint_template); a literal "{pr}"
// substring within it (if present) is replaced with prNumber — this
// adapter's own simple, documented templating convention for the common
// case where one repo's `.rwx` run definition serves more than one PR's
// own preview endpoint (a single, unparameterized endpoint would
// otherwise collide every PR of a repo onto the same friendly URL). A
// template with no "{pr}" placeholder is used verbatim — a legitimate
// choice for a repo whose own .rwx definition only ever previews one
// fixed endpoint (e.g. a docs site with no per-PR variation).
//
// §4.1.3 names the accepted risk this function's own existence implies:
// Narvi renders this URL from its OWN copy of the endpoint template; the
// repo's own .rwx run definition owns the real one. Drift between the two
// produces a dead link (annoying, never corrupting).
//
// Security fix (host-pinning): endpointTemplate/orgSlug are admin-
// configured (repo_settings, §4.1.2 point 1), not attacker-controlled on
// any single request, but this function used to string-concatenate them
// into "https://..." with NO validation at all — a hostile or merely
// corrupted template such as "rwx.run@evil.example.com/" used to render a
// completely off-domain URL that the platform bot would then vouch for via
// the narvi/preview commit status (githubapi's own CreateCommitStatus,
// previewlinknotifier.go) as though it genuinely belonged to RWX. This now
// returns (string, error): callers (previewpr.go's enqueuePreviewBestEffort)
// must treat an error as "skip the preview for this repo, warn log" — the
// same best-effort posture the pushed.Sha validation next to that call
// site already established.
//
// Rejects an empty endpointTemplate or orgSlug outright, before ever
// rendering a URL — previewpr.go's own readPreviewSettings already treats
// either as "not configured" and never calls this function with one in
// production, but this function must not depend on every future caller
// re-deriving that same discipline to stay safe.
//
// Otherwise, the rendered string is parsed with url.Parse — real,
// standards-compliant authority parsing, rather than a hand-rolled string
// check a "@"-userinfo or scheme-lookalike trick could otherwise fool —
// and rejected unless ALL of: scheme is exactly "https"; there is no
// userinfo (u.User, the "@" trick above: a URL with "user@" before the
// host is quietly interpreted as authenticating to whatever host follows,
// with "user" discarded, not as part of the trusted host); and
// u.Hostname() ends with rwxPreviewDomainSuffix (".rwx.run") AND is
// strictly longer than the suffix alone (rejects the degenerate hostname
// that IS just the suffix, with nothing real preceding it). A pinned
// SUFFIX match, never "contains" or "HasPrefix": a hostname's trust
// derives from its rightmost labels, unlike a URL path, so anchoring
// anywhere but the end would let an attacker-chosen prefix precede a
// legitimate-looking suffix (e.g. "rwx.run.evil.example.com").
func FriendlyPreviewURL(endpointTemplate string, prNumber int, orgSlug string) (string, error) {
	if endpointTemplate == "" || orgSlug == "" {
		return "", fmt.Errorf("rwx: preview url: endpoint template and org slug must both be non-empty")
	}

	endpoint := strings.ReplaceAll(endpointTemplate, "{pr}", strconv.Itoa(prNumber))
	rendered := "https://" + endpoint + "--" + orgSlug + rwxPreviewDomainSuffix

	u, err := url.Parse(rendered)
	if err != nil {
		return "", fmt.Errorf("rwx: preview url: rendered an unparseable url: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("rwx: preview url: rendered scheme %q, want https", u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("rwx: preview url: rendered host carries userinfo, refusing to trust it as an rwx.run url")
	}
	if host := u.Hostname(); !strings.HasSuffix(host, rwxPreviewDomainSuffix) || len(host) <= len(rwxPreviewDomainSuffix) {
		return "", fmt.Errorf("rwx: preview url: rendered host %q is not a genuine %s subdomain", host, rwxPreviewDomainSuffix)
	}

	return rendered, nil
}
