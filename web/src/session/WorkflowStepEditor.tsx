// WorkflowStepEditor.tsx -- the workflow canvas editor's own "selected
// step" rail form (§25.12): every WorkflowStepDefinition field, plus this
// step's own outgoing-edge routing.
//
// # Where "make the obviously-invalid hard to draw" actually lives
//
// §25.12 requires constraining what a user can draw against the engine's
// closed model; §25.4 draws that model precisely: onStatus is a closed
// 3-value enum, and a toStepId must resolve inside this same definition.
// Both are enforced HERE, structurally, not by client-side validation logic
// that could drift from the real rule: the onStatus <select> is built from
// workflowFormat.ts's own EDGE_STATUSES (and only the statuses THIS step
// does not already have an edge for -- workflow_edges_from_status_uniq
// allows at most one per status), and the target <select> is built from
// workflowGraphModel.ts's own edgeTargetsForStep, i.e. this definition's own
// steps and nothing else. Neither control can produce a value the engine
// would reject on either axis -- an edge drawn through this form is
// unconditionally representable, though the server still re-validates the
// WHOLE document on save (§25.10: "validation belongs to the save, not to
// the canvas") for the rules this form cannot see locally (step order
// uniqueness across a concurrent edit, etc).
//
// # Rendering safety
//
// step.promptTemplate/step.modelId/step.effort are all operator-entered
// free text (§25.12's own top-level instruction: "a prompt template is
// arbitrary multi-line content"). Every read-only render of them here goes
// through T/truncateForDisplay; every EDITABLE one binds directly to a
// controlled input's `value`, which React never interprets as markup
// either way (mirrors RepoSettingsView.tsx's own identical split).
import { useState } from 'react'

import type { WorkflowEdge, WorkflowStepDefinition } from '@narvi/contracts/rest-dtos'

import { EDGE_STATUSES } from './workflowFormat'
import { edgeTargetsForStep, usedOnStatusesForStep } from './workflowGraphModel'
import { truncateForDisplay } from './textSafety'

const MAX_FIELD_CHARS = 4000

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

export interface WorkflowStepEditorProps {
  step: WorkflowStepDefinition
  allSteps: WorkflowStepDefinition[]
  readOnly: boolean
  canDelete: boolean
  onChange: (patch: Partial<WorkflowStepDefinition>) => void
  onDelete: () => void
  onMove: (direction: 'up' | 'down') => void
  onAddEdge: (onStatus: WorkflowEdge['onStatus'], toStepId: string) => void
  onRemoveEdge: (onStatus: WorkflowEdge['onStatus']) => void
}

