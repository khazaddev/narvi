// AnalyticsView.tsx -- §12.2 items 5-6's own Analytics screen.
//
// # What renders as real data, and what renders "not available yet"
//
// mockups.html's own Analytics view draws 5 platform-wide KPI tiles
// (Sessions, Success rate, False failures, Cost, Boot p95) and 4 charts
// (Sessions per day, Cost by model, Review finding outcomes, Top failure
// reasons). Row 86's own technical-plan citations (§14.1, §14.2, §21,
// §24, §27) name exactly ONE analytics read model anywhere in this
// codebase: §21.1's GetReviewAnalytics (repo-scoped, over
// review_verdicts/review_findings). Nothing anywhere designs a
// platform-wide session-count/cost/boot-latency rollup -- no table, no
// query, no Step. Rather than invent one (a materially large, genuinely
// separate read-model project this Step has no mandate to design), this
// view renders those 5 tiles and 3 of the 4 charts as an explicit,
// honest "not available yet" -- and builds the ONE section that DOES
// have a real, designed backing: the review-risk analytics section
// (KPI tiles, trend chart, top-risk table), fed by §21.1's own read
// model, WITH the "not available yet" states that model's own
// TimeseriesComputed/TopRiskDriversComputed/FindingOutcomesComputed/
// DigestContestationRateComputed sentinels are specifically designed to
// carry -- row 86's own explicit instruction. The digest-scope section
// (§21.3) is likewise real, read-only, and derived rather than a
// fabricated "cadence" setting -- see api/endpoints.ts's own
// getRepoDigestScope doc comment.
//
// Both real sections are repo-scoped (the underlying REST routes are
// GET /api/repos/:owner/:repo/...), so this view takes a plain
// owner/repo text input rather than a dropdown -- mirroring
// AutomationsView.tsx's own free-text repo entry precedent (no
// repo-enumeration endpoint exists for a client to populate a dropdown
// from; a caller who does not already know a repo name types it,
// exactly like reposettings.go's own resolveKnownRepo -- an unknown repo
// 404s honestly rather than ever being silently offered as a choice).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { getRepoDigestScope, getReviewAnalytics } from '../api/endpoints'
import { ApiError } from '../api/http'
import { repoAnalyticsQueryKeys, repoDigestScopeQueryKeys } from '../api/queryKeys'
import { lookbackDaysLabel } from './settingsFormat'
import { truncateForDisplay } from './textSafety'

const TAG_LABEL: Record<string, string> = {
  auth: 'auth',
  migrations: 'migrations',
  contracts: 'contracts',
  secrets: 'secrets',
  infra: 'infra',
  public_api: 'public API',
  data_layer: 'data layer',
  dependencies: 'dependencies',
}

const STATUS_LABEL: Record<string, string> = {
  open: 'open',
  rebutted: 'rebutted',
  fix_pending: 'fix pending',
  fix_open: 'fix open',
  fix_merged: 'fix merged',
  fix_applied: 'fix applied',
}

const MAX_FIELD_CHARS = 500
function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

function NotAvailableTile({ label }: { label: string }) {
  return (
    <div className="tile">
      <span className="tl">{label}</span>
      <span className="big" style={{ color: 'var(--faint)', fontSize: 15 }}>
        not available yet
      </span>
    </div>
  )
}

