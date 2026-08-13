package reviewtriage

import (
	"sort"
	"strings"

	"github.com/khazaddev/narvi/internal/app/modelcatalog"
)

// counterReviewerProviderPreference is the fixed, deterministic order this
// function walks Step 59's own model catalog in when picking the counter-
// reviewer sub-task's own opposing-model-family override (§26.4: "family
// opposed to the PR's authoring model... via the engine's own per-sub-
// agent model selection, using Step 53's credential injection + Step 59's
// model catalog"). Exactly the three providers Step 53 already wires
// credential injection for (internal/app/modelcatalog/doc.go: "the 3
// providers Step 53 already wires credential injection for
// (google/anthropic/openai)") -- there is no fourth provider this
// function could ever select, by construction.
var counterReviewerProviderPreference = []string{"anthropic", "openai", "google"}

// ResolveCounterReviewerModel picks the counter-reviewer sub-task's own
// opposing-model-family override (§26.4, Step 69) -- the "provider/model"
// string internal/adapters/outbound/opencode.MergeReviewSubAgentsConfig
// writes into the counter-reviewer custom OpenCode agent's own "model"
// field (opencode.json), pinning that ONE sub-task to a model family
// different from whichever family authored this PR.
//
// Returns "" (no override -- the counter-reviewer sub-task then simply
// inherits whatever model the deep-path turn's own dispatch already
// resolved, reviewtriage.ModelAndEffort's own deep-tier choice) when
// authoringModel is empty or has no "provider/model" shape at all (no "/",
// or a blank provider before it) -- the COMMON case, not a degraded one:
// opposition is only a meaningful concept for a Narvi-authored PR with a
// KNOWN authoring model (provenance.NarviAuthored, session.build_model_id)
// -- the overwhelming majority of PRs are human-authored, with no
// "authoring model" to oppose in the first place, and §26.4's own wording
// ("family opposed to the PR's authoring model") presupposes one exists.
// This function does not invent a family to oppose when there is nothing
// on record to oppose.
//
// An authoringModel naming a provider OUTSIDE this catalog's own three
// (counterReviewerProviderPreference) is deliberately NOT a third "return
// no override" case: such a provider can never equal any of the three
// preference entries below, so none of them is ever excluded, and this
// function still returns a real opposing-family pick -- any of the three
// known providers is, by construction, a different family from an
// unrecognized one. "Opposition" only needs a provider ID to compare
// against, never catalog membership of the authoring side itself.
//
// credentialedProviders (B2 fix) is the set of lowercase provider IDs this
// SESSION actually has a resolvable credential for (internal/app/
// reviewtriage.CredentialedProviders' own reduction of ProviderCredentialStore.
// ListForResolution -- "would Step 53's own delivery endpoint produce
// anything for this provider at all", never a decrypted value). A
// candidate provider absent from this set (including every provider when
// credentialedProviders is nil -- a Go nil-map read is always false, no
// special-casing needed) is skipped exactly like an excluded authoring-
// family match above, falling through to the next preference-order
// candidate, or to "" if none remain. This function never GUESSES: Step
// 53's own credential injection is best-effort and per-(repo/environment/
// global) configured, so counterReviewerProviderPreference's fixed 3-
// provider list is only a statement of which providers the MECHANISM
// supports, never a promise that all 3 (or even one) are actually usable
// for a given session -- pinning the counter-reviewer sub-task to a
// provider with no usable credential would not fail loudly here; it would
// surface later as an opaque auth failure deep inside the sandboxed
// OpenCode process, far from this decision. "" (no override, the
// sub-task then simply inherits the deep-path turn's own already-
// dispatched model, which IS known to have a usable credential -- it is
// already running) is always the safe fallback over a blind pin.
//
// "tier from depth (§26.3, already shipped)": this function is only ever
// called for a deep-path review (counter-review never runs on light,
// §26.9), so there is exactly one tier to pick from -- no depth parameter
// is threaded through here at all, mirroring reviewtriage.CostBudget.
// ForDepth's own "the caller already knows which path it's on" precedent.
// Within the opposing provider's own model list, this function prefers a
// Reasoning-capable model (modelcatalog.Model.Reasoning) -- the closest
// proxy this catalog exposes to "frontier tier" (§29's own doc comment:
// no dedicated review-model-selection/tiering mechanism predates Step 68,
// which itself is "an optional operator override rather than a catalog-
// driven tiering system" -- there is no richer tier signal to consult),
// falling back to every model in that provider's own list when none is
// marked Reasoning. See pickReasoningOrFirst's own doc comment for how a
// single winner is then chosen from that pool -- ContextWindow then Cost,
// never alphabetically: an alphabetically-first model name has no
// relationship to capability at all (e.g. this catalog's own
// "gpt-5.3-codex-spark", a narrow 128k-context coding variant, sorts
// before "gpt-5.4"'s own 1.05M-context general model purely because '3'
// precedes '4' -- picking it as this catalog's own best available
// "opposing frontier" proxy would be an accident of string sort order,
// not a considered choice).
func ResolveCounterReviewerModel(authoringModel string, credentialedProviders map[string]bool) string {
	authoringProvider, _, ok := strings.Cut(authoringModel, "/")
	authoringProvider = strings.ToLower(strings.TrimSpace(authoringProvider))
	if !ok || authoringProvider == "" {
		return ""
	}

	byProvider := make(map[string][]modelcatalog.Model, len(counterReviewerProviderPreference))
	for _, p := range modelcatalog.Catalog() {
		byProvider[p.ID] = p.Models
	}

	for _, providerID := range counterReviewerProviderPreference {
		if providerID == authoringProvider {
			continue
		}
		if !credentialedProviders[providerID] {
			continue
		}
		models := byProvider[providerID]
		if len(models) == 0 {
			continue
		}
		if modelID, ok := pickReasoningOrFirst(models); ok {
			return providerID + "/" + modelID
		}
	}
	return ""
}

