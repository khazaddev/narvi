// SessionRail.tsx -- decision 6 ("'What happened?' as self-service") /
// row 83's own "sandbox rail (transitions, gen, fingerprint, boot phases,
// artifacts, cost incl. sub-task roll-up)". Four panels, each sourced from
// real data only -- see sandboxRail.ts's own top comment for the full
// accounting of what IS and is NOT available on the wire today (runtime
// fingerprint and correlation id are named there as a genuine, documented
// gap; this component renders an honest "not reported yet" for both
// rather than a fabricated value).
//
// Every artifact-sourced string rendered here (filename, PR/preview repo
// name) is plain React text content only -- never dangerouslySetInnerHTML
// -- and every artifact URL is passed through urlSafety.ts's isSafeHref
// before it is EVER used as an href; a `javascript:`-scheme or otherwise
// unsafe url renders as plain text instead of a link (this Step's own
// defining security requirement, §28: "the filename is attacker-supplied
// ... never render it as HTML" / "any artifact URL you render must go
// through urlSafety.ts").
import { Fragment } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'

import { listArtifacts } from '../api/endpoints'
import { sessionQueryKeys } from '../api/queryKeys'
import type { ParsedArtifact } from './artifactPayloads'
import { parseArtifacts } from './artifactPayloads'
import type { CostRollup } from './costRollup'
import { formatRelativeTime } from './relativeTime'
import type { SandboxRailModel } from './sandboxRail'
import { isSafeHref } from './urlSafety'

function formatUsd(usd: number | null): string {
  return usd === null ? '—' : `$${usd.toFixed(2)}`
}

