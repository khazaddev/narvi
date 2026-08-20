// attachments.test.ts -- §28's own mint -> transfer -> confirm lifecycle,
// exercised client-side via runUpload's own async orchestration, with a
// fully stubbed UploadDeps (no real network) so this stays a pure Node
// test (vitest.config.ts's own "plain Node, no jsdom" environment).
import { describe, expect, it } from 'vitest'

import type { ConfirmUploadResponse, MintUploadResponse } from '@narvi/contracts/rest-dtos'

import type { Attachment, AttachmentStatusUpdate, UploadDeps } from '../attachments'
import { isAttachmentInFlight, readyAttachmentIds, runUpload } from '../attachments'

function fakeFile(name: string, bytes = 10): File {
  return new File([new Uint8Array(bytes)], name, { type: 'text/plain' })
}

describe('isAttachmentInFlight / readyAttachmentIds', () => {
  function attachment(overrides: Partial<Attachment>): Attachment {
    return { localId: 'a', file: fakeFile('x.txt'), status: 'ready', uploadId: 'u1', errorMessage: null, ...overrides }
  }

  it('minting/uploading/confirming all count as in-flight', () => {
    expect(isAttachmentInFlight(attachment({ status: 'minting' }))).toBe(true)
    expect(isAttachmentInFlight(attachment({ status: 'uploading' }))).toBe(true)
    expect(isAttachmentInFlight(attachment({ status: 'confirming' }))).toBe(true)
  })
  it('ready and failed are terminal, not in-flight', () => {
    expect(isAttachmentInFlight(attachment({ status: 'ready' }))).toBe(false)
    expect(isAttachmentInFlight(attachment({ status: 'failed' }))).toBe(false)
  })

  it('readyAttachmentIds includes only ready attachments with a real uploadId, in order', () => {
    const list = [
      attachment({ localId: 'a', status: 'ready', uploadId: 'u1' }),
      attachment({ localId: 'b', status: 'failed', uploadId: 'u2' }),
      attachment({ localId: 'c', status: 'uploading', uploadId: 'u3' }),
      attachment({ localId: 'd', status: 'ready', uploadId: 'u4' }),
    ]
    expect(readyAttachmentIds(list)).toEqual(['u1', 'u4'])
  })
})

describe('runUpload', () => {
  const mintResponse: MintUploadResponse = { uploadId: 'up-1', putUrl: 'https://storage.invalid/put', headers: { 'Content-Type': 'text/plain' }, expiresAt: '2026-01-01T00:00:00Z' }

  function deps(overrides: Partial<UploadDeps> = {}): UploadDeps {
    return {
      mintUpload: async () => mintResponse,
      confirmUpload: async () => ({ status: 'ready', failureReason: null }) as ConfirmUploadResponse,
      putBytes: async () => {},
      ...overrides,
    }
  }

  it('walks minting -> uploading -> confirming -> ready on the happy path', async () => {
    const updates: AttachmentStatusUpdate[] = []
    await runUpload(fakeFile('spec.pdf'), deps(), (u) => updates.push(u))
    expect(updates.map((u) => u.status)).toEqual(['minting', 'uploading', 'confirming', 'ready'])
    expect(updates[updates.length - 1]).toEqual({ status: 'ready', uploadId: 'up-1' })
  })

  it('mint failure (e.g. the server\'s own size/quota 413) reports failed with the SERVER message verbatim, never invented', async () => {
    const updates: AttachmentStatusUpdate[] = []
    await runUpload(
      fakeFile('huge.bin'),
      deps({ mintUpload: async () => { throw new Error('file exceeds the maximum upload size of 104857600 bytes') } }),
      (u) => updates.push(u),
    )
    expect(updates).toEqual([{ status: 'minting' }, { status: 'failed', uploadId: null, errorMessage: 'file exceeds the maximum upload size of 104857600 bytes' }])
  })

  it('a storage PUT failure reports failed without ever reaching confirm', async () => {
    const updates: AttachmentStatusUpdate[] = []
    let confirmCalled = false
    await runUpload(
      fakeFile('x.txt'),
      deps({
        putBytes: async () => { throw new Error('network error') },
        confirmUpload: async () => { confirmCalled = true; return { status: 'ready', failureReason: null } },
      }),
      (u) => updates.push(u),
    )
    expect(confirmCalled).toBe(false)
    expect(updates.map((u) => u.status)).toEqual(['minting', 'uploading', 'failed'])
    const last = updates[updates.length - 1]
    if (last?.status === 'failed') expect(last.uploadId).toBe('up-1')
  })

  it('a confirm-reported failure (server verification) surfaces the exact failureReason code, not invented prose', async () => {
    const updates: AttachmentStatusUpdate[] = []
    await runUpload(
      fakeFile('x.txt'),
      deps({ confirmUpload: async () => ({ status: 'failed', failureReason: 'verification_failed' }) }),
      (u) => updates.push(u),
    )
    const last = updates[updates.length - 1]
    expect(last?.status).toBe('failed')
    if (last?.status === 'failed') expect(last.errorMessage).toContain('verification_failed')
  })

  it('a confirm CALL failure (network/5xx) reports failed with the thrown message', async () => {
    const updates: AttachmentStatusUpdate[] = []
    await runUpload(
      fakeFile('x.txt'),
      deps({ confirmUpload: async () => { throw new Error('verification temporarily unavailable, please retry') } }),
      (u) => updates.push(u),
    )
    const last = updates[updates.length - 1]
    expect(last?.status).toBe('failed')
    if (last?.status === 'failed') expect(last.errorMessage).toBe('verification temporarily unavailable, please retry')
  })

  it('never throws -- every failure path resolves via onUpdate instead', async () => {
    await expect(runUpload(fakeFile('x.txt'), deps({ mintUpload: async () => { throw new Error('boom') } }), () => {})).resolves.toBeUndefined()
  })
})
