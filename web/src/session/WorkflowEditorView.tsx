// WorkflowEditorView.tsx -- the workflow canvas editor (§25.12): authoring a
// lane's workflow definitions as a node/edge graph, plus the workflow
// bindings that decide which definition actually drives dispatch for a
// lane/repo (§25.10).
//
// # A definition is one document -- this screen never invents per-step/
// # per-edge writes
//
// WorkflowDefinition embeds `steps`, and each WorkflowStepDefinition embeds
// its own outgoing `edges` (§25.10). GET returns, and PUT accepts, the WHOLE
// document -- there are deliberately no per-step or per-edge routes. Every
// edit in this screen (add/remove/reorder a step, add/remove/retarget an
// edge, drag a node) mutates ONE local `draft` object shaped exactly like
// that document; Save sends the whole thing in a single PUT. Nothing here
// ever calls a step- or edge-scoped endpoint, because none exists.
//
// # Validation belongs to the save (§25.10) -- this screen makes the
// # cheaply-invalid hard to draw, and nothing more
//
// The onStatus/target controls on a step's own outgoing edge (
// WorkflowStepEditor.tsx) are both closed <select> pickers built from the
// real closed vocabulary (the 3 StepOutcomeStatus values; this definition's
// own steps) -- an edge drawn through this screen cannot name a status the
// engine does not recognize or a step outside this definition. Everything
// else -- step order uniqueness under a concurrent edit, non-empty prompt
// templates, and any rule this screen has no visibility into -- is left to
// the server's own PUT-time re-validation (internal/domain/workflow.
// ValidateDefinition) precisely because duplicating that logic client-side
// is the "reimplementing the guarantee" §25.10 explicitly warns against.
// The one cheap exception: an empty prompt template is trivially detectable
// here and guaranteed to be refused server-side, so Save is disabled (with
// an honest count, never a silent block) until every step has one -- saving
// one guaranteed round trip, not replacing the real check.
//
// # The three edit refusals, and what this screen can and cannot know
// # about them before a save is attempted
//
// §25.10/§25.11 name three reasons PUT/DELETE on a definition are refused,
// unconditionally, even for an admin: built-in, bound (referenced by any
// workflow_bindings row), or has run history. All three arrive as ONE
// server-computed verdict, WorkflowDefinition.editRefusal, produced by the
// same function the write path enforces -- so this screen renders a decision
// and never re-derives the rules. structuralRefusalFor (workflowFormat.ts)
// maps that verdict to copy; the rule lives on the server, the wording lives
// here, and each reason keeps its own remedy because duplicating and
// unbinding are different actions.
//
// An earlier cut of this file derived the first two itself (isBuiltIn plus a
// join over the bindings list) and could not know the third at all, so a
// definition frozen by run history was only discovered by letting the
// operator finish and then failing the save. Do not reintroduce either half:
// a second copy of the rules drifts from the one that decides, and a reason
// that is only learned reactively wastes the operator's work.
//
// The 409 path still exists as a fallback -- a definition can become bound
// between this screen's read and its save -- and its message is surfaced
// verbatim, never reworded.
//
// # RBAC
//
// GET /api/workflow-definitions and GET /api/workflow-bindings are BOTH
// gated maintainer+ (authz.ActionManageWorkflowDefinitions) -- there is no
// separate read-only tier, so a member/viewer sees an explicit "not
// available" notice and nothing else (mirrors MembersPanel.tsx/
// PromptTemplatesPanel.tsx's own identical admin-tier-gated-read
// precedent), never a broken or partially-rendered shell. Activating a
// binding is admin-only (authz.ActionActivateWorkflowBinding) -- a strictly
// narrower action than editing/duplicating a definition, gated separately
// below. Every mutating call here (putWorkflowDefinition, createWorkflow
// Definition, putWorkflowBinding -- api/endpoints.ts) is sent unconditionally
// regardless of this screen's own role check; a 403 the server returns
// surfaces as a genuine ApiError with its own message, exactly like
// settingsAuthorization.test.ts's own established proof for this pattern.
//
// # Rendering safety
//
// definition.name and every step's promptTemplate/modelId/effort are
// operator-entered free text. DefinitionRow and WorkflowStepNode (
// WorkflowCanvas.tsx) are both exported for direct render-safety testing;
// every read-only render of that text goes through the T/truncateForDisplay
// path MembersPanel.tsx established, never dangerouslySetInnerHTML.
import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import type { CreateWorkflowDefinitionRequest, WorkflowBinding, WorkflowDefinition, WorkflowEdge, WorkflowStepDefinition } from '@narvi/contracts/rest-dtos'

