// Test runner config for the §12.1 data layer's own pipeline tests
// (web/src/ws/__tests__/, web/src/api/__tests__/). Deliberately separate
// from vite.config.ts, not merged into it via `vitest/config`'s
// mergeConfig helper: vite.config.ts's own `build.outDir` points directly
// at internal/adapters/inbound/webui/dist (go:embed's real target) with
// `emptyOutDir: true` -- accidentally pulling that build config into a
// test run has no upside and a real downside (a test invocation that
// somehow triggers a build step wiping that directory). Environment is
// plain Node (`environment: 'node'`, not jsdom/happy-dom): nothing under
// src/ws or src/api touches the DOM -- src/ws/transport.ts talks to the
// browser-standard global `WebSocket` (present as a Node 22+ built-in,
// see web/src/ws/transport.ts's own top comment) and src/api/http.ts
// talks to global `fetch`, both already real in a plain Node runtime, so
// jsdom would only add startup cost for zero benefit here.
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from 'vitest/config'

const here = path.dirname(fileURLToPath(import.meta.url))

export default defineConfig({
  resolve: {
    // Mirrors vite.config.ts's own resolve.alias -- see that file's own
    // comment for why generated contracts are imported through this one
    // stable specifier rather than a relative path.
    alias: {
      '@narvi/contracts': path.resolve(here, '../contracts/gen/ts'),
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/__tests__/**/*.test.ts'],
    // A hung fetch_history round-trip (the exact failure mode
    // src/ws/sessionStream.ts's own backfill loop is written to recover
    // from -- see its top comment) must fail the offending test loudly
    // well before CI's own job-level timeout, not hang until that fires.
    testTimeout: 10_000,
  },
})
