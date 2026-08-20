// buildClientWsUrl computes the full ws(s):// URL for GET
// /sessions/{sessionID}/ws?type=client (§6.2) from the SPA's own origin --
// §12.1: "one binary... on one port", so the WS endpoint is always
// same-host/same-scheme as the page itself, never a separately-configured
// backend URL. Kept out of transport.ts/sessionStream.ts on purpose: both
// of those take a plain `url: string` and know nothing about `window`,
// which is exactly what makes them constructable against a local test
// server with zero browser globals involved (see web/src/ws/__tests__/).
export function buildClientWsUrl(sessionId: string, location: Pick<Location, 'protocol' | 'host'> = window.location): string {
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${scheme}//${location.host}/sessions/${encodeURIComponent(sessionId)}/ws?type=client`
}
