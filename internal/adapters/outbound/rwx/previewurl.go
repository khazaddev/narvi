package rwx

import (
	"strconv"
	"strings"
)

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
func FriendlyPreviewURL(endpointTemplate string, prNumber int, orgSlug string) string {
	endpoint := strings.ReplaceAll(endpointTemplate, "{pr}", strconv.Itoa(prNumber))
	return "https://" + endpoint + "--" + orgSlug + ".rwx.run"
}
