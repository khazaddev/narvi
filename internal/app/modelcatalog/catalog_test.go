package modelcatalog

import "testing"

// TestCatalog_HasExpectedProviders proves the embedded snapshot parses and
// carries the 3 providers Step 53 already wires credential injection for,
// each with a real, non-empty model list.
func TestCatalog_HasExpectedProviders(t *testing.T) {
	t.Parallel()

	got := Catalog()
	byID := make(map[string]Provider, len(got))
	for _, p := range got {
		byID[p.ID] = p
	}

	for _, want := range []string{"openai", "anthropic", "google"} {
		p, ok := byID[want]
		if !ok {
			t.Errorf("Catalog() has no provider %q", want)
			continue
		}
		if len(p.Models) == 0 {
			t.Errorf("Catalog() provider %q has zero models", want)
		}
	}
}

// TestCatalog_CodexModelHasExpectedShape spot-checks one real,
// live-verified model entry (§29.1's own "gpt-5.3-codex-spark" example,
// live-verified during this Step's own research) end to end: real
// reasoning variants, zero cost (§29.10 risk 5: "subscription turns
// report cost 0").
func TestCatalog_CodexModelHasExpectedShape(t *testing.T) {
	t.Parallel()

	var codex *Model
	for _, p := range Catalog() {
		if p.ID != "openai" {
			continue
		}
		for i := range p.Models {
			if p.Models[i].ID == "gpt-5.3-codex-spark" {
				codex = &p.Models[i]
			}
		}
	}
	if codex == nil {
		t.Fatal(`Catalog() has no openai model "gpt-5.3-codex-spark"`)
	}
	if !codex.ToolCall {
		t.Error("gpt-5.3-codex-spark ToolCall = false, want true")
	}
	if !codex.Reasoning {
		t.Error("gpt-5.3-codex-spark Reasoning = false, want true")
	}
	if len(codex.Variants) == 0 {
		t.Error("gpt-5.3-codex-spark Variants is empty, want a real reasoning-effort list")
	}
	if codex.Cost.Input != 0 || codex.Cost.Output != 0 {
		t.Errorf("gpt-5.3-codex-spark Cost = %+v, want zero (§29.10 risk 5: subscription turns report cost 0)", codex.Cost)
	}
}

// TestCatalog_ReturnsDefensiveCopy proves a caller mutating what Catalog()
// returns can never corrupt a LATER caller's own view.
func TestCatalog_ReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	first := Catalog()
	if len(first) == 0 {
		t.Fatal("Catalog() returned empty, want at least 1 provider")
	}
	first[0].ID = "MUTATED"

	second := Catalog()
	if second[0].ID == "MUTATED" {
		t.Error("Catalog() second call reflects a mutation made to the first call's own returned slice, want an independent copy")
	}
}
