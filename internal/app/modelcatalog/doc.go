// Package modelcatalog is §8.8's own "Catalog" deliverable
// (§8.8; §8 item 8; §29; §25.2's own "GET
// /provider catalog is the source of truth for providers/models/variants/
// cost" framing).
//
// # Structural decision (named here, since §29 leaves it open)
//
// snapshot.json is a control-plane-embedded SNAPSHOT of OpenCode's own
// live GET /provider catalog for the 3 providers Step 53 already wires
// credential injection for (google/anthropic/openai) -- live-verified
// against the pinned OpenCode 1.17.15 binary during this Step's own
// implementation research (a clean-config instance; the same discipline
// §25.2's own Gemini finding used), NOT a live per-sandbox proxy.
//
// Why a snapshot, not a live call: the control-plane image does not ship
// the OpenCode binary (§29.9's own identical reasoning for why the
// ChatGPT device-flow client is a direct CP-side adapter rather than
// brokered through a spawned sandbox) -- there is no running OpenCode
// server this package could query even if it wanted to. This mirrors the
// SAME "pinned known-good set" convention §7 already established for the
// sandbox-side per-turn fallback (internal/adapters/outbound/opencode's
// own resolveProviderModel/fallbackModel) -- applied here as the control
// plane's ONLY source rather than a fallback of last resort. Refreshed
// by hand whenever the pinned OpenCode version bumps, exactly like that
// fallback constant already is.
//
// Every model id is the exact catalog id OpenCode itself recognizes,
// usable verbatim as the "<providerId>/<modelId>" string modelId/
// buildModelId/effort/buildEffort already accept end to end (§25.1's own
// "no Narvi-side allowlist" passthrough, unchanged by this catalog's
// existence -- it is a discovery aid, never a validating allowlist: an
// unlisted "provider/model" string still works exactly as it does today,
// resolved live by the sandbox-side adapter at dispatch time).
//
// A live per-sandbox catalog proxy (a new wire command surfacing THIS
// session's exact sandbox's own live catalog) is a natural future
// extension once Phase 7's composer actually needs it, but is
// deliberately NOT built here -- REST is additive, so adding one later
// costs nothing today.
package modelcatalog
