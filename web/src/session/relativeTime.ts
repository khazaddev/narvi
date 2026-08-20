// relativeTime.ts -- the sidebar's own compact "2 min" / "1 h" elapsed-time
// text (mockups.html's own .sess .m rows). A small, deliberately coarse
// formatter -- this is decoration, not a precision instrument, so it
// rounds down to the nearest whole unit exactly like the mockup's own
// static examples ("2 min", "1 h") do.
export function formatRelativeTime(iso: string, now: Date = new Date()): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const diffMs = Math.max(0, now.getTime() - then)
  const minutes = Math.floor(diffMs / 60_000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} h`
  const days = Math.floor(hours / 24)
  return `${days} d`
}
