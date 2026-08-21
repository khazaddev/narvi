// SettingsView.tsx -- §12.2 item 5's own Settings screen:
// Environments, Secrets, Members & access, Prompt templates, plus the
// §27 enterprise-glue surfaces the row's own text calls out (cloud
// identity & cluster bindings, per-Environment docker/egress display,
// OpenCode config editor) folded into the Environments panel, since each
// of those sub-resources is itself keyed by environments.id.
//
// # Where this Step drew the line against row 89 ("ui repository
// settings")
//
// Row 89 owns the per-repository surface in full: the four automation
// toggles (auto_merge_enabled/auto_retrigger_review_enabled/
// sentinel_autofix_enabled/description_autofix_enabled),
// block_on_high_risk, review-depth/cost-budget config, and
// sensitive_blast_radius_tags/rwx_preview_* -- none of that lives here,
// including the per-repo auto-merge/auto-retrigger-review toggles row
// 86's own plan-row text also mentions (docs/IMPLEMENTATION_PLAN.md's own
// explicit tie-breaker: "row 89 owns the per-repo view and this Step owns
// the org-level Settings screens"). This screen owns ORG-LEVEL
// configuration only: Environments, Secrets (repo/environment/global
// scoped, but the SCOPE PICKER lives here, not a per-repo settings page),
// Members & access, Prompt templates.
//
// # A nav tab for every mockup entry, real content behind only some
//
// mockups.html's own Settings nav draws 8 entries (General, Environments,
// Secrets, Members & access, Integrations, Models, Prompt templates,
// Image builds). Four of those have no backing surface THIS Step owns:
// Integrations is row 90's own screen ("ui integrations & provider
// accounts" -- Slack/Linear/GitHub/ChatGPT/cloud-identity-rotation on one
// unified screen); General/Models/Image builds name no §-cited data model
// anywhere in row 86's own citations (§14.1, §14.2, §21, §24, §27) and
// building one would be inventing scope, not implementing it. All 8 tabs
// are still drawn (visual parity with the mockup, which the UI phase's
// own screenshot-review exit criterion requires), but the 4 without a
// real surface render an explicit, honest "not built here" notice naming where
// that surface actually lives -- never a fabricated panel.
import { useState } from 'react'

import { EnvironmentsPanel } from './EnvironmentsPanel'
import { MembersPanel } from './MembersPanel'
import { PromptTemplatesPanel } from './PromptTemplatesPanel'
import { SecretsPanel } from './SecretsPanel'

type SettingsTab = 'general' | 'environments' | 'secrets' | 'members' | 'integrations' | 'models' | 'prompt-templates' | 'image-builds'

const TABS: { id: SettingsTab; label: string }[] = [
  { id: 'general', label: 'General' },
  { id: 'environments', label: 'Environments' },
  { id: 'secrets', label: 'Secrets' },
  { id: 'members', label: 'Members & access' },
  { id: 'integrations', label: 'Integrations' },
  { id: 'models', label: 'Models' },
  { id: 'prompt-templates', label: 'Prompt templates' },
  { id: 'image-builds', label: 'Image builds' },
]

function NotPartOfThisStep({ ownerNote }: { ownerNote: string }) {
  return (
    <div className="panel">
      <p className="notavailable">
        <b>Not part of this Step.</b> {ownerNote}
      </p>
    </div>
  )
}

export function SettingsView() {
  const [tab, setTab] = useState<SettingsTab>('environments')

  return (
    <div className="app one">
      <section className="main">
        <div className="settings">
          <nav className="setnav" aria-label="Settings sections">
            {TABS.map((t) => (
              <button key={t.id} type="button" className={tab === t.id ? 'on' : ''} onClick={() => setTab(t.id)} aria-current={tab === t.id ? 'page' : undefined}>
                {t.label}
              </button>
            ))}
          </nav>
          <div className="setbody">
            {tab === 'general' && <NotPartOfThisStep ownerNote="No §-cited data model exists for org-wide general settings yet -- nothing in docs/TECHNICAL_PLAN.md names one." />}
            {tab === 'environments' && <EnvironmentsPanel />}
            {tab === 'secrets' && <SecretsPanel />}
            {tab === 'members' && <MembersPanel />}
            {tab === 'integrations' && <NotPartOfThisStep ownerNote="Integrations (Slack/Linear/GitHub connections, ChatGPT linking, cloud-identity signing-key rotation) is docs/IMPLEMENTATION_PLAN.md row 90's own screen." />}
            {tab === 'models' && <NotPartOfThisStep ownerNote="No settings-level model-catalog management surface is named by row 86's own citations -- GET /api/models already backs the composer's model selector directly." />}
            {tab === 'prompt-templates' && <PromptTemplatesPanel />}
            {tab === 'image-builds' && <NotPartOfThisStep ownerNote="No per-Environment image-build observability read model exists yet (fingerprint/duration/backoff) -- see EnvironmentsPanel's own doc comment for the same gap on the Environments card itself." />}
          </div>
        </div>
      </section>
    </div>
  )
}
