// attachments.ts -- the composer's own file-attachment flow (§12.2:
// "file attachment on the composer itself (ActionUploadToSession, §28) --
// an upload belongs where the prompt is written"). §28.4's real lifecycle
// is mint -> transfer -> confirm, verified server-side; this module is the
// CLIENT's own state machine over that same lifecycle, plus the async
// orchestration that drives one attachment through it.
//
// Two upload endpoints exist for the SAME lifecycle (§28.5): a
// sandbox-bearer variant for the agent-produced direction, and this
// module's own browser (/api) variant for the direction §12.2 actually
// asks for -- a human attaching a file to their own prompt.
import type { ConfirmUploadResponse, MintUploadResponse } from '@narvi/contracts/rest-dtos'

export type AttachmentStatus = 'minting' | 'uploading' | 'confirming' | 'ready' | 'failed'

export interface Attachment {
  /** Client-generated, stable React key -- assigned the instant a file is picked, independent of uploadId (which does not exist until mint succeeds), so a mint failure still has something to key the (now-failed) chip on. */
  localId: string
  file: File
  status: AttachmentStatus
  uploadId: string | null
  /** Set only when status is 'failed' -- the server's own exact message where one exists (mint's 4xx body, or confirm's typed failureReason), never an invented phrasing (this Step's own "mirror the message, do not invent one" rule). */
  errorMessage: string | null
}

/** isAttachmentInFlight reports whether `a` is still somewhere in mint/PUT/confirm -- the composer's own can-submit predicate (canSubmit.ts) blocks Send while any attachment is in this state, so an attachmentIds list is never sent with a gap where an in-flight id belongs. */
export function isAttachmentInFlight(a: Attachment): boolean {
  return a.status === 'minting' || a.status === 'uploading' || a.status === 'confirming'
}

/** readyAttachmentIds returns the uploadId of every 'ready' attachment, in list order -- exactly the array CreateTurnRequest.attachmentIds expects. A 'failed' or in-flight attachment contributes nothing (silently, by design -- the composer surfaces its own failed chip separately; sending must never block on, or wait for, a user to explicitly dismiss it). */
export function readyAttachmentIds(attachments: readonly Attachment[]): string[] {
  const ids: string[] = []
  for (const a of attachments) {
    if (a.status === 'ready' && a.uploadId !== null) ids.push(a.uploadId)
  }
  return ids
}

// FAILURE_REASON_LABEL -- confirm's own typed failureReason values
// (§28.4/§28.6), shown verbatim rather than translated into new invented
// prose: the exact enum value IS the server's own message here (confirm's
// response carries no free-text explanation at all, only this code), so
// echoing it verbatim is the honest choice, not a shortcut.
function describeConfirmFailure(reason: ConfirmUploadResponse['failureReason']): string {
  return `Upload failed: ${reason ?? 'verification_failed'}`
}

function messageFromError(err: unknown): string {
  // ApiError's own .message IS the server's exact structured error text
  // (http.ts's own request<T>: body.error, verbatim) for every REST 4xx
  // this module's two calls can produce -- mint's own size/quota-exceeded
  // message (uploadmint.go's mintLimitMessage) included. Never rephrased.
  if (err instanceof Error) return err.message
  return 'Upload failed.'
}

export interface UploadDeps {
  mintUpload: (body: { filename: string; contentType: string; sizeBytes: number }) => Promise<MintUploadResponse>
  confirmUpload: (uploadId: string) => Promise<ConfirmUploadResponse>
  /** putBytes PUTs file to url with EXACTLY the headers mint returned (§28.4: "the headers the uploader must send... for the signature to verify") -- a separate seam so tests can stub the cross-origin storage PUT without a real network call. */
  putBytes: (url: string, headers: Record<string, string>, file: File) => Promise<void>
}

export type AttachmentStatusUpdate = { status: 'minting' } | { status: 'uploading'; uploadId: string } | { status: 'confirming'; uploadId: string } | { status: 'ready'; uploadId: string } | { status: 'failed'; uploadId: string | null; errorMessage: string }

/**
 * runUpload drives ONE file through mint -> PUT -> confirm, reporting each
 * transition via onUpdate as it happens (the caller folds these into its
 * own Attachment state, keyed by localId -- this function is deliberately
 * unaware of localId/React state, so it stays plain-data testable). Never
 * throws: every failure path resolves to a 'failed' update instead,
 * carrying the real server message where the server supplied one.
 */
export async function runUpload(file: File, deps: UploadDeps, onUpdate: (update: AttachmentStatusUpdate) => void): Promise<void> {
  onUpdate({ status: 'minting' })
  let mint: MintUploadResponse
  try {
    mint = await deps.mintUpload({ filename: file.name, contentType: file.type || 'application/octet-stream', sizeBytes: file.size })
  } catch (err) {
    onUpdate({ status: 'failed', uploadId: null, errorMessage: messageFromError(err) })
    return
  }

  onUpdate({ status: 'uploading', uploadId: mint.uploadId })
  try {
    await deps.putBytes(mint.putUrl, mint.headers, file)
  } catch {
    // The storage endpoint's own failure body is not this codebase's to
    // parse or trust (a third-party S3-compatible API, §28.2) -- an
    // honest, generic message here, never an invented specific cause.
    onUpdate({ status: 'failed', uploadId: mint.uploadId, errorMessage: 'Upload to storage failed. Try again.' })
    return
  }

  onUpdate({ status: 'confirming', uploadId: mint.uploadId })
  let confirmed: ConfirmUploadResponse
  try {
    confirmed = await deps.confirmUpload(mint.uploadId)
  } catch (err) {
    onUpdate({ status: 'failed', uploadId: mint.uploadId, errorMessage: messageFromError(err) })
    return
  }

  if (confirmed.status === 'ready') {
    onUpdate({ status: 'ready', uploadId: mint.uploadId })
  } else {
    onUpdate({ status: 'failed', uploadId: mint.uploadId, errorMessage: describeConfirmFailure(confirmed.failureReason) })
  }
}

/** putFileToStorage is the real (non-test) UploadDeps.putBytes: a direct, credential-less PUT to the presigned URL (§28.2: "bytes move directly between client and storage, the CP never proxies a payload"). credentials 'omit' is deliberate, not the fetch default -- the storage origin is never this app's own origin, and this codebase's own cookie must never be sent to it. */
export async function putFileToStorage(url: string, headers: Record<string, string>, file: File): Promise<void> {
  const response = await fetch(url, { method: 'PUT', headers, body: file, credentials: 'omit' })
  if (!response.ok) {
    throw new Error(`storage PUT failed: ${response.status}`)
  }
}
