import { describe, expect, it } from 'vitest'

import { safeJsonPreview, truncateForDisplay } from '../textSafety'

describe('truncateForDisplay', () => {
  it('returns short text unchanged', () => {
    expect(truncateForDisplay('hello', 100)).toBe('hello')
  })

  it('caps a 50k-character string at maxChars, with a visible, honest truncation marker', () => {
    const huge = 'x'.repeat(50_000)
    const out = truncateForDisplay(huge, 4000)
    expect(out.length).toBeLessThan(huge.length)
    expect(out).toContain('more characters truncated')
    expect(out.startsWith('x'.repeat(4000))).toBe(true)
  })

  it('caps a single 10k-character word with no spaces the same way (the truncation is a character count, not a word-boundary heuristic)', () => {
    const oneWord = 'a'.repeat(10_000)
    const out = truncateForDisplay(oneWord, 100)
    expect(out.slice(0, 100)).toBe('a'.repeat(100))
    expect(out.length).toBeLessThan(oneWord.length)
  })

  // Named mutation-test target: removing the cap (returning `text`
  // unchanged regardless of maxChars) must make THIS test fail --
  // verified manually during this Step's own verification pass
  // (temporarily replacing truncateForDisplay's body with `return text`),
  // then reverted byte-identical.
  it('never returns a string longer than maxChars plus the marker -- proves this is a real cap, not a passthrough', () => {
    const huge = 'y'.repeat(1_000_000)
    const out = truncateForDisplay(huge, 4000)
    expect(out.length).toBeLessThan(4100)
  })
})

describe('safeJsonPreview', () => {
  it('formats a plain object as indented JSON', () => {
    expect(safeJsonPreview({ a: 1 })).toBe('{\n  "a": 1\n}')
  })

  it('truncates a pathologically large value instead of hanging on it', () => {
    const out = safeJsonPreview({ blob: 'z'.repeat(200_000) }, 1000)
    expect(out.length).toBeLessThan(1200)
  })

  it('never throws on a value JSON.stringify itself rejects (a BigInt)', () => {
    expect(() => safeJsonPreview({ n: 10n })).not.toThrow()
    expect(safeJsonPreview({ n: 10n })).toBe('(unrenderable value)')
  })
})