function ReviewRiskSection({ owner, repo }: { owner: string; repo: string }) {
  const enabled = owner.trim().length > 0 && repo.trim().length > 0
  const repoFullName = `${owner}/${repo}`
  const query = useQuery({
    queryKey: repoAnalyticsQueryKeys.reviewAnalytics(repoFullName),
    queryFn: ({ signal }) => getReviewAnalytics(owner, repo, signal),
    enabled,
    retry: false,
  })

  if (!enabled) return <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>Enter a repo above to load its review-risk analytics.</p>
  if (query.isPending) return <p className="rail-empty">Loading…</p>
  if (query.isError && query.error instanceof ApiError && query.error.status === 404) return <p className="notavailable">Repo not known to this deployment yet.</p>
  if (query.isError) return <p className="rail-empty">Couldn't load review analytics.</p>

  const data = query.data
  const maxDriverCount = data.topRiskDriversComputed && data.topRiskDrivers ? Math.max(1, ...data.topRiskDrivers.map((d) => d.count)) : 1
  const totalOutcomes = data.findingOutcomesComputed && data.findingOutcomes ? data.findingOutcomes.reduce((sum, o) => sum + o.count, 0) : 0

  return (
    <div className="charts2">
      <div className="chart">
        <h4>Shippable classification, per day</h4>
        <p className="ch">§21.1's own timeseries rollup</p>
        {!data.timeseriesComputed && <p className="notavailable">Not available yet -- no review verdicts posted for this repo in the analytics window.</p>}
        {data.timeseriesComputed && data.timeseries && (
          <>
            <div className="legend">
              <span>
                <i style={{ background: 'var(--ok)' }} /> auto
              </span>
              <span>
                <i style={{ background: 'var(--warn)' }} /> needs human
              </span>
              <span>
                <i style={{ background: 'var(--crit)' }} /> block
              </span>
            </div>
            <div className="cols" role="img" aria-label="Daily verdict classification">
              {data.timeseries.map((b) => {
                const total = Math.max(1, b.autoCount + b.needsHumanCount + b.blockCount)
                const scale = 90 / total
                return (
                  <div className="col" key={b.day} title={`${b.day}: ${b.autoCount} auto, ${b.needsHumanCount} needs human, ${b.blockCount} block`}>
                    <div className="seg s-ok" style={{ height: `${b.autoCount * scale}px` }} />
                    <div className="seg s-crit" style={{ height: `${b.blockCount * scale}px` }} />
                    <div className="seg s-neu" style={{ height: `${b.needsHumanCount * scale}px`, background: 'var(--warn)' }} />
                  </div>
                )
              })}
            </div>
          </>
        )}
      </div>

      <div className="chart">
        <h4>Top risk drivers</h4>
        <p className="ch">§21.1's own top-risk-driver breakdown</p>
        {!data.topRiskDriversComputed && <p className="notavailable">Not available yet -- no review verdicts posted for this repo in the analytics window.</p>}
        {data.topRiskDriversComputed && data.topRiskDrivers && data.topRiskDrivers.length === 0 && <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>Verdicts exist, but none tagged a risk driver.</p>}
        {data.topRiskDriversComputed && data.topRiskDrivers && data.topRiskDrivers.length > 0 && (
          <div className="hbars">
            {data.topRiskDrivers.map((d) => (
              <div className="hbar" key={d.tag}>
                <span className="hl">{TAG_LABEL[d.tag] ?? d.tag}</span>
                <span className="track">
                  <span className="fill" style={{ width: `${(d.count / maxDriverCount) * 100}%` }} />
                </span>
                <span className="hv">{d.count}</span>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="chart">
        <h4>Review · finding outcomes</h4>
        <p className="ch">§21.1's own "Review finding outcomes" KPI</p>
        {!data.findingOutcomesComputed && <p className="notavailable">Not available yet -- no review findings reported for this repo in the analytics window.</p>}
        {data.findingOutcomesComputed && data.findingOutcomes && data.findingOutcomes.length > 0 && (
          <>
            <div className="outcomebar" role="img" aria-label="Finding outcome distribution">
              {data.findingOutcomes.map((o) => (
                <i key={o.status} style={{ width: `${(o.count / Math.max(1, totalOutcomes)) * 100}%`, background: 'var(--accent)' }} title={`${STATUS_LABEL[o.status] ?? o.status} · ${o.count}`} />
              ))}
            </div>
            <div className="olabels">
              {data.findingOutcomes.map((o) => (
                <span key={o.status}>
                  <i style={{ background: 'var(--accent)' }} />
                  {STATUS_LABEL[o.status] ?? o.status} {o.count}
                </span>
              ))}
            </div>
          </>
        )}
      </div>

      <div className="chart">
        <h4>Digest contestation rate</h4>
        <p className="ch">§26.5's own "digest precision" KPI -- deep-path arch recaps only</p>
        {!data.digestContestationRateComputed && <p className="notavailable">Not available yet -- no deep-path verdicts posted for this repo in the analytics window.</p>}
        {data.digestContestationRateComputed && data.digestContestationRatePercent !== null && <div className="tile" style={{ border: 'none', padding: 0 }}>
          <span className="big">{data.digestContestationRatePercent.toFixed(1)}%</span>
        </div>}
      </div>
    </div>
  )
}

function DigestScopeSection({ owner, repo }: { owner: string; repo: string }) {
  const enabled = owner.trim().length > 0 && repo.trim().length > 0
  const repoFullName = `${owner}/${repo}`
  const query = useQuery({
    queryKey: repoDigestScopeQueryKeys.detail(repoFullName),
    queryFn: ({ signal }) => getRepoDigestScope(owner, repo, signal),
    enabled,
    retry: false,
  })

  if (!enabled) return null
  if (query.isPending) return <p className="rail-empty">Loading digest scope…</p>
  if (query.isError && query.error instanceof ApiError && query.error.status === 404) return null
  if (query.isError) return <p className="rail-empty">Couldn't load digest scope.</p>

  const data = query.data
  return (
    <div className="chart">
      <h4>Digest scope</h4>
      <p className="ch">
        Sent daily on a fixed schedule. The recipients below are derived from which channels threaded a review session for this repository in the last {lookbackDaysLabel(data.lookbackDays)}; they are not configurable
        (§21.3).
      </p>
      {data.slackChannelIds.length === 0 && data.linearOrganizationIds.length === 0 && <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>No Slack channel or Linear organization has threaded a review session for this repository recently, so nothing is in scope to receive its digest.</p>}
      {data.slackChannelIds.length > 0 && (
        <p>
          <b>Slack:</b> {data.slackChannelIds.map((id, i) => (
            <span key={id}>
              {i > 0 && ', '}
              <T text={id} />
            </span>
          ))}
        </p>
      )}
      {data.linearOrganizationIds.length > 0 && (
        <p>
          <b>Linear:</b> {data.linearOrganizationIds.map((id, i) => (
            <span key={id}>
              {i > 0 && ', '}
              <T text={id} />
            </span>
          ))}
        </p>
      )}
    </div>
  )
}

export function AnalyticsView() {
  const [owner, setOwner] = useState('')
  const [repo, setRepo] = useState('')

  return (
    <div className="app one">
      <section className="main">
        <div className="anav">
          {/*
            These read as dropdowns in the mockup and were rendered as such
            here, wired to nothing -- and "Last 7 days" additionally named a
            window the page does not use: every section below is fixed at 30
            days (ReviewVerdictAnalyticsWindow, DigestChannelDiscoveryLookback).
            An inert control that states the wrong number is worse than no
            control, so this states the window the data actually covers and
            claims no filtering that does not exist.
          */}
          <span className="ph">Last 30 days · all sessions</span>
          <span style={{ flex: 1 }} />
          <span className="cost">Platform-wide KPIs are not available yet — nothing computes them.</span>
        </div>

        <div className="abody">
          <div className="kpis">
            <NotAvailableTile label="Sessions" />
            <NotAvailableTile label="Success rate" />
            <NotAvailableTile label="False failures" />
            <NotAvailableTile label="Cost" />
            <NotAvailableTile label="Boot p95" />
          </div>

          <div className="panel">
            <h4>Review-risk analytics &amp; digest scope</h4>
            <p className="ph">Live per-repository figures (§21.1, §21.3).</p>
            <div className="formrow">
              <input placeholder="owner" value={owner} onChange={(e) => setOwner(e.target.value)} />
              <input placeholder="repo" value={repo} onChange={(e) => setRepo(e.target.value)} />
            </div>
            <ReviewRiskSection owner={owner} repo={repo} />
            <DigestScopeSection owner={owner} repo={repo} />
          </div>
        </div>
      </section>
    </div>
  )
}
