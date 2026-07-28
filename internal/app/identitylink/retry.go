package identitylink

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// meterName is this package's own OTel meter name -- mirrors app/
// outboxworker's/app/sessionactor's/app/reconciler's own "narvi/<package>"
// convention exactly (see those packages' own NewBuilder/NewRegistry/
// NewReconciler for the "construct once, at process-construction time"
// precedent emailFetchFailures below adapts -- see its own doc comment for
// why a package-level var, not a NewXxx constructor, is the right shape
// HERE).
const meterName = "narvi/identitylink"

// emailFetchFailures counts M19's own "a permanently broken provider
// email-fetch API disables auto-link platform-wide in total silence"
// finding: FetchEmailWithRetry below used to return ("", false) with ZERO
// logging and ZERO metrics whenever platform.Retry exhausted every retry
// attempt with a genuine error -- indistinguishable, from the outside,
// from the provider simply having no email on file for this user. This
// counter records ONLY the former ("the fetch itself is broken", an
// operationally actionable signal) -- see FetchEmailWithRetry's own doc
// comment for the exact line this distinction is drawn on. Labeled by
// provider (attribute "provider", sqlcgen.IdentityProvider's own string
// value) so a broken Slack fetch and a broken Linear fetch are
// distinguishable in the metric itself, since both providers share this
// one package's own FetchEmailWithRetry.
//
// Constructed once, as a package-level var, rather than through a
// NewBuilder-style fallible constructor (outboxworker.NewBuilder/
// sessionactor.NewRegistry/reconciler.NewReconciler's own established
// "construct once, at process-construction time, via otel.Meter(meterName)"
// pattern): unlike those packages, identitylink has no long-lived,
// process-constructed object of its own to hang this counter off of --
// FetchEmailWithRetry and Resolve are free functions, called directly off
// a platform.Timeouts/Deps value built via a plain struct literal at every
// one of this package's own several call sites (cmd/control-plane/main.go's
// production wiring AND roughly ten existing test files across this
// package and internal/adapters/inbound/{slack,linear}) -- threading a
// NEW, fallible NewXxx constructor through all of them, for a change this
// narrow, would ripple far past what this finding actually needs.
// otel.Meter is documented safe to call before platform.SetupOTel ever
// registers the real, globally-configured MeterProvider (go.opentelemetry.
// io/otel's own package doc: "It is also unnecessary to wait until the
// MeterProvider is configured to create Meters... still valid after") --
// every meter/instrument obtained from the global provider before that
// point is transparently upgraded in place the moment
// otel.SetMeterProvider runs, so this package-level var still ends up
// recording against the exact SAME real MeterProvider cmd/control-plane/
// main.go configures for every other instrument in this codebase, with no
// explicit wiring required at any of this package's own call sites.
var emailFetchFailures = newEmailFetchFailuresCounter()

func newEmailFetchFailuresCounter() metric.Int64Counter {
	counter, err := otel.Meter(meterName).Int64Counter(
		"identity_email_fetch_failures_total",
		metric.WithDescription("Count of FetchEmailWithRetry calls where every retry attempt genuinely failed with a real error (platform.Retry exhausted every attempt, or hit a Permanent error) -- a broken provider profile-email API (M19 audit fix). Deliberately excludes the provider simply reporting no email on file for a user, a normal, non-actionable outcome. Labeled by provider."),
		metric.WithUnit("{fetch}"),
	)
	if err != nil {
		// The instrument name above is a well-formed constant literal --
		// meter.Int64Counter only ever errors on a malformed name, so this
		// is genuinely unreachable in practice (mirrors this codebase's own
		// other otel.Meter(...).Int64Counter call sites, which all treat
		// this error as real but exceedingly unlikely). Panicking here, at
		// package-load time and well before main() does any real work,
		// surfaces a broken build immediately rather than silently
		// degrading this counter to a permanently-nil, always-panicking-
		// on-Add instrument for the rest of the process's life.
		panic(fmt.Sprintf("identitylink: construct identity_email_fetch_failures_total counter: %v", err))
	}
	return counter
}

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
// fetch closure itself differs between Slack and Linear. provider (the
// SAME sqlcgen.IdentityProvider constant the caller passes into Resolve
// right after this call) and logger are used ONLY for the M19 audit-fix
// observability below -- they change nothing about the retry behavior
// itself.
//
// email/ok mirrors Resolve's own (email string, emailOK bool) parameter
// shape exactly: ok=false covers BOTH "every retry attempt failed" and "a
// retry succeeded but reported no email at all" (fetch's own second
// return value) -- Resolve treats both identically (§13.2's own "never
// null-out an email on transient failure" rule means neither case should
// ever be treated as a confirmed empty identity, just "we don't know
// right now").
//
// M19 audit fix ("a permanently broken provider email-fetch API disables
// auto-link platform-wide in total silence"): ok=false's two cases above
// are IDENTICAL for Resolve's own purposes, by design, but must be told
// apart for OBSERVABILITY:
//
//  1. Every retry attempt failed with a genuine error (platform.Retry
//     returns non-nil, below) -- "the fetch itself is broken". Logged at
//     Warn (provider + the real underlying error) and counted by
//     emailFetchFailures above -- an operationally actionable signal a
//     permanently broken Slack/Linear profile-email API would otherwise
//     disable auto-linking platform-wide with zero trace anywhere.
//  2. A fetch attempt SUCCEEDED (platform.Retry returns nil) but the
//     provider's own API genuinely reported no email for this user
//     (fetch's own second return value, ok, is false) -- "the user simply
//     has no email on file", a normal, expected, non-actionable outcome.
//     Deliberately NEITHER logged NOR counted: doing so would defeat the
//     entire point of this distinction, burying the one actionable signal
//     (case 1) under routine noise from case 2, which is expected to be
//     common and is not, by itself, ever a sign of anything broken.
func FetchEmailWithRetry(ctx context.Context, logger *slog.Logger, timeouts platform.Timeouts, provider sqlcgen.IdentityProvider, fetch func(ctx context.Context) (email string, ok bool, err error)) (email string, ok bool) {
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
		// way, never guessing and never writing anything. M19 audit fix:
		// THIS case -- a genuine error, not a successful "no email"
		// report -- is the one this package now surfaces to an operator.
		logger.Warn("identitylink: profile-email fetch exhausted every retry attempt", "provider", string(provider), "error", err)
		emailFetchFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", string(provider))))
		return "", false
	}
	return email, ok
}
