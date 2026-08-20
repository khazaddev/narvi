import { useState } from 'react'

import { getStoredPreference, setThemePreference, type ThemePreference } from '../lib/theme'

const OPTIONS: { value: ThemePreference; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

/** The "explicit toggle" half of §12.1's theming requirement -- a 3-way
 * control (System / Light / Dark) rather than a single light/dark switch,
 * so "go back to following the OS" is a real, reachable state and not just
 * whatever the toggle happened to start at. */
export function ThemeToggle() {
  const [preference, setPreference] = useState<ThemePreference>(() => getStoredPreference())

  return (
    <div role="group" aria-label="Theme" style={{ display: 'flex', gap: 'var(--space-2)' }}>
      {OPTIONS.map((option) => (
        <button
          key={option.value}
          type="button"
          className="pill-button"
          aria-pressed={preference === option.value}
          onClick={() => {
            setThemePreference(option.value)
            setPreference(option.value)
          }}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}
