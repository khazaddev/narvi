// Package gitstate will hold the pure git state machine (stash-if-dirty →
// checkout session branch → pop; branch-name normalization) — implemented in
// PR-09 (§3.4).
package gitstate
