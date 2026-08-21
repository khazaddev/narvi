// repoSettingsFormat.ts -- pure, testable formatting/derivation helpers for
// RepoSettingsView.tsx, mirroring this codebase's own established split
// (settingsFormat.ts, automationFormat.ts, planFormat.ts, reviewFormat.ts):
// render logic that does not need React lives here, so it can be
// unit-tested without rendering anything.

/** BLAST_RADIUS_TAGS is RepoSettings.sensitiveBlastRadiusTags' own closed vocabulary (contracts/rest/v1/dtos.schema.json), rendered in a fixed, stable order rather than whatever order the server happens to return. */
export const BLAST_RADIUS_TAGS = ['auth', 'migrations', 'contracts', 'secrets', 'infra', 'public_api', 'data_layer', 'dependencies'] as const

export type BlastRadiusTag = (typeof BLAST_RADIUS_TAGS)[number]

/** blastRadiusTagLabel renders one closed-enum tag as operator-facing text -- mirrors AnalyticsView.tsx's own local TAG_LABEL map (not shared: two small, independently-evolving file-local maps over the same 8-value enum is the established precedent here, e.g. TAG_LABEL/STATUS_LABEL in that file). */
export function blastRadiusTagLabel(tag: string): string {
  switch (tag) {
    case 'auth':
      return 'auth'
    case 'migrations':
      return 'migrations'
    case 'contracts':
      return 'contracts'
    case 'secrets':
      return 'secrets'
    case 'infra':
      return 'infra'
    case 'public_api':
      return 'public API'
    case 'data_layer':
      return 'data layer'
    case 'dependencies':
      return 'dependencies'
    default:
      return tag
  }
}

/** REVIEW_DEPTH_MODES is reviewtriage.Mode's own three legal wire values (internal/domain/reviewtriage), matching reviewDepthModeString's own validation in httpapi/reposettings.go. */
export const REVIEW_DEPTH_MODES = ['auto', 'always_light', 'always_deep'] as const

/** reviewDepthModeLabel renders one reviewDepth mode value as operator-facing text. */
export function reviewDepthModeLabel(mode: string): string {
  switch (mode) {
    case 'auto':
      return 'auto (routes each review by its own signals)'
    case 'always_light':
      return 'always light'
    case 'always_deep':
      return 'always deep'
    default:
      return mode
  }
}

/**
 * parseDeepPathsInput turns a textarea's raw text (one glob pattern per
 * line, blank lines ignored) into the wire shape UpdateReviewDepthConfigRequest.deepPaths
 * expects: null for "nothing configured", never an empty array -- mirrors
 * RepoSettings.reviewDepthDeepPaths' own doc comment ("Null means 'no
 * repo-specific deep paths configured'").
 */
export function parseDeepPathsInput(raw: string): string[] | null {
  const lines = raw
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
  return lines.length > 0 ? lines : null
}

/** formatDeepPathsForTextarea is parseDeepPathsInput's own inverse, for populating the textarea from a loaded RepoSettings.reviewDepthDeepPaths. */
export function formatDeepPathsForTextarea(paths: string[] | null): string {
  return paths !== null ? paths.join('\n') : ''
}

/**
 * parseOptionalPositiveInt turns a number input's raw text into
 * UpdateAutoApprovalSettingsRequest.maxAutoApproveFilesChanged's own wire
 * shape: null for blank ("use the engine's own built-in default"), a
 * parsed integer otherwise, or 'invalid' for anything that isn't a
 * non-negative whole number -- callers disable Save on 'invalid' rather
 * than sending it and letting the server 400.
 */
export function parseOptionalPositiveInt(raw: string): number | null | 'invalid' {
  const trimmed = raw.trim()
  if (trimmed.length === 0) return null
  if (!/^\d+$/.test(trimmed)) return 'invalid'
  const n = Number(trimmed)
  return Number.isSafeInteger(n) ? n : 'invalid'
}

/**
 * parseOptionalPositiveUsd mirrors parseOptionalPositiveInt above for the
 * two reviewCostBudget fields -- PutReviewCostBudget's own server-side rule
 * (httpapi/reposettings.go) rejects a zero or negative value with a 400
 * (it would collide with reviewtriage.CostBudget's own "unconfigured"
 * zero-value sentinel and silently mean unlimited spend), so 'invalid'
 * covers zero and negative here too, not just non-numeric text.
 */
export function parseOptionalPositiveUsd(raw: string): number | null | 'invalid' {
  const trimmed = raw.trim()
  if (trimmed.length === 0) return null
  const n = Number(trimmed)
  if (!Number.isFinite(n) || n <= 0) return 'invalid'
  return n
}
