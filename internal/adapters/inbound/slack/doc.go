// Package slack is the Slack Events API ingress adapter (Step 33, "Slack
// ingress", §8.10 "Slack/Linear fidelity" -- the Slack half only; Step 34
// covers Linear, in a separate package/worktree in parallel). One route,
// wired in cmd/control-plane/main.go: POST /webhooks/slack.
//
// # Request handling, in order (fail closed at every step)
//
//  1. Read the raw request body (bounded by http.MaxBytesReader) BEFORE
//     any JSON decoding -- Slack's own signature is computed over the
//     exact raw bytes, so decoding first and re-marshaling would risk
//     signing a byte-for-byte different string than what Slack actually
//     sent (whitespace/key-order are not guaranteed to round-trip).
//  2. Verify the signature: assemble "v0:{X-Slack-Request-Timestamp}:
//     {raw body}" (confirmed against Slack's own real, current
//     documentation, docs.slack.dev/authentication/verifying-requests-
//     from-slack, during this Step's own design phase -- NOT taken on
//     faith from a summary), strip the "v0=" prefix from
//     X-Slack-Signature, and call platform.VerifyWebhookSignature. Every
//     request is signed this same way, including the one-time
//     url_verification handshake below -- there is no unsigned/exempt
//     request shape.
//  3. Verify freshness: platform.VerifyWebhookTimestamp against
//     platform.Timeouts.WebhookTimestampFreshnessWindow (Slack's own
//     documented guidance: reject a request whose timestamp is more than
//     5 minutes from now, guarding against a replayed-but-genuinely-
//     valid-signature request).
//  4. Branch on the outer envelope's own "type": "url_verification" is a
//     one-time setup handshake (Slack POSTs {"token","challenge","type"}
//     once, when an Events API subscription URL is first configured) --
//     handled by simply echoing {"challenge": ...} back as JSON, 200,
//     WITHOUT ever touching Postgres or CreateSessionCore. Anything other
//     than "event_callback" is logged and 200'd as a no-op (forward
//     compatibility with outer envelope types this adapter doesn't yet
//     need to understand, rather than erroring on them).
//  5. For a real "event_callback": WebhookDeliveryStore.Claim(ctx,
//     "slack", envelope.EventID) BEFORE any processing (§5.1's dedupe
//     claim, Step 31) -- envelope.EventID is Slack's own "globally unique
//     across all workspaces" event_id field (confirmed against Slack's
//     own Events API documentation), which Slack repeats byte-for-byte on
//     every redelivery of the SAME event (a real, common occurrence any
//     time this handler doesn't answer within Slack's own ~3s budget). A
//     lost claim (Inserted == false) short-circuits straight to 200 --
//     already processed, never reprocessed.
//  6. Filter the inner event: any event carrying a non-empty bot_id (our
//     own in-thread ack below, or any other bot's message) is ignored --
//     otherwise the ack this Step posts would immediately re-trigger
//     ingestion of itself. A "message" event with a non-empty subtype
//     (edits, deletes, channel-join notices, ...) is likewise ignored. An
//     event whose own type is neither "app_mention" nor "message" is
//     logged and 200'd as a no-op.
//  7. Thread<->session mapping (design decision, see below) then either
//     adds a turn to an existing session or creates a brand-new one via
//     httpapi.CreateSessionCore.
//  8. Best-effort in-thread ack (see the scoping note below): a failure
//     here is logged, never turned into a non-2xx response -- the core
//     session/turn work already succeeded by this point, and failing the
//     whole request over the ack alone would make Slack redeliver an
//     event WebhookDeliveryStore has already (correctly) claimed, which
//     would then be silently skipped forever (see the delivery-claim-
//     before-process tradeoff note below).
//
// # Thread<->session mapping design
//
// Slack's own real identity for "one thread" is (channel_id, thread_ts)
// -- thread_ts is the root message's own "ts", which the docs confirm
// every reply in that thread carries back unchanged. This adapter derives
// a threadKey per inbound event as: event.ThreadTS if non-empty, else
// event.TS itself (covers both a Slack quirk this Step's own doc research
// could not fully pin down -- whether a brand-new, not-yet-threaded
// message's app_mention event carries a self-referential thread_ts at
// all -- and, more importantly, establishes OUR OWN thread the moment we
// post the very first ack in reply to a plain, non-threaded mention).
//
// A concurrency-safe claim (migrations/000028_slack_thread_sessions.up.sql,
// postgres.SlackThreadSessionStore) is needed for the case two
// near-simultaneous first messages race to create the SAME brand-new
// thread's mapping. The design deliberately sequences around the fact
// that httpapi.CreateSessionCore only fires its own post-commit
// GetOrSpawn+EnsureDispatched dispatch when the request carries a
// non-nil prompt:
//
//  1. Create a BARE session (SpawnSource "slack", Prompt: nil) via
//     CreateSessionCore -- cheap, side-effect-free: no turn, no
//     dispatch, no sandbox ever touched.
//  2. Atomically claim (channel_id, thread_ts) for that session's id
//     (SlackThreadSessionStore.Claim -- INSERT ... ON CONFLICT DO
//     NOTHING, mirroring §5.1's/WebhookDeliveryStore's own atomic-claim
//     house style).
//  3. If the claim won: this bare session IS now the thread's own
//     mapped session. If it lost (a concurrent racer already claimed
//     this thread first): SlackThreadSessionStore.Get resolves the
//     WINNER's real session id instead, and the bare session THIS
//     request just created is simply left as an idle, never-dispatched
//     orphan row -- an accepted, honestly-documented tradeoff of a
//     genuinely concurrency-safe claim (no Postgres advisory lock, no
//     held transaction spanning two separate connections, no polling
//     loop) rather than a speculative cleanup mechanism this Step does
//     not build.
//  4. Either way, a turn (the normalized prompt) is added to whichever
//     session id resulted, via the SAME locked, 409-equivalent-checked
//     path turn.go's own CreateTurn REST endpoint uses (lock the session
//     row, refuse a second turn while one is already Pending/Dispatched/
//     Processing) -- a reply arriving while a prior turn on the same
//     thread is still in flight is acknowledged but does not queue a
//     second turn.
//
// A REPLY on an ALREADY-mapped thread skips steps 1-3 entirely --
// SlackThreadSessionStore.Get resolves its session id directly, then
// step 4 runs unchanged.
//
// # Repo-selection scoping gap (honestly named, not silently papered
// over)
//
// The technical plan has no per-channel/per-workspace repo-routing design
// yet -- a brand-new Slack-spawned session's CreateSessionRequest.Repos
// needs SOME repo, and today's only source is one operator-configured
// default (platform.Config.SlackDefaultRepoName/SlackDefaultRepoURL,
// deliberately optional -- see that field's own doc comment). If either
// is unset, a NEW mention gets a best-effort ack explaining ingress isn't
// configured with a default repo yet and no session is created; a REPLY
// on an already-mapped thread is entirely unaffected (it never needs a
// repo). Real per-channel repo routing is left to a future Step (most
// naturally automations, §8.4/Step 47).
//
// # In-thread acks -- scoping decision (Step 33's own row, "in-thread
// acks")
//
// internal/app/ports has no Notifier port yet, and the outbox table
// (migrations/000010_outbox.up.sql) has no delivery-worker consumer --
// both are explicitly Step 35's ("outbox delivery") own job, confirmed
// against IMPLEMENTATION_PLAN.md before this Step started. Building
// either here would be scope creep into that Step's own territory. An
// in-thread ack is also a latency-sensitive UX signal ("a mention was
// received") that Slack's own redelivery behavior means must happen
// synchronously, inside THIS request, well before a general outbox/
// notifier abstraction could plausibly exist. This package therefore
// makes the smallest possible direct call instead: ackClient (ack.go) is
// a tiny, unexported chat.postMessage client, used for exactly one
// message per processed event -- never a queued, retried, or
// dead-lettered delivery the way Step 35's real Notifier will be. A
// failure here is logged and swallowed (see step 8 above), never
// escalated into a redelivery.
package slack
