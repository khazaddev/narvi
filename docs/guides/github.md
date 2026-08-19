# GitHub guide

Everything GitHub sends Narvi arrives at one route:

```json narvi-command
{"name": "GitHub webhook", "route": "POST /webhooks/github"}
```

HMAC-verified (`X-Hub-Signature-256`) over the raw body before anything
else happens, and deduplicated by GitHub's own `X-GitHub-Delivery`
header — a redelivery is acknowledged without being reprocessed. Only two
`X-GitHub-Event` values can ever start or continue a review session:
`issue_comment` (a comment on an issue OR a pull request — GitHub's own
API models a PR as a kind of issue; only the PR case is acted on) and
`pull_request_review_comment` (a comment on a specific diff line). Every
other event type — pushes, stars, issue events on a genuine (non-PR)
issue, reviews, checks, ... — is acknowledged and silently ignored.

## What triggers a session or a turn

**@-mention the configured bot handle** in a PR comment or a diff-line
review comment:

```json narvi-command
{"name": "@-mention the bot in a PR comment or review comment (always classified target=review, deterministically — not the LLM's own judgment call)", "classifier": {"surface": "github", "target": "review"}}
```

**Apply the configured re-review label** to a pull request:

```json narvi-command
{"name": "Apply the configured re-review label to re-trigger a review", "classifier": {"surface": "github", "target": "review"}}
```

Both resolve to the exact same "mention" shape internally and are handed
to the same per-PR coalescing claim — a first trigger on a PR (either
kind) creates a brand-new review session; every later trigger on the SAME
PR (either kind, from anyone) **joins** it instead of creating a
duplicate. Unlike Slack/Linear's shadow-only classification (see
[slack.md](slack.md)/[linear.md](linear.md)), GitHub's own `target` is
**deterministically forced to `"review"`** before the LLM classifier ever
runs — being reached at all structurally means a PR was mentioned or
labeled, so there is no ambiguity for a model to resolve. The mention
regex is case-insensitive and rejects both a longer handle that merely
starts with yours (`@narvi` never matches `@narvi-bot-2`) and a team
mention sharing your handle as a prefix (`@narvi` never matches
`@narvi/maintainers`).

**Negative — always queued, never dropped, never conflicted.** Unlike
every other surface, GitHub-triggered turns use
`CreateTurnPolicy.AlwaysQueue`: N concurrent mentions on a PR that
already has a review session all succeed, producing exactly one session
and N turns, dispatched one at a time — never a `409` ([web.md](web.md)),
never a silently dropped message ([slack.md](slack.md)/
[linear.md](linear.md)). There is no way to jump the queue.

A third, REST-only re-trigger surface exists — see [web.md](web.md)'s own
`POST /api/sessions/{sessionID}/review/retrigger` — targeting an
already-known session directly rather than routing through this per-PR
mention claim.

## Review verdicts and decision inbox

Posting a review verdict, applying a suggested fix, and rebutting a
finding are REST-only actions a human takes on the web surface after a
GitHub-triggered review posts its results — see [web.md](web.md)'s own
"Review" and "Decision inbox" sections. GitHub ingress itself never
accepts a verdict/rebuttal/apply-suggestion command directly; it only
starts and continues the review session those actions operate on.

## Honest negatives

**A comment on a plain issue (not a PR) is never acted on**, even if it
mentions the bot by handle — `§8.2` is PR review only, and
`parseIssueComment` explicitly rejects the non-PR case before a mention
is ever even checked for.

**An unlinked GitHub account gets a reply — an unenrolled repo gets
none.** These look similar (a mention that "does nothing new") but are
structurally different refusals:

- A commenter whose GitHub account isn't linked to a Narvi identity
  (`ErrActorNotAuthorized`, `actor.Valid == false`) gets **one reply**
  per PR within `platform.Timeouts.GitHubActorNoticeTTL` (24h) explaining
  how to link an account — never a second reply spamming the same PR
  within that window for a repeat mention from the same unlinked
  account.
- Under cohort rollout (`NARVI_ROLLOUT_MODE=cohort`, [see the production
  checklist](../PRODUCTION_CHECKLIST.md)), a repo that is not enrolled
  refuses **with total silence on GitHub specifically** — no comment, no
  label, no status check, nothing an outside observer could distinguish
  from the bot simply not existing. This is deliberately stricter than
  every other refusal on this surface: an unenrolled repo gives a
  commenter no action to take (linking an account wouldn't help), so
  there is nothing honest to say beyond silence, and posting anything
  would itself be the exact customer-repository-visible egress cohort
  rollout exists to prevent before a repo is actually onboarded. Slack
  and Linear post an honest denial notice for the equivalent refusal
  instead — see `docs/TECHNICAL_PLAN.md` §32.5's own full per-channel
  table for why GitHub alone is silent.

**No slash commands, no free-text prompting.** A GitHub PR comment is
either a recognized @-mention (starts or joins a review) or it is
nothing at all — there is no way to send GitHub-ingress Narvi an
arbitrary build-mode prompt the way a Slack/Linear message can; every
GitHub-originated turn is review-shaped.

**No cancellation.** Identical limitation to [linear.md](linear.md)'s own
— there is no session/turn stop command anywhere in this codebase yet;
an in-flight review turn runs to completion or `platform.Timeouts.
TurnDeadline` (60 min), whichever comes first.
