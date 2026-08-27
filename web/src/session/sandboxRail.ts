// sandboxRail.ts -- decision 6 ("'What happened?' as self-service") /
// §12.2's own "sandbox rail (transitions, gen, fingerprint, boot phases,
// artifacts, cost incl. sub-task roll-up)": the pure model builder behind
// SessionRail.tsx's "Sandbox" and "Boot progress" panels. Combines
// sandboxSnapshot.ts's own one-shot WS snapshot (real, authoritative, but
// only refreshed on (re)subscribe) with this session's own live event log
// (boot_progress/ready/error, refreshed continuously) -- the same
// "REST/snapshot base + WS-derived overlay" shape SessionHeader.tsx
// already uses for session.title vs. model.latestTitle, applied here to
// sandbox status instead.
//
// # What this module can and cannot honestly show (documented, not
// silently gapped -- this codebase's own established convention,
// sessionStatus.ts's BOOT_TOTAL comment is the precedent)
//
// - status/gen/lastSeenAt: REAL. gen and "last seen" are derived
//   generically -- every sandbox-ws event carries its own `gen` field
//   (verified directly against contracts/sandbox-ws/v1/events.schema.json:
//   every one of its 20 event $defs requires `gen`), so the most recent
//   event of ANY type is a valid, honest proxy for "last seen" (§3.2's own
//   heartbeat-updated last_seen_at column, approximated the same way
//   sessionStatus.ts's own BOOT_TOTAL already approximates a different
//   field this codebase doesn't persist a finer-grained version of).
// - boot phases with durations: REAL, computed from consecutive
//   boot_progress event timestamps (each phase's duration = the next
//   phase's -- or 'ready''s -- own timestamp minus this phase's own). Not
//   sourced from the separate `boot_timing` event (real pre-measured
//   seconds, §33.1) -- deliberately deferred: boot_progress phases alone
//   already satisfy "boot phases with durations" honestly, and boot_timing
//   only covers 4 fixed metrics, not every named phase boot_progress
//   itself reports. A precision upgrade, not a gap in what this Step ships.
// - transitions: REAL but coarser than the mockup's own idealized
//   4-stage "spawning -> connecting -> booting -> ready" chain. No client-
//   visible source reports THOSE coarse lifecycle-stage transitions live
//   (sandbox_history, migrations/000007, is provably unpopulated by any
//   real code path today -- confirmed by reading every call site of the
//   pure internal/domain/sandbox.Transition function; every one commits
//   only sandboxes.status, never a history row). What IS real and shown
//   here instead: this session's own boot_progress/ready/error events,
//   each with its genuine server timestamp -- a richer, more specific
//   signal than the mockup's abstract stage names, just not the same
//   granularity. Labeled in the UI as observed from this session's own
//   events, never claimed as the authoritative transition ledger.
// - runtime fingerprint / correlation id: NOT AVAILABLE ANYWHERE ON THE
//   WIRE TODAY, confirmed by reading the full producer chain:
//   sandboxboot.BootFingerprint (internal/domain/sandboxboot/
//   fingerprint.go) is computed inside cmd/sandbox-agent and only ever
//   passed to slog.Info -- it is never attached to any sandbox-ws event,
//   never written to Postgres, never reaches the control plane at all.
//   Correlation id is a PER-REQUEST concept (internal/platform/
//   correlation.go's own X-Correlation-Id), persisted only onto
//   audit_log/outbox/automation_runs rows -- never onto sessions or
//   sandboxes. Closing this gap for real needs new sandbox-agent -> CP
//   wire plumbing (a new event field or endpoint) and is out of this
//   Step's own scope (a UI Step, not a wire-contract Step) -- named here,
//   the same way §19.2 names image GC as future work, rather than
//   fabricated or silently omitted. SessionRail.tsx renders an honest
//   "not reported yet" for both rather than inventing a value.
import type { EventEnvelope } from '../ws/types'
import { isPlainObject } from '../ws/util'
import { asBootProgress, asReady, asSandboxError } from './eventPayloads'
import type { SandboxSnapshot } from './sandboxSnapshot'

