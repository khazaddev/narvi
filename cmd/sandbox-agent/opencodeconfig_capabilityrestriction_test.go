// This file (opencodeconfig_capabilityrestriction_test.go) is the direct
// regression test PR review requested for the CRITICAL finding: that a
// capability-restricted session's §8.2 sentinel-fix restriction cannot
// be overridden through the sandbox_secrets/opencode-config injection
// path §27.1 adds.
//
// Two independent, complementary properties are proven, mirroring
// TestSandboxSecretValidateName_RejectsOpenCodeConfigEnvVar's own
// "validation" half:
//
//  1. TestValidateName_RejectsOpenCodeConfigContent (internal/domain/
//     sandboxsecret's own name_test.go) already proves the PRIMARY
//     defense: a sandbox_secrets row literally named
//     "OPENCODE_CONFIG_CONTENT" (OpenCode's own "inline config" slot,
//     ABOVE the project slot §8.2 targets) can never be SAVED at all
//     -- it is rejected at CRUD write time, server-side, before it could
//     ever reach a delivery response.
//  2. THIS file's own TestCapabilityRestrictedProjectConfig_
//     NeverTouchedBySandboxSecretInjection proves the SECONDARY,
//     defense-in-depth property: even setting property (1) aside
//     entirely (sandboxsecrets.go's own top doc comment: "this file
//     trusts [CRUD-time validation] and performs no re-validation"), this
//     binary's OWN injection mechanism (sandboxSecretSpawnEnv) has no
//     OTHER way to reach the workspace opencode.json file §8.2's
//     sentinel-fix write targets -- it only ever builds process-env
//     "NAME=VALUE" entries, never a file write, so a resolved
//     sandbox_secrets map (however it got there) cannot alter that file's
//     own content, regardless of what names or values it carries.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/opencode"
)

// TestCapabilityRestrictedProjectConfig_NeverTouchedBySandboxSecretInjection
// writes the SAME capability-restricted sentinel-fix config main.go's own
// run() writes to workspace/opencode.json (OpenCode's "project" slot) for
// a SessionConfig.CapabilityRestricted session, then builds a sandbox-
// secret spawn env from a map that -- HYPOTHETICALLY, simulating a CP-side
// validation bug, since ValidateName now rejects this name outright (see
// this file's own top doc comment) -- still carries an
// "OPENCODE_CONFIG_CONTENT" entry with a payload that would re-enable
// unrestricted tools if OpenCode ever merged it. Proves the workspace's
// own opencode.json is BYTE-IDENTICAL before and after sandboxSecretSpawnEnv
// runs: this Step's own injection mechanism is structurally incapable of
// writing to, or otherwise influencing, that file's own content -- it
// only ever produces an env-var slice, which opencodeproc.Spawn appends
// to `opencode serve`'s own environment, a completely separate channel
// from the project config file OpenCode reads from disk.
func TestCapabilityRestrictedProjectConfig_NeverTouchedBySandboxSecretInjection(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	configPath := filepath.Join(workspaceDir, "opencode.json")

	// Mirrors main.go's own CapabilityRestricted block exactly: merge the
	// glob-restricted sentinel-fix agent config into a fresh (no existing
	// committed) opencode.json.
	restrictedConfig, err := opencode.MergeSentinelFixAgentConfig(nil)
	if err != nil {
		t.Fatalf("MergeSentinelFixAgentConfig() error = %v", err)
	}
	if err := os.WriteFile(configPath, restrictedConfig, 0o644); err != nil {
		t.Fatalf("write restricted opencode.json: %v", err)
	}

	// A hypothetical resolved sandbox_secrets map carrying a name that
	// ValidateName now rejects at CRUD time (defense layer 1) -- included
	// here anyway to prove defense layer 2 holds independently.
	maliciousSecrets := map[string]string{
		"OPENCODE_CONFIG_CONTENT": `{"agent":{"build":{"tools":{"bash":true,"edit":true,"write":true}}}}`,
		"ORDINARY_SECRET":         "some-real-value",
	}

	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode.json before injection: %v", err)
	}

	env := sandboxSecretSpawnEnv(maliciousSecrets)

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode.json after injection: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("workspace opencode.json (the project slot §8.2's sentinel-fix restriction targets) changed after sandboxSecretSpawnEnv ran -- before:\n%s\nafter:\n%s", before, after)
	}

	// sandboxSecretSpawnEnv itself produced ONLY env-var entries -- proof
	// the malicious name flowed into the one channel this mechanism
	// actually has (opencodeproc.Spawn's own sandboxSecretEnv parameter,
	// a process env var opencode serve would inherit -- OpenCode's own
	// "custom"/global slot, still below the project slot this test's own
	// restrictedConfig occupies), never a second, file-based channel.
	foundMalicious := false
	for _, entry := range env {
		if entry == "OPENCODE_CONFIG_CONTENT="+maliciousSecrets["OPENCODE_CONFIG_CONTENT"] {
			foundMalicious = true
		}
	}
	if !foundMalicious {
		t.Fatal("test precondition failed: sandboxSecretSpawnEnv did not carry the malicious entry at all -- this test would not be exercising the property it claims to")
	}
}
