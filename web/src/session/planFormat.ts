// planFormat.ts -- small, pure formatting/mapping helpers for
// PlanModeView.tsx, mirroring reviewFormat.ts's own "closed-enum ->
// chip-tone/label mappings kept in one place" shape and its own "no I/O,
// no rendering" discipline.
import type { Plan } from '@narvi/contracts/rest-dtos'

import type { ChipTone } from './reviewFormat'

export function planStatusTone(status: Plan['status']): ChipTone {
  if (status === 'awaiting_approval') return 'warn'
  if (status === 'approved') return 'ok'
  if (status === 'rejected') return 'crit'
  return 'neutral' // superseded
}

export function planStatusLabel(status: Plan['status']): string {
  switch (status) {
    case 'awaiting_approval':
      return 'awaiting approval'
    case 'approved':
      return 'approved'
    case 'rejected':
      return 'rejected'
    case 'superseded':
      return 'superseded'
    default:
      return status
  }
}

/** latestPlan picks the plan version PlanModeView.tsx should feature in its main card: the awaiting_approval one if there is one (there is at most one, per the DB's own partial unique index), otherwise the highest version. */
export function latestPlan(plans: Plan[]): Plan | null {
  if (plans.length === 0) return null
  const awaiting = plans.find((p) => p.status === 'awaiting_approval')
  if (awaiting) return awaiting
  return plans.reduce((a, b) => (b.version > a.version ? b : a))
}

/** modelLabel renders a nullable model id honestly: the real id when set, or "default" when null (§29.8/§12.2 item 3's own established "null means use the default model catalog entry" convention) -- never blank, never a fabricated model name. */
export function modelLabel(modelId: string | null): string {
  return modelId ?? 'default'
}

/**
 * canActOnPlan is this view's own CLIENT-SIDE approximation of the REAL
 * server-side gate (domain/authz.Authorize(ActionApprovePlan, ...),
 * internal/adapters/inbound/httpapi/planauthz.go's canActOnPlan) -- §13.3's
 * matrix: admin/maintainer may approve/reject ANY plan; a member only one
 * on a session they created OR JOINED; a viewer never.
 *
 * This function can only ever check "created" -- "joined" (a participants
 * row) reaches no React state anywhere in this client. The WS subscribed
 * payload does carry a real participants array and the transport validates
 * its shape, but nothing consumes it: multiplayer presence is a named,
 * tracked gap. (An earlier version of this comment justified itself with a
 * grep count instead -- "exactly one, in a WS test fixture". The count went
 * stale the moment the transport gained its validation, while the claim it
 * supported stayed true. A justification should rest on the property, not
 * on a number that moves.) So this is a
 * CONSERVATIVE under-approximation: it can wrongly HIDE the approval
 * affordance from a genuinely-joined member (who the server would in fact
 * allow), but can never wrongly SHOW it to someone the server would
 * refuse -- the safe direction for a UI that must never be the real
 * enforcement point (this view's own top doc comment). The server
 * re-checks the real matrix, including "joined", independently on every
 * approve/reject call regardless of what this function decides to render.
 */
export function canActOnPlan(role: string | undefined, meId: string | undefined, sessionCreatedBy: string | null): boolean {
  if (role === 'admin' || role === 'maintainer') return true
  if (role === 'member' && meId !== undefined && sessionCreatedBy !== null && meId === sessionCreatedBy) return true
  return false
}
