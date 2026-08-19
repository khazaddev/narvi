# Slack guide

Everything Slack sends Narvi arrives at exactly two routes — one for
ordinary Events API traffic (mentions and thread replies), one for Block
Kit button clicks and modal submissions:

```json narvi-command
{"name": "Slack Events API webhook", "route": "POST /webhooks/slack"}
```

```json narvi-command
{"name": "Slack Interactivity webhook (buttons, modals)", "route": "POST /webhooks/slack/interactive"}
```

Both are signature-verified (Slack's own `v0=` HMAC scheme) and
freshness-checked (`platform.Timeouts.WebhookTimestampFreshnessWindow`,
5 min) before anything else happens; a bad signature or a stale timestamp
is rejected before any Postgres write. Every event is also deduplicated
by Slack's own globally-unique `event_id` — a redelivery (Slack retries
any event this handler doesn't `200` within its own ~3s budget) is
acknowledged without being reprocessed.

## What starts a new session

@-mention the bot, or DM it, to start a brand-new thread-mapped session
(`spawnSource: "slack"`). Only two Slack event types are ever acted on at
all — `app_mention` and `message` — everything else (reactions, channel
joins, file-share notices, ...) is acknowledged and silently ignored. A
`message` event carrying a **subtype** (an edit, a delete, a channel-join
notice) is ignored too, and so is any event carrying a `bot_id` — the
bot's own in-thread acknowledgments never re-trigger themselves.

```json narvi-command
{"name": "Mention or DM the bot to start a new session (shadow-classified, never LLM-routed in a way that changes behavior)", "classifier": {"surface": "slack", "source": "classifier"}}
```

**Negative — shadow classification only.** A brand-new Slack thread's
first message IS run through the LLM intent classifier
(`ports.IntentClassifier.Classify`) and a §18.4 routing-decision record IS
persisted for it — but §18.5's shadow gate has **zero production
consumers today**: nothing reads that record's `target`/`mode` to change
what actually happens. The message always becomes an ordinary build-mode
turn on a `request`-shaped session regardless of what the classifier
decided; only an explicit `revise:`-prefixed reply or a typed/clicked
plan verdict (below) changes that. Treat the classifier's own recorded
`target`/`mode` as an audit trail today, never as something that steers
your message.

**Negative — a default repo must be configured.** A brand-new mention
needs SOME repo to create a session against, and this deployment has no
per-channel repo-routing yet — only one operator-configured default
(`NARVI_SLACK_DEFAULT_REPO_NAME`/`NARVI_SLACK_DEFAULT_REPO_URL`, both
optional). If either is unset, a new mention gets an honest ack saying
ingress isn't configured with a default repo, and **no session is
created**. A reply on an already-mapped thread is unaffected — it never
needs a repo lookup.

## Replying in an existing thread

Reply anywhere in a thread Narvi already mapped to a session (Slack's own
`(channel_id, thread_ts)` identity) to add a turn.

**Negative — one turn in flight at a time, dropped not queued.** If the
session already has a non-terminal turn (`Pending`/`Dispatched`/
`Processing`), your reply is **acknowledged but dropped** —
`CreateTurnPolicy.DropIfOpen` — with an honest "still working on the
previous message" in-thread reply. This is deliberately different from
[web.md](web.md) (which returns `409` instead) and from
[github.md](github.md) (which queues a backlog turn and never drops
anything).

**Negative — authorization gate on every reply.** Replying requires the
`approve_plan`/`prompt_session`-shaped own-or-joined check (§13.3) against
your linked Narvi identity. An unlinked Slack account gets one auto-link
attempt (matched by verified email against an existing Narvi user) with a
private, ephemeral confirmation when it succeeds — but a reply from an
account that stays unresolved, or that resolves to a user without prompt
access to that specific session, gets a **whole-channel-visible** "not
authorized" ack (not a DM), and the reply is never added as a turn.

## Plan mode: approve, reject, revise

While a session has a plan `awaiting_approval`, three additional inputs
are recognized on top of ordinary replies — all three go through the
exact same `httpapi.DecidePlan` core [web.md](web.md)'s own approve/reject
REST endpoints use, so a plan decided here is decided everywhere.

```json narvi-command
{"name": "Type an approve keyword (\"approve\", \"approved\", or \"lgtm\") in the thread", "route": "POST /webhooks/slack"}
```

```json narvi-command
{"name": "Type a reject keyword (\"reject\", \"rejected\", or \"no\") in the thread", "route": "POST /webhooks/slack"}
```

```json narvi-command
{"name": "Type \"revise: <feedback>\" to request changes as a real revision turn", "route": "POST /webhooks/slack"}
```

```json narvi-command
{"name": "Click the Approve or Reject button on the plan message", "route": "POST /webhooks/slack/interactive"}
```

```json narvi-command
{"name": "Click \"Request changes\" and submit the feedback modal", "route": "POST /webhooks/slack/interactive"}
```

**Negatives.**

- The keyword match is **exact, whole-message, case-insensitive** —
  `plandomain.MatchVerdict` matches the ENTIRE trimmed, lower-cased
  message against `{"approve","approved","lgtm"}` /
  `{"reject","rejected","no"}`, never a substring. "approve this please"
  does not match — it falls through to an ordinary reply, which the
  awaiting-approval gate then holds with a clarifying ack, since almost
  no plain-build-mode reply is accepted while a plan is pending (see
  below).
- `revise:` with no real feedback after the prefix (blank, or only
  whitespace/zero-width characters) is refused with its own specific
  ack — it does **not** silently start an empty revision turn.
- **The "Request changes" modal is the one plan-mode entry point that can
  return `409` instead of dropping.** Its submission goes through
  `CreateTurnPolicy.RejectIfOpen` — the same REST policy, not the
  `DropIfOpen` ordinary-reply policy above — because a modal submission
  has a real, visible failure surface (an inline modal error) to report
  a conflict through, unlike a plain thread message.
- **While a plan is `awaiting_approval`, almost nothing else gets
  through.** An ordinary reply that matches neither a verdict keyword nor
  `revise:` is held by the same gate `createTurnLocked` enforces for
  every surface — you get an honest "there's a plan awaiting your
  decision" ack, and no turn is created from it.

## What Slack does not support

- No slash commands (`/narvi ...`) — only @mentions, DMs, thread replies,
  and Block Kit interactions.
- No way to change which repo a session targets after creation, and no
  per-channel default repo override — one deployment-wide default only.
- The in-thread acknowledgment this adapter posts is **best-effort and
  synchronous** — it is not queued, retried, or dead-lettered the way an
  outbox-delivered notification is (§8.10's own outbox-backed
  notifications are a separate mechanism, built on top of this ingress
  adapter, not part of it). A failed ack post is logged and silently
  dropped; it never causes a redelivery or a visible error to the user.
