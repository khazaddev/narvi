package ports

import "errors"

// ErrShadowSuppressed is what a suppressed MergePR returns, and it is
// deliberately not like the other five suppressions.
//
// §30.7: five of the six writes return a coherent synthetic result, which
// keeps the state machines that consume them honest and moving. A merge is
// the exception, and the reason is specific rather than aesthetic. A
// fabricated merge success is a false-record generator: the auto-merge
// worker would log "merged", write an audit row carrying an invented SHA,
// and feed fake confirmations into the contradiction-rate metric -- the
// very instrument whose evidence is supposed to justify arming auto-merge
// for real. Meanwhile the pull request, which in a demotion scenario is a
// real one, stays open and re-candidates on every tick.
//
// So a suppressed merge returns neither success nor a failure of the
// merge. It returns this sentinel, which callers map to "recorded, not
// merged": no RecordConfirmed, no Merged: true reaching a screen, and a
// distinct audit action of its own.
//
// It is an error value so that a caller which forgets it cannot mistake
// suppression for success -- the failure direction is "the merge did not
// happen", which is true. But every caller that consumes MergePR is
// expected to test for it explicitly with errors.Is and take the recorded
// path, rather than reporting a merge failure that did not occur.
var ErrShadowSuppressed = errors.New("shadow: recorded, not merged")
