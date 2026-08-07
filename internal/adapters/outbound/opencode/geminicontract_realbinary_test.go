package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// This file implements §25.2's own required Gemini CI contract test:
// "§25.2 resolved that no new adapter is needed, but explicitly requires
// a contract test against the real pinned OpenCode binary verifying
// Gemini's actual tool-calling/streaming behaviour -- because every
// existing contract test is Claude-backed and proves nothing
// transitively about another provider." Split into two, mirroring this
// package's own established "what needs a real binary vs. what needs a
// real credentialed provider too" split (helpers_test.go's own
// skipIfNoProvider precedent) -- both live-verified/scoped during this
// Step's own implementation research, not guessed:
//
//  1. TestGeminiContract_RealBinary_CatalogShape (below): a REAL,
//     unconditional, always-runs-in-CI assertion against the pinned
//     OpenCode 1.17.15 binary's own GET /provider catalog -- no AI-
//     provider credential needed at all, since a catalog listing is
//     static metadata OpenCode serves regardless of what's configured.
//     This is the SAME class of "no credential needed" test
//     TestSentinelFixAgent_RegistersAgainstRealPinnedBinary already
//     establishes in this package. It genuinely proves the google
//     provider still exists, lists real Gemini models, and reports
//     tool-calling support -- §25.2's own live-verified finding, re-
//     verified here as a permanent regression guard.
//  2. TestGeminiContract_RealTurn_ToolCallingAndStreaming (below): the
//     ACTUAL tool-calling/streaming turn §25.2 asks for. This genuinely
//     needs a real, working GOOGLE_API_KEY/GEMINI_API_KEY credential --
//     this Step's own hard constraint is explicit that no such credential
//     exists in this environment. Structured exactly like this
//     codebase's own accepted precedent for exactly this situation,
//     internal/adapters/outbound/rwx/provider_realbinary_test.go: an
//     unconditional, documented t.Skip naming precisely what this test
//     would verify once a real credential is available in CI, never a
//     faked pass.

// providerCatalogEntryForTest is the subset of GET /provider's own "all"
// entry shape this test reads -- Models is a MAP keyed by model id
// (verified live during this Step's own research: GET /provider's own
// "all" entries carry models as an object, not an array) -- decoded as
// map[string]json.RawMessage so this test reads exactly the capabilities
// it needs without modeling the catalog's full, large per-model shape.
type providerCatalogEntryForTest struct {
	ID     string                     `json:"id"`
	Env    []string                   `json:"env"`
	Models map[string]json.RawMessage `json:"models"`
}

// TestGeminiContract_RealBinary_CatalogShape proves the pinned OpenCode
// binary's own GET /provider catalog still lists a real, tool-calling-
// capable Gemini model under the "google" provider -- §25.2's own live
// finding ("a google provider ... 41 real Gemini models ... gemini-3.5-
// flash checked directly: capabilities.toolcall: true"), re-verified here
// as a permanent CI regression guard: an OpenCode version bump that drops
// the google provider, renames its own env vars, or stops reporting
// tool-calling support must fail CI, not silently ship.
func TestGeminiContract_RealBinary_CatalogShape(t *testing.T) {
	baseURL := startServer(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/provider", nil)
	if err != nil {
		t.Fatalf("build GET /provider request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /provider: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /provider status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var parsed struct {
		All []providerCatalogEntryForTest `json:"all"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode GET /provider response: %v", err)
	}

	var google *providerCatalogEntryForTest
	for i := range parsed.All {
		if parsed.All[i].ID == "google" {
			google = &parsed.All[i]
			break
		}
	}
	if google == nil {
		t.Fatal(`GET /provider's own "all" list has no "google" provider entry`)
	}

	wantEnvVar := map[string]bool{"GOOGLE_API_KEY": false, "GOOGLE_GENERATIVE_AI_API_KEY": false, "GEMINI_API_KEY": false}
	for _, e := range google.Env {
		if _, ok := wantEnvVar[e]; ok {
			wantEnvVar[e] = true
		}
	}
	for envVar, found := range wantEnvVar {
		if !found {
			t.Errorf("google provider's own env var list = %v, want it to include %q (internal/domain/providercredential.EnvVarNames's own mapping for \"google\")", google.Env, envVar)
		}
	}

	if len(google.Models) == 0 {
		t.Fatal("google provider carries zero models, want real Gemini models")
	}

	var foundToolCallingModel bool
	for modelID, raw := range google.Models {
		var m struct {
			Capabilities struct {
				ToolCall bool `json:"toolcall"`
			} `json:"capabilities"`
			Limit struct {
				Context int `json:"context"`
			} `json:"limit"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode google model %q: %v", modelID, err)
		}
		if m.Capabilities.ToolCall && m.Limit.Context > 0 {
			foundToolCallingModel = true
			break
		}
	}
	if !foundToolCallingModel {
		t.Error("google provider has no model reporting capabilities.toolcall=true with a real context window -- §25.2's own \"gemini-3.5-flash ... capabilities.toolcall: true\" finding may have regressed")
	}
}

// TestGeminiContract_RealTurn_ToolCallingAndStreaming is §25.2's own
// still-genuinely-unverified half: an ACTUAL scripted turn through a real
// Gemini model, asserting real token/tool_call/tool_result/
// execution_complete events stream correctly through this adapter --
// proving Gemini's own tool-calling/streaming behavior through OpenCode,
// not just that its catalog entry claims support for it. This needs a
// real, working GOOGLE_API_KEY or GEMINI_API_KEY -- this Step's own hard
// constraint ("no real ChatGPT/OpenAI/Gemini credentials exist in this
// environment") makes that genuinely impossible to provide honestly here.
func TestGeminiContract_RealTurn_ToolCallingAndStreaming(t *testing.T) {
	t.Skip("no real GOOGLE_API_KEY/GEMINI_API_KEY credential is available in this environment/CI yet " +
		"(deliberate, per this Step's own hard constraint -- see this file's own top comment for exactly " +
		"what this test would verify: a real scripted turn against a real Gemini model, asserting genuine " +
		"token/tool_call/tool_result/execution_complete events stream correctly through this adapter, mirroring " +
		"realturn_test.go's own TestRealTurn_ToolInvokingPrompt shape but pinned to a google/gemini-* model " +
		"instead of the default Claude one). Configure a real GOOGLE_API_KEY or GEMINI_API_KEY in CI (e.g. via " +
		"OpenCode's own auth store, mirroring how an Anthropic/OpenAI key would be configured for realturn_test.go), " +
		"then replace this Skip with a real scripted turn asserting the events named above -- see this codebase's " +
		"own accepted precedent for exactly this situation, internal/adapters/outbound/rwx/provider_realbinary_test.go.")
}
