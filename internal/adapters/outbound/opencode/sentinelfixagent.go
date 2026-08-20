package opencode

import "encoding/json"

// This file implements Step 48's own ("sentinels + suggestions", §17.2)
// capability-restriction mechanism: "the child session's write/edit tool
// capability is additionally restricted server-side to test/doc path
// patterns at spawn time... a second, independent layer alongside §17.4's
// own post-hoc diff-scope check, not a fifth check within it."
//
// # What this mechanism actually is, empirically confirmed
//
// OpenCode's own real, native permission engine (types.go's own doc
// comment already documents the "plan" agent's identical use of it to
// deny the edit tool globally) supports a PER-PATTERN action for the
// "edit" permission specifically -- confirmed live, during this Step's
// own research pass, against the pinned OpenCode 1.17.15 binary, in TWO
// independent ways:
//
//  1. The SHIPPED, PRODUCTION "plan" agent's own permission list (GET
//     /agent) already contains {"permission":"edit","pattern":
//     ".opencode/plans/*.md","action":"allow"} alongside {"permission":
//     "edit","pattern":"*","action":"deny"} -- i.e. glob-scoped edit
//     permission is not a hypothetical capability this Step is hoping
//     exists; it is the EXACT mechanism ALREADY gating every plan-mode
//     turn in production today (Step 37).
//  2. A CUSTOM agent, defined via an opencode.json config file's own
//     "agent" object with a narrower glob (e.g. "tests/**": "allow",
//     "docs/**": "allow", "*": "deny" for the "edit" permission), was
//     registered live against the same pinned binary during this Step's
//     research pass and correctly appeared, with EXACTLY the configured
//     pattern list, via GET /agent -- proving OpenCode's config loader
//     accepts and correctly wires a custom agent's own glob-restricted
//     edit permission, not just the two built-in "plan"/"build" agents
//     types.go's own doc comment previously documented as the only
//     empirically-confirmed case.
//
// See sentinelfixagent_test.go's own real-binary contract test (mirrors
// this package's OWN established "verified live against the pinned
// binary" convention, e.g. realturn_test.go) for the checked-in proof:
// starting the real pinned binary against a temp repo carrying the config
// this file generates, and asserting GET /agent reflects EXACTLY the
// configured permission list.
//
// # Honest, named residual (not papered over)
//
// This checked-in contract test proves CONFIGURATION correctness against
// the real binary -- that OpenCode's own config loader registers this
// agent with exactly the glob patterns intended. It does NOT itself
// dispatch a real model turn that attempts a denied edit and asserts the
// tool call is actually rejected end to end (that would require a
// configured AI-provider credential and a real, billed model call on
// every test run, mirroring realturn_test.go's own accepted cost/
// precedent for EXACTLY that kind of test -- deliberately not duplicated
// here to avoid a second class of live-provider-dependent test). The
// underlying enforcement mechanism itself is not hypothetical, though: it
// is the SAME permission engine already gating every real plan-mode turn
// in production (point 1 above) -- this Step's own addition is a new
// custom agent definition using the identical, already-relied-upon
// mechanism, not a new kind of guarantee.
//
// This restricts the "edit" tool specifically -- it does NOT restrict
// "bash" (mirroring the "plan" agent's own identical, already-documented
// gap, types.go). This is exactly why §17.2 frames this as "defense in
// depth alongside §17.4's own post-hoc diff-scope check" rather than a
// complete guarantee on its own -- a bash-tool-invoked write outside
// test/doc paths is NOT caught by this layer; it is caught by §17.4's
// merge-gating diff-scope re-check instead (this package's own scope
// stops at the OpenCode-agent-configuration layer; §17.4 is implemented
// in internal/adapters/inbound/github's own merge-gating lane).

// sentinelFixAgentName is the literal custom OpenCode agent name this
// adapter requests for a sentinel-auto-fix child session's own build-mode
// turns -- a NARVI-invented name (unlike planAgentName, which is one of
// OpenCode's own 7 native agents), registered via the opencode.json
// config SentinelFixAgentConfigJSON (below) that sandbox-agent writes
// into the workspace before ever spawning `opencode serve` for such a
// session (cmd/sandbox-agent/main.go).
const sentinelFixAgentName = "sentinel-fix"

// sentinelFixAllowedEditGlobs is the fixed, server-computed set of path
// patterns a sentinel-auto-fix child session's own edit tool is allowed
// to touch -- test and documentation files only (§17.1: "sentinel fixes
// touch test/doc files, never a scoped prototyping environment"; §17.2:
// "restricted server-side to test/doc path patterns"). Fixed, not
// per-repo-configurable, matching this Step's own scope -- a repo-
// specific override is not something the plan asks for.
var sentinelFixAllowedEditGlobs = []string{
	"**/*_test.go",
	"**/testdata/**",
	"docs/**",
	"**/*.md",
	"**/*.mdx",
}

// sentinelFixAgentEntry is one entry in the opencode.json "agent" config
// object's own shape -- VERIFIED live during this Step's own research
// pass to be exactly the shape OpenCode's config loader accepts (see this
// file's own top doc comment): "mode"/"description" alongside a
// "permission" object whose "edit" key is itself a plain pattern -> action
// map.
type sentinelFixAgentEntry struct {
	Mode        string                       `json:"mode"`
	Description string                       `json:"description"`
	Permission  map[string]map[string]string `json:"permission"`
}

// sentinelFixAgentEntryValue builds the "sentinel-fix" agent's own config
// entry -- this package's one place that constructs it, reused by
// MergeSentinelFixAgentConfig (below).
func sentinelFixAgentEntryValue() sentinelFixAgentEntry {
	permission := map[string]string{"*": "deny"}
	for _, glob := range sentinelFixAllowedEditGlobs {
		permission[glob] = "allow"
	}
	return sentinelFixAgentEntry{
		Mode:        "primary",
		Description: "Sentinel auto-fix: restricted to test/doc paths only (§17.2).",
		Permission: map[string]map[string]string{
			"edit": permission,
		},
	}
}

// MergeSentinelFixAgentConfig folds the "sentinel-fix" agent definition
// into existingConfigJSON (the workspace's own current opencode.json
// content, nil/empty when no such file exists yet) WITHOUT disturbing any
// other key already present -- a repo's own committed opencode.json
// (custom agents, MCP config, formatter/LSP settings, ...) is preserved
// as-is; only top-level "agent"."sentinel-fix" is added or overwritten.
// This is a real, deliberate scope decision: a full 3-way config merge
// (e.g. a repo's own top-level "agent" key being something OTHER than an
// object) is out of this Step's own scope -- see this function's own
// error return for that one case, which the caller (cmd/sandbox-agent/
// main.go) logs and falls back to NOT writing a config at all (a
// sentinel-fix session would then run with the SAME agent selection as
// an ordinary build-mode session -- degrading to today's unrestricted
// behavior rather than corrupting the repo's own config file).
func MergeSentinelFixAgentConfig(existingConfigJSON []byte) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if len(existingConfigJSON) > 0 {
		if err := json.Unmarshal(existingConfigJSON, &doc); err != nil {
			return nil, err
		}
	}

	agents := map[string]json.RawMessage{}
	if raw, ok := doc["agent"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &agents); err != nil {
			return nil, err
		}
	}

	entryJSON, err := json.Marshal(sentinelFixAgentEntryValue())
	if err != nil {
		return nil, err
	}
	agents[sentinelFixAgentName] = entryJSON

	agentsJSON, err := json.Marshal(agents)
	if err != nil {
		return nil, err
	}
	doc["agent"] = agentsJSON

	return json.MarshalIndent(doc, "", "  ")
}
