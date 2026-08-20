// canSubmit.test.ts -- row 83's own "one shared can-submit predicate" and
// "IME composition guard" requirements, both pinned here at the pure-
// function level (this repo's vitest config runs in plain Node, no jsdom/
// @testing-library/react -- see web/vitest.config.ts's own top comment --
// which is exactly why canSubmit.ts's own decision logic is factored out
// as plain functions taking plain data: fully unit-testable without a real
// DOM or synthetic event system).
import { describe, expect, it } from 'vitest'

import { canSubmitComposer, type ComposerSubmitState, shouldSubmitOnKeyDown } from '../canSubmit'

const BASE: ComposerSubmitState = { promptText: 'fix the bug', isSubmitting: false, hasInFlightAttachment: false, hasOpenTurn: false }

describe('canSubmitComposer', () => {
  it('is true for a non-empty prompt with nothing blocking it', () => {
    expect(canSubmitComposer(BASE)).toBe(true)
  })
  it('is false for an empty prompt', () => {
    expect(canSubmitComposer({ ...BASE, promptText: '' })).toBe(false)
  })
  it('is false for a whitespace-only prompt (trimmed before checking)', () => {
    expect(canSubmitComposer({ ...BASE, promptText: '   \n\t  ' })).toBe(false)
  })
  it('is false while a submit is already in flight', () => {
    expect(canSubmitComposer({ ...BASE, isSubmitting: true })).toBe(false)
  })
  it('is false while an attachment is still minting/uploading/confirming', () => {
    expect(canSubmitComposer({ ...BASE, hasInFlightAttachment: true })).toBe(false)
  })
  it('is false while the session already has an open (pending/dispatched/processing) turn', () => {
    expect(canSubmitComposer({ ...BASE, hasOpenTurn: true })).toBe(false)
  })
})

// --- Parity suite -----------------------------------------------------
//
// Written to FAIL if Composer.tsx's Send button and its keydown handler
// are ever wired to two independently-maintained checks instead of both
// funneling through canSubmitComposer. shouldSubmitOnKeyDown must agree
// with canSubmitComposer for every one of these states (holding the key
// event itself fixed at a plain, unmodified Enter with no active IME
// composition) -- MUTATION TEST: temporarily change
// shouldSubmitOnKeyDown's own `return canSubmitComposer(state)` line to a
// hand-duplicated check (e.g. one that forgets the hasOpenTurn guard) and
// re-run this suite -- the 'blocked while a turn is already open' case
// below fails immediately.
describe('shouldSubmitOnKeyDown / canSubmitComposer parity (mutation-tested)', () => {
  const plainEnter = { key: 'Enter', shiftKey: false, isComposing: false }

  const cases: { name: string; state: ComposerSubmitState }[] = [
    { name: 'sendable prompt', state: BASE },
    { name: 'empty prompt', state: { ...BASE, promptText: '' } },
    { name: 'whitespace-only prompt', state: { ...BASE, promptText: '   ' } },
    { name: 'submit already in flight', state: { ...BASE, isSubmitting: true } },
    { name: 'attachment in flight', state: { ...BASE, hasInFlightAttachment: true } },
    { name: 'turn already open', state: { ...BASE, hasOpenTurn: true } },
  ]

  for (const { name, state } of cases) {
    it(`keydown decision matches canSubmitComposer for: ${name}`, () => {
      expect(shouldSubmitOnKeyDown(plainEnter, state)).toBe(canSubmitComposer(state))
    })
  }
})

describe('Enter sends, Shift+Enter inserts a newline (day one, row 83)', () => {
  it('a plain Enter (no Shift, no IME) submits when canSubmitComposer allows it', () => {
    expect(shouldSubmitOnKeyDown({ key: 'Enter', shiftKey: false, isComposing: false }, BASE)).toBe(true)
  })
  it('Shift+Enter never submits, regardless of prompt state -- left to the textarea\'s own default newline insertion', () => {
    expect(shouldSubmitOnKeyDown({ key: 'Enter', shiftKey: true, isComposing: false }, BASE)).toBe(false)
  })
  it('a non-Enter key never submits', () => {
    expect(shouldSubmitOnKeyDown({ key: 'a', shiftKey: false, isComposing: false }, BASE)).toBe(false)
  })
})

// --- IME composition guard ---------------------------------------------
//
// MUTATION TEST: remove shouldSubmitOnKeyDown's own `if (event.isComposing)
// return false` line and re-run -- 'confirming an IME composition never
// sends' below fails (it would return true instead).
describe('IME composition guard (row 83: "confirming an IME composition never sends")', () => {
  it('an Enter that fires WHILE a composition is in progress never submits, even with a sendable prompt', () => {
    expect(shouldSubmitOnKeyDown({ key: 'Enter', shiftKey: false, isComposing: true }, BASE)).toBe(false)
  })
  it('the SAME state submits normally once composition has ended (isComposing false)', () => {
    expect(shouldSubmitOnKeyDown({ key: 'Enter', shiftKey: false, isComposing: false }, BASE)).toBe(true)
  })
  it('IME guard takes priority even if Shift is somehow also set', () => {
    // Shift+Enter already returns false on its own; this proves isComposing
    // is checked independently, not merely coincidentally covered by the
    // Shift branch.
    expect(shouldSubmitOnKeyDown({ key: 'Enter', shiftKey: false, isComposing: true }, { ...BASE, promptText: 'ok' })).toBe(false)
  })
})
