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

export type StructuralRefusalKind = 'built_in' | 'bound' | 'has_runs'

export interface StructuralRefusal {
  kind: StructuralRefusalKind
  message: string
}

/**
 * structuralRefusalFor renders WorkflowDefinition.editRefusal -- the server's
 * own verdict -- into the copy this screen shows. It does NOT re-derive the
 * rules.
 *
 * An earlier version did: it read isBuiltIn and joined the bindings list
 * itself, carrying a second copy of two of the three refusal rules and of
 * their wording. Two things were wrong with that. The rules would drift from
 * refusalReasonForMutation (httpapi/workflowdefinitions.go), which is the one
 * that actually decides; and the THIRD refusal — a definition frozen by run
 * history — was not derivable from anything on the wire at all, so the screen
 * could only discover it by letting the operator do the work and then failing
 * the save.
 *
 * The server now sends the verdict and this maps it to operator language. The
 * rule lives in one place; the wording lives here, where wording belongs.
 * Each reason keeps its own remedy: duplicating and unbinding are different
 * actions and must never be collapsed into one message (§25.10).
 */
export function structuralRefusalFor(definition: Pick<WorkflowDefinition, 'editRefusal'>): StructuralRefusal | null {
  switch (definition.editRefusal) {
    case 'built_in':
      return {
        kind: 'built_in',
        message: 'This is one of the three built-in lane defaults. It is read-only for everyone, admins included — duplicate it to get an editable copy.',
      }
    case 'bound':
      return {
        kind: 'bound',
        message: 'This definition is in use: a lane is bound to it, so editing it would change what runs in production without an admin activating anything. Duplicate it and edit the copy, then have an admin point the binding at the copy — or unbind it first.',
      }
    case 'has_runs':
      return {
        kind: 'has_runs',
        message: 'This definition has already run at least once, which freezes it: a completed run describes what it executed only by pointing at these steps, so changing them would rewrite history. Duplicate it and edit the copy.',
      }
    default:
      return null
  }
}

/** nextStepOrder returns the order value a newly-added step should carry -- one past the current maximum, so a fresh step always lands after every existing one and order stays positive/unique by construction (internal/domain/workflow.ValidateDefinition's own rule, enforced client-side here as cheaply as possible, per §25.12's "make the obviously-invalid hard to draw" mandate). */
export function nextStepOrder(orders: readonly number[]): number {
  return orders.length === 0 ? 1 : Math.max(...orders) + 1
}

/** refusalChipLabel names each edit refusal in one or two words -- exhaustive over StructuralRefusalKind so a new reason is a compile error rather than a silent fallback to someone else's label. */
export function refusalChipLabel(kind: StructuralRefusalKind): string {
  switch (kind) {
    case 'built_in':
      return 'built-in'
    case 'bound':
      return 'bound'
    case 'has_runs':
      return 'has run'
  }
}

/** refusalChipTone maps a refusal to its chip tone: built-in is a neutral fact about a template, the other two are states an operator may want to change. */
export function refusalChipTone(kind: StructuralRefusalKind): string {
  return kind === 'built_in' ? 'neutral' : 'warn'
}
