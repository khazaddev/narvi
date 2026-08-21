// WorkflowCanvas.tsx -- the visual half of the workflow canvas editor
// (§25.12): a @xyflow/react graph rendering one WorkflowDefinition's own
// steps as nodes and outgoing edges as colored/labeled arrows.
//
// # What this canvas does NOT let you draw, and why
//
// §25.12 requires the editor to "validate/constrain what a user can draw
// against the engine's closed model... rejecting an undrawable-by-the-engine
// graph at save time, not silently accepting it." The cheapest, most
// reliable way to make an invalid edge hard to draw is to never let the
// canvas gesture create one at all: nodesConnectable/edgesReconnectable are
// both false here, so there is no drag-a-line-between-handles interaction
// to constrain in the first place. Every edge is authored through
// WorkflowEditorView.tsx's own step rail instead -- a form whose onStatus
// control is a <select> over workflowFormat.ts's own closed EDGE_STATUSES
// list and whose target control is a <select> over this definition's own
// steps only (workflowGraphModel.ts's own edgeTargetsForStep) -- so an
// invalid onStatus or a dangling toStepId is structurally unrepresentable
// in the form, not merely rejected after the fact. This canvas is left to
// do what it draws best: an accurate, live-updating PICTURE of the graph
// those form edits already produced, plus drag-to-reposition (canvasPosition
// is opaque and purely cosmetic server-side, §25.10, so dragging is the one
// gesture this canvas is safe to allow even in every other respect being
// read-only-by-construction).
//
// # Rendering safety
//
// step.promptTemplate (arbitrary multi-line operator-entered text, §25.12's
// own top-level instruction) and step.modelId (an opaque provider/model
// passthrough string, never Narvi-validated, §25.7) both render through the
// SAME plain-text T/truncateForDisplay path MembersPanel.tsx established --
// never dangerouslySetInnerHTML. WorkflowStepNode is exported for direct
// render-safety testing, mirroring MemberRow/AutomationRow's own precedent.
import { useEffect } from 'react'
import { Background, Controls, Handle, Position, ReactFlow, ReactFlowProvider, useEdgesState, useNodesState } from '@xyflow/react'
import type { Edge, NodeProps, NodeTypes } from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { WorkflowStepDefinition } from '@narvi/contracts/rest-dtos'

import { truncateForDisplay } from './textSafety'
import type { StepNode } from './workflowGraphModel'
import { stepsToEdges, stepsToNodes } from './workflowGraphModel'

const MAX_FIELD_CHARS = 300

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

/** promptPreview renders the first line of a step's own prompt template as its node caption -- WorkflowStepDefinition carries no name/title field at all (contracts/rest/v1/dtos.schema.json has none), so mockups.html's own fabricated node titles ("Draft spec", "Scaffold", ...) have no real column behind them; this uses the closest REAL substitute instead of inventing one. */
function promptPreview(template: string): string {
  const firstLine = template.split('\n', 1)[0] ?? ''
  return firstLine.trim().length > 0 ? firstLine : '(empty prompt template)'
}

/**
 * WorkflowStepNode renders one step's own canvas card. Exported for direct
 * render-safety testing (mirrors AutomationRow/MemberRow's own established
 * precedent).
 */
export function WorkflowStepNode({ data }: NodeProps<StepNode>) {
  const { step, selected, readOnly } = data
  return (
    <div className={`wfnode${selected ? ' selected' : ''}`}>
      <Handle type="target" position={Position.Left} isConnectable={false} style={{ opacity: 0 }} />
      <div className="nh">
        <span className="nord">{step.order}</span>
        <span className="nt">
          <T text={promptPreview(step.promptTemplate)} />
        </span>
      </div>
      <span className="nmodel">{step.modelId ? <T text={step.modelId} /> : 'inherit session model'}</span>
      <span className="nmeta">
        {step.executionScope} · {step.conversationContinuity}
      </span>
      {(step.hitlBefore || step.hitlAfter) && (
        <span className="hitl">
          ◆ HITL {step.hitlBefore && step.hitlAfter ? 'before & after' : step.hitlBefore ? 'before' : 'after'}
        </span>
      )}
      {readOnly && <span className="wfnode-lock">read-only</span>}
      <Handle type="source" position={Position.Right} isConnectable={false} style={{ opacity: 0 }} />
    </div>
  )
}

