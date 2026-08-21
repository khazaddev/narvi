// workflowGraphModel.ts -- pure functions turning a WorkflowDefinition's own
// steps/edges (§25.10) into @xyflow/react's node/edge shapes, and back for
// the canvasPosition half of a save. No React, no I/O: WorkflowCanvas.tsx is
// the thin rendering shell around this; every branch here is unit-testable
// without mounting anything (mirrors timelineModel.ts's own identical split
// from Timeline.tsx).
//
// # The DTO trap this module defends against
//
// WorkflowStepDefinition.edges is schema-required and non-nullable
// (contracts/rest/v1/dtos.schema.json), but the wire trap workflowdefinitions
// .go's own doc comment documents (a Go nil slice marshals as `null`) means a
// hostile or buggy server response could still send `edges: null` for a step
// -- and this codebase has already paid for the identical assumption twice
// (Member.identities, this same field). stepsToEdges/stepsToNodes below never
// index `step.edges` without a defensive `?? []` first.
import { MarkerType } from '@xyflow/react'
import type { Edge, Node } from '@xyflow/react'

import type { WorkflowStepDefinition } from '@narvi/contracts/rest-dtos'

import { edgeStatusColorVar } from './workflowFormat'

/** AUTO_LAYOUT_X_GAP/AUTO_LAYOUT_Y are the fallback grid this canvas lays a step out on when it has never had a canvasPosition saved (every built-in, and every definition until a canvas first drags one of its nodes -- WorkflowStepDefinition.canvasPosition's own doc comment). */
export const AUTO_LAYOUT_X_GAP = 260
export const AUTO_LAYOUT_Y = 90

/**
 * autoPositionForOrder computes a deterministic DISPLAY-ONLY fallback
 * position for a step with no saved canvasPosition -- left to right by
 * `order`, matching the engine's own default "with no explicit edge, ok
 * advances to the next step in order" semantics (§25.4), so an unlaid-out
 * graph still reads left-to-right in actual execution order. This is never
 * written back onto the step's own canvasPosition unless the operator
 * actually drags that node -- WorkflowEditorView.tsx's own onNodeDragStop is
 * the only writer, so a definition nobody has ever repositioned still saves
 * with canvasPosition null/absent on every step, exactly as before.
 */
export function autoPositionForOrder(order: number): { x: number; y: number } {
  return { x: (order - 1) * AUTO_LAYOUT_X_GAP + 40, y: AUTO_LAYOUT_Y }
}

export interface StepNodeData extends Record<string, unknown> {
  step: WorkflowStepDefinition
  selected: boolean
  readOnly: boolean
}

export type StepNode = Node<StepNodeData, 'workflowStep'>

/** stepsToNodes converts a definition's own steps into React Flow nodes, one per step, positioned from canvasPosition when present or autoPositionForOrder as a display-only fallback otherwise. */
export function stepsToNodes(steps: readonly WorkflowStepDefinition[], selectedStepId: string | null, readOnly: boolean): StepNode[] {
  return steps.map((step) => ({
    id: step.id,
    type: 'workflowStep',
    position: step.canvasPosition ?? autoPositionForOrder(step.order),
    data: { step, selected: step.id === selectedStepId, readOnly },
    draggable: !readOnly,
    connectable: false,
    selectable: true,
  }))
}

/** stepsToEdges converts every step's own outgoing edges into React Flow edges, colored/labeled by onStatus (workflowFormat.ts's own edgeStatusColorVar) -- see this file's own top comment for why `step.edges ?? []` guards every read. */
export function stepsToEdges(steps: readonly WorkflowStepDefinition[]): Edge[] {
  const out: Edge[] = []
  for (const step of steps) {
    const edges = step.edges ?? []
    for (const edge of edges) {
      const color = edgeStatusColorVar(edge.onStatus)
      out.push({
        id: `${edge.fromStepId}:${edge.onStatus}:${edge.toStepId}`,
        source: edge.fromStepId,
        target: edge.toStepId,
        label: edge.onStatus,
        style: { stroke: color, strokeWidth: 1.6 },
        labelStyle: { fill: color, fontWeight: 600, fontSize: 11 },
        labelBgStyle: { fill: 'var(--surface)', fillOpacity: 0.9 },
        markerEnd: { type: MarkerType.ArrowClosed, color, width: 16, height: 16 },
      })
    }
  }
  return out
}

/** stepOrders extracts every step's own `order` -- the input nextStepOrder (workflowFormat.ts) needs, kept here since callers already have a steps array on hand and this is a one-line map. */
export function stepOrders(steps: readonly WorkflowStepDefinition[]): number[] {
  return steps.map((s) => s.order)
}

/**
 * edgeTargetsForStep lists which OTHER steps a given step's own outgoing
 * edge is allowed to target -- every step in the SAME definition, including
 * itself (a same-step retry loop) and an earlier step (a backward loop,
 * WorkflowEdge.toStepId's own doc comment) -- i.e. every step, no
 * exclusions. Named as its own function (rather than inlined at the one call
 * site) because "which targets are legal" is exactly the kind of rule this
 * module exists to make impossible to get wrong a second time.
 */
export function edgeTargetsForStep(steps: readonly WorkflowStepDefinition[]): WorkflowStepDefinition[] {
  return [...steps].sort((a, b) => a.order - b.order)
}

/** usedOnStatusesForStep returns the onStatus values step already has an outgoing edge for -- workflow_edges_from_status_uniq (migration 000057) allows at most one edge per onStatus per step, so the "add edge" control offers only what remains. */
export function usedOnStatusesForStep(step: WorkflowStepDefinition): Set<string> {
  return new Set((step.edges ?? []).map((e) => e.onStatus))
}
