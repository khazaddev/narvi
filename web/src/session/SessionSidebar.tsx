// SessionSidebar.tsx -- decision 1 ("Statuses that tell the truth") +
// decision 31 ("The source stays attached to the session"): the session
// workspace's own left rail (mockups.html's own `.sidebar` in the Session
// view) -- status chips, session-source icons, the My sessions/All
// creator filter (§12.2 item 1: "'My sessions' = created or joined /
// 'All sessions'; no 'Team' option"), and the compact "booting n/m"
// indicator.
import { useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { listSessions } from '../api/endpoints'
import { sessionListQueryKeys } from '../api/queryKeys'
import { deriveBootProgress, deriveStatusChip } from './sessionStatus'
import { formatRelativeTime } from './relativeTime'
import { SourceIcon } from './SourceIcon'

type Filter = 'mine' | 'all'

export function SessionSidebar() {
  const [filter, setFilter] = useState<Filter>('mine')
  const params = useParams({ strict: false })
  const activeSessionId = params.sessionId

  const query = useQuery({
    queryKey: sessionListQueryKeys.list(filter),
    queryFn: ({ signal }) => listSessions(filter, signal),
    // The list is a background-refreshed convenience, not this Step's own
    // live-update mechanism (a session's OWN open view gets live updates
    // via the WS pipeline, useSessionStream) -- refetch on an interval
    // generous enough to notice another session finishing/booting without
    // hammering the endpoint on every render.
    refetchInterval: 15_000,
  })

  return (
    <nav className="sidebar" aria-label="Sessions">
      <div className="side-head">
        <button type="button" className="newbtn" disabled title="Not available yet">
          New session
        </button>
      </div>
      <div className="side-label">
        Sessions
        <select
          className="sel sidebar-filter"
          aria-label="Filter sessions"
          value={filter}
          onChange={(e) => setFilter(e.target.value === 'all' ? 'all' : 'mine')}
        >
          <option value="mine">My sessions</option>
          <option value="all">All sessions</option>
        </select>
      </div>

      {query.isPending && (
        <p className="sidebar-notice" aria-live="polite">
          Loading sessions…
        </p>
      )}
      {query.isError && (
        <p className="sidebar-notice" role="alert">
          Couldn't load sessions.{' '}
          <button type="button" className="fold" onClick={() => void query.refetch()}>
            Retry
          </button>
        </p>
      )}
      {query.isSuccess && query.data.sessions.length === 0 && <p className="sidebar-notice">No sessions yet.</p>}

      {query.isSuccess &&
        query.data.sessions.map((session) => {
          const chip = deriveStatusChip(session, session.sandboxStatus)
          const boot = deriveBootProgress(session.sandboxStatus)
          const primaryRepo = session.repos[0]
          return (
            <Link
              key={session.id}
              to="/session/$sessionId"
              params={{ sessionId: session.id }}
              className={`sess${session.id === activeSessionId ? ' active' : ''}`}
            >
              <span className="t">{session.title ?? '(untitled session)'}</span>
              <span className="m">
                <span className={`chip ${chip.tone}`}>
                  <span className="dot" />
                  {chip.label}
                </span>
                <SourceIcon source={session.spawnSource} />
                {primaryRepo ? primaryRepo.name : ''} · {formatRelativeTime(session.updatedAt)}
              </span>
              {boot !== null && (
                <div className="boot-mini" role="progressbar" aria-valuenow={boot.index} aria-valuemin={0} aria-valuemax={boot.total}>
                  <i style={{ width: `${Math.round((boot.index / boot.total) * 100)}%` }} />
                </div>
              )}
            </Link>
          )
        })}
    </nav>
  )
}
