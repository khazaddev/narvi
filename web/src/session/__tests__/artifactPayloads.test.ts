import { describe, expect, it } from 'vitest'

import { parseArtifact, parseArtifacts } from '../artifactPayloads'

describe('parseArtifact', () => {
  it('parses a well-formed upload artifact row, including the §12.2 item 1 Go-side filename/sizeBytes/contentType fix', () => {
    const raw = {
      id: 'a1',
      type: 'upload',
      url: '/api/sessions/s1/uploads/a1/content',
      createdAt: '2026-08-20T10:00:00Z',
      status: 'ready',
      failureReason: null,
      filename: 'spec.pdf',
      sizeBytes: 1024,
      contentType: 'application/pdf',
      metadata: {},
    }
    expect(parseArtifact(raw)).toEqual(raw)
  })

  it('parses a pr artifact row (filename/sizeBytes/contentType absent -- pr rows never set them)', () => {
    const raw = { id: 'a2', type: 'pr', url: 'https://github.example/x/y/pull/12', createdAt: 'x', status: 'ready', failureReason: null, metadata: { repo: 'acme/narvi', number: 1204 } }
    const parsed = parseArtifact(raw)
    expect(parsed).toMatchObject({ id: 'a2', type: 'pr', filename: null, sizeBytes: null, contentType: null })
    expect(parsed?.metadata).toEqual({ repo: 'acme/narvi', number: 1204 })
  })

  it('defaults status to "ready" when the key is absent (the wire\'s own additive convention)', () => {
    const parsed = parseArtifact({ id: 'a3', type: 'pr', url: 'https://x.invalid/y', createdAt: 'x' })
    expect(parsed?.status).toBe('ready')
  })

  it('returns null for a non-object', () => {
    expect(parseArtifact(null)).toBeNull()
    expect(parseArtifact('x')).toBeNull()
  })

  it('returns null when a required field is missing or the wrong type', () => {
    expect(parseArtifact({ type: 'pr', url: 'x', createdAt: 'x' })).toBeNull()
    expect(parseArtifact({ id: 1, type: 'pr', url: 'x', createdAt: 'x' })).toBeNull()
  })
})

describe('parseArtifacts', () => {
  it('drops a malformed element without dropping its valid siblings', () => {
    const rows = [
      { id: 'a1', type: 'pr', url: 'https://x.invalid/1', createdAt: 'x' },
      { not: 'an artifact' },
      { id: 'a2', type: 'preview', url: 'https://x.invalid/2', createdAt: 'x' },
    ]
    expect(parseArtifacts(rows).map((a) => a.id)).toEqual(['a1', 'a2'])
  })
})