import { createWorkflowDefinition, listWorkflowBindings, listWorkflowDefinitions, putWorkflowBinding, putWorkflowDefinition } from '../api/endpoints'
import { ApiError } from '../api/http'
import { workflowBindingQueryKeys, workflowDefinitionQueryKeys } from '../api/queryKeys'
import { meQueryOptions } from '../auth/session'
import { formatDateTime } from './settingsFormat'
import { truncateForDisplay } from './textSafety'
import { WorkflowCanvas } from './WorkflowCanvas'
import { WorkflowStepEditor } from './WorkflowStepEditor'
import type { WorkflowLaneValue } from './workflowFormat'
import { WORKFLOW_LANES, laneLabel, nextStepOrder, refusalChipLabel, refusalChipTone, structuralRefusalFor, summarizeBindingsForDefinition } from './workflowFormat'
import { stepOrders } from './workflowGraphModel'

const MAX_FIELD_CHARS = 2000

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

function LockIcon() {
  return (
    <span className="lockicon" title="Built-in -- a read-only starting template, never itself a live setting" aria-hidden="true">
      <svg width="11" height="11" viewBox="0 0 12 12" fill="none">
        <rect x="2.5" y="5.3" width="7" height="5.2" rx="1" stroke="currentColor" strokeWidth="1.1" />
        <path d="M3.8 5.3V3.6a2.2 2.2 0 0 1 4.4 0v1.7" stroke="currentColor" strokeWidth="1.1" />
      </svg>
    </span>
  )
}

function blankStep(order: number, promptTemplate: string): WorkflowStepDefinition {
  return {
    id: crypto.randomUUID(),
    order,
    kind: 'agent',
    modelId: null,
    effort: null,
    promptTemplate,
    executionScope: 'same_session',
    conversationContinuity: 'continue',
    hitlBefore: false,
    hitlAfter: false,
    edges: [],
  }
}

function definitionNameFor(definitions: WorkflowDefinition[], id: string): string {
  return definitions.find((d) => d.id === id)?.name ?? 'unknown definition'
}

/**
 * asNonEmptySteps narrows a plain steps array to the non-empty tuple shape
 * UpdateWorkflowDefinitionRequest.steps requires on the wire (schema
 * minItems: 1). Unreachable in practice: removeStep refuses to drop a
 * definition's own last remaining step (WorkflowStepEditor.tsx's own
 * canDelete), and every draft starts from either an already-saved document
 * or a freshly created one-step definition. Guarded with a thrown error
 * rather than an unchecked type assertion, so a future bug here surfaces as
 * a caught, reported save failure instead of shipping a malformed request.
 */
function asNonEmptySteps(steps: WorkflowStepDefinition[]): [WorkflowStepDefinition, ...WorkflowStepDefinition[]] {
  if (steps.length === 0) {
    throw new Error('a workflow definition must have at least one step')
  }
  return steps as [WorkflowStepDefinition, ...WorkflowStepDefinition[]]
}

