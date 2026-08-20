// Theming (§12.1): "light/dark via CSS custom properties, prefers-color-scheme
// + explicit toggle." The CSS side (../styles/tokens.css) already gives
// "system" for free -- no `data-theme` attribute on <html> means the
// `@media (prefers-color-scheme: dark)` block alone decides, and the browser
// re-evaluates that media query live on an OS-level scheme change with no JS
// involved. This module owns only the "explicit toggle that overrides it in
// both directions" half: writing a `data-theme="light"|"dark"` attribute
// that the same stylesheet's `:root[data-theme="light"]`/`:root[data-theme=
// "dark"]` blocks take priority over.
//
// storageKey's literal value is duplicated in index.html's own inline
// pre-paint script (see that file's own comment) -- the two must be kept in
// sync by hand, since an inline <script> in raw HTML cannot import a TS
// constant. There is no third mechanism a future edit could drift instead
// of one of those two call sites.
export const storageKey = 'narvi:theme'

export type ThemePreference = 'system' | 'light' | 'dark'

function isThemePreference(value: string | null): value is ThemePreference {
  return value === 'system' || value === 'light' || value === 'dark'
}

/** Reads the persisted preference, defaulting to "system" for a first visit,
 * a cleared store, or a value this version no longer recognizes -- storage
 * access itself is wrapped because a privacy mode / disabled-storage browser
 * throws on `localStorage.getItem`, and "system" is the correct fallback
 * behavior in that case too, not a crash. */
export function getStoredPreference(): ThemePreference {
  try {
    const stored = localStorage.getItem(storageKey)
    return isThemePreference(stored) ? stored : 'system'
  } catch {
    return 'system'
  }
}

/** Applies pref to the document without touching storage -- the pure
 * DOM-mutation half, shared by initTheme (reads storage once at boot) and
 * setThemePreference (writes storage, then applies) so there is exactly one
 * place that knows how a preference maps to the `data-theme` attribute. */
export function applyTheme(pref: ThemePreference): void {
  const root = document.documentElement
  if (pref === 'system') {
    root.removeAttribute('data-theme')
  } else {
    root.setAttribute('data-theme', pref)
  }
}

/** Called once at startup (main.tsx, before the first React render) to sync
 * the DOM with whatever index.html's own inline script already applied
 * pre-paint -- redundant with that script on a normal load, but the single
 * source of truth for the mapping either way, and the only path taken at
 * all when JS is the first thing to run (e.g. a hard-reloaded SPA route). */
export function initTheme(): void {
  applyTheme(getStoredPreference())
}

/** Persists pref and applies it immediately -- the toggle UI's only write
 * path, so a future second call site can never persist without applying or
 * vice versa. */
export function setThemePreference(pref: ThemePreference): void {
  try {
    localStorage.setItem(storageKey, pref)
  } catch {
    // Same privacy-mode/disabled-storage carve-out as getStoredPreference:
    // the preference still applies for this page load, it just won't
    // persist across a reload -- never a thrown error surfaced to the user
    // over a mechanism this non-essential.
  }
  applyTheme(pref)
}
