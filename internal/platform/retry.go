// This file (retry.go) implements a small, generic synchronous retry
// helper -- Step 39's ("identities + full RBAC", §13.2) own need: "Fetch
// the actor's profile email from the provider API. This MUST be a
// retryable operation... a provider email-API failure is a retryable
// error, not an empty identity... retry with backoff and keep the last
// known value; never null-out an email on transient failure."
//
// This is DELIBERATELY distinct from domain/outbox.EvaluateBackoff (and
// domain/imagebuild's own identical precedent): those compute a decision
// (when should a LATER, separately-scheduled attempt run) for a
// background worker that persists its own next-attempt time and returns
// immediately -- they never sleep in-process. What internal/app/
// identitylink needs is the opposite shape: a foreground, INLINE retry
// loop bounded by the caller's own request-handling budget (a Slack/
// Linear webhook handler must still answer promptly either way -- see
// that package's own doc.go for how few attempts/how short a budget this
// is actually given), so this DOES sleep in-process, in exactly the same
// deliberate sense platform.Timeouts/other adapter code already performs
// real I/O and real waits -- domain purity (§11: "no I/O, time.Now(), or
// randomness in /internal/domain") only constrains internal/domain,
// never internal/platform.

package platform

import (
	"context"
	"errors"
	"time"
)

// permanentError is Retry's own "stop retrying, this will never succeed"
// marker -- deliberately unexported (Permanent below is the only way to
// construct one, mirroring how internal/domain/authz.ForbiddenError is
// always constructed through Authorize itself, never built directly by a
// caller). Retry unwraps it via errors.As and returns the WRAPPED error
// unchanged (never the marker type itself) so a caller of Retry never
// needs to know this package's own internal retry bookkeeping leaked into
// its error type.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as NOT retryable -- a fn passed to Retry should wrap
// its own error with this whenever it can positively identify the failure
// as permanent (e.g. a provider API's own "no such user" response), as
// opposed to a transient one (a timeout, a 5xx, a dropped connection).
// Retry stops immediately on a Permanent error, without waiting out its
// remaining attempts.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err was wrapped by Permanent above (directly,
// or reachable via errors.As-style unwrapping of it). Exported specifically
// for a caller of Retry that needs to tell "fn positively identified this
// failure as unrecoverable" apart from "every attempt failed transiently
// and attempts were exhausted" -- Retry's own returned error cannot be used
// for that check (see Retry's own doc comment: it deliberately returns the
// WRAPPED error unchanged, never the marker type itself, so a plain caller
// of Retry never needs to know this package's own internal retry
// bookkeeping leaked into its error type). A caller that DOES need the
// distinction (identitylink.FetchEmailWithRetry, retry.go) must therefore
// check the error fn itself returned, inside its own closure passed to
// Retry, BEFORE that error ever reaches Retry's own unwrapping -- not the
// value Retry hands back to ITS caller.
func IsPermanent(err error) bool {
	var perm *permanentError
	return errors.As(err, &perm)
}

// Retry calls fn up to attempts times (attempts < 1 is treated as 1 -- a
// single, unretried call, never zero calls), sleeping between failed
// attempts with a doubling delay starting at baseDelay and capped at
// maxDelay (mirroring domain/outbox.EvaluateBackoff's own doubling-
// capped-at-max SHAPE, but computed and slept inline here rather than
// persisted for a later tick).
//
// Returns nil on the first successful call. Returns the wrapped error
// immediately (no further attempts, no further waiting) the moment fn
// returns an error satisfying errors.As into *permanentError (i.e.
// wrapped via Permanent) -- a caller's fn should use this for any error
// it can positively identify as unrecoverable. Returns ctx.Err() if ctx is
// canceled/expires while waiting between attempts (fn itself is expected
// to respect ctx on its own outbound call; Retry does not additionally
// wrap each fn() call with its own timeout -- the caller's ctx already
// carries whatever per-attempt or overall deadline is appropriate).
// Returns the LAST attempt's own error once attempts is exhausted with no
// success and no permanent error along the way.
func Retry(ctx context.Context, attempts int, baseDelay, maxDelay time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}

	delay := baseDelay
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		var perm *permanentError
		if errors.As(err, &perm) {
			return perm.err
		}
		lastErr = err

		if attempt == attempts {
			break
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	return lastErr
}
