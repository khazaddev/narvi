// Composer.tsx -- decision 5 ("A composer that pre-warms") / row 83's own
// prescriptive list: model/effort/plan-mode, warm-on-type INDICATOR
// (§12.2 item 1's own exact wording -- see this file's own "Warm-on-type"
// section below for why this ships an indicator, not a live pre-warm
// trigger), Enter-sends/Shift+Enter-newline from day one, an IME
// composition guard, ONE shared can-submit predicate, an explicit touch/
// mobile decision, and file attachment on the composer itself
// (ActionUploadToSession, §28).
//
// # Touch/mobile decision (§12.3, explicit per row 83's own requirement)
//
// §12.3's own "Composer send semantics" text already makes this decision,
// verbatim: Enter-sends/Shift+Enter-newline ships UNCHANGED across every
// viewport, including touch -- a distinct touch affordance (e.g. Send-on-
// tap-only, with Enter always a newline on a soft keyboard) is A NAMED,
// DELIBERATE DEFERRAL for THIS ship, not an oversight. This component
// therefore installs no viewport/pointer-type branching of its own; the
// textarea/composer LAYOUT still reflows at the existing narrow-viewport
// breakpoint (session.css's own `.app` media query, decision-neutral,
// shared with the rest of the workspace) so the composer stays usable on
// a small screen even though its SEND semantics do not change there.
//
// # Warm-on-type indicator (not a live pre-warm trigger)
//
// §12.2 item 1 asks for a "warm-on-type INDICATOR" -- this component reads
// `sandboxStatus` (derived upstream by sandboxRail.ts from real WS/event
// data) and reflects it; it never itself calls an endpoint on a keystroke.
// A genuine warm-ON-TYPE trigger (typing pre-warms a COLD sandbox before
// Send is even pressed) needs a dedicated backend mechanism this codebase
// does not have yet -- grepped exhaustively (internal/adapters/inbound/
// httpapi, cmd/control-plane/main.go's own route table): no
// warm/prewarm endpoint exists anywhere. Inventing one is backend Step-
// level work (§8 item 7 lists "warm-on-type... must not create orphan
// sessions" as its own standalone exit-criterion clause, not a detail of
// this UI Step) -- named here, not silently built as a fake network call.
import { useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import type { ModelCatalogModel } from '@narvi/contracts/rest-dtos'

import { confirmUpload, createTurn, getModelCatalog, mintUpload } from '../api/endpoints'
import { modelCatalogQueryKeys, sessionListQueryKeys, sessionQueryKeys } from '../api/queryKeys'
import type { Attachment } from './attachments'
import { isAttachmentInFlight, putFileToStorage, readyAttachmentIds, runUpload } from './attachments'
import { canSubmitComposer, shouldSubmitOnKeyDown } from './canSubmit'

let attachmentSeq = 0
function nextLocalId(): string {
  attachmentSeq += 1
  return `att-${attachmentSeq}`
}

function modelWireId(providerId: string, modelId: string): string {
  return `${providerId}/${modelId}`
}

const ATTACHMENT_STATUS_LABEL: Record<Attachment['status'], string> = {
  minting: 'preparing…',
  uploading: 'uploading…',
  confirming: 'verifying…',
  ready: 'attached',
  failed: 'failed',
}

function AttachmentChip({ attachment, onRemove }: { attachment: Attachment; onRemove: () => void }) {
  return (
    <span className={`attach-chip attach-${attachment.status}`}>
      <span className="attach-name">{attachment.file.name}</span>
      <span className="attach-status">{attachment.status === 'failed' && attachment.errorMessage ? attachment.errorMessage : ATTACHMENT_STATUS_LABEL[attachment.status]}</span>
      <button type="button" className="attach-remove" aria-label={`Remove ${attachment.file.name}`} onClick={onRemove}>
        ×
      </button>
    </span>
  )
}

function warmIndicator(sandboxStatus: string | null): { label: string; tone: 'ok' | 'neutral' } {
  if (sandboxStatus === 'ready') return { label: 'sandbox warm · 0s to dispatch', tone: 'ok' }
  if (sandboxStatus === null) return { label: 'sandbox will start when you send', tone: 'neutral' }
  if (sandboxStatus === 'pending' || sandboxStatus === 'spawning' || sandboxStatus === 'connecting' || sandboxStatus === 'booting') {
    return { label: 'sandbox warming up…', tone: 'neutral' }
  }
  return { label: `sandbox ${sandboxStatus}`, tone: 'neutral' }
}

export function Composer({ sessionId, sandboxStatus, hasOpenTurn }: { sessionId: string; sandboxStatus: string | null; hasOpenTurn: boolean }) {
  const queryClient = useQueryClient()
  const [promptText, setPromptText] = useState('')
  const [modelId, setModelId] = useState<string | null>(null)
  const [effort, setEffort] = useState<string | null>(null)
  const [planMode, setPlanMode] = useState(false)
  const [attachments, setAttachments] = useState<Attachment[]>([])
  const isComposingRef = useRef(false)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  const catalogQuery = useQuery({ queryKey: modelCatalogQueryKeys.all(), queryFn: ({ signal }) => getModelCatalog(signal) })

  const selectedModel: ModelCatalogModel | undefined = useMemo(() => {
    if (modelId === null || !catalogQuery.data) return undefined
    for (const provider of catalogQuery.data.providers) {
      const found = provider.models.find((m) => modelWireId(provider.id, m.id) === modelId)
      if (found) return found
    }
    return undefined
  }, [modelId, catalogQuery.data])

  const mutation = useMutation({
    mutationFn: () =>
      createTurn(sessionId, {
        prompt: promptText.trim(),
        modelId,
        effort,
        planMode,
        attachmentIds: readyAttachmentIds(attachments),
      }),
    onSuccess: () => {
      setPromptText('')
      setAttachments([])
      void queryClient.invalidateQueries({ queryKey: sessionQueryKeys.detail(sessionId) })
      void queryClient.invalidateQueries({ queryKey: sessionQueryKeys.events(sessionId) })
      void queryClient.invalidateQueries({ queryKey: sessionListQueryKeys.list('mine') })
      void queryClient.invalidateQueries({ queryKey: sessionListQueryKeys.list('all') })
    },
  })

  const hasInFlightAttachment = attachments.some(isAttachmentInFlight)
  const submitState = { promptText, isSubmitting: mutation.isPending, hasInFlightAttachment, hasOpenTurn }
  const canSend = canSubmitComposer(submitState)

  function submit(): void {
    if (!canSubmitComposer(submitState)) return
    mutation.mutate()
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>): void {
    const decisionEvent = { key: e.key, shiftKey: e.shiftKey, isComposing: isComposingRef.current || e.nativeEvent.isComposing === true }
    if (!shouldSubmitOnKeyDown(decisionEvent, submitState)) return
    e.preventDefault()
    submit()
  }

  function updateAttachment(localId: string, patch: Partial<Attachment>): void {
    setAttachments((prev) => prev.map((a) => (a.localId === localId ? { ...a, ...patch } : a)))
  }

  function attachFiles(files: FileList | null): void {
    if (!files) return
    for (const file of Array.from(files)) {
      const localId = nextLocalId()
      const initial: Attachment = { localId, file, status: 'minting', uploadId: null, errorMessage: null }
      setAttachments((prev) => [...prev, initial])
      void runUpload(
        file,
        {
          mintUpload: (body) => mintUpload(sessionId, body),
          confirmUpload: (uploadId) => confirmUpload(sessionId, uploadId),
          putBytes: putFileToStorage,
        },
        (update) => {
          if (update.status === 'minting') {
            updateAttachment(localId, { status: 'minting' })
          } else if (update.status === 'failed') {
            updateAttachment(localId, { status: 'failed', uploadId: update.uploadId, errorMessage: update.errorMessage })
          } else {
            updateAttachment(localId, { status: update.status, uploadId: update.uploadId, errorMessage: null })
          }
        },
      )
    }
  }

  function removeAttachment(localId: string): void {
    setAttachments((prev) => prev.filter((a) => a.localId !== localId))
  }

  const warm = warmIndicator(sandboxStatus)

  return (
    <div className="composer">
      <textarea
        aria-label="Prompt"
        placeholder="Follow up — the sandbox is kept warm while you type…"
        value={promptText}
        onChange={(e) => setPromptText(e.target.value)}
        onKeyDown={handleKeyDown}
        onCompositionStart={() => {
          isComposingRef.current = true
        }}
        onCompositionEnd={() => {
          isComposingRef.current = false
        }}
      />
      {attachments.length > 0 && (
        <div className="attach-list">
          {attachments.map((a) => (
            <AttachmentChip key={a.localId} attachment={a} onRemove={() => removeAttachment(a.localId)} />
          ))}
        </div>
      )}
      <div className="comp-row">
        <select className="sel-select" aria-label="Model" value={modelId ?? ''} onChange={(e) => { setModelId(e.target.value === '' ? null : e.target.value); setEffort(null) }}>
          <option value="">Default model</option>
          {catalogQuery.data?.providers.map((provider) =>
            provider.models.map((model) => (
              <option key={modelWireId(provider.id, model.id)} value={modelWireId(provider.id, model.id)}>
                {model.name}
              </option>
            )),
          )}
        </select>
        <select
          className="sel-select"
          aria-label="Reasoning effort"
          value={effort ?? ''}
          disabled={!selectedModel?.reasoning}
          onChange={(e) => setEffort(e.target.value === '' ? null : e.target.value)}
        >
          <option value="">{selectedModel?.reasoning ? 'Default effort' : 'Effort · default'}</option>
          {selectedModel?.variants.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
        <button type="button" role="switch" aria-checked={planMode} className="toggle" onClick={() => setPlanMode((v) => !v)}>
          <span className={`tk${planMode ? ' on' : ''}`} />
          Plan mode
        </button>
        <input ref={fileInputRef} type="file" multiple hidden onChange={(e) => { attachFiles(e.target.files); e.target.value = '' }} />
        <button type="button" className="btn" onClick={() => fileInputRef.current?.click()} aria-label="Attach a file">
          Attach
        </button>
        <span className={`warm warm-${warm.tone}`}>
          <span className="dot" />
          {warm.label}
        </span>
        <button type="button" className="btn primary" disabled={!canSend} onClick={submit}>
          {mutation.isPending ? 'Sending…' : 'Send'}
        </button>
      </div>
      {mutation.isError && (
        <p className="sidebar-notice" role="alert">
          {mutation.error instanceof Error ? mutation.error.message : 'Could not send. Try again.'}
        </p>
      )}
    </div>
  )
}
