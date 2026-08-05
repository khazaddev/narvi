package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/khazaddev/narvi/internal/sandboxagent/opencodeproc"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// TestSentinelFixAgent_RegistersAgainstRealPinnedBinary is this Step's own
// checked-in proof of the capability-restriction mechanism's
// CONFIGURATION half (sentinelfixagent.go's own top doc comment explains
// exactly what this test does and does not prove, and why): write the
// config this package generates into a fresh workspace, start the REAL
// pinned OpenCode binary against it (mirroring this package's own
// established startServer(t) real-binary convention, helpers_test.go),
// and assert GET /agent reflects the "sentinel-fix" agent with EXACTLY
// the configured test/doc allow-list plus a wildcard deny for "edit" --
// no AI-provider credential or model call involved, so this test is not
// gated behind skipIfNoProvider and costs nothing to run in CI without
// one configured (unlike realturn_test.go's own real-model tests).
func TestSentinelFixAgent_RegistersAgainstRealPinnedBinary(t *testing.T) {
	dir := t.TempDir()

	configJSON, err := MergeSentinelFixAgentConfig(nil)
	if err != nil {
		t.Fatalf("MergeSentinelFixAgentConfig() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), configJSON, 0o644); err != nil {
		t.Fatalf("write opencode.json: %v", err)
	}

	sup := supervisor.New()
	ctx, cancel := context.WithTimeout(context.Background(), testReadinessTimeout)
	defer cancel()
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testReadinessTimeout)
		defer stopCancel()
		_ = sup.StopAll(stopCtx, testReadinessPollInterval)
	})

	result, err := opencodeproc.Spawn(ctx, sup, dir, nil, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("opencodeproc.Spawn() error = %v (is the real opencode binary on PATH?)", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, result.BaseURL+"/agent", nil)
	if err != nil {
		t.Fatalf("build GET /agent request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /agent: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /agent status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET /agent response: %v", err)
	}

	var agents []struct {
		Name       string `json:"name"`
		Mode       string `json:"mode"`
		Native     bool   `json:"native"`
		Permission []struct {
			Permission string `json:"permission"`
			Pattern    string `json:"pattern"`
			Action     string `json:"action"`
		} `json:"permission"`
	}
	if err := json.Unmarshal(body, &agents); err != nil {
		t.Fatalf("decode GET /agent response: %v (body: %s)", err, body)
	}

	var found *struct {
		Name       string `json:"name"`
		Mode       string `json:"mode"`
		Native     bool   `json:"native"`
		Permission []struct {
			Permission string `json:"permission"`
			Pattern    string `json:"pattern"`
			Action     string `json:"action"`
		} `json:"permission"`
	}
	for i := range agents {
		if agents[i].Name == sentinelFixAgentName {
			found = &agents[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("real opencode server's own GET /agent does not list %q -- full response: %s", sentinelFixAgentName, body)
	}
	if found.Native {
		t.Errorf("agent %q reported native=true, want false (a custom agent)", sentinelFixAgentName)
	}

	editEntries := map[string]string{}
	for _, p := range found.Permission {
		if p.Permission == "edit" {
			editEntries[p.Pattern] = p.Action
		}
	}
	if editEntries["*"] != "deny" {
		t.Errorf(`real server's own edit["*"] = %q, want "deny"`, editEntries["*"])
	}
	for _, glob := range sentinelFixAllowedEditGlobs {
		if editEntries[glob] != "allow" {
			t.Errorf("real server's own edit[%q] = %q, want \"allow\"", glob, editEntries[glob])
		}
	}
}
