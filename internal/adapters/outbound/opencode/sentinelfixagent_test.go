package opencode

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMergeSentinelFixAgentConfig_NoExistingFile proves the common case:
// a workspace with no opencode.json at all gets exactly one agent
// definition, "sentinel-fix", with the fixed test/doc allow-list plus a
// wildcard deny.
func TestMergeSentinelFixAgentConfig_NoExistingFile(t *testing.T) {
	out, err := MergeSentinelFixAgentConfig(nil)
	if err != nil {
		t.Fatalf("MergeSentinelFixAgentConfig(nil) error = %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	var agents map[string]sentinelFixAgentEntry
	if err := json.Unmarshal(doc["agent"], &agents); err != nil {
		t.Fatalf("agent key is not the expected shape: %v", err)
	}

	entry, ok := agents[sentinelFixAgentName]
	if !ok {
		t.Fatalf("agent[%q] missing from output: %s", sentinelFixAgentName, out)
	}
	if entry.Mode != "primary" {
		t.Errorf("entry.Mode = %q, want %q", entry.Mode, "primary")
	}
	edit := entry.Permission["edit"]
	if edit["*"] != "deny" {
		t.Errorf(`edit["*"] = %q, want "deny"`, edit["*"])
	}
	for _, glob := range sentinelFixAllowedEditGlobs {
		if edit[glob] != "allow" {
			t.Errorf("edit[%q] = %q, want \"allow\"", glob, edit[glob])
		}
	}
}

// TestMergeSentinelFixAgentConfig_PreservesExistingKeys proves a repo's
// own committed opencode.json (custom agents, MCP config, whatever else)
// is preserved untouched -- only agent.sentinel-fix is added.
func TestMergeSentinelFixAgentConfig_PreservesExistingKeys(t *testing.T) {
	existing := []byte(`{
		"$schema": "https://opencode.ai/config.json",
		"username": "someone",
		"agent": {
			"my-custom-agent": {"mode": "primary", "description": "unrelated"}
		}
	}`)

	out, err := MergeSentinelFixAgentConfig(existing)
	if err != nil {
		t.Fatalf("MergeSentinelFixAgentConfig() error = %v", err)
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
	if _, ok := agents[sentinelFixAgentName]; !ok {
		t.Errorf("agent[%q] missing from output: %s", sentinelFixAgentName, out)
	}
}

// TestMergeSentinelFixAgentConfig_OverwritesStalePriorEntry proves a
// SECOND merge call (e.g. a sandbox reboot on the same workspace) replaces
// -- never duplicates or additively merges -- a prior "sentinel-fix" entry
// of its own.
func TestMergeSentinelFixAgentConfig_OverwritesStalePriorEntry(t *testing.T) {
	first, err := MergeSentinelFixAgentConfig(nil)
	if err != nil {
		t.Fatalf("first MergeSentinelFixAgentConfig() error = %v", err)
	}
	second, err := MergeSentinelFixAgentConfig(first)
	if err != nil {
		t.Fatalf("second MergeSentinelFixAgentConfig() error = %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(second, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var agents map[string]json.RawMessage
	if err := json.Unmarshal(doc["agent"], &agents); err != nil {
		t.Fatalf("agent key is not the expected shape: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("agent has %d entries after a second merge, want exactly 1 (no duplication)", len(agents))
	}
}

// TestMergeSentinelFixAgentConfig_RejectsMalformedExistingFile proves a
// malformed existing opencode.json surfaces an error rather than silently
// discarding it or panicking -- the CALLER (cmd/sandbox-agent/main.go) is
// responsible for falling back to "don't write a config at all" on this
// error, never corrupting the repo's own file.
func TestMergeSentinelFixAgentConfig_RejectsMalformedExistingFile(t *testing.T) {
	_, err := MergeSentinelFixAgentConfig([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("MergeSentinelFixAgentConfig() error = nil, want an error for malformed input")
	}
}

// TestMergeSentinelFixAgentConfig_NeverAllowsBashOrOtherPermissions proves
// this file's own honest, named scope: ONLY the "edit" permission is
// touched -- no "bash" key is ever introduced, matching §17.2's own
// "defense in depth alongside §17.4's own post-hoc diff-scope check"
// framing (bash stays a gap THIS layer does not close).
func TestMergeSentinelFixAgentConfig_NeverAllowsBashOrOtherPermissions(t *testing.T) {
	out, err := MergeSentinelFixAgentConfig(nil)
	if err != nil {
		t.Fatalf("MergeSentinelFixAgentConfig(nil) error = %v", err)
	}
	if strings.Contains(string(out), `"bash"`) {
		t.Errorf("output mentions \"bash\", want no bash permission override at all: %s", out)
	}
}