const NODE_TYPES: NodeTypes = { workflowStep: WorkflowStepNode }

export interface WorkflowCanvasProps {
  steps: WorkflowStepDefinition[]
  selectedStepId: string | null
  readOnly: boolean
  onSelectStep: (stepId: string | null) => void
  onPositionChange: (stepId: string, position: { x: number; y: number }) => void
}

/**
 * WorkflowCanvas owns the @xyflow/react instance. nodes/edges are seeded
 * from props and then re-synced whenever the definition's own STRUCTURE
 * changes (steps/edges added or removed, selection moves, read-only flips)
 * -- never on every render, which would fight React Flow's own internal drag
 * state mid-gesture. A drag commits its result back to the caller only on
 * release (onNodeDragStop), which is the one write this canvas ever makes.
 */
export function WorkflowCanvas({ steps, selectedStepId, readOnly, onSelectStep, onPositionChange }: WorkflowCanvasProps) {
  const [nodes, setNodes, onNodesChange] = useNodesState<StepNode>(stepsToNodes(steps, selectedStepId, readOnly))
  const [edges, setEdges] = useEdgesState<Edge>(stepsToEdges(steps))

  // Content signature: every field a node card or edge actually renders,
  // deliberately excluding ONLY canvasPosition -- a drag's own in-flight
  // position updates flow through onNodesChange/local React Flow state,
  // never through this re-sync (re-applying the SAME position the drag just
  // committed would be a harmless no-op, but there is no reason to pay for
  // it on every drag release either). Everything else -- promptTemplate,
  // modelId, effort, execution scope/continuity, HITL flags, edges -- IS
  // included: setNodes/setEdges below only replace node/edge DATA, never
  // the camera (pan/zoom is separate viewport state @xyflow/react tracks
  // internally, untouched by either call), so there is no cost to keeping
  // the canvas card in sync with a rail edit as it happens. Excluding these
  // fields was tried first and was a real bug: editing a step's prompt
  // template in the rail left the OLD text showing on its own canvas node
  // until some unrelated structural change happened to fire this effect.
  const contentSignature = JSON.stringify(
    steps.map((s) => ({
      id: s.id,
      order: s.order,
      modelId: s.modelId,
      effort: s.effort,
      promptTemplate: s.promptTemplate,
      executionScope: s.executionScope,
      conversationContinuity: s.conversationContinuity,
      hitlBefore: s.hitlBefore,
      hitlAfter: s.hitlAfter,
      edges: (s.edges ?? []).map((e) => `${e.onStatus}>${e.toStepId}`),
    })),
  )

  useEffect(() => {
    setNodes(stepsToNodes(steps, selectedStepId, readOnly))
    setEdges(stepsToEdges(steps))
    // contentSignature stands in for `steps` in this dependency list on
    // purpose -- see this effect's own leading comment.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [contentSignature, selectedStepId, readOnly, setNodes, setEdges])

  return (
    <ReactFlowProvider>
      <ReactFlow
        className="wfcanvas-wrap"
        nodes={nodes}
        edges={edges}
        nodeTypes={NODE_TYPES}
        onNodesChange={onNodesChange}
        onNodeClick={(_evt, node) => onSelectStep(node.id)}
        onPaneClick={() => onSelectStep(null)}
        onNodeDragStop={(_evt, node) => onPositionChange(node.id, node.position)}
        nodesConnectable={false}
        edgesReconnectable={false}
        nodesDraggable={!readOnly}
        elementsSelectable
        fitView
        minZoom={0.25}
        maxZoom={1.5}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </ReactFlowProvider>
  )
}
