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
// carries (§29's own "Catalog" deliverable) -- a defensive copy of the
// top-level slice (callers may freely append to what they get back;
// the package's own parsed backing array must never be mutated out from
// under every other caller), mirroring providercredential.EnvVarNames'
// own identical "defensive copy" precedent.
func Catalog() []Provider {
	out := make([]Provider, len(parsed))
	copy(out, parsed)
	return out
}
