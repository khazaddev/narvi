// SessionsListView.tsx -- the sessions list's own top-level surface,
// docs/IMPLEMENTATION_PLAN.md row 87's own "the sessions list moves to a
// second tab": now that `/` is the decision-inbox home view (§16), the
// session list -- previously reachable only as SessionSidebar.tsx's own
// narrow rail inside an already-open session -- gets a real, full-page
// home of its own at `/sessions` so a signed-in user can browse sessions
// without opening one first.
//
// Deliberately reuses listSessions/sessionListQueryKeys and
// deriveStatusChip/deriveBootProgress/SourceIcon -- the SAME data source
// and status taxonomy SessionSidebar.tsx already established (decision 1:
// "statuses that tell the truth") -- rather than a second, independently-
// drifting read of the same table. This is a plain table layout
// (AutomationsView.tsx's own `.atable` precedent), not a copy of the
// sidebar's own narrow-rail markup, since a full page has the width to
// show more per row (repo, source, updated) without truncating.
//
// session.title is rendered as plain JSX text content only (React's
// default escaping), mirroring SessionSidebar.tsx's own identical,
// already-shipped treatment of the same field -- this view adds no new
// rendering-safety surface beyond what that established precedent already
// covers.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'

import { listSessions } from '../api/endpoints'
import { sessionListQueryKeys } from '../api/queryKeys'
import { formatRelativeTime } from './relativeTime'
import { deriveBootProgress, deriveStatusChip } from './sessionStatus'
import { SourceIcon } from './SourceIcon'

type Filter = 'mine' | 'all'

export function SessionsListView() {
  const [filter, setFilter] = useState<Filter>('mine')

  const query = useQuery({
    queryKey: sessionListQueryKeys.list(filter),
    queryFn: ({ signal }) => listSessions(filter, signal),
    // Mirrors SessionSidebar.tsx's own refetchInterval exactly -- same
    // data source, same "no live-update mechanism, but stale data
    // actively misleads" reasoning.
    refetchInterval: 15_000,
  })

  return (
    <div className="app one">
      <section className="main">
        <div className="toolbar">
          <select className="sel" value={filter} onChange={(e) => setFilter(e.target.value === 'all' ? 'all' : 'mine')} aria-label="Filter sessions">
            <option value="mine">My sessions</option>
            <option value="all">All sessions</option>
          </select>
        </div>

        {query.isPending && (
          <div className="session-state" aria-live="polite">
            <p>Loading sessions…</p>
          </div>
        )}
        {query.isError && (
          <div className="session-state" role="alert">
            <p>Couldn't load sessions.</p>
          </div>
        )}
        {query.isSuccess && query.data.sessions.length === 0 && (
          <div className="session-state">
            <p>No sessions yet.</p>
          </div>
        )}
        {query.isSuccess && query.data.sessions.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="atable">
              <thead>
                <tr>
                  <th>Session</th>
                  <th>Status</th>
                  <th>Repo</th>
                  <th>Source</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {query.data.sessions.map((session) => {
                  const chip = deriveStatusChip(session, session.sandboxStatus)
                  const boot = deriveBootProgress(session.sandboxStatus)
                  const primaryRepo = session.repos[0]
                  return (
                    <tr key={session.id}>
                      <td>
                        <Link to="/session/$sessionId" params={{ sessionId: session.id }} style={{ color: 'var(--ink)', textDecoration: 'none', fontWeight: 600 }}>
                          {session.title ?? '(untitled session)'}
                        </Link>
                        {boot !== null && (
                          <div className="boot-mini" role="progressbar" aria-valuenow={boot.index} aria-valuemin={0} aria-valuemax={boot.total} style={{ maxWidth: 120 }}>
                            <i style={{ width: `${Math.round((boot.index / boot.total) * 100)}%` }} />
                          </div>
                        )}
                      </td>
                      <td>
                        <span className={`chip ${chip.tone}`}>
                          <span className="dot" />
                          {chip.label}
                        </span>
                      </td>
                      <td className="trig">{primaryRepo ? primaryRepo.name : '—'}</td>
                      <td>
                        <SourceIcon source={session.spawnSource} />
                      </td>
                      <td className="num">{formatRelativeTime(session.updatedAt)} ago</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
