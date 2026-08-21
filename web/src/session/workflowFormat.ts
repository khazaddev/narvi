// workflowFormat.ts -- pure, testable formatting/derivation helpers for the
// workflow canvas editor (§25.12), mirroring this codebase's own established
// split (automationFormat.ts, settingsFormat.ts): render logic that does not
// need React lives here, so it can be unit-tested without rendering
// anything.
import type { WorkflowBinding, WorkflowDefinition, WorkflowEdge } from '@narvi/contracts/rest-dtos'

/** WORKFLOW_LANES is the closed 3-value Lane enum on the wire (§25.4: internal/domain/workflow.Lane) -- the same fixed vocabulary every lane picker in this file iterates over. */
export const WORKFLOW_LANES = ['review', 'request', 'plan'] as const
export type WorkflowLaneValue = (typeof WORKFLOW_LANES)[number]

/** laneLabel renders a lane value for display -- the vocabulary is closed (matches Postgres workflow_lane exactly), so this is safe to hard-code rather than routed through the plain-text T path, mirroring identityProviderLabel's own identical reasoning (settingsFormat.ts). */
export function laneLabel(lane: string): string {
  switch (lane) {
    case 'review':
      return 'Review'
    case 'request':
      return 'Request'
    case 'plan':
      return 'Plan'
    default:
      return lane
  }
}

/** EDGE_STATUSES is WorkflowEdge.onStatus's own closed 3-value vocabulary (§25.4: onStatus is the ONLY thing an edge may condition on) -- every onStatus <select> in this screen is built from this list, never a free-text input, so an edge cannot be drawn against a status the engine does not recognize. */
export const EDGE_STATUSES: readonly WorkflowEdge['onStatus'][] = ['ok', 'needs_fix', 'blocked']

/** edgeStatusTone maps an onStatus value to this codebase's own chip-tone vocabulary (ok/warn/crit), matching mockups.html's own e-ok/e-fix/e-blocked stroke colors on the canvas. */
export function edgeStatusTone(status: string): 'ok' | 'warn' | 'crit' | 'neutral' {
  switch (status) {
    case 'ok':
      return 'ok'
    case 'needs_fix':
      return 'warn'
    case 'blocked':
      return 'crit'
    default:
      return 'neutral'
  }
}

/** edgeStatusColorVar maps an onStatus value to the design-token CSS custom property backing its chip tone (tokens.css) -- used for the canvas's own edge stroke/marker color, which is drawn as raw SVG rather than a `.chip` element and so needs the token VALUE, not just the tone class name. */
export function edgeStatusColorVar(status: string): string {
  switch (status) {
    case 'ok':
      return 'var(--ok)'
    case 'needs_fix':
      return 'var(--warn)'
    case 'blocked':
      return 'var(--crit)'
    default:
      return 'var(--faint)'
  }
}

export interface DefinitionBindingSummary {
  global: boolean
  repos: string[]
}

/**
 * summarizeBindingsForDefinition reduces the full workflow-bindings list down
 * to what ONE definition is bound to -- global, one or more repo overrides,
 * or neither. There is no per-definition "am I bound" field on the wire
 * (WorkflowDefinition carries no such flag); this is the join the editor
 * performs client-side over the two lists it already fetches (GET
 * /api/workflow-definitions, GET /api/workflow-bindings), the same "list
 * bindings so the editor knows which definitions are safe to edit" reasoning
 * ListWorkflowBindings' own doc comment states server-side.
 */
export function summarizeBindingsForDefinition(bindings: readonly WorkflowBinding[], definitionId: string): DefinitionBindingSummary {
  const repos: string[] = []
  let global = false
  for (const b of bindings) {
    if (b.workflowDefinitionId !== definitionId) continue
    if (b.repoFullName === null) global = true
    else repos.push(b.repoFullName)
  }
  return { global, repos }
}

export type StructuralRefusalKind = 'built_in' | 'bound'

export interface StructuralRefusal {
  kind: StructuralRefusalKind
  message: string
}

/**
 * structuralRefusalFor derives the two edit refusals this screen can know
 * about WITHOUT ever attempting a write (§25.10/§25.11): is_built_in and
 * "referenced by any workflow_bindings row" are both structural,
 * unconditional (even for an admin), and readable straight off data this
 * screen already has on hand -- WorkflowDefinition.isBuiltIn and the join
 * summarizeBindingsForDefinition above performs.
 *
 * The THIRD refusal (has run history) has no such signal ANYWHERE on the
 * wire -- WorkflowDefinition carries no "has run history" flag, and there is
 * no endpoint to ask "has this definition ever run" other than attempting
 * the write itself and reading refusalReasonForMutation's own 409 (workflow
 * definitions.go). This function can only ever return null for that case --
 * a null result means "editable as far as this screen can tell", not
 * "definitely editable"; WorkflowEditorView.tsx's own save handler is what
 * catches the third refusal reactively, from the server's own verbatim
 * message, when it fires.
 */
export function structuralRefusalFor(definition: Pick<WorkflowDefinition, 'id' | 'isBuiltIn'>, bindings: readonly WorkflowBinding[]): StructuralRefusal | null {
  if (definition.isBuiltIn) {
    return {
      kind: 'built_in',
      message: 'This is a built-in system template. It is read-only, even for an admin -- duplicate it to make an editable copy.',
    }
  }
  const summary = summarizeBindingsForDefinition(bindings, definition.id)
  if (summary.global || summary.repos.length > 0) {
    const where = [summary.global ? 'the global binding' : null, ...summary.repos].filter((s): s is string => s !== null)
    return {
      kind: 'bound',
      message: `This definition is bound (${where.join(', ')}) and cannot be edited or deleted while bound, even by an admin -- duplicate it, edit the copy, then have an admin activate the copy instead.`,
    }
  }
  return null
}

/** nextStepOrder returns the order value a newly-added step should carry -- one past the current maximum, so a fresh step always lands after every existing one and order stays positive/unique by construction (internal/domain/workflow.ValidateDefinition's own rule, enforced client-side here as cheaply as possible, per §25.12's "make the obviously-invalid hard to draw" mandate). */
export function nextStepOrder(orders: readonly number[]): number {
  return orders.length === 0 ? 1 : Math.max(...orders) + 1
}
