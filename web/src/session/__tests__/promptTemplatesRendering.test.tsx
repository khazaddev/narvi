// promptTemplatesRendering.test.tsx -- PromptTemplatesPanel.tsx's own
// defining risk: prompt_templates.name is an admin-chosen but never
// Narvi-validated free-text primary key (classifiertemplates.go's own
// doc comment) and must render as plain text only. Mirrors
// reviewRendering.test.tsx's own established pattern exactly.
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import type { PromptTemplate } from '@narvi/contracts/rest-dtos'

import { PromptTemplateHeaderRow } from '../PromptTemplatesPanel'

const XSS_SCRIPT = '<script>alert(document.cookie)</script>'

function baseTemplate(overrides: Partial<PromptTemplate> = {}): PromptTemplate {
  return {
    name: 'intent_classifier_system',
    template: 'You are a classifier.',
    updatedAt: '2026-08-20T02:00:00Z',
    ...overrides,
  }
}

describe('PromptTemplateHeaderRow rendering -- adversarial name stays text, never markup', () => {
  it('a hostile template name renders as text', () => {
    const html = renderToStaticMarkup(<PromptTemplateHeaderRow tpl={baseTemplate({ name: `custom_template ${XSS_SCRIPT}` })} label="Edit" onToggle={() => {}} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })
})
