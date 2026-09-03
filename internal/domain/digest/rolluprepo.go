package digest

import (
	"time"

	"github.com/narvidev/narvi/internal/domain/review"
)

// RepoSection is one repo's own contribution to a single channel's daily
// digest -- built by internal/app/digest from review_verdicts/
// auto_approval_outcomes history (never live SCM data -- §21.3's own
// "renders from the same read model", not a fresh GitHub read).
type RepoSection struct {
	RepoFullName string

	// ShippableCounts/VerdictsComputed mirror internal/domain/
	// reviewverdict's own "not yet computed" sentinel discipline: a repo
	// with genuinely zero verdicts posted in the digest's own window
	// (VerdictsComputed=false) renders a distinct, honest line from one
	// with verdicts but none classified some particular way (a real,
	// present-but-zero map entry).
	ShippableCounts  map[review.Shippable]int
	VerdictsComputed bool

	// ContradictionRate/ContradictionContested/ContradictionSampleSize/
	// ContradictionComputed mirror reviewverdict.ContradictionRate's own
	// identical sentinel -- ContradictionComputed=false means no
	// auto-approval outcome has been recorded for this repo yet (whether
	// or not any verdicts were posted -- an outcome requires a MERGE or
	// an override, a stricter bar than a verdict merely existing).
	// ContradictionContested is carried alongside the already-divided
	// ContradictionRate (rather than re-multiplied back out of it at
	// render time, which would round-trip through floating point for no
	// reason) -- both are the SAME two integers reviewverdict.
	// ContradictionRate's own caller already has in hand.
	ContradictionRate       float64
	ContradictionContested  int
	ContradictionSampleSize int
	ContradictionComputed   bool

	// AutoMergeEnabled is this repo's CURRENT toggle state (repo_settings.
	// auto_merge_enabled) -- rendered so a reader can tell, without
	// leaving the digest, whether "ready to merge (auto)" rows for this
	// repo are still waiting on a human click or are merging unattended.
	AutoMergeEnabled bool
}

// RollupData is Render's own complete input -- one channel's worth of
// digest content, spanning however many repos that channel is scoped to
// (§21.3: "per-repo/per-channel... a person's digest shows what their
// own inbox would show, not a global fan-out" -- applied here as ONE
// message per channel, sectioned by repo, rather than one message per
// (repo, channel) pair; see internal/app/digest's own doc comment for
// the full channel-scoping design).
type RollupData struct {
	// SendDate is the calendar day this digest reports on (typically
	// "yesterday" relative to the pump tick that built it) -- rendered
	// verbatim, never re-derived from time.Now() by this pure package.
	SendDate time.Time
	Repos    []RepoSection
}
