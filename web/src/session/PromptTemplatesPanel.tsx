// PromptTemplatesPanel.tsx -- Settings -> Prompt templates (§18.6, Step
// 86): list, edit (upsert), and preview-assembled-prompt.
//
// # What this panel declines to render, and why
//
// mockups.html's own template rows show a version badge ("v7"), an
// active/shadow status chip, a divergence percentage, and an "edited by"
// attribution. prompt_templates has exactly three columns --
// name/template/updated_at (migrations/000033_intent_classifier.up.sql's
// own top comment: "versioning/audit history is explicitly NOT required
// by this Step") -- no version counter, no enabled/disabled concept, no
// shadow-mode comparison, no editor attribution. This panel renders every
// column that IS real (name, template text, updatedAt) and an honest
// note where the richer mockup fields would go, rather than inventing a
// version number or a fabricated "edited by @someone" -- the same
// discipline classifiertemplates.go's own UpsertIntentTemplate doc
// comment already states server-side ("This handler makes no attempt to
// invent versioning/disabling that does not exist").
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import type { PromptTemplate } from '@narvi/contracts/rest-dtos'

import { listPromptTemplates, previewIntentTemplate, upsertIntentTemplate } from '../api/endpoints'
import { ApiError } from '../api/http'
import { promptTemplateQueryKeys } from '../api/queryKeys'
import { meQueryOptions } from '../auth/session'
import { formatDateTime } from './settingsFormat'
import { truncateForDisplay } from './textSafety'

const MAX_FIELD_CHARS = 4000

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

/** PromptTemplateHeaderRow renders one template's own name/updated/toggle header -- exported for direct render-safety testing: tpl.name is an admin-chosen but never Narvi-validated key (prompt_templates.name, a free-text TEXT PRIMARY KEY, classifiertemplates.go's own doc comment), and must render as plain text only. */
export function PromptTemplateHeaderRow({ tpl, label, onToggle }: { tpl: PromptTemplate; label: string; onToggle: () => void }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
      <b>
        <T text={tpl.name} />
      </b>
      <span className="meta">updated {formatDateTime(tpl.updatedAt)}</span>
      <span style={{ marginLeft: 'auto' }}>
        <button type="button" className="btn" style={{ padding: '2px 9px', fontSize: 11 }} onClick={onToggle}>
          {label}
        </button>
      </span>
    </div>
  )
}

function TemplateEditor({ tpl, canManage, onDone }: { tpl: PromptTemplate; canManage: boolean; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [text, setText] = useState(tpl.template)
  const [previewVars, setPreviewVars] = useState('{}')
  const [previewError, setPreviewError] = useState<string | null>(null)

  const previewMutation = useMutation({
    mutationFn: () => {
      let vars: Record<string, string> = {}
      try {
        vars = JSON.parse(previewVars) as Record<string, string>
      } catch {
        throw new Error('vars must be valid JSON')
      }
      return previewIntentTemplate({ name: tpl.name, template: text, vars })
    },
    onError: (err) => setPreviewError(err instanceof Error ? err.message : 'Preview failed'),
    onSuccess: () => setPreviewError(null),
  })
  const saveMutation = useMutation({
    mutationFn: () => upsertIntentTemplate({ name: tpl.name, template: text }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: promptTemplateQueryKeys.list() })
      onDone()
    },
  })

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, paddingTop: 8 }}>
      <textarea
        className="btn"
        style={{ width: '100%', minHeight: 120, textAlign: 'left', fontFamily: 'var(--mono)', fontSize: 'var(--text-sm)', resize: 'vertical' }}
        readOnly={!canManage}
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="formrow">
        <input placeholder='preview vars, e.g. {"surface":"web"}' value={previewVars} onChange={(e) => setPreviewVars(e.target.value)} style={{ flex: 1 }} />
        <button type="button" className="btn" disabled={previewMutation.isPending} onClick={() => previewMutation.mutate()}>
          {previewMutation.isPending ? 'Assembling…' : 'Preview assembled prompt'}
        </button>
        {canManage && (
          <button type="button" className="btn primary" disabled={saveMutation.isPending} onClick={() => saveMutation.mutate()}>
            {saveMutation.isPending ? 'Saving…' : 'Save'}
          </button>
        )}
        <button type="button" className="btn" onClick={onDone}>
          Close
        </button>
      </div>
      {previewError && <p className="sidebar-notice">{previewError}</p>}
      {previewMutation.isError && !previewError && <p className="sidebar-notice">{previewMutation.error instanceof ApiError ? previewMutation.error.message : 'Preview failed.'}</p>}
      {previewMutation.isSuccess && (
        <pre style={{ background: 'var(--ground)', border: '1px solid var(--line)', borderRadius: 6, padding: 10, fontSize: 'var(--text-sm)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
          <T text={previewMutation.data.assembled} />
        </pre>
      )}
      {saveMutation.isError && <p className="sidebar-notice">{saveMutation.error instanceof ApiError ? saveMutation.error.message : 'Save failed.'}</p>}
    </div>
  )
}

export function PromptTemplatesPanel() {
  const meQuery = useQuery(meQueryOptions)
  const canManage = meQuery.data?.role === 'admin'
  const [editing, setEditing] = useState<string | null>(null)

  const listQuery = useQuery({
    queryKey: promptTemplateQueryKeys.list(),
    queryFn: ({ signal }) => listPromptTemplates(signal),
    retry: false,
  })

  const forbidden = listQuery.isError && listQuery.error instanceof ApiError && listQuery.error.status === 403

  return (
    <div className="panel">
      <h4>Prompt templates</h4>
      <p className="ph">editable in production · name/template/updatedAt only -- no version counter, shadow mode, or edit attribution exists yet (see this file's own top doc comment)</p>
      {forbidden && <p className="notavailable">Prompt templates are admin-only. Your role cannot view this panel -- enforced server-side (authz.ActionActivatePromptTemplate), not merely hidden here.</p>}
      {!forbidden && listQuery.isPending && <p className="rail-empty">Loading templates…</p>}
      {!forbidden && listQuery.isError && <p className="rail-empty">Couldn't load prompt templates.</p>}
      {!forbidden && listQuery.isSuccess && listQuery.data.promptTemplates.length === 0 && <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>No templates exist yet.</p>}
      {!forbidden &&
        listQuery.isSuccess &&
        listQuery.data.promptTemplates.map((tpl) => (
          <div className="tplrow" key={tpl.name} style={{ flexDirection: 'column', alignItems: 'stretch' }}>
            <PromptTemplateHeaderRow tpl={tpl} label={editing === tpl.name ? 'Hide' : canManage ? 'Edit' : 'View'} onToggle={() => setEditing(editing === tpl.name ? null : tpl.name)} />
            {editing === tpl.name && <TemplateEditor tpl={tpl} canManage={canManage} onDone={() => setEditing(null)} />}
          </div>
        ))}
    </div>
  )
}