/** DefinitionRow renders one definition's own row in the lane list -- exported for direct render-safety testing (mirrors AutomationRow/MemberRow's own established precedent): definition.name is operator-entered free text and must render as plain text only. */
export function DefinitionRow({ definition, bindings, isSelected, onSelect, onDuplicateClick }: { definition: WorkflowDefinition; bindings: WorkflowBinding[]; isSelected: boolean; onSelect: () => void; onDuplicateClick: () => void }) {
  const summary = summarizeBindingsForDefinition(bindings, definition.id)
  const refusal = structuralRefusalFor(definition)
  return (
    <div className={`wfitem${isSelected ? ' active' : ''}`} onClick={onSelect} role="button" tabIndex={0} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ' ? onSelect() : undefined)}>
      <span className="wt">
        {definition.isBuiltIn && <LockIcon />}
        <T text={definition.name} />
      </span>
      <span className="wm">
        {definition.steps.length} step{definition.steps.length === 1 ? '' : 's'} · v{definition.version}
        {/*
          One label per reason, never a two-way fallback: the ternary this
          replaces rendered "bound" for anything that was not built-in, so a
          definition frozen by run history wore the wrong chip and pointed the
          operator at unbinding, which would not have helped. The three
          remedies differ (§25.10), so the three labels must.
        */}
        {refusal && (
          <span className={`chip ${refusalChipTone(refusal.kind)}`} style={{ marginLeft: 4 }} title={refusal.message}>
            <span className="dot" />
            {refusalChipLabel(refusal.kind)}
          </span>
        )}
        {summary.global && (
          <span className="chip ok" style={{ marginLeft: 4 }}>
            <span className="dot" />
            global
          </span>
        )}
        {summary.repos.length > 0 && (
          <span className="chip neutral" style={{ marginLeft: 4 }}>
            <span className="dot" />
            {summary.repos.length} repo override{summary.repos.length === 1 ? '' : 's'}
          </span>
        )}
      </span>
      <button
        type="button"
        className="wfdup"
        onClick={(e) => {
          e.stopPropagation()
          onDuplicateClick()
        }}
      >
        Duplicate →
      </button>
    </div>
  )
}

function NewDefinitionForm({ lane, pending, onCancel, onCreate }: { lane: WorkflowLaneValue; pending: boolean; onCancel: () => void; onCreate: (name: string, promptTemplate: string) => void }) {
  const [name, setName] = useState('')
  const [promptTemplate, setPromptTemplate] = useState('')
  const canSubmit = name.trim().length > 0 && promptTemplate.trim().length > 0

  return (
    <div className="formrow" style={{ flexDirection: 'column', alignItems: 'stretch', padding: '6px 2px' }}>
      <input className="btn" style={{ textAlign: 'left' }} placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} />
      <textarea className="btn" style={{ textAlign: 'left', minHeight: 50, resize: 'vertical' }} placeholder="First step's prompt template" value={promptTemplate} onChange={(e) => setPromptTemplate(e.target.value)} />
      <div className="btnrow">
        <button type="button" className="btn primary" disabled={!canSubmit || pending} onClick={() => onCreate(name.trim(), promptTemplate)}>
          {pending ? 'Creating…' : `Create ${laneLabel(lane).toLowerCase()} definition`}
        </button>
        <button type="button" className="btn" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  )
}

function DuplicateForm({ initialName, pending, onCancel, onSubmit }: { initialName: string; pending: boolean; onCancel: () => void; onSubmit: (name: string) => void }) {
  const [name, setName] = useState(initialName)
  return (
    <div className="formrow" style={{ paddingLeft: 8 }}>
      <input className="btn" style={{ textAlign: 'left', flex: 1 }} value={name} onChange={(e) => setName(e.target.value)} />
      <button type="button" className="btn primary" disabled={pending || name.trim().length === 0} onClick={() => onSubmit(name.trim())}>
        {pending ? 'Copying…' : 'Create copy'}
      </button>
      <button type="button" className="btn" onClick={onCancel}>
        Cancel
      </button>
    </div>
  )
}

