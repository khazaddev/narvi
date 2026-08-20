package reviewtriage

// EffortHigh is the fixed reasoning-effort variant string §26.3's own
// deep path forces (§26.3: "deep = frontier tier + high effort") --
// mirrors sandboxws.Prompt.Effort's own already-shipped, free-form
// per-model "variant" string (§29.8: "valid values owned per-
// model by OpenCode's catalog variants maps, no Narvi-side enum"); "high"
// is the one variant present on every real model in this codebase's own
// model catalog snapshot (internal/app/modelcatalog/snapshot.json),
// verified directly rather than assumed.
const EffortHigh = "high"

// ModelAndEffort resolves depth into the (modelID, effort) override pair
// a review turn's own creation call threads onto turns.model_id/effort --
// CreateTurnOptions.Effort/the modelID parameter every review-turn-creation
// path already accepts, §8.8's own reasoning-effort threading, an
// ALREADY-EXISTING, already-wired-end-to-end mechanism this Step reuses
// rather than inventing a second one. "Dedicated review-model selection"
// ITSELF is a separate claim -- §26.3 is explicit that no such mechanism
// predates this Step at all (§8 item 2 names it only as a feature-set
// line, never a built one, before now) -- §26.3 is what introduces it,
// as an optional operator override layered on top of §8.8's own
// pre-existing threading. Both return values
// are nil for anything other than DepthDeep -- the light path leaves
// BOTH completely unset, preserving §26.9's own invariant to the letter:
// "the light path's behavior remains exactly today's review", for model
// selection too, not just rigor.
//
// deepModelID is the operator-configured frontier-tier model id for the
// deep path (platform.Config.ReviewModelDeep, an OPTIONAL env var) --
// empty when unconfigured, in which case modelID stays nil too (the
// turn simply inherits whatever the deployment's own OpenCode-side
// default model is, exactly as every review turn does today) while
// Effort is still forced to EffortHigh unconditionally: high reasoning
// effort needs no per-deployment model-catalog knowledge to be safe to
// request on ANY model (a genuinely unsupported variant string is the
// adapter's own concern, not this package's), unlike a concrete model
// id, which this codebase has no existing per-purpose config surface to
// default from -- see platform/config.go's own doc comment on
// ReviewModelDeep for the full "why this is the one new config knob
// this Step adds".
func ModelAndEffort(depth ReviewDepth, deepModelID string) (modelID *string, effort *string) {
	if depth != DepthDeep {
		return nil, nil
	}
	e := EffortHigh
	effort = &e
	if deepModelID != "" {
		m := deepModelID
		modelID = &m
	}
	return modelID, effort
}
