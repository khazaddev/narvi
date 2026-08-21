// workflowRendering.test.tsx -- WorkflowEditorView.tsx/WorkflowCanvas.tsx's
// own defining risk, proven at the RENDER boundary: WorkflowDefinition.name
// and every WorkflowStepDefinition's promptTemplate/modelId/effort are all
// operator-entered free text (§25.12's own top-level instruction: "a prompt
// template is arbitrary multi-line content"). Mirrors decisionInboxRendering
// .test.tsx/membersRendering.test.tsx's own established pattern exactly:
// renderToStaticMarkup, no jsdom needed, proving React's default escaping is
// actually in effect for both the canvas node and the definition-list row.
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ReactFlowProvider } from '@xyflow/react'

import type { NodeProps } from '@xyflow/react'
import type { WorkflowBinding, WorkflowDefinition, WorkflowStepDefinition } from '@narvi/contracts/rest-dtos'

import { DefinitionRow } from '../WorkflowEditorView'
import { WorkflowStepNode } from '../WorkflowCanvas'
import type { StepNode } from '../workflowGraphModel'

const XSS_IMG = '<img src=x onerror=alert(1)>'
const XSS_SCRIPT = '<script>alert(document.cookie)</script>'

function withQueryClient(node: React.ReactNode) {
  const client = new QueryClient()
  return renderToStaticMarkup(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

function baseStep(overrides: Partial<WorkflowStepDefinition> = {}): WorkflowStepDefinition {
  return {
    id: 's1',
    order: 1,
    kind: 'agent',
    modelId: null,
    effort: null,
    promptTemplate: 'do the thing',
    executionScope: 'same_session',
    conversationContinuity: 'continue',
    hitlBefore: false,
    hitlAfter: false,
    edges: [],
    ...overrides,
  }
}

function baseDefinition(overrides: Partial<WorkflowDefinition> = {}): WorkflowDefinition {
  return {
    id: 'd1',
    lane: 'request',
    name: 'My workflow',
    isBuiltIn: false,
    version: 1,
    steps: [baseStep()],
    createdAt: '2026-08-20T00:00:00Z',
    updatedAt: '2026-08-20T00:00:00Z',
    ...overrides,
  }
}

function nodeProps(step: WorkflowStepDefinition, readOnly = false): NodeProps<StepNode> {
  return {
    id: step.id,
    data: { step, selected: false, readOnly },
    type: 'workflowStep',
    selected: false,
    isConnectable: false,
    zIndex: 0,
    dragging: false,
    positionAbsoluteX: 0,
    positionAbsoluteY: 0,
    // WorkflowStepNode reads only `data` off its props -- the rest of
    // NodeProps' own (large) required surface is irrelevant to this
    // component and is stubbed here just to satisfy the type.
  } as unknown as NodeProps<StepNode>
}

/** renderNode wraps WorkflowStepNode in ReactFlowProvider -- its own <Handle> children call useStoreApi internally and throw without a real React Flow store ancestor (xyflow's own documented requirement), even for a plain static render. */
function renderNode(step: WorkflowStepDefinition, readOnly = false): string {
  return renderToStaticMarkup(
    <ReactFlowProvider>
      <WorkflowStepNode {...nodeProps(step, readOnly)} />
    </ReactFlowProvider>,
  )
}

describe('WorkflowStepNode -- adversarial prompt template / model id stay text', () => {
  it('a hostile promptTemplate first line renders as text, never markup', () => {
    const step = baseStep({ promptTemplate: `${XSS_SCRIPT}\nrest of the prompt` })
    const html = renderNode(step)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a hostile modelId renders as text, never markup', () => {
    const step = baseStep({ modelId: `anthropic/${XSS_IMG}` })
    const html = renderNode(step)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('does not hang or break layout on a 50KB prompt template', () => {
    const step = baseStep({ promptTemplate: 'x'.repeat(50_000) })
    const start = Date.now()
    let html = ''
    expect(() => {
      html = renderNode(step)
    }).not.toThrow()
    expect(Date.now() - start).toBeLessThan(2000)
    expect(html.length).toBeLessThan(50_000)
  })

  it('a step with a null edges field (the Member.identities/WorkflowStepDefinition.edges wire trap) still renders without throwing', () => {
    const step = { ...baseStep(), edges: null } as unknown as WorkflowStepDefinition
    expect(() => renderNode(step)).not.toThrow()
  })

  it('marks a read-only node visibly, so a refused definition\'s graph is shown but not implied editable', () => {
    const html = renderNode(baseStep(), true)
    expect(html).toContain('read-only')
  })
})

describe('DefinitionRow -- adversarial definition name stays text', () => {
  it('a hostile definition name renders as text, never markup', () => {
    const def = baseDefinition({ name: `fix: bug ${XSS_IMG}` })
    const html = withQueryClient(<DefinitionRow definition={def} bindings={[]} isSelected={false} onSelect={() => {}} onDuplicateClick={() => {}} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a hostile repo override name (a bound definition\'s own summary chip) stays text via the summary path, not injected as markup', () => {
    const def = baseDefinition({ id: 'd1' })
    const bindings: WorkflowBinding[] = [{ id: 'b1', lane: 'request', repoFullName: `evil/${XSS_SCRIPT}`, workflowDefinitionId: 'd1', definitionVersion: 1, createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' }]
    // DefinitionRow only counts repo overrides (summarizeBindingsForDefinition), it never renders their names directly -- this proves the count path never leaks the raw string into markup either.
    const html = withQueryClient(<DefinitionRow definition={def} bindings={bindings} isSelected={false} onSelect={() => {}} onDuplicateClick={() => {}} />)
    expect(html).not.toContain('<script>')
  })

  it('shows a built-in definition as read-only (locked), and a bound definition as read-only, each with their own distinct chip label', () => {
    const builtIn = baseDefinition({ id: 'd1', isBuiltIn: true })
    const builtInHtml = withQueryClient(<DefinitionRow definition={builtIn} bindings={[]} isSelected={false} onSelect={() => {}} onDuplicateClick={() => {}} />)
    expect(builtInHtml).toContain('built-in')

    const bound = baseDefinition({ id: 'd2', isBuiltIn: false })
    const boundBindings: WorkflowBinding[] = [{ id: 'b1', lane: 'request', repoFullName: null, workflowDefinitionId: 'd2', definitionVersion: 1, createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' }]
    const boundHtml = withQueryClient(<DefinitionRow definition={bound} bindings={boundBindings} isSelected={false} onSelect={() => {}} onDuplicateClick={() => {}} />)
    expect(boundHtml).toContain('bound')
    expect(boundHtml).toContain('global')
  })
})