function formatTokenCount(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

function formatSeconds(seconds: number | null): string {
  if (seconds === null) return 'running…'
  if (seconds < 10) return `${seconds.toFixed(1)} s`
  return `${Math.round(seconds)} s`
}

function statusTone(status: string | null): 'ok' | 'warn' | 'crit' | 'neutral' {
  if (status === 'ready') return 'ok'
  if (status === 'failed') return 'crit'
  if (status === 'pending' || status === 'spawning' || status === 'connecting' || status === 'booting') return 'warn'
  return 'neutral'
}

function SandboxPanel({ model }: { model: SandboxRailModel }) {
  return (
    <div>
      <h3>Sandbox</h3>
      <dl className="kv">
        <dt>status</dt>
        <dd>
          <span className={`chip ${statusTone(model.status)}`}>
            <span className="dot" />
            {model.status ?? 'not started'}
          </span>
        </dd>
        <dt>gen</dt>
        <dd>{model.gen ?? '—'}</dd>
        <dt>last seen</dt>
        <dd>{model.lastSeenAt ? formatRelativeTime(model.lastSeenAt) : '—'}</dd>
        {/* runtime fingerprint / correlation id: NOT available on the wire
            today -- see sandboxRail.ts's own top comment for the full
            "why" (sandbox-agent computes a real BootFingerprint but only
            ever logs it locally; correlation id is a per-request concept
            never persisted onto a session/sandbox row). Rendered honestly
            rather than omitted or fabricated, so the gap stays visible
            instead of silently disappearing from the UI. */}
        <dt>runtime</dt>
        <dd>not reported yet</dd>
        <dt>trace</dt>
        <dd>not reported yet</dd>
      </dl>
      {model.transitions.length > 0 && (
        <ul className="transitions">
          {model.transitions.map((t, i) => (
            <li key={t.id} className={i === model.transitions.length - 1 ? `now tone-${t.tone}` : `tone-${t.tone}`}>
              {t.label} · {new Date(t.at).toLocaleTimeString()}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function BootProgressPanel({ model }: { model: SandboxRailModel }) {
  if (model.bootPhases.length === 0) return null
  return (
    <div>
      <h3>Boot progress</h3>
      <dl className="kv">
        {model.bootPhases.map((p, i) => (
          <Fragment key={i}>
            <dt>{p.phase}</dt>
            <dd>
              {formatSeconds(p.seconds)}
              {p.endedAt !== null ? ' ✓' : ''}
            </dd>
          </Fragment>
        ))}
      </dl>
    </div>
  )
}

/** Exported for direct render-safety testing (__tests__/sessionRailRendering.test.tsx) -- takes a ParsedArtifact as a plain prop, no I/O, so it can be rendered via react-dom/server's renderToStaticMarkup exactly like Timeline.tsx's own precedent, without needing a QueryClient/fetch mock. */
export function ArtifactRow({ artifact }: { artifact: ParsedArtifact }) {
  const safe = isSafeHref(artifact.url)
  if (artifact.type === 'upload') {
    const failed = artifact.status === 'failed'
    return (
      <div className="art">
        <span className="ttl">
          {failed && (
            <span className="chip crit">
              <span className="dot" />
              failed
            </span>
          )}
          {artifact.filename ?? '(unnamed upload)'}
        </span>
        {failed && artifact.failureReason && <span className="sub">reason: {artifact.failureReason}</span>}
        {!failed &&
          (safe ? (
            <a href={artifact.url} target="_blank" rel="noreferrer noopener">
              Download →
            </a>
          ) : (
            <span className="sub">link unavailable</span>
          ))}
      </div>
    )
  }

  if (artifact.type === 'pr') {
    const repo = typeof artifact.metadata.repo === 'string' ? artifact.metadata.repo : null
    const number = typeof artifact.metadata.number === 'number' ? artifact.metadata.number : null
    return (
      <div className="art">
        <span className="ttl">{number !== null ? `PR #${number}` : 'Pull request'}</span>
        {repo && <span className="sub">{repo}</span>}
        {safe ? (
          <a href={artifact.url} target="_blank" rel="noreferrer noopener">
            View on GitHub →
          </a>
        ) : (
          <span className="sub">link unavailable</span>
        )}
      </div>
    )
  }

  // 'preview' (or an unrecognized future type -- rendered the same
  // generic way rather than dropped, this file's own top comment on
  // never crashing/silently discarding an artifact row).
  const repo = typeof artifact.metadata.repo === 'string' ? artifact.metadata.repo : null
  const sha = typeof artifact.metadata.sha === 'string' ? artifact.metadata.sha.slice(0, 7) : null
  return (
    <div className="art">
      <span className="ttl">{artifact.type === 'preview' ? 'Preview' : artifact.type}</span>
      <span className="sub">
        {sha ? `deployed at ${sha}` : 'deployed'}
        {repo ? ` · ${repo}` : ''}
      </span>
      {safe ? (
        <a href={artifact.url} target="_blank" rel="noreferrer noopener">
          Open preview →
        </a>
      ) : (
        <span className="sub">link unavailable</span>
      )}
    </div>
  )
}

function ArtifactsPanel({ sessionId }: { sessionId: string }) {
  const query = useQuery({
    queryKey: sessionQueryKeys.artifacts(sessionId),
    queryFn: ({ signal }) => listArtifacts(sessionId, signal),
  })
  const artifacts = query.data ? parseArtifacts(query.data.artifacts) : []

  return (
    <div>
      <h3>Artifacts</h3>
      {artifacts.length === 0 && <p className="rail-empty">Nothing yet.</p>}
      {artifacts.map((a, i) => (
        <div key={a.id} style={i > 0 ? { marginTop: 8 } : undefined}>
          <ArtifactRow artifact={a} />
        </div>
      ))}
    </div>
  )
}

function CostPanel({ cost }: { cost: CostRollup }) {
  return (
    <div>
      <h3>Cost</h3>
      <dl className="kv">
        <dt>this turn</dt>
        <dd>{formatUsd(cost.turnUsd)}</dd>
        <dt>session</dt>
        <dd>{formatUsd(cost.sessionUsd)}</dd>
        <dt>tokens</dt>
        <dd>
          {formatTokenCount(cost.sessionInputTokens)} in · {formatTokenCount(cost.sessionOutputTokens)} out
        </dd>
      </dl>
    </div>
  )
}

// ReviewLinksPanel: entry points into the code-review/release-review
// views (§26.1/§15, §12.2 items 2/9) -- always rendered, since the
// session DTO carries no "does this session have an associated PR"
// signal to gate on cheaply; a session with no PR simply gets a graceful
// "no associated pull request" state on the destination route itself
// (CodeReviewView/ReleaseReviewView's own isError branch), never a
// crash.
function ReviewLinksPanel({ sessionId }: { sessionId: string }) {
  return (
    <div>
      <h3>Review</h3>
      <div className="btnrow" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
        <Link to="/session/$sessionId/review" params={{ sessionId }} className="btn" style={{ textDecoration: 'none', textAlign: 'center' }}>
          Code review
        </Link>
        <Link to="/session/$sessionId/release-review" params={{ sessionId }} className="btn" style={{ textDecoration: 'none', textAlign: 'center' }}>
          Release review
        </Link>
      </div>
    </div>
  )
}

// PlanLinkPanel: entry point into the plan-mode view (§12.2 item 3) --
// always rendered, mirroring ReviewLinksPanel's own identical
// precedent immediately above and its own doc comment's reasoning: the
// session DTO carries no "does this session have a plan" signal to gate
// on cheaply, so a session with no plan simply gets PlanModeView's own
// graceful "no plan has been proposed" state on the destination route.
function PlanLinkPanel({ sessionId }: { sessionId: string }) {
  return (
    <div>
      <h3>Plan</h3>
      <Link to="/session/$sessionId/plan" params={{ sessionId }} className="btn" style={{ textDecoration: 'none', textAlign: 'center', display: 'block' }}>
        View plan
      </Link>
    </div>
  )
}

// WorkflowRunsLinkPanel: entry point into the workflow run view + human
// decision gate (§25.9/§25.10) -- always
// rendered, mirroring PlanLinkPanel's own identical precedent immediately
// above: the session DTO carries no "does this session have a workflow
// run" signal to gate on cheaply, so a session with none simply gets
// WorkflowRunsView's own graceful "no workflow runs have started" state on
// the destination route.
function WorkflowRunsLinkPanel({ sessionId }: { sessionId: string }) {
  return (
    <div>
      <h3>Workflow runs</h3>
      <Link to="/session/$sessionId/runs" params={{ sessionId }} className="btn" style={{ textDecoration: 'none', textAlign: 'center', display: 'block' }}>
        View runs
      </Link>
    </div>
  )
}

export function SessionRail({ sessionId, sandbox, cost }: { sessionId: string; sandbox: SandboxRailModel; cost: CostRollup }) {
  return (
    <aside className="rail" aria-label="Session details">
      <SandboxPanel model={sandbox} />
      <BootProgressPanel model={sandbox} />
      <ArtifactsPanel sessionId={sessionId} />
      <CostPanel cost={cost} />
      <PlanLinkPanel sessionId={sessionId} />
      <WorkflowRunsLinkPanel sessionId={sessionId} />
      <ReviewLinksPanel sessionId={sessionId} />
    </aside>
  )
}
