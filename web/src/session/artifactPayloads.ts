// artifactPayloads.ts -- turns one element of GET /api/sessions/:id/
// artifacts's own `ArtifactsResponse.artifacts` (typed only as
// `{[k: string]: unknown}[]` by the generated schema -- restdtos'
// ArtifactsResponse doc comment: "loosely typed like every element in
// this array already was") into a typed ParsedArtifact, the SAME runtime-
// narrowing discipline session/eventPayloads.ts already applies to WS
// event payloads (no type here is redeclared from a real schema -- this
// module adds only the narrowing a loosely-typed wire shape has no
// mechanism of its own to perform client-side).
//
// filename/sizeBytes/contentType (§12.2 item 1's own Go-side fix,
// internal/adapters/inbound/httpapi/artifacts.go's artifactWireMap): these
// three columns exist on every upload row since migration 000060 and are
// populated by every real mint call, but the wire map never surfaced them
// until this Step -- SessionRail.tsx (this module's own consumer) is the
// first real reader of GET .../artifacts, which is what exposed the gap.
import { isPlainObject } from '../ws/util'

export interface ParsedArtifact {
  id: string
  /** 'pr' | 'preview' | 'upload' -- kept as `string`, not a closed union, the same defensive posture eventPayloads.ts applies to every server-owned enum: an unrecognized future type still renders via SessionRail.tsx's own fallback branch rather than being dropped or crashing. */
  type: string
  /** Untrusted (§28.3: pr/preview carry an absolute external link; upload carries this session's own stable relative content path) -- SessionRail.tsx renders this ONLY via urlSafety.ts's isSafeHref, never as a raw href. */
  url: string
  createdAt: string
  /** 'pending' | 'ready' | 'failed' -- defaults to 'ready' when the key is absent, mirroring the wire's own additive convention (§28.6: "absent status means ready"). */
  status: string
  failureReason: string | null
  /** Attacker-supplied (§28: "the filename is attacker-supplied") -- SessionRail.tsx renders this as plain React text content ONLY, never dangerouslySetInnerHTML, never interpolated into a URL or a `download` attribute. Null for a pr/preview row (this column is upload-only). */
  filename: string | null
  sizeBytes: number | null
  contentType: string | null
  /**
   * Freeform, producer-specific (pushpr.go's own {repo, number} for a
   * "pr" row; previewpr.go's own {repo, pr_number, sha} for a "preview"
   * row; always {} for an upload row -- mintUploadCore never sets it).
   * Every value inside is exactly as untrusted as `filename` -- SessionRail.tsx
   * reads individual keys defensively (typeof-checked) and renders them as
   * plain text only, never HTML, never interpolated into a URL.
   */
  metadata: Record<string, unknown>
}

export function parseArtifact(raw: unknown): ParsedArtifact | null {
  if (!isPlainObject(raw)) return null
  const { id, type, url, createdAt, status, failureReason, filename, sizeBytes, contentType, metadata } = raw
  if (typeof id !== 'string' || typeof type !== 'string' || typeof url !== 'string' || typeof createdAt !== 'string') return null
  return {
    id,
    type,
    url,
    createdAt,
    status: typeof status === 'string' ? status : 'ready',
    failureReason: typeof failureReason === 'string' ? failureReason : null,
    filename: typeof filename === 'string' ? filename : null,
    sizeBytes: typeof sizeBytes === 'number' ? sizeBytes : null,
    contentType: typeof contentType === 'string' ? contentType : null,
    metadata: isPlainObject(metadata) ? metadata : {},
  }
}

/** parseArtifacts silently drops (never throws on) any element that doesn't shape up -- a malformed row from the server must not crash the rail, it must just not be trusted as artifact data (the same posture sessionStream.ts's own parseEnvelopes already applies to events). */
export function parseArtifacts(raw: readonly unknown[]): ParsedArtifact[] {
  const out: ParsedArtifact[] = []
  for (const item of raw) {
    const parsed = parseArtifact(item)
    if (parsed !== null) out.push(parsed)
  }
  return out
}
