package digest

import (
	"context"
	"sort"
	"time"

	appreviewverdict "github.com/khazaddev/narvi/internal/app/reviewverdict"
	digestdomain "github.com/khazaddev/narvi/internal/domain/digest"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/platform"
)

// buildRollup assembles one channel's own digest.RollupData from
// repoFullNames -- entirely from review_verdicts/repo_settings/
// auto_approval_outcomes (deps.ReviewVerdict, the SAME §21.1/§21.2 read
// model), never a fresh GitHub/SCM read (§21.3: "renders from the same
// read model above via a template, not a model call"). Content is
// bounded to deps.Timeouts.DigestContentWindow (one calendar day, "a
// daily digest"), deliberately narrower than the channel-discovery
// lookback (channels.go) -- see that field's own doc comment
// (platform/timeouts.go).
func buildRollup(ctx context.Context, deps Deps, sendDate time.Time, repoFullNames []string, now time.Time) digestdomain.RollupData {
	repos := make([]string, len(repoFullNames))
	copy(repos, repoFullNames)
	sort.Strings(repos)

	sections := make([]digestdomain.RepoSection, 0, len(repos))
	for _, repoFullName := range repos {
		sections = append(sections, buildRepoSection(ctx, deps, repoFullName, now))
	}

	return digestdomain.RollupData{SendDate: sendDate, Repos: sections}
}

func buildRepoSection(ctx context.Context, deps Deps, repoFullName string, now time.Time) digestdomain.RepoSection {
	logger := platform.Logger(ctx)
	section := digestdomain.RepoSection{RepoFullName: repoFullName}

	section.AutoMergeEnabled = appreviewverdict.AutoMergeEnabled(ctx, deps.ReviewVerdict, repoFullName)

	// §30.8: a shadow-era verdict must never reveal a phantom review in
	// this customer-facing rollup -- ListNonShadowRecordsSince is
	// ListRecordsSince's own query-level exclusion, never a call-site
	// filter over the unfiltered read.
	since := now.Add(-deps.Timeouts.DigestContentWindow)
	records, err := appreviewverdict.ListNonShadowRecordsSince(ctx, deps.ReviewVerdict, repoFullName, since)
	if err != nil {
		logger.Error("digest: list review_verdicts records failed", "error", err, "repo_full_name", repoFullName)
	} else if len(records) > 0 {
		section.VerdictsComputed = true
		section.ShippableCounts = make(map[review.Shippable]int)
		for _, r := range records {
			section.ShippableCounts[r.Verdict.Shippable]++
		}
	}

	// Deliberately NOT bounded to DigestContentWindow (24h) -- a
	// calibration statistic (§21.2's own "is this repo ready to arm
	// auto-merge" question) needs ReviewVerdictAnalyticsWindow's own
	// wider, 30-day sample to mean anything; a single day's own
	// confirmed/overridden count would be too small to report as a rate
	// at all most days. The shippable counts ABOVE are the "what
	// happened today" half of this section; this is the "how is this
	// repo trending overall" half -- two different windows, by design.
	rate, contested, total, ok, err := appreviewverdict.ContradictionRate(ctx, deps.ReviewVerdict, repoFullName, now)
	if err != nil {
		logger.Error("digest: compute contradiction rate failed", "error", err, "repo_full_name", repoFullName)
	} else if ok {
		section.ContradictionComputed = true
		section.ContradictionRate = rate
		section.ContradictionContested = contested
		section.ContradictionSampleSize = total
	}

	return section
}
