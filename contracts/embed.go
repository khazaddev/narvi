// Package contracts embeds every versioned JSON Schema under this directory
// into the compiled binary (mirrors migrations/embed.go, PR-04) — so the
// eventual single-binary self-host story (§12.1) and this package's own
// round-trip contract tests (contracts/contractstest) never need to read
// schema files off disk by path.
package contracts

import "embed"

// FS embeds every *.schema.json file under sandbox-ws/, client-ws/,
// session-config/, and rest/ (all versioned v1/ subdirectories today, §6).
// It deliberately does NOT embed gen/ (generated code, not schema source)
// or the npm project under this directory.
//
//go:embed sandbox-ws/v1/*.schema.json client-ws/v1/*.schema.json session-config/v1/*.schema.json rest/v1/*.schema.json
var FS embed.FS