function WorkflowBindingsSection({ lane, bindings, definitions, selectedDefinitionId, canActivate }: { lane: string; bindings: WorkflowBinding[]; definitions: WorkflowDefinition[]; selectedDefinitionId: string; canActivate: boolean }) {
  const queryClient = useQueryClient()
  const [repoInput, setRepoInput] = useState('')

  const activateMutation = useMutation({
    mutationFn: (repoFullName: string | null) => putWorkflowBinding({ lane: lane as WorkflowBinding['lane'], repoFullName, workflowDefinitionId: selectedDefinitionId }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: workflowBindingQueryKeys.list() })
      // Binding a definition makes it uneditable: editRefusal flips to
      // "bound" on the definition itself, not just on the bindings list.
      // Without this the screen would go on presenting a now-bound
      // definition as editable until something else refetched, and the
      // operator would learn otherwise by having a save refused.
      void queryClient.invalidateQueries({ queryKey: workflowDefinitionQueryKeys.list() })
      setRepoInput('')
    },
  })

  const laneBindings = bindings.filter((b) => b.lane === lane)
  const globalBinding = laneBindings.find((b) => b.repoFullName === null)
  const repoBindings = laneBindings.filter((b) => b.repoFullName !== null)

  return (
    <div>
      <h3>
        Bindings <span className="wm">· {laneLabel(lane)}</span>
      </h3>
      <dl className="kv">
        <dt>global</dt>
        <dd>
          {globalBinding ? <T text={definitionNameFor(definitions, globalBinding.workflowDefinitionId)} /> : 'unresolved'}
          {globalBinding?.workflowDefinitionId === selectedDefinitionId && (
            <span className="chip ok" style={{ marginLeft: 4 }}>
              <span className="dot" />
              this definition
            </span>
          )}
        </dd>
      </dl>
      {repoBindings.length > 0 && (
        <div className="repolist" style={{ marginTop: 6 }}>
          {repoBindings.map((b) => (
            <div key={b.id}>
              <T text={b.repoFullName ?? ''} /> → <T text={definitionNameFor(definitions, b.workflowDefinitionId)} />
              {b.workflowDefinitionId === selectedDefinitionId && (
                <span className="chip ok" style={{ marginLeft: 4 }}>
                  <span className="dot" />
                  this definition
                </span>
              )}
            </div>
          ))}
        </div>
      )}
      {!canActivate && (
        <p className="ph" style={{ marginTop: 6 }}>
          activating a binding is admin-only
        </p>
      )}
      <div className="btnrow" style={{ marginTop: 8 }}>
        <button type="button" className="btn" disabled={activateMutation.isPending || !canActivate} title={!canActivate ? 'Admin only' : undefined} onClick={() => activateMutation.mutate(null)}>
          {activateMutation.isPending ? 'Activating…' : `Activate globally for ${laneLabel(lane)}`}
        </button>
      </div>
      <div className="formrow">
        <input className="btn" style={{ textAlign: 'left', flex: 1 }} placeholder="owner/repo" value={repoInput} onChange={(e) => setRepoInput(e.target.value)} disabled={!canActivate} />
        <button type="button" className="btn" disabled={activateMutation.isPending || !canActivate || repoInput.trim().length === 0} title={!canActivate ? 'Admin only' : undefined} onClick={() => activateMutation.mutate(repoInput.trim())}>
          Activate as repo override
        </button>
      </div>
      {activateMutation.isError && (
        <p className="sidebar-notice" role="alert">
          {activateMutation.error instanceof ApiError ? (activateMutation.error.status === 403 ? "You're not authorized to activate a workflow binding (admin-only)." : <T text={activateMutation.error.message} />) : 'Activating the binding failed.'}
        </p>
      )}
    </div>
  )
}

interface Draft {
  name: string
  steps: WorkflowStepDefinition[]
}