/** WorkflowStepEditor renders one step's own full field set plus its outgoing-edge routing. Exported for direct render-safety testing (mirrors TemplateEditor's own identical shape in PromptTemplatesPanel.tsx): the read-only preview path (step.promptTemplate/modelId/effort when readOnly) must render as plain text only. */
export function WorkflowStepEditor({ step, allSteps, readOnly, canDelete, onChange, onDelete, onMove, onAddEdge, onRemoveEdge }: WorkflowStepEditorProps) {
  const targets = edgeTargetsForStep(allSteps)
  const used = usedOnStatusesForStep(step)
  const available = EDGE_STATUSES.filter((s) => !used.has(s))
  const edges = step.edges ?? []

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div className="btnrow">
        <button type="button" className="btn" style={{ padding: '2px 9px', fontSize: 11 }} disabled={readOnly} onClick={() => onMove('up')}>
          ↑ move earlier
        </button>
        <button type="button" className="btn" style={{ padding: '2px 9px', fontSize: 11 }} disabled={readOnly} onClick={() => onMove('down')}>
          ↓ move later
        </button>
        <button type="button" className="btn danger" style={{ padding: '2px 9px', fontSize: 11, marginLeft: 'auto' }} disabled={readOnly || !canDelete} title={!canDelete ? 'A definition needs at least one step' : undefined} onClick={onDelete}>
          Delete step
        </button>
      </div>

      <div className="wffield">
        <label>Model</label>
        {readOnly ? (
          <span>{step.modelId ? <T text={step.modelId} /> : 'inherit session model'}</span>
        ) : (
          <input
            className="btn"
            style={{ textAlign: 'left', flex: 1 }}
            placeholder="inherit session model (e.g. anthropic/claude-opus-4-8)"
            value={step.modelId ?? ''}
            onChange={(e) => onChange({ modelId: e.target.value.trim().length > 0 ? e.target.value : null })}
          />
        )}
      </div>

      <div className="wffield">
        <label>Effort</label>
        {readOnly ? (
          <span>{step.effort ? <T text={step.effort} /> : 'inherit'}</span>
        ) : (
          <input className="btn" style={{ textAlign: 'left', flex: 1 }} placeholder="inherit" value={step.effort ?? ''} onChange={(e) => onChange({ effort: e.target.value.trim().length > 0 ? e.target.value : null })} />
        )}
      </div>

      <div className="wffield">
        <label>Execution scope</label>
        {readOnly ? (
          <span>{step.executionScope}</span>
        ) : (
          <select className="sel-select" value={step.executionScope} onChange={(e) => onChange({ executionScope: e.target.value as WorkflowStepDefinition['executionScope'] })}>
            <option value="same_session">same_session</option>
            <option value="child_session">child_session</option>
          </select>
        )}
      </div>

      <div className="wffield">
        <label>Conversation continuity</label>
        {readOnly ? (
          <span>{step.conversationContinuity}</span>
        ) : (
          <select className="sel-select" value={step.conversationContinuity} onChange={(e) => onChange({ conversationContinuity: e.target.value as WorkflowStepDefinition['conversationContinuity'] })}>
            <option value="continue">continue</option>
            <option value="fresh">fresh</option>
          </select>
        )}
      </div>

      <div className="wffield row">
        <label className="toggle">
          <input type="checkbox" checked={step.hitlBefore} disabled={readOnly} onChange={(e) => onChange({ hitlBefore: e.target.checked })} style={{ marginRight: 4 }} />
          HITL before
        </label>
        <label className="toggle">
          <input type="checkbox" checked={step.hitlAfter} disabled={readOnly} onChange={(e) => onChange({ hitlAfter: e.target.checked })} style={{ marginRight: 4 }} />
          HITL after
        </label>
      </div>

      <div className="wffield" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
        <label>Prompt template</label>
        <textarea
          className="btn"
          style={{ width: '100%', minHeight: 100, textAlign: 'left', fontFamily: 'var(--mono)', fontSize: 'var(--text-sm)', resize: 'vertical', overflowWrap: 'anywhere' }}
          readOnly={readOnly}
          value={step.promptTemplate}
          onChange={(e) => onChange({ promptTemplate: e.target.value })}
        />
      </div>

      <div>
        <b style={{ fontSize: 'var(--text-sm)' }}>Routing</b>
        <p className="ph" style={{ margin: '2px 0 6px' }}>
          no explicit edge for a status: ok advances to the next step in order; needs_fix/blocked escalate (fail-conservative)
        </p>
        {edges.length === 0 && <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm-alt)', margin: 0 }}>No explicit edges -- default routing applies.</p>}
        {edges.map((edge) => (
          <div key={edge.onStatus} className="formrow" style={{ padding: '3px 0' }}>
            <span className={`chip ${edge.onStatus === 'ok' ? 'ok' : edge.onStatus === 'needs_fix' ? 'warn' : 'crit'}`}>
              <span className="dot" />
              {edge.onStatus}
            </span>
            <span>→</span>
            {readOnly ? (
              <span>step {targets.find((t) => t.id === edge.toStepId)?.order ?? '?'}</span>
            ) : (
              <select className="sel-select" value={edge.toStepId} onChange={(e) => onAddEdge(edge.onStatus, e.target.value)}>
                {targets.map((t) => (
                  <option key={t.id} value={t.id}>
                    step {t.order}
                    {t.id === step.id ? ' (self)' : ''}
                  </option>
                ))}
              </select>
            )}
            {!readOnly && (
              <button type="button" className="btn" style={{ padding: '1px 7px', fontSize: 11 }} onClick={() => onRemoveEdge(edge.onStatus)}>
                remove
              </button>
            )}
          </div>
        ))}
        {!readOnly && available.length > 0 && targets.length > 0 && (
          <AddEdgeControl key={step.id} currentStepId={step.id} available={available} targets={targets} onAdd={onAddEdge} />
        )}
      </div>
    </div>
  )
}

/**
 * AddEdgeControl -- a small, self-contained "pick a status + a target, then
 * add" row. `available`/`targets` can shrink out from under an
 * already-chosen selection (adding an edge removes that status from
 * `available` on the next render, for the SAME step, without remounting
 * this component) -- every read of the current selection therefore falls
 * back to the first still-legal option rather than trusting stale local
 * state, so the controls never offer or submit a status/target this step
 * can no longer take.
 */
function AddEdgeControl({ currentStepId, available, targets, onAdd }: { currentStepId: string; available: readonly WorkflowEdge['onStatus'][]; targets: WorkflowStepDefinition[]; onAdd: (onStatus: WorkflowEdge['onStatus'], toStepId: string) => void }) {
  const [status, setStatus] = useState<WorkflowEdge['onStatus']>(available[0])
  const [targetId, setTargetId] = useState<string>(targets[0]?.id ?? '')

  const effectiveStatus = available.includes(status) ? status : available[0]
  const effectiveTargetId = targets.some((t) => t.id === targetId) ? targetId : (targets[0]?.id ?? '')

  return (
    <div className="formrow" style={{ padding: '5px 0 0' }}>
      <select className="sel-select" value={effectiveStatus} onChange={(e) => setStatus(e.target.value as WorkflowEdge['onStatus'])}>
        {available.map((s) => (
          <option key={s} value={s}>
            {s}
          </option>
        ))}
      </select>
      <span>→</span>
      <select className="sel-select" value={effectiveTargetId} onChange={(e) => setTargetId(e.target.value)}>
        {targets.map((t) => (
          <option key={t.id} value={t.id}>
            step {t.order}
            {t.id === currentStepId ? ' (self)' : ''}
          </option>
        ))}
      </select>
      <button type="button" className="btn" style={{ padding: '1px 7px', fontSize: 11 }} disabled={!effectiveTargetId} onClick={() => onAdd(effectiveStatus, effectiveTargetId)}>
        + add edge
      </button>
    </div>
  )
}
