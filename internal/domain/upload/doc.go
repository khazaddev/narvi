// Package upload holds §8.6's ("uploads, blob storage & the in-sandbox
// download_file tool", §28) pure business rules: the object-storage key
// convention (§28.3), the shared size/quota evaluation both mint and
// confirm re-run (§28.4), and the deterministic turn-prompt rendering for
// attachments and the upload tool (§28.5) -- the internal/domain/review's
// RenderTurnPrompt / cmd/sandbox-agent/reviewverdicttoolprompt.go pattern,
// reused for a second tool rather than inventing a second mechanism.
//
// Pure per §11 (CLAUDE.md: "no I/O, time.Now(), or randomness in
// /internal/domain"): no I/O, no time.Now(), no randomness. This package
// deliberately imports only internal/app/ports (for the BlobKey type
// BuildBlobKey produces) and the standard library -- never
// internal/adapters/* (sqlcgen, postgres, ...): domain packages never
// depend on adapters (hexagonal architecture, CLAUDE.md's "don't couple a
// port to a single adapter" applied to the dependency direction itself).
// internal/adapters/outbound/postgres converts between this package's own
// FailureReason and sqlcgen.ArtifactFailureReason at ITS boundary, never
// the other way around.
package upload
