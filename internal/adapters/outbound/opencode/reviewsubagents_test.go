package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/domain/review"
)

// TestMergeReviewSubAgentsConfig_NoExistingFile proves the common case: a
// workspace with no opencode.json at all gets all three agent
// definitions -- architecture-scribe/counter-reviewer read-only (edit
// denied, nothing else), fact-check additionally with every other tool
// denied too.
func TestMergeReviewSubAgentsConfig_NoExistingFile(t *testing.T) {
	out, err := MergeReviewSubAgentsConfig(nil, "")
	if err != nil {
		t.Fatalf("MergeReviewSubAgentsConfig(nil, \"\") error = %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var agents map[string]reviewSubAgentEntry
	if err := json.Unmarshal(doc["agent"], &agents); err != nil {
		t.Fatalf("agent key is not the expected shape: %v", err)
	}

	for _, name := range []string{review.ArchitectureScribeAgentName, review.CounterReviewerAgentName, review.FactCheckAgentName} {
		entry, ok := agents[name]
		if !ok {
			t.Fatalf("agent[%q] missing from output: %s", name, out)
		}
		if entry.Mode != "subagent" {
			t.Errorf("agent[%q].Mode = %q, want %q (never selectable as a session's own primary agent)", name, entry.Mode, "subagent")
		}
		if entry.Permission["edit"]["*"] != "deny" {
			t.Errorf("agent[%q].Permission[edit][*] = %q, want %q", name, entry.Permission["edit"]["*"], "deny")
		}
	}

	if agents[review.ArchitectureScribeAgentName].Model != "" {
		t.Errorf("architecture-scribe.Model = %q, want empty (no override)", agents[review.ArchitectureScribeAgentName].Model)
	}
	if agents[review.CounterReviewerAgentName].Model != "" {
		t.Errorf("counter-reviewer.Model = %q, want empty when counterReviewerModel==\"\"", agents[review.CounterReviewerAgentName].Model)
	}
	if len(agents[review.ArchitectureScribeAgentName].Tools) != 0 {
		t.Errorf("architecture-scribe.Tools = %v, want empty (retains default tool access minus edit)", agents[review.ArchitectureScribeAgentName].Tools)
	}
	if len(agents[review.CounterReviewerAgentName].Tools) != 0 {
		t.Errorf("counter-reviewer.Tools = %v, want empty (retains default tool access minus edit -- adversarial review needs to verify claims against real files)", agents[review.CounterReviewerAgentName].Tools)
	}

	factCheckTools := agents[review.FactCheckAgentName].Tools
	for _, tool := range []string{"bash", "read", "grep", "glob", "list", "webfetch", "websearch", "task"} {
		if allowed, ok := factCheckTools[tool]; !ok || allowed {
			t.Errorf("fact-check.Tools[%q] = (%v, %v), want (false, true) -- §26.6: 'NO tool access'", tool, allowed, ok)
		}
	}
}

// TestMergeReviewSubAgentsConfig_CounterReviewerModelOverride proves
// counterReviewerModel, when non-empty, is forwarded verbatim onto ONLY
// the counter-reviewer entry -- never architecture-scribe or fact-check,
// which never carry a model override at all regardless of this argument.
func TestMergeReviewSubAgentsConfig_CounterReviewerModelOverride(t *testing.T) {
	out, err := MergeReviewSubAgentsConfig(nil, "openai/gpt-5.1")
	if err != nil {
		t.Fatalf("MergeReviewSubAgentsConfig(nil, ...) error = %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var agents map[string]reviewSubAgentEntry
	if err := json.Unmarshal(doc["agent"], &agents); err != nil {
		t.Fatalf("agent key is not the expected shape: %v", err)
	}

	if got := agents[review.CounterReviewerAgentName].Model; got != "openai/gpt-5.1" {
		t.Errorf("counter-reviewer.Model = %q, want %q", got, "openai/gpt-5.1")
	}
	if got := agents[review.ArchitectureScribeAgentName].Model; got != "" {
		t.Errorf("architecture-scribe.Model = %q, want empty (never overridden)", got)
	}
	if got := agents[review.FactCheckAgentName].Model; got != "" {
		t.Errorf("fact-check.Model = %q, want empty (never overridden)", got)
	}
}

// TestMergeReviewSubAgentsConfig_PreservesExistingKeys mirrors
// TestMergeSentinelFixAgentConfig_PreservesExistingKeys's own identical
// precedent: a repo's own committed opencode.json (custom agents, MCP
// config, whatever else) is preserved as-is; only agent.<the three names>
// are added.
func TestMergeReviewSubAgentsConfig_PreservesExistingKeys(t *testing.T) {
	existing := []byte(`{
		"$schema": "https://opencode.ai/config.json",
		"username": "someone",
		"agent": {
			"my-custom-agent": {"mode": "primary", "description": "unrelated"}
		}
	}`)

	out, err := MergeReviewSubAgentsConfig(existing, "")
	if err != nil {
		t.Fatalf("MergeReviewSubAgentsConfig() error = %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var username string
	if err := json.Unmarshal(doc["username"], &username); err != nil || username != "someone" {
		t.Errorf("username = %q, err = %v, want %q preserved", username, err, "someone")
	}

	var agents map[string]json.RawMessage
	if err := json.Unmarshal(doc["agent"], &agents); err != nil {
		t.Fatalf("agent key is not the expected shape: %v", err)
	}
	if _, ok := agents["my-custom-agent"]; !ok {
		t.Errorf("agent[%q] was dropped, want it preserved: %s", "my-custom-agent", out)
	}
	for _, name := range []string{review.ArchitectureScribeAgentName, review.CounterReviewerAgentName, review.FactCheckAgentName} {
		if _, ok := agents[name]; !ok {
			t.Errorf("agent[%q] missing from output: %s", name, out)
		}
	}
}

// TestMergeReviewSubAgentsConfig_CoexistsWithSentinelFixAgent proves this
// mechanism and §8.2's own sentinel-fix agent config can both apply to
// the SAME workspace without clobbering each other -- a sentinel-auto-fix
// child session that ALSO happens to be reviewed (e.g. §8.2's own fix
// PR getting its own review session, a genuinely reachable case since
// every PR gets reviewed) must retain both.
func TestMergeReviewSubAgentsConfig_CoexistsWithSentinelFixAgent(t *testing.T) {
	sentinelConfig, err := MergeSentinelFixAgentConfig(nil)
	if err != nil {
		t.Fatalf("MergeSentinelFixAgentConfig(nil) error = %v", err)
	}

	out, err := MergeReviewSubAgentsConfig(sentinelConfig, "")
	if err != nil {
		t.Fatalf("MergeReviewSubAgentsConfig() error = %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var agents map[string]json.RawMessage
	if err := json.Unmarshal(doc["agent"], &agents); err != nil {
		t.Fatalf("agent key is not the expected shape: %v", err)
	}
	if _, ok := agents[sentinelFixAgentName]; !ok {
		t.Errorf("agent[%q] (sentinel-fix) was dropped, want it preserved: %s", sentinelFixAgentName, out)
	}
	for _, name := range []string{review.ArchitectureScribeAgentName, review.CounterReviewerAgentName, review.FactCheckAgentName} {
		if _, ok := agents[name]; !ok {
			t.Errorf("agent[%q] missing from output: %s", name, out)
		}
	}
}

// TestMergeReviewSubAgentsConfig_OverwritesStalePriorEntries mirrors
// TestMergeSentinelFixAgentConfig_OverwritesStalePriorEntry's own
// identical precedent: a SECOND merge call (e.g. a sandbox reboot on the
// same workspace) replaces -- never duplicates -- all three prior
// entries.
func TestMergeReviewSubAgentsConfig_OverwritesStalePriorEntries(t *testing.T) {
	first, err := MergeReviewSubAgentsConfig(nil, "anthropic/claude-opus-4-5")
	if err != nil {
		t.Fatalf("first MergeReviewSubAgentsConfig() error = %v", err)
	}
	second, err := MergeReviewSubAgentsConfig(first, "openai/gpt-5.1")
	if err != nil {
		t.Fatalf("second MergeReviewSubAgentsConfig() error = %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(second, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var agents map[string]reviewSubAgentEntry
	if err := json.Unmarshal(doc["agent"], &agents); err != nil {
		t.Fatalf("agent key is not the expected shape: %v", err)
	}
	if len(agents) != 3 {
		t.Errorf("agent has %d entries after a second merge, want exactly 3 (no duplication)", len(agents))
	}
	if got := agents[review.CounterReviewerAgentName].Model; got != "openai/gpt-5.1" {
		t.Errorf("counter-reviewer.Model after second merge = %q, want the SECOND call's own %q (never stale from the first)", got, "openai/gpt-5.1")
	}
}

// TestMergeReviewSubAgentsConfig_RejectsMalformedExistingFile mirrors
// TestMergeSentinelFixAgentConfig_RejectsMalformedExistingFile's own
// identical precedent.
func TestMergeReviewSubAgentsConfig_RejectsMalformedExistingFile(t *testing.T) {
	_, err := MergeReviewSubAgentsConfig([]byte(`{not valid json`), "")
	if err == nil {
		t.Fatal("MergeReviewSubAgentsConfig() error = nil, want an error for malformed input")
	}
}

// TestMergeReviewSubAgentsConfig_ArchitectureScribeAndCounterReviewerNeverDenyBash
// pins the deliberate split this file's own doc comment states: unlike
// fact-check, architecture-scribe and counter-reviewer both KEEP their
// own default tool access (bash/read/grep/etc) -- only "edit" is ever
// denied for these two. A regression that accidentally applied
// noToolAccess to either of them would silently strip their ability to
// read the repo/verify claims at all.
func TestMergeReviewSubAgentsConfig_ArchitectureScribeAndCounterReviewerNeverDenyBash(t *testing.T) {
	out, err := MergeReviewSubAgentsConfig(nil, "")
	if err != nil {
		t.Fatalf("MergeReviewSubAgentsConfig() error = %v", err)
	}
	s := string(out)
	// A crude but effective structural check: "bash" must appear NOWHERE
	// in the whole document (mirrors TestMergeSentinelFixAgentConfig_
	// NeverAllowsBashOrOtherPermissions's own identical technique) --
	// fact-check's own noToolAccess IS expected to mention "bash", so this
	// test would need updating the day fact-check's own tool-deny list
	// changes; until then, "bash" appearing at all can only mean it leaked
	// onto architecture-scribe/counter-reviewer, which this test exists to
	// catch, OR onto fact-check as intended -- disambiguate by checking it
	// only ever appears inside the fact-check entry's own JSON object.
	factCheckIdx := strings.Index(s, `"`+review.FactCheckAgentName+`"`)
	if factCheckIdx < 0 {
		t.Fatalf("fact-check entry not found in output: %s", s)
	}
	beforeFactCheck := s[:factCheckIdx]
	if strings.Contains(beforeFactCheck, `"bash"`) {
		t.Errorf("\"bash\" appears before the fact-check entry -- want it to appear ONLY inside fact-check's own tools map: %s", s)
	}
}
