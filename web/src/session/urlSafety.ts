// urlSafety.ts -- the ONE guard every third-party URL rendered by the
// session timeline must pass before it becomes an `href` (this Step's own
// defining risk: "a PR link, a branch link, a file link... must be
// validated before it becomes an href. `javascript:` is the cheap attack;
// a scheme allowlist is the answer, not a substring check").
//
// Two shapes are legitimate here, both real, both seen on the wire today:
//   - an ABSOLUTE external link (a GitHub PR/preview URL -- Artifact.url's
//     own schema doc comment, contracts/sandbox-ws/v1/events.schema.json:
//     "pr/preview artifacts always carry an ABSOLUTE external link").
//   - a SAME-ORIGIN RELATIVE path (an upload artifact's own stable
//     /api/sessions/{id}/uploads/{uploadId}/content path -- same doc
//     comment: "upload artifacts carry the artifacts row's own STABLE,
//     RELATIVE ... path").
// Everything else -- `javascript:`, `data:`, `vbscript:`, a bare
// protocol-relative `//evil.example` (which a browser resolves against
// evil.example, not this origin, despite LOOKING relative), a malformed
// string that fails to parse at all -- is rejected.
//
// # Why `new URL(..., base)`, not a regex/substring check on the scheme
//
// A substring check ("does this string contain 'javascript:'?") is
// exactly the cheap, bypassable check this module's own top comment
// warns against -- `\tjavascript:alert(1)`, `java\nscript:alert(1)`, and
// `%6a%61%76%61...` all defeat a naive substring test while a real
// browser still executes them. Handing the string to the platform's own
// URL parser (`new URL`) and reading back its OWN normalized `.protocol`
// is the same discipline a browser itself uses to decide what an anchor's
// href actually resolves to -- there is no smarter hand-rolled parser to
// write here.
const SAFE_SCHEMES = new Set(['http:', 'https:'])

/**
 * isSafeHref reports whether `raw` is safe to render as an anchor's
 * `href` -- an absolute http(s) URL, or a same-origin path starting with
 * exactly one `/` (never `//`, which a browser treats as protocol-relative
 * and resolves against a DIFFERENT origin entirely). Never throws: a
 * malformed or hostile string is simply unsafe, not an error.
 */
export function isSafeHref(raw: string): boolean {
  if (raw.length === 0) return false

  // Same-origin relative path: exactly one leading '/', never '//' or
  // '/\' (a browser normalizes a leading backslash to a second slash in
  // some parsers -- rejected here defensively even though `new URL` below
  // would also catch most such cases once resolved against a base).
  if (raw.startsWith('/') && !raw.startsWith('//') && !raw.startsWith('/\\')) {
    // Still hand it to the real parser (resolved against a fixed,
    // trusted base) rather than trusting the leading-slash shape alone --
    // a value like "/x\njavascript:alert(1)" must still resolve to a
    // plain path, not something a consumer could misinterpret.
    try {
      const resolved = new URL(raw, 'https://narvi.invalid')
      return resolved.protocol === 'https:' && resolved.origin === 'https://narvi.invalid'
    } catch {
      return false
    }
  }

  // Absolute URL: must parse on its own (no base) and use an allowed
  // scheme. `new URL` throws on anything that isn't a well-formed
  // absolute URL, which is exactly the "reject if it doesn't parse"
  // behavior wanted here -- a protocol-relative "//evil.example" DOES
  // parse without a base (browsers accept it as absolute, inheriting the
  // CURRENT page's scheme) and is rejected below not because it throws,
  // but because it starts with "//" and was never routed into this
  // branch at all (the check above only handles single-slash paths).
  if (raw.startsWith('//')) return false
  try {
    const parsed = new URL(raw)
    return SAFE_SCHEMES.has(parsed.protocol)
  } catch {
    return false
  }
}
