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
// authoringModel is empty or names a provider this catalog does not
// recognize. This is the COMMON case, not a degraded one: opposition is
// only a meaningful concept for a Narvi-authored PR with a KNOWN authoring
// model (provenance.NarviAuthored, session.build_model_id) -- the
// overwhelming majority of PRs are human-authored, with no "authoring
// model" to oppose in the first place, and §26.4's own wording ("family
// opposed to the PR's authoring model") presupposes one exists. This
// function does not invent a family to oppose when there is nothing on
// record to oppose.
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
// falling back to the alphabetically-first model in that provider's own
// list when none is marked Reasoning, so the choice is always
// deterministic and never depends on map/slice iteration order.
func ResolveCounterReviewerModel(authoringModel string) string {
	authoringProvider, _, ok := strings.Cut(authoringModel, "/")
	if !ok || strings.TrimSpace(authoringProvider) == "" {
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

// pickReasoningOrFirst returns the alphabetically-first Reasoning-capable
// model's own ID, or (if none is Reasoning-capable) the alphabetically-
// first model's own ID overall -- always deterministic, never dependent
// on models' own input order (modelcatalog.Catalog's own doc comment: a
// deep defensive copy, but the underlying snapshot's own array order is
// still whatever the embedded JSON file happens to list, not itself a
// stable contract this function should rely on).
func pickReasoningOrFirst(models []modelcatalog.Model) (string, bool) {
	if len(models) == 0 {
		return "", false
	}

	ids := make([]string, len(models))
	reasoningIDs := make([]string, 0, len(models))
	for i, m := range models {
		ids[i] = m.ID
		if m.Reasoning {
			reasoningIDs = append(reasoningIDs, m.ID)
		}
	}

	if len(reasoningIDs) > 0 {
		sort.Strings(reasoningIDs)
		return reasoningIDs[0], true
	}
	sort.Strings(ids)
	return ids[0], true
}
