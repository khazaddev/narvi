package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/actorauthz"
	"github.com/khazaddev/narvi/internal/app/intentclassifier"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/authz"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	"github.com/khazaddev/narvi/internal/platform"
)

// intentClassifierSurface is the sessions.spawn_source value (§18.1's
// IntentClassifierInput.Surface / §18.4's IntentDecisionRecord.Surface)
// this package's own mentions are classified/recorded under.
const intentClassifierSurface = "github"

// ErrActorNotAuthorized is CreateOrJoin's own sentinel for "a resolved,
// linked commenter's role failed domain/authz.Authorize" -- batch
// fix/audit-github-actor-rbac's own addition (see identity.go's own top
// doc comment for the full finding this closes). Deliberately DISTINCT
// from every other error CreateOrJoin returns: handler.go checks for this
// one specifically and responds 200 without releasing the claimed webhook
// delivery for a GitHub redelivery retry -- retrying a denied comment
// changes nothing (the SAME actor would be denied again), unlike every
// OTHER CreateOrJoin error (a transient Postgres failure, say), which
// SHOULD be retried via GitHub's own redelivery mechanism.
var ErrActorNotAuthorized = errors.New("github: actor not authorized")

// SessionCoalescer bundles the stores/registry CreateOrJoin needs -- a
// small struct rather than a long positional-parameter list, constructed
// once at wiring time (cmd/control-plane/main.go), mirroring this
// codebase's own construct-once-thread-through convention for every other
// store/registry pair. Environments is only here because
// httpapi.CreateSessionOnTx's own signature requires a
// *postgres.EnvironmentStore argument -- a GitHub-sourced
// restdtos.CreateSessionRequest never sets PathScope or MockConfig
// (handler.go never populates either), so the CreateSessionOnTx call below
// never actually exercises the environment-insert branch for any request
// this package ever hands it; Environments is simply threaded through
// unused on this path.
//
// IntentClassifier is Step 36's own wiring point (§8.3/§18): classify+
// record runs ONCE, on the WINNER (brand-new session) path only -- see
// CreateOrJoin's own doc comment below for why the REUSE path never
// re-classifies. Optional (nil-safe): a nil IntentClassifier simply skips
// classification entirely, so existing tests/wiring that don't care about
// this Step keep working unchanged.
type SessionCoalescer struct {
	Pool             *pgxpool.Pool
	PRSessions       *postgres.GitHubPRSessionStore
	Sessions         *postgres.SessionStore
	Turns            *postgres.TurnStore
	Environments     *postgres.EnvironmentStore
	Registry         *sessionactor.Registry
	IntentClassifier *intentclassifier.Service

	// Plans (Step 37/38 follow-up fix, §8.1) is threaded through to the
	// REUSE path's own httpapi.CreateTurnForBot call below, exactly like
	// every other createTurnLocked caller now gets -- see that function's
	// own doc comment (httpapi/turn.go) for the nil-safe "skips the
	// awaiting-plan gate" contract a nil value here keeps.
	Plans *postgres.PlanStore

	// AuditLog is Step 39's own addition (§13.3): threaded through to the
	// WINNER path's own httpapi.CreateSessionOnTx call below, exactly like
	// Environments already is, so a GitHub-originated session creation
	// gets the SAME audit_log row every other CreateSessionOnTx caller now
	// gets. actor_user_id is NULL only until batch fix/audit-github-actor-
	// rbac's own commenter-identity resolution (identity.go) resolves a
	// real user -- otherwise it carries that resolved user_id, exactly
	// mirroring created_by's own identical convention below.
	AuditLog *postgres.AuditLogStore

	// Identities/Users/Participants are batch fix/audit-github-actor-rbac's
	// own additions, closing the H4 audit finding that GitHub ingress never
	// gated session/turn creation behind domain/authz.Authorize at all
	// (Slack/Linear ingress already do, since Step 39). Identities backs
	// handler.go's own resolveCommenterActor (identity.go) -- a direct
	// (provider, external_id) lookup, no auto-linking algorithm needed (see
	// that file's own doc comment for why). Users/Participants are exactly
	// the SAME two collaborators actorauthz.AuthorizeLinkedActor (batch
	// fix/deny-unlinked-github-actors; formerly AuthorizeResolvedActor)/
	// actorauthz.OwnedOrJoined need, mirroring Slack's/Linear's own
	// Deps.IdentityLink.Users / Deps.Participants precedent -- production
	// wiring (cmd/control-plane/main.go) passes the SAME userStore/
	// participantStore/identityStore instances every other caller already
	// uses, never a second, independently-constructed copy of any of them.
	Identities   *postgres.IdentityStore
	Users        *postgres.UserStore
	Participants *postgres.ParticipantStore
}

