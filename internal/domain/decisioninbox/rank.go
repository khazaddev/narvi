package decisioninbox

import (
	"sort"
	"time"
)

// RankKey is the minimal shape SortItems needs from a row to order it --
// the app-layer aggregator's own richer Item type (internal/app/
// decisioninbox) is never imported here (this package stays pure and
// dependency-free, per its own doc comment); a caller builds a []RankKey
// alongside its own richer rows and permutes both in lockstep (see
// SortItems' own doc comment for the exact contract).
type RankKey struct {
	Kind Kind
	// EnteredQueueAt is the instant this row first became a pending
	// decision -- e.g. a PR's own became-eligible instant, a plan's
	// created_at, an automation's paused_at. Never re-derived by this
	// package (no I/O, no Clock -- CLAUDE.md/§11); the caller computes it
	// once from already-fetched data before calling SortItems.
	EnteredQueueAt time.Time
}

// Less reports whether a should sort before b: primarily by DecisionCost
// ascending (§16.1: "ready_to_merge... first"), then -- within the SAME
// Kind -- by EnteredQueueAt ascending (the row that has been waiting
// LONGEST surfaces first within its own section, so a stale item is never
// buried behind fresher arrivals in the very kind meant to surface it).
func Less(a, b RankKey) bool {
	ca, cb := DecisionCost(a.Kind), DecisionCost(b.Kind)
	if ca != cb {
		return ca < cb
	}
	return a.EnteredQueueAt.Before(b.EnteredQueueAt)
}

// SortIndex returns a permutation of [0, len(keys)) -- the ORDER a caller
// should render its own parallel slice of rows in -- stable (sort.
// SliceStable) so two rows with an identical Kind and EnteredQueueAt
// (e.g. two plans created in the same instant) keep their original
// relative order rather than swapping unpredictably across two otherwise-
// identical calls.
func SortIndex(keys []RankKey) []int {
	idx := make([]int, len(keys))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		return Less(keys[idx[i]], keys[idx[j]])
	})
	return idx
}
