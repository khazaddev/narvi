package identitylink

import (
	"context"

	"github.com/khazaddev/narvi/internal/platform"
)

// FetchEmailWithRetry wraps platform.Retry around fetch -- ONE provider-
// specific, single-attempt profile-email fetch (internal/adapters/outbound/
// slackapi.Client.GetUserEmail / internal/adapters/outbound/linearapi.
// Client.GetUserEmail, each already threaded with whatever provider-
// specific auth/lookup they individually need by the caller's own
// closure) -- using timeouts.IdentityEmailFetch* to configure both the
// attempt count/backoff AND a per-attempt deadline (§13.2: "a provider
// email-API failure is a retryable error... retry with backoff").
//
// Centralized here (rather than duplicated once per ingress package)
// since the retry POLICY itself (how many attempts, how long to wait,
// how long one attempt gets) is entirely provider-agnostic -- only the
// fetch closure itself differs between Slack and Linear.
//
// email/ok mirrors Resolve's own (email string, emailOK bool) parameter
// shape exactly: ok=false covers BOTH "every retry attempt failed" and "a
// retry succeeded but reported no email at all" (fetch's own second
// return value) -- Resolve treats both identically (§13.2's own "never
// null-out an email on transient failure" rule means neither case should
// ever be treated as a confirmed empty identity, just "we don't know
// right now").
func FetchEmailWithRetry(ctx context.Context, timeouts platform.Timeouts, fetch func(ctx context.Context) (email string, ok bool, err error)) (email string, ok bool) {
	err := platform.Retry(ctx, timeouts.IdentityEmailFetchMaxAttempts, timeouts.IdentityEmailFetchRetryBaseDelay, timeouts.IdentityEmailFetchRetryMaxDelay, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, timeouts.IdentityEmailFetchTimeout)
		defer cancel()

		fetchedEmail, fetchedOK, fetchErr := fetch(attemptCtx)
		if fetchErr != nil {
			return fetchErr
		}
		email, ok = fetchedEmail, fetchedOK
		return nil
	})
	if err != nil {
		// Every attempt failed (or the fetch closure reported a
		// platform.Permanent error on its first try) -- §13.2's own rule
		// means this is NOT "confirmed no email", just "unknown right
		// now"; the caller (Resolve) treats ok=false identically either
		// way, never guessing and never writing anything.
		return "", false
	}
	return email, ok
}
