package modelcatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed snapshot.json
var snapshotJSON []byte

// Provider is one catalog provider (e.g. "openai") plus its own models.
type Provider struct {
	ID     string  `json:"id"`
	Models []Model `json:"models"`
}

// Model is one catalog model entry -- the subset of OpenCode's own real
// GET /provider catalog fields (§25.2/§29.1) this Step's catalog exposes.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow int      `json:"contextWindow"`
	ToolCall      bool     `json:"toolCall"`
	Reasoning     bool     `json:"reasoning"`
	Variants      []string `json:"variants"`
	Cost          Cost     `json:"cost"`
}

// Cost is one model's own USD-per-million-token pricing.
type Cost struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cacheRead,omitempty"`
	CacheWrite *float64 `json:"cacheWrite,omitempty"`
}

// snapshot is the embedded file's own top-level shape.
type snapshot struct {
	Providers []Provider `json:"providers"`
}

// parsed is computed once, at package init, from the embedded snapshot --
// this data never changes at runtime (see package doc.go for why: a
// hand-refreshed, compiled-in snapshot, never a live fetch), so parsing
// it once and reusing the result is correct, not merely an optimization.
var parsed = mustParseSnapshot()

func mustParseSnapshot() []Provider {
	var s snapshot
	if err := json.Unmarshal(snapshotJSON, &s); err != nil {
		// A malformed embedded snapshot is a build-time/packaging bug,
		// never a runtime/user-facing condition -- panicking at package
		// init (before any request is ever served) is the correct,
		// loud failure mode, mirroring how a malformed contracts/gen/go
		// file would already fail this same way at compile/link time.
		panic(fmt.Sprintf("modelcatalog: embedded snapshot.json is malformed: %v", err))
	}
	return s.Providers
}

// Catalog returns every provider/model this Step's own embedded snapshot
// carries (§29's own "Catalog" deliverable) -- a DEEP defensive copy:
// every caller gets its own independent Providers slice, own independent
// per-provider Models slice, own independent per-model Variants slice, and
// own independent Cost.CacheRead/CacheWrite pointers, so mutating anything
// any caller gets back -- at any depth -- can never corrupt the package's
// own parsed backing data or any OTHER caller's own already-returned copy.
//
// M2 (adversarial review): an earlier version of this copied only the
// top-level []Provider slice, the same shallow shape
// providercredential.EnvVarNames uses for its own defensive copy -- correct
// THERE because EnvVarNames returns a flat []string with no nested
// reference types to alias, but NOT here: Provider/Model both nest slices
// (Models, Variants) and Cost nests two pointers, none of which a
// top-level-only copy protects. Verified experimentally: mutating
// first[0].Models[0].ID under that old shape corrupted the process-global
// parsed for every later caller.
func Catalog() []Provider {
	out := make([]Provider, len(parsed))
	for i, p := range parsed {
		out[i] = Provider{ID: p.ID, Models: make([]Model, len(p.Models))}
		for j, m := range p.Models {
			out[i].Models[j] = copyModel(m)
		}
	}
	return out
}

// copyModel deep-copies m's own nested reference fields (Variants, and
// Cost's two optional pointers) -- see Catalog's own doc comment for why
// a plain `cp := m` alone is not enough.
func copyModel(m Model) Model {
	cp := m
	cp.Variants = append([]string(nil), m.Variants...)
	if m.Cost.CacheRead != nil {
		v := *m.Cost.CacheRead
		cp.Cost.CacheRead = &v
	}
	if m.Cost.CacheWrite != nil {
		v := *m.Cost.CacheWrite
		cp.Cost.CacheWrite = &v
	}
	return cp
}
