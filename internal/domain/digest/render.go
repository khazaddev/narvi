package digest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/narvidev/narvi/internal/domain/review"
)

// Provider is which channel dialect Render formats for -- a closed,
// two-value vocabulary (this Step ships exactly Slack and Linear
// delivery, mirroring internal/domain/reviewverdict.Outcome's own
// "closed vocabulary lives in Go" precedent).
type Provider string

// The two Provider values Render accepts.
const (
	ProviderSlack  Provider = "slack"
	ProviderLinear Provider = "linear"
)

// shippableDisplayOrder is the FIXED, deterministic order Render prints
// review.Shippable counts in -- never Go map iteration order (which
// RollupData.ShippableCounts, a map, does not itself guarantee), and
// never derived from review.Shippable's own const declaration order
// (mirrors that package's OWN "ranking is an explicit table, never iota
// order" discipline, shippable.go) -- most to least permissive, the same
// direction review's own shippableRank total order already runs.
var shippableDisplayOrder = []review.Shippable{review.ShippableAuto, review.ShippableNeedsHuman, review.ShippableBlock}

// Render produces data's own complete digest text for provider -- a
// plain, deterministic string template (doc.go's own "no LLM call, ever"
// discipline). Never errors: RollupData is already-validated,
// already-fetched data by the time it reaches this pure function (no
// external input this function itself could reject).
func Render(data RollupData, provider Provider) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s Review digest — %s\n", heading(provider), data.SendDate.Format("2006-01-02"))

	if len(data.Repos) == 0 {
		b.WriteString("\nNo review activity to report for this channel.\n")
		return b.String()
	}

	repos := make([]RepoSection, len(data.Repos))
	copy(repos, data.Repos)
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoFullName < repos[j].RepoFullName })

	for _, r := range repos {
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s  (auto-merge: %s)\n", bold(provider, r.RepoFullName), onOff(r.AutoMergeEnabled))

		if !r.VerdictsComputed {
			b.WriteString("  no verdicts posted in this window\n")
		} else {
			var counts []string
			for _, s := range shippableDisplayOrder {
				counts = append(counts, fmt.Sprintf("%s: %d", s, r.ShippableCounts[s]))
			}
			fmt.Fprintf(&b, "  %s\n", strings.Join(counts, "   "))
		}

		if !r.ContradictionComputed {
			b.WriteString("  contradiction rate: not yet computed\n")
		} else {
			fmt.Fprintf(&b, "  contradiction rate: %.0f%% (%d of %d confirmed/overridden)\n", r.ContradictionRate*100, r.ContradictionContested, r.ContradictionSampleSize)
		}
	}

	return b.String()
}

// heading is a fixed, provider-agnostic marker prefix -- kept as a
// function (rather than inlined) so a future third provider's own
// heading convention has one obvious place to join, mirroring bold/
// onOff's own identical shape below.
func heading(provider Provider) string {
	switch provider {
	case ProviderSlack:
		return ":bar_chart:"
	default:
		return "📊"
	}
}

// bold wraps s in provider's own bold-emphasis syntax -- Slack's single-
// asterisk mrkdwn vs Linear's plain CommonMark double-asterisk (see
// internal/adapters/outbound/slackapi/mrkdwn_outbound.go's own doc
// comment for Slack's dialect, confirmed against Slack's real formatting
// reference during that Step's own design phase).
func bold(provider Provider, s string) string {
	switch provider {
	case ProviderSlack:
		return "*" + s + "*"
	default:
		return "**" + s + "**"
	}
}

// onOff renders a boolean toggle state as the SAME "on"/"off" vocabulary
// this codebase's own repo_settings HTTP surface and mockups use for a
// human-facing toggle -- never "true"/"false".
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
