package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/sandboxagent/opencodeproc"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// realAgentEntry is this file's own decode target for GET /agent's real
// response shape -- mirrors sentinelfixagent_realbinary_test.go's own
// identical anonymous-struct shape, promoted to a named type here since
// THIS file's own doc comment (reviewsubagents.go) additionally checks
// "model" -- a field that test never needed to decode.
type realAgentEntry struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	Native     bool   `json:"native"`
	Permission []struct {
		Permission string `json:"permission"`
		Pattern    string `json:"pattern"`
		Action     string `json:"action"`
	} `json:"permission"`
	Model *struct {
		ModelID    string `json:"modelID"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
}

// TestReviewSubAgents_RegisterAgainstRealPinnedBinary is this Step's own
// checked-in proof of the review sub-agent registration mechanism's
// CONFIGURATION half (reviewsubagents.go's own top doc comment explains
// exactly what this test does and does not prove, and why, mirroring
// TestSentinelFixAgent_RegistersAgainstRealPinnedBinary's own identical
// precedent immediately above in this package): write the config this
// package generates -- WITH a real counter-reviewer model override, so
// this test also proves finding 2 of reviewsubagents.go's own top doc
// comment (a "provider/model" string round-trips through the real
// server's own GET /agent as a decomposed {modelID, providerID} object)
// -- into a fresh workspace, start the REAL pinned OpenCode binary
// against it, and assert GET /agent reflects all three agents with
// exactly the configured mode/permission/tools/model. No AI-provider
// credential or model call involved (the "model" field here is
// CONFIGURATION the real server's own config loader parses and echoes
// back, never a live call to that provider) -- not gated behind
// skipIfNoProvider, costs nothing to run in CI without one configured.
func TestReviewSubAgents_RegisterAgainstRealPinnedBinary(t *testing.T) {
	dir := t.TempDir()

	const wantCounterReviewerModel = "anthropic/claude-opus-4-5"
	configJSON, err := MergeReviewSubAgentsConfig(nil, wantCounterReviewerModel)
	if err != nil {
		t.Fatalf("MergeReviewSubAgentsConfig() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), configJSON, 0o644); err != nil {
		t.Fatalf("write opencode.json: %v", err)
	}

	sup := supervisor.New()
	ctx, cancel := context.WithTimeout(context.Background(), testReadinessTimeout)
	defer cancel()
	// Known, honest gap -- see helpers_test.go's startServer (this same
	// package), the canonical fuller explanation: this t.Cleanup never
	// runs at all if the TEST BINARY itself is killed abruptly (SIGKILL,
	// Ctrl-C's default SIGINT, `go test -timeout` firing) rather than
	// exiting normally -- not something this function itself can fix.
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testReadinessTimeout)
		defer stopCancel()
		_ = sup.StopAll(stopCtx, testReadinessPollInterval)
	})

	result, err := opencodeproc.Spawn(ctx, sup, dir, nil, nil, testReadinessTimeout, testReadinessPollInterval)
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

	var agents []realAgentEntry
	if err := json.Unmarshal(body, &agents); err != nil {
		t.Fatalf("decode GET /agent response: %v (body: %s)", err, body)
	}

	byName := map[string]*realAgentEntry{}
	for i := range agents {
		byName[agents[i].Name] = &agents[i]
	}

	for _, name := range []string{review.ArchitectureScribeAgentName, review.CounterReviewerAgentName, review.FactCheckAgentName} {
		found, ok := byName[name]
		if !ok {
			t.Fatalf("real opencode server's own GET /agent does not list %q -- full response: %s", name, body)
		}
		if found.Native {
			t.Errorf("agent %q reported native=true, want false (a custom agent)", name)
		}
		if found.Mode != "subagent" {
			t.Errorf("agent %q mode = %q, want %q", name, found.Mode, "subagent")
		}

		editDeny := false
		for _, p := range found.Permission {
			if p.Permission == "edit" && p.Pattern == "*" && p.Action == "deny" {
				editDeny = true
			}
		}
		if !editDeny {
			t.Errorf("agent %q: real server's own permission list has no edit[*]=deny entry: %+v", name, found.Permission)
		}
	}

	// architecture-scribe: no model override.
	if m := byName[review.ArchitectureScribeAgentName].Model; m != nil {
		t.Errorf("architecture-scribe model = %+v, want nil (no override)", m)
	}

	// counter-reviewer: the configured override, round-tripped through the
	// real server's own config loader as a decomposed {modelID,providerID}
	// object -- this Step's own finding 2 (reviewsubagents.go's top doc
	// comment), pinned end to end here.
	crModel := byName[review.CounterReviewerAgentName].Model
	if crModel == nil {
		t.Fatalf("counter-reviewer model = nil, want %q", wantCounterReviewerModel)
	}
	if crModel.ProviderID != "anthropic" || crModel.ModelID != "claude-opus-4-5" {
		t.Errorf("counter-reviewer model = {providerID:%q modelID:%q}, want {providerID:%q modelID:%q}", crModel.ProviderID, crModel.ModelID, "anthropic", "claude-opus-4-5")
	}

	// fact-check: no tool access -- every non-edit tool this Step's own
	// noToolAccess map names must show up as its own deny entry too
	// (this Step's own finding 3, reviewsubagents.go's top doc comment).
	factCheck := byName[review.FactCheckAgentName]
	denied := map[string]bool{}
	for _, p := range factCheck.Permission {
		if p.Pattern == "*" && p.Action == "deny" {
			denied[p.Permission] = true
		}
	}
	for tool := range noToolAccess {
		if !denied[tool] {
			t.Errorf("fact-check: real server's own permission list has no %s[*]=deny entry: %+v", tool, factCheck.Permission)
		}
	}
}