export function WorkflowEditorView() {
  const queryClient = useQueryClient()
  const meQuery = useQuery(meQueryOptions)
  const canActivate = meQuery.data?.role === 'admin'

  const definitionsQuery = useQuery({
    queryKey: workflowDefinitionQueryKeys.list(),
    queryFn: ({ signal }) => listWorkflowDefinitions(signal),
    retry: false,
  })
  const bindingsQuery = useQuery({
    queryKey: workflowBindingQueryKeys.list(),
    queryFn: ({ signal }) => listWorkflowBindings(signal),
    retry: false,
  })

  const forbidden = (definitionsQuery.isError && definitionsQuery.error instanceof ApiError && definitionsQuery.error.status === 403) || (bindingsQuery.isError && bindingsQuery.error instanceof ApiError && bindingsQuery.error.status === 403)

  const definitions = definitionsQuery.data?.definitions ?? []
  const bindings = bindingsQuery.data?.bindings ?? []

  const [selectedDefinitionId, setSelectedDefinitionId] = useState<string | null>(null)
  const [selectedStepId, setSelectedStepId] = useState<string | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [learnedRefusalMessage, setLearnedRefusalMessage] = useState<string | null>(null)
  const [creatingForLane, setCreatingForLane] = useState<WorkflowLaneValue | null>(null)
  const [duplicatingId, setDuplicatingId] = useState<string | null>(null)
  const lastSyncedId = useRef<string | null>(null)

  const selectedDefinition = definitions.find((d) => d.id === selectedDefinitionId) ?? null

  // Default to the first definition once the list loads, so the screen
  // never opens on an empty "nothing selected" state when there is
  // something real to show.
  useEffect(() => {
    if (selectedDefinitionId === null && definitions.length > 0) {
      setSelectedDefinitionId(definitions[0].id)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [definitions.length])

  // Re-syncs `draft` from the server's own current document whenever the
  // SELECTED definition changes, or its version changes (a save just
  // landed, ours or someone else's). Step selection/learned-refusal state
  // is only reset on an actual definition SWITCH, never on a same-
  // definition version bump -- see this file's own top comment for why a
  // background refetch of an UNCHANGED version is safe to leave alone.
  useEffect(() => {
    if (!selectedDefinition) {
      setDraft(null)
      setSelectedStepId(null)
      lastSyncedId.current = null
      return
    }
    const isSwitch = lastSyncedId.current !== selectedDefinition.id
    setDraft({ name: selectedDefinition.name, steps: structuredClone(selectedDefinition.steps ?? []) })
    if (isSwitch) {
      setSelectedStepId(null)
      setLearnedRefusalMessage(null)
    }
    lastSyncedId.current = selectedDefinition.id
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedDefinition?.id, selectedDefinition?.version])

  const preemptiveRefusal = selectedDefinition ? structuralRefusalFor(selectedDefinition) : null
  const effectiveRefusalMessage = preemptiveRefusal?.message ?? learnedRefusalMessage
  const effectiveReadOnly = effectiveRefusalMessage !== null

  const selectedStep = draft?.steps.find((s) => s.id === selectedStepId) ?? null
  const emptyPromptCount = draft?.steps.filter((s) => s.promptTemplate.trim().length === 0).length ?? 0

  const saveMutation = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Draft }) => putWorkflowDefinition(id, { name: body.name, steps: asNonEmptySteps(body.steps) }),
    onSuccess: (doc) => {
      setLearnedRefusalMessage(null)
      setDraft({ name: doc.name, steps: structuredClone(doc.steps ?? []) })
      void queryClient.invalidateQueries({ queryKey: workflowDefinitionQueryKeys.list() })
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) setLearnedRefusalMessage(err.message)
    },
  })

  const createMutation = useMutation({
    mutationFn: (body: CreateWorkflowDefinitionRequest) => createWorkflowDefinition(body),
    onSuccess: (doc) => {
      void queryClient.invalidateQueries({ queryKey: workflowDefinitionQueryKeys.list() })
      setSelectedDefinitionId(doc.id)
      setCreatingForLane(null)
      setDuplicatingId(null)
    },
  })

  function mutateDraft(fn: (prev: Draft) => Draft) {
    setDraft((prev) => (prev ? fn(prev) : prev))
  }

  function updateStep(stepId: string, patch: Partial<WorkflowStepDefinition>) {
    mutateDraft((prev) => ({ ...prev, steps: prev.steps.map((s) => (s.id === stepId ? { ...s, ...patch } : s)) }))
  }

  function removeStep(stepId: string) {
    mutateDraft((prev) => ({
      ...prev,
      steps: prev.steps.filter((s) => s.id !== stepId).map((s) => ({ ...s, edges: (s.edges ?? []).filter((e) => e.toStepId !== stepId) })),
    }))
    setSelectedStepId((cur) => (cur === stepId ? null : cur))
  }

  function moveStep(stepId: string, direction: 'up' | 'down') {
    mutateDraft((prev) => {
      const sorted = [...prev.steps].sort((a, b) => a.order - b.order)
      const idx = sorted.findIndex((s) => s.id === stepId)
      const swapWith = direction === 'up' ? idx - 1 : idx + 1
      if (idx < 0 || swapWith < 0 || swapWith >= sorted.length) return prev
      const a = sorted[idx]
      const b = sorted[swapWith]
      return { ...prev, steps: prev.steps.map((s) => (s.id === a.id ? { ...s, order: b.order } : s.id === b.id ? { ...s, order: a.order } : s)) }
    })
  }

  function upsertEdge(stepId: string, onStatus: WorkflowEdge['onStatus'], toStepId: string) {
    mutateDraft((prev) => ({
      ...prev,
      steps: prev.steps.map((s) => (s.id === stepId ? { ...s, edges: [...(s.edges ?? []).filter((e) => e.onStatus !== onStatus), { fromStepId: stepId, onStatus, toStepId }] } : s)),
    }))
  }

  function removeEdgeStatus(stepId: string, onStatus: WorkflowEdge['onStatus']) {
    mutateDraft((prev) => ({ ...prev, steps: prev.steps.map((s) => (s.id === stepId ? { ...s, edges: (s.edges ?? []).filter((e) => e.onStatus !== onStatus) } : s)) }))
  }

  function addStep() {
    if (!draft) return
    const order = nextStepOrder(stepOrders(draft.steps))
    const step = blankStep(order, '')
    mutateDraft((prev) => ({ ...prev, steps: [...prev.steps, step] }))
    setSelectedStepId(step.id)
  }

  function handlePositionChange(stepId: string, position: { x: number; y: number }) {
    updateStep(stepId, { canvasPosition: position })
  }

  function handleSave() {
    if (!selectedDefinition || !draft) return
    saveMutation.mutate({ id: selectedDefinition.id, body: draft })
  }

  if (forbidden) {
    return (
      <div className="app one">
        <section className="main">
          <div className="panel" style={{ margin: 18 }}>
            <p className="notavailable">Workflow definitions are visible to maintainers and admins. Your role cannot view this screen. The server enforces this, so the data is withheld rather than merely hidden here.</p>
          </div>
        </section>
      </div>
    )
  }

  if (definitionsQuery.isPending || bindingsQuery.isPending) {
    return (
      <div className="session-state" aria-live="polite">
        <p>Loading workflow definitions…</p>
      </div>
    )
  }

  if (definitionsQuery.isError || bindingsQuery.isError) {
    return (
      <div className="session-state" role="alert">
        <p>Couldn't load workflow definitions.</p>
      </div>
    )
  }

  return (
    <div className="app one">
      <section className="main">
        <div className="wftoolbar">
          {selectedDefinition ? (
            <>
              {!effectiveReadOnly ? (
                <input className="btn wfdefname-input" style={{ textAlign: 'left' }} value={draft?.name ?? ''} onChange={(e) => setDraft((d) => d && { ...d, name: e.target.value })} />
              ) : (
                <span className="wfdefname">
                  <T text={selectedDefinition.name} />
                </span>
              )}
              <span className="chip neutral">{laneLabel(selectedDefinition.lane)} lane</span>
              <span className="sel">v{selectedDefinition.version}</span>
              {effectiveRefusalMessage && (
                <span className="chip warn" title={effectiveRefusalMessage}>
                  <span className="dot" />
                  read-only
                </span>
              )}
            </>
          ) : (
            <span className="wfdefname">No workflow selected</span>
          )}
          <span style={{ flex: 1 }} />
          {selectedDefinition && !effectiveReadOnly && (
            <button type="button" className="btn" disabled={effectiveReadOnly} onClick={addStep}>
              + Add step
            </button>
          )}
          {selectedDefinition && (
            <button
              type="button"
              className="btn"
              onClick={() => {
                setDuplicatingId(selectedDefinition.id)
                setCreatingForLane(null)
              }}
            >
              Duplicate
            </button>
          )}
          {selectedDefinition && !effectiveReadOnly && (
            <button type="button" className="btn primary" disabled={saveMutation.isPending || emptyPromptCount > 0} title={emptyPromptCount > 0 ? `${emptyPromptCount} step(s) still need a prompt template` : undefined} onClick={handleSave}>
              {saveMutation.isPending ? 'Saving…' : 'Save'}
            </button>
          )}
        </div>

        <div className="wfshell">
          <nav className="wflist" aria-label="Workflows">
            {WORKFLOW_LANES.map((lane) => {
              const laneDefs = definitions.filter((d) => d.lane === lane)
              const globalBinding = bindings.find((b) => b.lane === lane && b.repoFullName === null)
              return (
                <div className="wflane" key={lane}>
                  <div className="wflane-head">
                    <b>{laneLabel(lane)}</b>
                    <span className="wm">global: {globalBinding ? definitionNameFor(definitions, globalBinding.workflowDefinitionId) : 'unresolved'}</span>
                  </div>
                  {laneDefs.map((d) => (
                    <div key={d.id}>
                      <DefinitionRow
                        definition={d}
                        bindings={bindings}
                        isSelected={d.id === selectedDefinitionId}
                        onSelect={() => setSelectedDefinitionId(d.id)}
                        onDuplicateClick={() => {
                          setDuplicatingId(d.id)
                          setCreatingForLane(null)
                        }}
                      />
                      {duplicatingId === d.id && (
                        <DuplicateForm
                          initialName={`${d.name} copy`}
                          pending={createMutation.isPending}
                          onCancel={() => setDuplicatingId(null)}
                          onSubmit={(name) => createMutation.mutate({ sourceDefinitionId: d.id, name })}
                        />
                      )}
                    </div>
                  ))}
                  {creatingForLane === lane ? (
                    <NewDefinitionForm
                      lane={lane}
                      pending={createMutation.isPending}
                      onCancel={() => setCreatingForLane(null)}
                      onCreate={(name, promptTemplate) => createMutation.mutate({ sourceDefinitionId: null, name, lane, steps: [blankStep(1, promptTemplate)] })}
                    />
                  ) : (
                    <button
                      type="button"
                      className="wfdup"
                      onClick={() => {
                        setCreatingForLane(lane)
                        setDuplicatingId(null)
                      }}
                    >
                      + New {laneLabel(lane).toLowerCase()} definition →
                    </button>
                  )}
                </div>
              )
            })}
            {createMutation.isError && (
              <p className="sidebar-notice" role="alert">
                {createMutation.error instanceof ApiError ? <T text={createMutation.error.message} /> : 'Creating the workflow definition failed.'}
              </p>
            )}
          </nav>

          <section className="wfmain">
            {selectedDefinition && draft ? (
              <WorkflowCanvas steps={draft.steps} selectedStepId={selectedStepId} readOnly={effectiveReadOnly} onSelectStep={setSelectedStepId} onPositionChange={handlePositionChange} />
            ) : (
              <div className="session-state">
                <p>Select a workflow definition from the list to view its graph.</p>
              </div>
            )}
          </section>

          <aside className="wfrail" aria-label="Workflow details">
            {!selectedDefinition && <p className="rail-empty">Select a workflow definition from the list.</p>}
            {selectedDefinition && (
              <>
                <div>
                  <h3>Definition</h3>
                  <dl className="kv">
                    <dt>lane</dt>
                    <dd>{selectedDefinition.lane}</dd>
                    <dt>version</dt>
                    <dd>v{selectedDefinition.version}</dd>
                    <dt>steps</dt>
                    <dd>{draft?.steps.length ?? selectedDefinition.steps.length}</dd>
                    <dt>updated</dt>
                    <dd>{formatDateTime(selectedDefinition.updatedAt)}</dd>
                  </dl>
                  {effectiveRefusalMessage && (
                    <p className="notavailable" style={{ marginTop: 8 }}>
                      <b>Read-only.</b> <T text={effectiveRefusalMessage} />
                    </p>
                  )}
                  {!effectiveRefusalMessage && emptyPromptCount > 0 && (
                    <p className="ph" style={{ marginTop: 8, color: 'var(--warn)' }}>
                      {emptyPromptCount} step{emptyPromptCount === 1 ? '' : 's'} still need a prompt template before this can be saved.
                    </p>
                  )}
                  {saveMutation.isError && !learnedRefusalMessage && (
                    <p className="sidebar-notice" role="alert">
                      {saveMutation.error instanceof ApiError ? <T text={saveMutation.error.message} /> : 'Save failed.'}
                    </p>
                  )}
                </div>

                <WorkflowBindingsSection lane={selectedDefinition.lane} bindings={bindings} definitions={definitions} selectedDefinitionId={selectedDefinition.id} canActivate={canActivate} />

                <div>
                  <h3>Selected step</h3>
                  {selectedStep ? (
                    <WorkflowStepEditor
                      step={selectedStep}
                      allSteps={draft?.steps ?? []}
                      readOnly={effectiveReadOnly}
                      canDelete={(draft?.steps.length ?? 0) > 1}
                      onChange={(patch) => updateStep(selectedStep.id, patch)}
                      onDelete={() => removeStep(selectedStep.id)}
                      onMove={(dir) => moveStep(selectedStep.id, dir)}
                      onAddEdge={(status, target) => upsertEdge(selectedStep.id, status, target)}
                      onRemoveEdge={(status) => removeEdgeStatus(selectedStep.id, status)}
                    />
                  ) : (
                    <p className="rail-empty">Click a step on the canvas to edit it.</p>
                  )}
                </div>
              </>
            )}
          </aside>
        </div>
      </section>
    </div>
  )
}
