// useSessionStream.ts -- the ONE hook that wires §12.1's own WS pipeline
// (web/src/ws/sessionStream.ts's own SessionStream: "WS transport ->
// event log -> reducer -> query invalidation") into React, exactly the
// shape that file's own top comment already anticipates ("most likely via
// a useSyncExternalStore(stream.subscribe, stream.getSnapshot) hook built
// on top of subscribe()/getSnapshot() below"). This Step is the pipeline's
// first real consumer -- every other session-view module (Timeline.tsx,
// SessionSidebar.tsx) reads a session's live event log through this hook,
// never by constructing a second, independent SessionStream/subscribing
// to the raw WS itself.
import { useEffect, useMemo, useSyncExternalStore } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import { mintWsToken } from '../api/endpoints'
import { SessionStream, type SessionStreamSnapshot } from '../ws/sessionStream'
import { buildClientWsUrl } from '../ws/url'

/**
 * useSessionStream owns one SessionStream for `sessionId`, started on
 * mount and torn down on unmount OR whenever sessionId itself changes
 * (switching which session is being viewed, in the sidebar, gets a fresh
 * stream -- never a stream still wired to the PREVIOUS session's id).
 * clientId is generated once per stream instance (crypto.randomUUID(),
 * present natively in every browser this SPA targets) -- a fresh
 * subscribe from a fresh tab/session-switch is a genuinely new client
 * from the server's own point of view (§6.2's subscribe{token,clientId}),
 * never reused across a stream teardown/recreate.
 */
export function useSessionStream(sessionId: string): SessionStreamSnapshot {
  const queryClient = useQueryClient()

  // A new SessionStream is constructed whenever sessionId OR queryClient
  // changes -- queryClient is a stable app-lifetime singleton in practice
  // (main.tsx's own single `new QueryClient()`), so in practice this only
  // ever fires on sessionId changing, but listing it here (rather than a
  // suppressed lint warning) keeps the dependency array honest about what
  // this closure actually captures: getToken below closes over `sessionId`
  // directly, so a stream built for one session must never survive a
  // sessionId change with a stale closure still minting tokens for the
  // PREVIOUS session.
  const stream = useMemo(() => {
    const clientId = crypto.randomUUID()
    return new SessionStream({
      sessionId,
      wsUrl: buildClientWsUrl(sessionId),
      clientId,
      getToken: async () => {
        const { token } = await mintWsToken(sessionId)
        return token
      },
      queryClient,
    })
  }, [sessionId, queryClient])

  useEffect(() => {
    stream.start()
    return () => stream.stop()
  }, [stream])

  return useSyncExternalStore((listener) => stream.subscribe(listener), () => stream.getSnapshot())
}
