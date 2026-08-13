package reviewtriage

// ReviewDepth is the light/deep routing decision §26.3 defines -- a
// closed, two-value string type (mirroring internal/domain/review's own
// closed-enum-as-string-type convention, e.g. review.Shippable). The Go
// zero value ("") is deliberately not one of the two legal members,
// matching review/doc.go's own "fail-conservative policy for every
// closed enum" -- see Floor below for how an unrecognized/zero value is
// handled.
type ReviewDepth string

const (
	// DepthLight is the default, lower-scrutiny path: balanced-tier
	// model, no forced effort override -- exactly today's review
	// behavior (§26.9: "the light path's behavior remains exactly
	// today's review").
	DepthLight ReviewDepth = "light"
	// DepthDeep is the higher-scrutiny path: frontier-tier model, forced
	// high effort, and (Step 69, not built by this Step) adversarial
	// counter-review + architecture recap.
	DepthDeep ReviewDepth = "deep"
)

// depthRank orders ReviewDepth for Floor's own max-of-two composition,
// mirroring internal/domain/review's own explicit-table-not-iota-order
// discipline (review/shippable.go's rank function, review/doc.go's own
// "Ranking is an explicit table, never iota order" section) -- an
// accidental reorder of the two consts above must never silently change
// this package's own policy.
var depthRank = map[ReviewDepth]int{
	DepthLight: 0,
	DepthDeep:  1,
}

// rank returns d's own position in depthRank -- an unrecognized/zero
// ReviewDepth (never legitimately produced by Decide, but a defensive
// case for a caller-supplied prior value read back from storage, e.g. a
// row written before this column existed) ranks with DepthLight, the
// LEAST conservative reading. This is deliberately the one place in this
// package that does NOT follow internal/domain/review's own "unrecognized
// ranks with the worst-known value" convention: an absent/garbled PRIOR
// depth means "we have no record this PR was ever routed deep", which is
// honestly indistinguishable from "it never was" -- there is no
// principled way to treat "unknown" as "deep" without inventing evidence
// that does not exist. Floor's own caller-facing contract (below) never
// depends on this defaulting toward deep; Decide's OWN fresh signals are
// what actually determine depth for a PR with no prior record at all.
func rank(d ReviewDepth) int {
	if r, ok := depthRank[d]; ok {
		return r
	}
	return 0
}

// Floor composes a freshly-computed depth with a PR's own previously
// recorded depth (§24's own re-review floor: "once deep, a PR stays
// deep, even if the delta itself would independently route light") --
// the more conservative (higher-ranked) of the two always wins, mirroring
// internal/domain/review's own maxShippable/max(rank) composition
// pattern (shippable.go) applied to this package's own two-value
// ReviewDepth instead. prior == "" (no prior verdict/depth on record --
// e.g. this PR's first-ever review) is the common, legitimate case: fresh
// alone decides, unaffected by an absent floor.
func Floor(fresh, prior ReviewDepth) ReviewDepth {
	if rank(prior) > rank(fresh) {
		return prior
	}
	return fresh
}
