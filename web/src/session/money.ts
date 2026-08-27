/**
 * One money formatter for the whole app, and the reason there is only one.
 *
 * Until the phase audit there were four, each two lines long, each owned by
 * the view that rendered a dollar figure -- a convention that reads as local
 * ownership and behaves as drift. Three of them used two decimals and one
 * used four, and the difference was not a decision anyone made: it was where
 * a later fix happened to land. A per-step agent cost is routinely a
 * fraction of a cent, so at two decimals the same real charge rendered as
 * "$0.0040" on one screen and "$0.00" -- indistinguishable from free -- on
 * another.
 *
 * Two distinctions this must never collapse, both of which cost money to get
 * wrong:
 *
 *   null is "no figure has arrived", NOT "free". It renders as an em dash.
 *   A real 0 is a real measurement and renders as $0.00.
 *
 *   A sub-cent figure is not zero. Anything that would round to zero without
 *   being zero gets four decimals, so a cheap step reads as cheap rather
 *   than as free. The database column carries six decimals for exactly this
 *   reason; rounding the honesty back out at the last hop would undo it.
 *
 * Totals and per-step figures share this function deliberately. An earlier
 * comment justified a two-decimal variant for totals on the grounds that a
 * total is "larger by construction" -- it is not: a session with one cheap
 * turn has a sub-cent total, and a reader comparing a total against the
 * steps that compose it should not have to know which rounding each used.
 */
export function formatUsd(usd: number | null): string {
  if (usd === null) return '—'
  if (usd !== 0 && Math.abs(usd) < 0.01) return `$${usd.toFixed(4)}`
  return `$${usd.toFixed(2)}`
}
