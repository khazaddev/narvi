// textSafety.ts -- guards against the OTHER half of this Step's own
// defining risk: content that is safe from an injection standpoint (React
// escapes it by construction, see this package's own top-level doc
// comment) but can still break the LAYOUT or hang the tab -- "a 50k-
// character tool output, a single 10k-character word with no spaces,
// deeply nested sub-tasks."
//
// Two distinct problems, two distinct fixes:
//   - A very LONG string (many characters) -- fixed by truncateForDisplay
//     below: cap the character count actually handed to the DOM, never
//     render the full 50k characters just to let CSS visually clip them
//     (that still costs layout/paint on every one of those characters).
//   - A single very LONG WORD (no whitespace to wrap on) -- CSS's job,
//     not this module's: every block that renders freeform event content
//     (session.css's own .evt-body/.evt-json rules) sets
//     `overflow-wrap: anywhere` so a wrap point exists even inside one
//     unbroken token, instead of `white-space: pre` alone (which would
//     force one giant unbroken horizontal line -- pushing the layout
//     width to match the content instead of the content wrapping to the
//     layout).
const MAX_DISPLAY_CHARS = 4000

/**
 * truncateForDisplay caps `text` at maxChars characters (default
 * MAX_DISPLAY_CHARS), appending a visible, honest marker noting how many
 * characters were cut -- never a silent truncation a reader could mistake
 * for the complete value. A no-op (returned unchanged) when text is
 * already within the cap.
 */
export function truncateForDisplay(text: string, maxChars: number = MAX_DISPLAY_CHARS): string {
  if (text.length <= maxChars) return text
  const cut = text.length - maxChars
  return `${text.slice(0, maxChars)}\n… (${cut.toLocaleString('en-US')} more characters truncated)`
}

/**
 * safeJsonPreview renders an arbitrary, untrusted JSON-shaped value
 * (tool-call `input`/`output`, e.g.) as a display string -- JSON.stringify
 * first (so a value that is itself pathological, e.g. a string containing
 * millions of characters, is captured as a bounded operation on already-
 * parsed data, never a fresh unbounded scan), then truncateForDisplay.
 * Never throws: a value JSON.stringify itself rejects (a BigInt, a
 * circular structure) degrades to a fixed placeholder rather than
 * crashing the timeline render.
 */
export function safeJsonPreview(value: unknown, maxChars: number = MAX_DISPLAY_CHARS): string {
  let text: string
  try {
    text = JSON.stringify(value, null, 2) ?? String(value)
  } catch {
    return '(unrenderable value)'
  }
  return truncateForDisplay(text, maxChars)
}