export interface BootPhase {
  phase: string
  startedAt: string
  /** Null while this is the most recently reported phase and nothing later (another phase, or 'ready') has arrived yet to bound its end. */
  endedAt: string | null
  /** Null while still open (endedAt is null) -- see endedAt's own doc comment. */
  seconds: number | null
}

export interface SandboxTransition {
  /** Stable React key -- event-id-derived, never re-derived from content (a hostile phase/message string must never collide two distinct transitions onto the same key). */
  id: string
  label: string
  at: string
  tone: 'neutral' | 'ok' | 'crit' | 'warn'
}

export interface SandboxRailModel {
  /** Matches sandbox_status verbatim when known; null when this session has no observed sandbox at all yet (sessionSnapshot null AND no gen-bearing event ever seen). */
  status: string | null
  gen: number | null
  lastSeenAt: string | null
  bootPhases: BootPhase[]
  transitions: SandboxTransition[]
  /** True once there is ANY evidence a sandbox exists (a snapshot, or at least one gen-bearing event) -- distinguishes "genuinely nothing yet" (a brand-new, not-yet-dispatched session) from "a sandbox exists but this session has nothing further to show". */
  hasSandbox: boolean
}

function extractGen(payload: unknown): number | null {
  if (!isPlainObject(payload)) return null
  return typeof payload.gen === 'number' ? payload.gen : null
}

export function buildSandboxRailModel(events: readonly EventEnvelope[], snapshot: SandboxSnapshot | null): SandboxRailModel {
  let status: string | null = snapshot?.status ?? null
  let gen: number | null = snapshot?.gen ?? null
  let lastSeenAt: string | null = snapshot?.lastSeenAt ?? null
  let hasSandbox = snapshot !== null

  const bootPhases: BootPhase[] = []
  const transitions: SandboxTransition[] = []
  let openPhase: BootPhase | null = null

  function closeOpenPhase(at: string): void {
    if (openPhase === null) return
    openPhase.endedAt = at
    const ms = new Date(at).getTime() - new Date(openPhase.startedAt).getTime()
    openPhase.seconds = Number.isFinite(ms) && ms >= 0 ? ms / 1000 : null
    openPhase = null
  }

  for (const event of events) {
    const eventGen = extractGen(event.payload)
    if (eventGen !== null) {
      gen = eventGen
      hasSandbox = true
      lastSeenAt = event.createdAt
    }

    const bootProgress = asBootProgress(event)
    if (bootProgress !== null) {
      closeOpenPhase(event.createdAt)
      openPhase = { phase: bootProgress.phase, startedAt: event.createdAt, endedAt: null, seconds: null }
      bootPhases.push(openPhase)
      status = 'booting'
      transitions.push({ id: `boot:${event.id}`, label: bootProgress.phase, at: event.createdAt, tone: 'neutral' })
      continue
    }

    const ready = asReady(event)
    if (ready !== null) {
      closeOpenPhase(event.createdAt)
      status = 'ready'
      transitions.push({ id: `ready:${event.id}`, label: 'ready', at: event.createdAt, tone: 'ok' })
      continue
    }

    const sandboxError = asSandboxError(event)
    if (sandboxError !== null) {
      if (sandboxError.fatal) {
        closeOpenPhase(event.createdAt)
        status = 'failed'
      }
      transitions.push({
        id: `error:${event.id}`,
        label: sandboxError.fatal ? `error: ${sandboxError.message}` : `warning: ${sandboxError.message}`,
        at: event.createdAt,
        tone: sandboxError.fatal ? 'crit' : 'warn',
      })
      continue
    }
  }

  return { status, gen, lastSeenAt, bootPhases, transitions, hasSandbox }
}
