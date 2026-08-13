package reviewpost

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DigestSection names one section of the merge-readout digest a maintainer
// can contest/confirm (§26.5, Step 69, "measuring the readout"). A closed,
// small vocabulary -- unlike Finding's file-path-keyed identity
// (finding.go), a digest section is not per-file, so this is the whole
// "what" a piece of feedback is about.
type DigestSection string

// The digest sections a maintainer can currently give feedback on. Only
// DigestSectionArchRecap has a dedicated capture COMMAND today (§26.5's
// own "arch recap wrong: <reason>", mirroring Step 63's "false positive:
// <reason>" exactly) -- the other three are named here so the read model
// (per-section contest/confirm counts) and ComputeDigestSectionIdentity
// below are never hard-coded to just one section, even though v1 ships
// only one capture path onto them.
const (
	DigestSectionSummary          DigestSection = "summary"
	DigestSectionArchRecap        DigestSection = "arch_recap"
	DigestSectionStackRisks       DigestSection = "stack_risks"
	DigestSectionUnverifiedLimits DigestSection = "unverified_limits"
)

// digestSectionIdentitySeparator mirrors findingIdentitySeparator's own
// choice exactly (finding.go) -- a NUL byte can never appear in a section
// name or its own rendered text, so there is no ambiguity between e.g.
// section="a", text="b" and section="a\x00b", text="" the way a plain
// delimiter could introduce.
const digestSectionIdentitySeparator = "\x00"

// ComputeDigestSectionIdentity is §26.5's own extension of §22.1's
// content-hash identity discipline (ComputeFindingIdentity, finding.go)
// from findings to digest sections: "each contest reconciled by a content
// hash of the digest section's own persisted text... never by section
// index or position, which would suffer the exact churn-fragility problem
// §22.1 already solved for findings: a PR update that merely reorders or
// rewords an unrelated section must never make an already-contested
// ArchDecision read as a new one."
//
// A sha256 hash over a canonical join of exactly two normalized
// components: section (the fixed DigestSection vocabulary above -- never
// normalized further, it is already a closed set of ASCII literals) and
// text (the section's own persisted content, normalized via
// normalizeDigestSectionText below -- the SAME whitespace-collapse+casefold
// treatment normalizeFindingDescription already applies to a finding's own
// description, for the identical reason: this survives WHITESPACE-level
// regeneration of the same underlying recap, not PARAPHRASE-level, an
// honest, named residual matching ComputeFindingIdentity's own).
//
// Server-computed, ALWAYS -- never accepted from a caller, the same
// "don't trust the model with anything authoritative" discipline
// ComputeFindingIdentity itself already establishes.
func ComputeDigestSectionIdentity(section DigestSection, text string) string {
	joined := string(section) + digestSectionIdentitySeparator + normalizeDigestSectionText(text)
	sum := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(sum[:])
}

// normalizeDigestSectionText mirrors normalizeFindingDescription (finding.go)
// exactly -- trims, casefolds, and collapses every run of whitespace to a
// single space -- so a digest section re-rendered with only whitespace-
// level differences (e.g. a trailing newline, doubled spaces) still hashes
// identically across two different verdicts on the same PR.
func normalizeDigestSectionText(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Join(strings.Fields(lower), " ")
}
