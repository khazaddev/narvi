// ReleaseReviewView.tsx -- §15's dedicated release-review screen (§12.2
// item 9): manifest table + aggregate-diff trigger banner + composition
// findings.
//
// Composition findings (§15.3's own actual composition-focused LLM pass)
// are NOT dispatched anywhere in this codebase's backend today -- only
// the pass's own TRIGGER decision is computed (see internal/app/
// releasereview/run.go's own doc comment, and httpapi/
// releasemanifestreadout.go's identical note). This view therefore never
// fabricates composition-finding cards: it renders the real trigger
// banner (why the conditional pass WOULD fire) and an honest "not yet
// available" state where the mockup draws two example finding cards,
// rather than inventing data no backend endpoint produces.
//
// Every third-party-authored string here (a constituent PR's own title, a
// manifest finding's own detail text) is plain React text content only --
// same discipline as CodeReviewView.tsx's own top comment.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'

import type { ReleaseManifestReadout } from '@narvi/contracts/rest-dtos'

import { getReleaseManifestReadout } from '../api/endpoints'
import { reviewQueryKeys } from '../api/queryKeys'
import { ciConclusionTone } from './reviewFormat'
import { truncateForDisplay } from './textSafety'

const MAX_FIELD_CHARS = 4000

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

function reviewStateChip(pr: { hasApprovingReview: boolean; mergedViaAdminOverride: boolean }) {
  if (pr.hasApprovingReview) {
    return (
      <span className="chip ok">
        <span className="dot" />
        approved
      </span>
    )
  }
  if (pr.mergedViaAdminOverride) {
    return (
      <span className="chip warn">
        <span className="dot" />
        admin override
      </span>
    )
  }
  return (
    <span className="chip crit">
      <span className="dot" />
      unreviewed
    </span>
  )
}

function manifestNote(pr: { hasApprovingReview: boolean; mergedViaAdminOverride: boolean; ciConclusion: string; wasReverted: boolean; revertReviewState: string }): string | null {
  if (!pr.hasApprovingReview && pr.mergedViaAdminOverride) return 'merged without an approving review'
  if (pr.ciConclusion === 'failure') return 'red at merge'
  if (pr.wasReverted && pr.revertReviewState === 'not_reviewed') return 'revert itself was not reviewed'
  return null
}

/** ReleaseManifestBody: the readout-to-markup half, taking already-fetched data as a plain prop -- exported for direct render-safety testing (mirrors SessionRail.tsx's own ArtifactRow precedent), no I/O of its own. */
export function ReleaseManifestBody({ readout }: { readout: ReleaseManifestReadout }) {
  const flaggedCount = readout.mergedPrs.filter((pr) => manifestNote(pr) !== null).length

  return (
    <div className="timeline">
      {!readout.computed && (
        <div className="card">
          <p>No release manifest check has run for this PR yet.</p>
        </div>
      )}

      {readout.computed && (
        <div className="card">
          <div className="who">
            <span className="avatar b">R</span>
            <b>Manifest</b>
            <time>always runs</time>
          </div>
          <div style={{ overflowX: 'auto' }}>
            <table className="atable">
              <thead>
                <tr>
                  <th>PR</th>
                  <th>Review</th>
                  <th>CI @ merge</th>
                  <th>Notes</th>
                </tr>
              </thead>
              <tbody>
                {readout.mergedPrs.map((pr) => {
                  const note = manifestNote(pr)
                  return (
                    <tr key={pr.number} style={note ? { background: pr.ciConclusion === 'failure' ? 'var(--warn-soft)' : !pr.hasApprovingReview ? 'var(--crit-soft)' : 'var(--warn-soft)' } : undefined}>
                      <td>
                        <span className="num">#{pr.number}</span> <span className="trig"><T text={pr.title} /></span>
                      </td>
                      <td>{reviewStateChip(pr)}</td>
                      <td>
                        <span className={`chip ${ciConclusionTone(pr.ciConclusion)}`}>
                          <span className="dot" />
                          {pr.ciConclusion === 'success' ? 'green' : pr.ciConclusion === 'failure' ? 'red at merge' : 'unknown'}
                        </span>
                      </td>
                      <td>{note ?? <span className="num">—</span>}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
          <div className="verdict-foot">
            <span className="lock">⛨ posted via server-side verdict tool</span>
            <span>
              · compliance check, not a risk verdict · {flaggedCount} of {readout.mergedPrs.length} PRs flagged
            </span>
            {readout.coveragePartial && <span>· coverage was partial for this check</span>}
          </div>
        </div>
      )}

      {readout.computed && (
        <div className="card">
          <div className="who">
            <b>Aggregate diff review</b>
            <span className={`chip ${readout.aggregateReviewTriggered ? 'warn' : 'neutral'}`} style={{ marginLeft: 8 }}>
              <span className="dot" />
              {readout.aggregateReviewTriggered ? 'triggered' : 'not triggered'}
            </span>
          </div>
          {readout.aggregateReviewTriggered && readout.aggregateReviewTriggerReasons.length > 0 && (
            <p style={{ margin: '6px 0 0', color: 'var(--muted)', fontSize: 'var(--text-base)' }}>
              Trigger: {readout.aggregateReviewTriggerReasons.map((r, i) => (i === 0 ? <T key={i} text={r} /> : <span key={i}> · <T text={r} /></span>))}
            </p>
          )}
          {!readout.aggregateReviewTriggered && <p style={{ margin: '6px 0 0', color: 'var(--faint)', fontSize: 'var(--text-base)' }}>None of the composition criteria were met for this release.</p>}
        </div>
      )}

      {readout.computed && (
        <div className="card">
          <div className="who">
            <b>Composition findings</b>
          </div>
          <p style={{ color: 'var(--faint)', fontSize: 'var(--text-base)' }}>
            Not yet available: the composition-focused aggregate diff review pass is not dispatched by this deployment yet -- only its own trigger decision, above, is computed today. This section will populate once that pass ships.
          </p>
        </div>
      )}
    </div>
  )
}

export function ReleaseReviewView({ sessionId }: { sessionId: string }) {
  const readoutQuery = useQuery({
    queryKey: reviewQueryKeys.releaseManifest(sessionId),
    queryFn: ({ signal }) => getReleaseManifestReadout(sessionId, signal),
  })

  if (readoutQuery.isPending) {
    return (
      <div className="session-state" aria-live="polite">
        <p>Loading release manifest…</p>
      </div>
    )
  }
  if (readoutQuery.isError) {
    return (
      <div className="session-state" role="alert">
        <p>Couldn't load this release manifest. This session may not be a release-review session.</p>
      </div>
    )
  }

  const readout = readoutQuery.data

  return (
    <div className="app one">
      <section className="main">
        <header className="sess-head">
          <Link to="/session/$sessionId" params={{ sessionId }} className="repo" style={{ textDecoration: 'none' }}>
            ← Session
          </Link>
          <span className="title">Release review · {readout.repoFullName}</span>
          <span className="repo">
            PR #{readout.prNumber}
            {readout.baseRef && readout.headRef ? ` · ${readout.baseRef} → ${readout.headRef}` : ''} · {readout.constituentPrCount} PRs
          </span>
          {readout.aggregateReviewTriggered && (
            <span className="chip warn">
              <span className="dot" />
              aggregate review triggered
            </span>
          )}
          <span className="spacer" />
        </header>

        <ReleaseManifestBody readout={readout} />
      </section>
    </div>
  )
}
