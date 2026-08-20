package opencode

import (
	"encoding/json"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// This file implements §26.4's own (§26.4/§26.6, "review deep path:
// adversarial counter-review + readout measurement") sub-agent
// registration: three custom OpenCode agents -- review.ArchitectureScribeAgentName,
// review.CounterReviewerAgentName, review.FactCheckAgentName -- the
// primary reviewer's own orchestration is instructed (internal/domain/
// review/context.go's own subAgentOrchestrationInstructions) to spawn via
// the engine's own "task" tool, exactly mirroring §8.2's own
// "sentinel-fix" custom agent (sentinelfixagent.go, this file's own
// direct precedent, generalized here from ONE agent to three).
//
// # What this mechanism actually is, empirically confirmed (this Step's own research pass)
//
// sentinelfixagent.go's own top doc comment already established that
// OpenCode's real, native permission engine accepts a custom agent
// definition via opencode.json's own "agent" object, with a
// pattern-scoped "edit" permission -- this Step's own research reused
// that SAME confirmed mechanism and additionally verified TWO more
// properties, live, against the pinned OpenCode 1.17.15 binary (a real
// `opencode serve` process, a probe opencode.json, GET /agent inspected
// directly -- mirrors sentinelfixagent_realbinary_test.go's own
// established "verify configuration correctness against the real binary"
// convention, generalized here):
//
//  1. "mode": "subagent" is a real, recognized value -- OpenCode's own
//     NATIVE sub-agent-only agents ("explore", "general") already carry
//     it, and a CUSTOM agent configured with "mode": "subagent" is
//     registered identically (GET /agent echoes it back verbatim). This
//     is the correct mode for all three agents this file registers: none
//     of them should ever be selectable as a session's own PRIMARY agent
//     (unlike "sentinel-fix", mode "primary", which IS a whole session's
//     own build-mode agent) -- these three exist ONLY to be spawned via
//     the "task" tool's own "subagent_type" parameter.
//  2. A custom agent's own "model" field (a plain "provider/model"
//     string -- the SAME shape this codebase's model selection already
//     uses everywhere else, turns.model_id/build_model_id/reviewtriage.
//     ResolveCounterReviewerModel's own return shape) is accepted and
//     round-trips through GET /agent as a decomposed {"modelID",
//     "providerID"} object -- e.g. a probe agent configured with
//     "model": "openai/gpt-5.1" was echoed back as
//     {"modelID":"gpt-5.1","providerID":"openai"}. This IS the "engine's
//     own per-sub-agent model selection" §26.4 names -- confirmed real,
//     not hypothetical, and reused here rather than any parallel
//     mechanism.
//  3. A custom agent's own "tools" object (a bool map keyed by tool name)
//     is accepted and materializes as ADDITIONAL deny permission entries
//     in GET /agent's own response, layered on top of whatever
//     "permission" itself already specifies -- e.g. a probe agent
//     configured with tools.bash/read/grep/glob/list/webfetch/task all
//     false produced exactly those as {"permission":<name>,"pattern":
//     "*","action":"deny"} entries. This is what fact-check's own "NO
//     tool access" (§26.6) is built from, below -- the "edit"
//     permission-map mechanism alone (sentinelfixagent.go's own
//     established technique) can only deny ONE tool family at a time by
//     pattern; "tools" is the mechanism that denies an entire tool
//     wholesale, which fact-check specifically needs (it has NOTHING to
//     read/search/execute with -- it reasons over text handed to it in
//     its own task-tool prompt, alone).
//
// # Honest, named residual (matches sentinelfixagent.go's own identical framing)
//
// The real-binary contract test this file's own sibling _realbinary_test.go
// adds proves CONFIGURATION correctness only -- that OpenCode's config
// loader registers these three agents with exactly the intended
// mode/model/permission/tools. It does NOT dispatch a real, billed model
// turn that spawns one via the "task" tool and asserts the SPAWNED
// sub-agent actually ran under the configured model/restrictions
// end-to-end (mirrors realturn_test.go's own accepted "a second class of
// live-provider-dependent test, deliberately not duplicated here"
// precedent) -- the underlying enforcement mechanism itself is not
// hypothetical, though: points 1-3 above are each independently verified
// against the real binary, and the "edit" deny half of this mechanism is
// the SAME one already gating every real sentinel-fix child session in
// production today.
// reviewSubAgentEntry is one entry in the opencode.json "agent" config
// object's own shape -- mirrors sentinelFixAgentEntry's own verified
// shape (sentinelfixagent.go) with two further fields this Step's own
// research pass additionally verified live (this file's own top doc
// comment, points 2-3): "model" (omitted entirely, via omitempty, when no
// override applies -- the agent then simply inherits whatever model the
// turn's own dispatch already resolved) and "tools" (omitted entirely for
// architecture-scribe/counter-reviewer, which both keep their own default
// tool access minus "edit").
type reviewSubAgentEntry struct {
	Mode        string                       `json:"mode"`
	Description string                       `json:"description"`
	Permission  map[string]map[string]string `json:"permission,omitempty"`
	Model       string                       `json:"model,omitempty"`
	Tools       map[string]bool              `json:"tools,omitempty"`
}

// readOnlyEditPermission is the "deny edit everywhere, no exceptions"
// permission map both architecture-scribe and counter-reviewer share --
// unlike sentinel-fix's own glob-ALLOWED-for-test/doc-paths shape
// (sentinelfixagent.go: this ONE child-session agent is allowed to WRITE
// test/doc fixes), these two review sub-agents are read-only in the
// stronger sense §26.4 itself uses the word: neither ever legitimately
// edits anything in the repo under review, full stop.
var readOnlyEditPermission = map[string]map[string]string{
	"edit": {"*": "deny"},
}

// noToolAccess is the fact-check sub-task's own "NO tool access" (§26.6,
// verbatim) -- every tool name this Step's own research pass observed
// live on a real OpenCode agent's own GET /agent permission list (this
// file's own top doc comment, point 3), each denied wholesale. "edit" is
// covered separately, via readOnlyEditPermissionForFactCheck below
// (mirrors architecture-scribe/counter-reviewer's own identical "edit" is
// a permission-map concern, every OTHER tool is a tools-map concern"
// split) -- kept as two separate mechanisms deliberately, rather than
// folding "edit" into this map too, so this file has exactly ONE
// edit-denial technique shared by all three agents, never two competing
// ways to express the identical intent.
var noToolAccess = map[string]bool{
	"bash":      false,
	"read":      false,
	"grep":      false,
	"glob":      false,
	"list":      false,
	"webfetch":  false,
	"websearch": false,
	"task":      false,
}

// architectureScribeAgentEntry builds the "architecture-scribe" agent's
// own config entry (§26.4): read-only, virgin-context recap of the diff's
// own architecture decisions -- no model override (it runs at the SAME
// tier the deep-path turn itself already dispatched at; §26.4 never asks
// for a distinct family/tier for this agent the way it does for
// counter-reviewer).
func architectureScribeAgentEntry() reviewSubAgentEntry {
	return reviewSubAgentEntry{
		Mode:        "subagent",
		Description: "Deep-path review: virgin-context architecture-decision recap from the diff + repo conventions (§26.4). Read-only.",
		Permission:  readOnlyEditPermission,
	}
}

// counterReviewerAgentEntry builds the "counter-reviewer" agent's own
// config entry (§26.4): adversarial, read-only (tool access retained for
// verifying claims against real files -- unlike fact-check below, it is
// NOT denied bash/read/grep/etc), optionally pinned to counterReviewerModel
// (a "provider/model" string, internal/app/reviewtriage.
// ResolveCounterReviewerModel's own return shape -- "" means no override,
// the common case per that function's own doc comment, and Model is left
// at its own zero value so the "model" key is omitted from the rendered
// JSON entirely, via omitempty).
func counterReviewerAgentEntry(counterReviewerModel string) reviewSubAgentEntry {
	return reviewSubAgentEntry{
		Mode:        "subagent",
		Description: "Deep-path review: adversarial counter-review of the primary reviewer's own findings (§26.4). Read-only, tool-equipped.",
		Permission:  readOnlyEditPermission,
		Model:       counterReviewerModel,
	}
}

// factCheckAgentEntry builds the "fact-check" agent's own config entry
// (§26.6): diff-only, NO tool access at all -- runs on both the light and
// deep paths (§26.6: "the light path's own single review turn spawns
// exactly one fact-check sub-task"). Deliberately NOT §22.1.1's own `LLM`
// port -- see review/context.go's own subAgentOrchestrationInstructions
// doc comment, and §26.4, for why this
// is mechanically an in-sandbox sub-task like the other two, never a
// CP-side call.
func factCheckAgentEntry() reviewSubAgentEntry {
	return reviewSubAgentEntry{
		Mode:        "subagent",
		Description: "Both review paths: diff-only fact-check that kills a finding only when provably wrong from the diff text alone (§26.6). No tool access.",
		Permission:  readOnlyEditPermission,
		Tools:       noToolAccess,
	}
}

// MergeReviewSubAgentsConfig folds all three review sub-agent definitions
// (architecture-scribe, counter-reviewer, fact-check) into
// existingConfigJSON (the workspace's own current opencode.json content,
// nil/empty when no such file exists yet) WITHOUT disturbing any other
// key already present -- mirrors MergeSentinelFixAgentConfig's own
// identical "preserve everything else, only touch this file's own named
// keys" contract (sentinelfixagent.go) exactly, generalized from one key
// under "agent" to three.
//
// Unlike MergeSentinelFixAgentConfig, called ONLY for a sentinel-auto-fix
// child session (SessionConfig.CapabilityRestricted), this function is
// called UNCONDITIONALLY, for every session (cmd/sandbox-agent/main.go) --
// registering three more agent DEFINITIONS is always harmless (a
// definition is inert unless a turn's own prompt actually instructs the
// agent to spawn it via the "task" tool's own "subagent_type" parameter,
// and only a deep-path/light-path review turn's own prompt, review/
// context.go's subAgentOrchestrationInstructions, ever does that) -- see
// this package's own review sub-agent config doc comment (this file, top)
// for the full "why unconditional is correct and not a rigor leak" -- the
// light path's own prompt never mentions architecture-scribe/
// counter-reviewer at all (§26.9), so their mere DEFINITION being present
// changes nothing about what the light path's own agent is ever
// instructed to do.
//
// counterReviewerModel is forwarded verbatim into counterReviewerAgentEntry
// -- "" (no PR association, or no resolvable authoring-model provenance,
// the common case) omits the "model" key entirely from that ONE entry;
// architecture-scribe/fact-check never carry a model override at all,
// regardless.
func MergeReviewSubAgentsConfig(existingConfigJSON []byte, counterReviewerModel string) ([]byte, error) {
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

	entries := map[string]reviewSubAgentEntry{
		review.ArchitectureScribeAgentName: architectureScribeAgentEntry(),
		review.CounterReviewerAgentName:    counterReviewerAgentEntry(counterReviewerModel),
		review.FactCheckAgentName:          factCheckAgentEntry(),
	}
	for name, entry := range entries {
		entryJSON, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		agents[name] = entryJSON
	}

	agentsJSON, err := json.Marshal(agents)
	if err != nil {
		return nil, err
	}
	doc["agent"] = agentsJSON

	return json.MarshalIndent(doc, "", "  ")
}
