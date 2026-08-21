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
// including the per-repo auto-merge/auto-retrigger-review toggles: the
// per-repository view (§21, §26.7, §26.8, §4.1.2) owns all of them, and
// this screen owns the org-level Settings surfaces. This screen owns
// ORG-LEVEL
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

/**
 * NotBuiltYet is what a tab with no real surface behind it renders.
 *
 * It used to say "Not part of this Step." and then cite a row of the
 * project's own build schedule by filename. Both halves were written for a
 * developer reading the repository, not for the operator who actually
 * reaches this screen: an operator has no Step, and cannot open a planning
 * document being cited at them. The honest thing to tell them is what is
 * missing and whether it is coming.
 */
function NotBuiltYet({ note }: { note: string }) {
  return (
    <div className="panel">
      <p className="notavailable">
        <b>Nothing to configure here yet.</b> {note}
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
            {tab === 'general' && <NotBuiltYet note="There are no org-wide general settings. Everything configurable today is scoped to an environment, a repository or a member." />}
            {tab === 'environments' && <EnvironmentsPanel />}
            {tab === 'secrets' && <SecretsPanel />}
            {tab === 'members' && <MembersPanel />}
            {tab === 'integrations' && <NotBuiltYet note="Slack, Linear and GitHub connections, ChatGPT account linking and cloud-identity signing-key rotation will live here. They are not built yet." />}
            {tab === 'models' && <NotBuiltYet note="There is nothing to manage: the model catalogue is read from the configured providers, and you choose a model per session in the composer." />}
            {tab === 'prompt-templates' && <PromptTemplatesPanel />}
            {tab === 'image-builds' && <NotBuiltYet note="Image-build history for each environment — fingerprint, duration and retry backoff — is not recorded anywhere yet, so there is nothing to show." />}
          </div>
        </div>
      </section>
    </div>
  )
}
