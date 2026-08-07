package modelcatalog

import (
	"fmt"
	"testing"
)

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
// live-verified model entry (§29.1's own "gpt-5.3-codex-spark" example)
// end to end: real reasoning variants, and real, non-zero per-million-
// token cost.
//
// R10 (adversarial review, settling a contested factual question): an
// earlier version of this test asserted Cost.Input/Output == 0, citing
// §29.10 risk 5 ("subscription turns report cost 0"). Re-verified live
// against the exact pinned OpenCode 1.17.15 binary CI installs (a
// clean-config instance, GET /provider): gpt-5.3-codex-spark's own cost
// object is NOT all zeros -- {input: 1.75, output: 14, cache.read: 0.175,
// cache.write: 0} USD per million tokens -- and neither is any of the
// other 18 openai models snapshot.json carries (all 19 were zero-cost
// before this fix; all 19 have real pricing live). anthropic's and
// google's own snapshot entries were independently re-verified the same
// way and found byte-for-byte correct -- this was an openai-only
// discrepancy. See snapshot.json's own git history for the corresponding
// fix to every other openai model's own cost fields.
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
	wantCost := Cost{Input: 1.75, Output: 14, CacheRead: floatPtr(0.175), CacheWrite: floatPtr(0)}
	if codex.Cost.Input != wantCost.Input || codex.Cost.Output != wantCost.Output {
		t.Errorf("gpt-5.3-codex-spark Cost.{Input,Output} = {%v,%v}, want {%v,%v} (live-verified against the pinned binary -- a zero-cost entry for a billed model means cost displays report $0)",
			codex.Cost.Input, codex.Cost.Output, wantCost.Input, wantCost.Output)
	}
	if codex.Cost.CacheRead == nil || *codex.Cost.CacheRead != *wantCost.CacheRead {
		t.Errorf("gpt-5.3-codex-spark Cost.CacheRead = %v, want %v", floatPtrString(codex.Cost.CacheRead), *wantCost.CacheRead)
	}
}

func floatPtr(f float64) *float64 { return &f }

func floatPtrString(f *float64) string {
	if f == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v", *f)
}

// TestCatalog_ReturnsDefensiveCopy proves a caller mutating what Catalog()
// returns -- AT ANY DEPTH, not just the top-level Provider.ID -- can never
// corrupt a LATER caller's own view, nor the package's own process-global
// parsed backing data.
//
// M2 (adversarial review): an earlier version of this test mutated ONLY
// first[0].ID -- the one field a shallow top-level-slice copy already
// protects -- so it was falsely reassuring: Catalog() at the time copied
// only the outer []Provider slice, leaving every nested Models/Variants
// slice and Cost pointer sharing the SAME backing memory as the package's
// own parsed global, un-caught by that narrower assertion. This version
// mutates one field at each nesting level Catalog() actually returns
// (Provider.ID, Model.ID via Models[0], Model.Variants' own backing
// array, Model.Cost.CacheRead's own pointee) and re-derives "second" (and,
// for the process-global check, a THIRD independent Catalog() call) after
// each, so a regression back to a shallow copy at any one of these levels
// fails this test immediately.
func TestCatalog_ReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	first := Catalog()
	if len(first) == 0 || len(first[0].Models) == 0 {
		t.Fatal("Catalog() returned no providers/models, want at least 1 of each")
	}

	first[0].ID = "MUTATED-PROVIDER-ID"
	if second := Catalog(); second[0].ID == "MUTATED-PROVIDER-ID" {
		t.Error("mutating first[0].ID leaked into a later Catalog() call, want an independent copy")
	}

	first[0].Models[0].ID = "MUTATED-MODEL-ID"
	if second := Catalog(); second[0].Models[0].ID == "MUTATED-MODEL-ID" {
		t.Error("mutating first[0].Models[0].ID leaked into a later Catalog() call, want Models to be independently copied too, not just the outer []Provider slice")
	}

	if len(first[0].Models[0].Variants) > 0 {
		originalVariant := first[0].Models[0].Variants[0]
		first[0].Models[0].Variants[0] = "MUTATED-VARIANT"
		second := Catalog()
		if second[0].Models[0].Variants[0] == "MUTATED-VARIANT" {
			t.Error("mutating first[0].Models[0].Variants[0] leaked into a later Catalog() call, want Variants to be independently copied too")
		}
		first[0].Models[0].Variants[0] = originalVariant // leave process-global state clean for every other (parallel) test in this package.
	}

	if first[0].Models[0].Cost.CacheRead != nil {
		originalCacheRead := *first[0].Models[0].Cost.CacheRead
		*first[0].Models[0].Cost.CacheRead = -999
		second := Catalog()
		if *second[0].Models[0].Cost.CacheRead == -999 {
			t.Error("mutating *first[0].Models[0].Cost.CacheRead leaked into a later Catalog() call, want Cost's own pointer fields to be independently copied too")
		}
		*first[0].Models[0].Cost.CacheRead = originalCacheRead
	}
}