// CreateOrJoin is Step 32's own per-PR coalescing entry point -- see
// doc.go's own "Per-PR coalescing design" section for the full two-step
// atomic-claim sequencing this implements. isNewSession reports which
// branch was taken (true: req was used to create a brand-new review
// session; false: an existing session for this PR was reused and only a
// new turn was enqueued on it) -- callers use it purely for
// logging/observability, never for a different response to GitHub (both
// branches ack 200 identically).
//
// # Connection-pool safety note (why the WINNER path does NOT call
// httpapi.CreateSessionForBot)
//
// This function holds ONE claim-row lock (LockForUpdate below) inside ONE
// transaction (tx) for its own entire winner-path critical section. If
// that critical section called httpapi.CreateSessionForBot -- which opens
// its OWN, separate transaction via *pgxpool.Pool.Begin -- a single
// request would need TWO simultaneous connections out of the SAME pool:
// one held open by tx (this function's own claim transaction) and one
// acquired by CreateSessionForBot's own inner Begin. Under enough
// concurrent @mentions on the SAME PR (enough that every OTHER, losing
// goroutine's own LockForUpdate call has also already acquired a
// connection and is parked waiting on Postgres's own row lock), the pool
// could be fully exhausted by parked losers by the time the winner tries
// to acquire ITS OWN second connection -- a genuine connection-pool
// deadlock (nothing can release a connection until the winner commits,
// and the winner cannot commit until it acquires a second connection that
// will never come). This is NOT hypothetical: pgxpool's default MaxConns
// is a small, fixed number (independent of this request's own
// concurrency), so it is the wrong assumption to lean on "the pool
// probably has enough spare capacity".
//
// The fix: the winner path below calls httpapi.CreateSessionOnTx directly,
// INLINE on the SAME tx/connection the claim lock already holds -- never a
// second connection. CreateSessionOnTx is the shared, exported piece of
// CreateSessionCore's own logic (internal/adapters/inbound/httpapi/
// create.go) that takes an ALREADY-OPEN transaction the caller owns
// entirely, built for exactly this "already holding an unrelated lock on
// my own open transaction" shape -- so this package no longer needs to
// hand-duplicate any repo-validation/session-insert/turn-insert logic of
// its own to get the same never-a-second-connection guarantee.
// httpapi.CreateSessionForBot itself is untouched and still
// exported/tested (bot.go) as a general-purpose, no-coalescing entry
// point for a caller that is NOT simultaneously holding a claim-row lock
// (e.g. a future Slack/Linear ingress path with no per-thread coalescing
// of its own).
//
// The REUSE (loser) path below has no such risk: it commits tx BEFORE
// calling httpapi.CreateTurnForBot, so only ever one connection is open
// at a time there too.
//
// # actor / domain/authz.Authorize gating (batch fix/audit-github-actor-rbac;
// # hardened to a DENY by batch fix/deny-unlinked-github-actors)
//
// actor is handler.go's own already-resolved commenter (identity.go's
// resolveCommenterActor) -- Valid iff this exact GitHub commenter already
// has a linked Narvi account, invalid otherwise, exactly mirroring Slack's/
// Linear's own resolved-actor precedent (§13.2).
//
// An invalid actor is now DENIED outright by BOTH authorization checks
// below (actorauthz.AuthorizeLinkedActor's own `!actorUserID.Valid ->
// false` short-circuit) -- a deliberate, repo-owner-decided reversal of
// this package's own PRIOR behavior (bot attribution: an unresolved
// commenter's action proceeded, gated by nothing). See
// AuthorizeLinkedActor's own doc comment (internal/app/actorauthz/
// authorize.go) for the full "why GitHub can now use this function, unlike
// when it was first written" reasoning, and handler.go's own
// ErrActorNotAuthorized branch for the actionable "please sign in" reply
// this denial now comes with (GitHub has no magic-link/pending-link
// mechanism to send in parallel the way Slack/Linear do, so a plain deny
// with no explanation would leave the commenter with nothing -- see
// actornotauthorizedreply.go's own doc comment).
//
// The WINNER path's own domain/authz.Authorize(ActionCreateSession) check
// (createAuthorized below) is deliberately resolved BEFORE tx.Begin, never
// inside the open claim transaction: actorauthz.AuthorizeLinkedActor
// performs its own Postgres read (users.GetByID) when actor IS resolved,
// and acquiring a SECOND pool connection while already holding tx open is
// exactly the connection-pool exhaustion risk this function's own
// "connection-pool safety note" above already goes to lengths to avoid
// for httpapi.CreateSessionForBot -- the same discipline applies here:
// resolve it once, cheaply, with no ambient transaction, then just
// consult the already-computed bool once inside the critical section (no
// query, no risk). The REUSE path's own domain/authz.
// Authorize(ActionPromptSession) check (below, ownership-aware) runs
// AFTER that path's own tx.Commit -- by then no transaction is open at
// all, so there is nothing to protect there either. Denying here (either
// path) leaves the claim row exactly as safe as an authorized denial
// always was -- see this function's own "claim row on the deny path"
// note further down, at each denial site, for why.
func (c *SessionCoalescer) CreateOrJoin(ctx context.Context, repoFullName string, prNumber int32, req restdtos.CreateSessionRequest, actor pgtype.UUID) (session sqlcgen.Session, turn sqlcgen.Turn, isNewSession bool, err error) {
	logger := platform.Logger(ctx)

	// Resolved BEFORE any transaction opens -- see this function's own
	// doc comment above for why. Only actually consulted by the WINNER
	// branch below (Resource{}: creating a session has no ownership
	// concept); the REUSE branch renders its OWN, ownership-aware
	// ActionPromptSession verdict further down instead, since a member
	// who may always create a session might still lack the "own/joined"
	// carve-out ActionPromptSession requires for the SAME actor against a
	// DIFFERENT, already-existing session.
	createAuthorized := actorauthz.AuthorizeLinkedActor(ctx, logger, authzSurface, c.Users, actor, authz.ActionCreateSession, authz.Resource{})

	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: begin claim tx: %w", err)
	}
	committed := false
	// Rollback is a safety net for every return path other than a
	// successful Commit below -- mirrors httpapi's own identical pattern
	// (create.go, turn.go, bot.go).
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	txPRSessions := c.PRSessions.WithTx(tx)
	if err := txPRSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: ensure claim row: %w", err)
	}

	// Locks the claim row for the rest of THIS transaction -- any
	// concurrent caller's own EnsureRow+LockForUpdate for the SAME
	// (repoFullName, prNumber) blocks here until this transaction commits
	// or rolls back. See migrations/000028_github_pr_sessions.up.sql's own
	// doc comment for the full reasoning.
	existing, err := txPRSessions.LockForUpdate(ctx, repoFullName, prNumber)
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: lock claim row: %w", err)
	}

	if existing.Valid {
		// Reuse case: this PR already has a review session. Nothing to
		// write to the claim row itself -- commit now (releasing the
		// lock, and this transaction's own connection, for whoever, if
		// anyone, is still queued behind it) BEFORE doing the SEPARATE,
		// independent work of enqueuing a new turn on the existing
		// session. Only one connection is ever open at a time on this
		// path.
		if err := tx.Commit(ctx); err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: commit claim tx (reuse path): %w", err)
		}
		committed = true

		// No transaction open from here on -- see this function's own top
		// doc comment for why the ownership-aware ActionPromptSession check
		// deliberately runs here, post-commit, rather than inside the
		// critical section above.
		existingSession, err := c.Sessions.Get(ctx, existing)
		if err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: get existing session: %w", err)
		}

		// actorauthz.AuthorizeLinkedActor is now called UNCONDITIONALLY --
		// batch fix/deny-unlinked-github-actors removed the previous
		// "if actor.Valid" guard that used to skip this whole block for an
		// unresolved commenter (bot attribution, the pre-batch behavior).
		// AuthorizeLinkedActor's own `!actorUserID.Valid -> false`
		// short-circuit now does the denying for that case, exactly
		// mirroring Slack's/Linear's own authorizeSessionAction call
		// (§13.2's hardened "a not-yet-linked identity's state-changing
		// action is denied" precedent) -- see this function's own top doc
		// comment for the full "why" and handler.go's own
		// ErrActorNotAuthorized branch for the reply this now triggers.
		//
		// OwnedOrJoined's own Participants read is still only performed
		// when actor IS valid: an invalid actor is denied by
		// AuthorizeLinkedActor regardless of what Resource.OwnedOrJoined
		// holds, so computing it for an unresolved commenter would just be
		// a wasted Postgres read on what is, today, still the common case
		// -- the SAME "no query, no risk" discipline this function's own
		// top doc comment already applies to createAuthorized above,
		// simply preserved here rather than lost when the guard came out.
		var joined bool
		if actor.Valid {
			var err error
			joined, err = actorauthz.OwnedOrJoined(ctx, c.Participants, existingSession, actor)
			if err != nil {
				return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: check participant for authorization: %w", err)
			}
		}
		if !actorauthz.AuthorizeLinkedActor(ctx, logger, authzSurface, c.Users, actor, authz.ActionPromptSession, authz.Resource{OwnedOrJoined: joined}) {
			logger.Warn("github: prompt on existing session denied by authz", "session_id", existingSession.ID, "repo", repoFullName, "pr_number", prNumber, "user_id", actor.String())
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, ErrActorNotAuthorized
		}

		var prompt string
		if req.Prompt != nil {
			prompt = *req.Prompt
		}
		// c.AuditLog/actor (audit-fix batch addition, H7): CreateTurnForBot
		// now writes the SAME turn.create audit_log row every other
		// createTurnLocked caller does, inside its own transaction -- actor
		// is the SAME already-resolved commenter identity passed to the
		// authz checks above (Valid iff linked, invalid/bot-attributed
		// otherwise), never a second, independently-resolved actor.
		createdTurn, err := httpapi.CreateTurnForBot(ctx, c.Pool, c.Sessions, c.Turns, c.Plans, c.AuditLog, c.Registry, existing, prompt, (*string)(req.ModelId), req.PlanMode, actor)
		if err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: create turn on existing session: %w", err)
		}

		logger.Info("github: coalesced mention onto existing review session",
			"session_id", existingSession.ID, "turn_id", createdTurn.ID, "repo", repoFullName, "pr_number", prNumber)
		return existingSession, createdTurn, false, nil
	}

	// Winner case: still holding the claim row lock (this transaction is
	// uncommitted). createAuthorized was already resolved, with no ambient
	// transaction, before this function even opened tx -- see this
	// function's own top doc comment for why -- so denying here needs no
	// further query at all: just roll back (the deferred Rollback above
	// handles it, since committed is still false) and report the denial.
	//
	// Claim row on the deny path (batch fix/deny-unlinked-github-actors,
	// traced explicitly, not assumed, since this path now denies far more
	// often than before): EnsureRow's own INSERT (above) ran INSIDE this
	// same uncommitted tx, so the Rollback this !createAuthorized branch
	// triggers undoes it too -- if this was the very first claim attempt
	// for this (repoFullName, prNumber), a denied WINNER leaves NO claim
	// row behind at all, not an orphaned one. A concurrently blocked
	// second caller parked on this SAME PR's own LockForUpdate (a
	// different commenter's mention, say) proceeds exactly as if the
	// denied attempt never happened, and gets its own independent,
	// fair authorization verdict -- no fix needed here, the existing
	// transaction boundary already makes this safe.
	if !createAuthorized {
		logger.Warn("github: create-session denied by authz", "repo", repoFullName, "pr_number", prNumber, "user_id", actor.String())
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, ErrActorNotAuthorized
	}

	// Create the session AND its turn INLINE, on this SAME tx/connection,
	// via the shared httpapi.CreateSessionOnTx (see this function's own
	// "connection-pool safety" doc comment above for why NOT httpapi.
	// CreateSessionForBot here). createdBy is actor -- batch
	// fix/audit-github-actor-rbac's own change: Valid (a real Narvi
	// user_id, attributed exactly like the REST API/Slack/Linear already
	// attribute a resolved creator) iff this commenter is linked, still
	// the pgtype.UUID zero value (Valid == false, a genuine SQL NULL,
	// today's existing bot-attribution behavior) otherwise -- mirrors
	// Slack's resolveOrClaimSession / Linear's handleCreated passing their
	// own resolved creator through to CreateSessionCore identically.
	created, hasPrompt, cerr := httpapi.CreateSessionOnTx(ctx, tx, c.Sessions, c.Turns, c.Environments, c.AuditLog, req, actor)
	if cerr != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: create session: %w", cerr)
	}

	// A GitHub mention always carries a real comment body -- handler.go
	// always populates req.Prompt -- so hasPrompt is always true on this
	// path in practice; CreateSessionOnTx doesn't hand the inserted turn
	// row back directly, so it's fetched here, still INSIDE this same
	// uncommitted tx (WithTx(tx), not a fresh pool connection) and still
	// holding the claim-row lock -- the only turn that can possibly exist
	// for this brand-new session.ID at this point is the one
	// CreateSessionOnTx just inserted; no concurrent caller can have
	// enqueued a turn of its own onto this session yet, since SetSessionID
	// below (which is what makes this session visible to a concurrent
	// REUSE-path caller at all) hasn't even run yet, let alone committed.
	// Fetching this AFTER commit instead would be a genuine race: a
	// concurrent loser could observe the just-committed session_id and
	// enqueue its own turn before this function's own ListForSession call
	// ran, breaking the "exactly one turn" assumption below under real
	// concurrent load.
	var createdTurn sqlcgen.Turn
	if hasPrompt {
		turnRows, err := c.Turns.WithTx(tx).ListForSession(ctx, created.ID)
		if err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: list turns for new session: %w", err)
		}
		if len(turnRows) != 1 {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: expected exactly one turn for new session, got %d", len(turnRows))
		}
		createdTurn = turnRows[0]
	}

	if err := txPRSessions.SetSessionID(ctx, repoFullName, prNumber, created.ID); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: set claim session id: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: commit claim tx (winner path): %w", err)
	}
	committed = true

	// Fire-and-forget, OUTSIDE the transaction above, and ONLY if a
	// prompt/turn was actually created -- mirrors every other
	// CreateSessionOnTx caller's own post-commit TriggerDispatch
	// sequencing (create.go's own CreateSessionCore does the same,
	// gated on the same hasPrompt CreateSessionOnTx returned).
	if hasPrompt {
		httpapi.TriggerDispatch(ctx, c.Registry, created.ID)
	}

	// Step 36 ("intent classifier", §8.3/§18): classify + record ONCE, on
	// this winner (brand-new session) path only -- IntentDecisionRecord
	// is a per-SESSION record (§18.4), and every GitHub-originated session
	// is created exactly here, so there is no gap left by never
	// re-classifying on the REUSE path above (a later @mention on an
	// already-tracked PR reuses a session that already went through this
	// exact path once). Runs entirely OUTSIDE the transaction above (a
	// real outbound LLM call must never hold a Postgres transaction open,
	// mirroring ports.Notifier.Deliver's/ports.SourceControl.CreatePR's own
	// identical "network call always outside any tx" discipline), and
	// never blocks the ack response on its own outcome beyond this
	// synchronous call -- shadow mode (§18.5, the default for every
	// surface until explicitly configured active) means nothing downstream
	// yet consumes the recorded Target/Mode for real behavior regardless.
	if hasPrompt && c.IntentClassifier != nil && req.Prompt != nil {
		// classify+record is now the shared intentclassifier.Service.
		// ClassifyAndRecord (H9/L11 audit fix) -- see that method's own
		// doc comment for the full "why a single shared call" reasoning.
		// This site's own two genuine differences from Slack/Linear are
		// both still expressed here directly: DecidedAtStageCreate (this
		// package's own IntentClassifierInput never has a "first prompt"
		// stage; the full prompt is always in hand at session-create
		// time), and DeterministicTarget below.
		c.IntentClassifier.ClassifyAndRecord(ctx, created.ID, ports.IntentClassifierInput{
			Text:    *req.Prompt,
			Surface: intentClassifierSurface,
			// DeterministicTarget IS a real, already-known signal here,
			// not an absent one: CreateOrJoin (this function) is only ever
			// reached via parseMention (handler.go/payload.go) resolving to
			// a genuine PR-scoped mention -- parseIssueComment explicitly
			// rejects a plain-issue comment (p.Issue.PullRequest == nil:
			// "A comment on a plain issue, not a PR -- §8.2 is PR review
			// only"), and parsePullRequestReviewComment's own event type
			// ("pull_request_review_comment") never fires for anything
			// other than a PR. So simply being on this code path at all --
			// regardless of which of the two event types produced it --
			// already deterministically means this mention landed on a
			// pull request, i.e. Target should be "review". This is
			// distinct from (and available strictly earlier than) the
			// existing-tracked-PR signal the REUSE path above has, which
			// never re-classifies anyway.
			DeterministicTarget: intentdomain.TargetReview,
		}, intentdomain.DecidedAtStageCreate)
	}

	logger.Info("github: created new review session for mention",
		"session_id", created.ID, "turn_id", createdTurn.ID, "repo", repoFullName, "pr_number", prNumber)
	return created, createdTurn, true, nil
}
