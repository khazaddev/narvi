// canSubmit.ts -- decision 5 / row 83 / §12.3's own named requirement:
// "one shared can-submit predicate driving both the Send button and the
// keydown handler". The row names this because the classic defect in this
// class of UI is a Send button and a keydown handler that quietly drift
// apart on when submission is allowed (Enter sends something the button
// would have refused, or vice versa). This module is the ONE place that
// question is answered -- Composer.tsx's Send button `disabled` prop calls
// canSubmitComposer directly, and its onKeyDown handler calls
// shouldSubmitOnKeyDown, which itself calls canSubmitComposer internally
// rather than re-implementing any part of it. See
// __tests__/canSubmit.test.ts's own parity suite, written specifically to
// fail if the two call sites are ever wired to diverge.

export interface ComposerSubmitState {
  /** The composer's current draft text, exactly as typed (not yet trimmed) -- trimming happens inside this predicate so every caller applies the identical rule, rather than each caller trimming (or not) on its own. */
  promptText: string
  /** True while a createTurn request from a PREVIOUS submit is still in flight -- a second submit before the first resolves would either double-send or race the server's own hasOpenTurn check (turn.go). */
  isSubmitting: boolean
  /** True while at least one attached file's mint/PUT/confirm sequence has not yet reached a terminal state (ready or failed) -- sending now would silently omit that attachment's id from attachmentIds even though it still looks "attached" in the composer. */
  hasInFlightAttachment: boolean
  /** True while this session's own most recent turn has no outcome yet (timelineModel.ts's own TurnNode.live) -- mirrors the server's own hasOpenTurn/RejectIfOpen precondition (turn.go: "a turn is already pending, dispatched, or processing for this session") client-side, so Send is disabled instead of guaranteed to 409. */
  hasOpenTurn: boolean
}

/**
 * canSubmitComposer is the ONE predicate: true iff the composer may be
 * submitted right now. Every other check in this module, and every call
 * site in Composer.tsx, funnels through this function -- it is never
 * re-implemented, only called.
 */
export function canSubmitComposer(state: ComposerSubmitState): boolean {
  if (state.isSubmitting) return false
  if (state.hasInFlightAttachment) return false
  if (state.hasOpenTurn) return false
  return state.promptText.trim().length > 0
}

export interface ComposerKeyDownEvent {
  key: string
  shiftKey: boolean
  /**
   * The browser's own IME composition state for this keydown -- read from
   * either the React SyntheticEvent's own nativeEvent.isComposing, or a
   * component-tracked compositionstart/compositionend ref (Composer.tsx's
   * own onKeyDown wiring uses both, belt and suspenders, since some
   * browsers are known to report isComposing inconsistently on the exact
   * keydown that confirms a composition). Never a heuristic over the text
   * itself -- always the browser's own composition state, per row 83's
   * own explicit requirement.
   */
  isComposing: boolean
}

/**
 * shouldSubmitOnKeyDown decides whether ONE keydown event should submit
 * the composer:
 *   - Enter (no Shift, no active IME composition) submits, IF
 *     canSubmitComposer(state) is also true.
 *   - Shift+Enter never submits -- the textarea's own default
 *     newline-insertion behavior is left alone (this function's caller
 *     must not call preventDefault in that branch).
 *   - An in-progress IME composition (isComposing true) NEVER submits,
 *     even on a bare Enter -- confirming a composed character (Japanese/
 *     Chinese/Korean input) itself fires a keydown Enter that must only
 *     close the IME candidate window, not send a half-written message.
 *
 * Delegates the "is there anything sendable right now" question entirely
 * to canSubmitComposer -- this function adds ONLY the key-event
 * interpretation (which key, which modifiers, IME state) on top of it. It
 * never re-implements canSubmitComposer's own checks, which is exactly
 * what keeps this call site and the Send button's own `disabled` prop
 * from ever silently drifting apart.
 */
export function shouldSubmitOnKeyDown(event: ComposerKeyDownEvent, state: ComposerSubmitState): boolean {
  if (event.key !== 'Enter') return false
  if (event.shiftKey) return false
  if (event.isComposing) return false
  return canSubmitComposer(state)
}