// pickReasoningOrFirst returns the counter-reviewer's own single best
// candidate model ID from models (a Reasoning-capable model preferred,
// falling back to every model in the list when none is Reasoning-capable
// -- ResolveCounterReviewerModel's own doc comment on why Reasoning is
// this catalog's closest "frontier tier" proxy), then, within that pool,
// ranks by rankCounterReviewerCandidates below: ContextWindow descending,
// then Cost.Output descending, then model ID ascending as the final,
// always-available tie-break -- never a bare alphabetically-first pick, which
// has no relationship to model capability at all (ResolveCounterReviewerModel's
// own doc comment: an accident of string sort order, not a considered
// choice). Always deterministic, never dependent on models' own input
// order (modelcatalog.Catalog's own doc comment: a deep defensive copy,
// but the underlying snapshot's own array order is still whatever the
// embedded JSON file happens to list, not itself a stable contract this
// function should rely on) -- safe to sort models' own returned slice
// in place: Catalog() hands every caller a fresh deep copy, never a
// shared reference, so reordering it here can never be observed by
// another caller or a later call.
func pickReasoningOrFirst(models []modelcatalog.Model) (string, bool) {
	if len(models) == 0 {
		return "", false
	}

	pool := models
	reasoning := make([]modelcatalog.Model, 0, len(models))
	for _, m := range models {
		if m.Reasoning {
			reasoning = append(reasoning, m)
		}
	}
	if len(reasoning) > 0 {
		pool = reasoning
	}

	sort.Slice(pool, func(i, j int) bool {
		return rankCounterReviewerCandidates(pool[i], pool[j])
	})
	return pool[0].ID, true
}

// rankCounterReviewerCandidates reports whether a ranks strictly ahead of
// b as a counter-reviewer candidate (pickReasoningOrFirst's own doc
// comment): a larger ContextWindow first -- this catalog's own next-
// closest proxy to "frontier tier" after Reasoning capability itself --
// then a higher Cost.Output as the tie-break (a frontier/flagship model
// is priced at the top of this catalog's own tiers, unlike a cheap mini/
// lite/flash budget variant that happens to share the same context
// window), and finally model ID ascending as the LAST-resort tie-break,
// so two candidates that share both a ContextWindow and a Cost.Output
// still resolve to one deterministic winner.
func rankCounterReviewerCandidates(a, b modelcatalog.Model) bool {
	if a.ContextWindow != b.ContextWindow {
		return a.ContextWindow > b.ContextWindow
	}
	if a.Cost.Output != b.Cost.Output {
		return a.Cost.Output > b.Cost.Output
	}
	return a.ID < b.ID
}
