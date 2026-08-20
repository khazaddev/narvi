package outboxworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/provenance"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file implements §8.2's own ("sentinels + suggestions", §17.2)
// sentinel-auto-fix notifier: ports.NotificationKindSentinelAutoFix's own
// real Deliver -- spawning the child session §17.2 describes. Lives in
// internal/app/outboxworker, NOT internal/app/sessionactor, for the exact
// reason this Step's own design settled on: sessionactor cannot import
// httpapi (§24.3, TECHNICAL_PLAN.md:846, already documents this
// constraint for a structurally identical problem), but this notifier
// MUST call httpapi's own session-creation machinery (the one sanctioned
// way to create a session with a parent/provenance tag) -- outboxworker
// is already the "real outbound network/side-effect work, never
// synchronously in the HTTP request" layer every other Notifier in this
// package lives at, and is free to import httpapi the same way internal/
// adapters/inbound/github's own coalesce.go already does (that package's
// own doc comment: "already callable from outside httpapi by design").
//
// Audit fix (double-spawn): Deliver below calls httpapi.CreateSessionOnTx
// and httpapi.TriggerDispatch directly (spawnClaimedChildSession), inline
// on a transaction this file opens and holds its OWN atomic
// SentinelFixStore.LockForUpdate claim on -- NOT httpapi.SpawnChildSession
// (childsession.go), which is a correct, general-purpose helper but opens
// its OWN separate transaction, incompatible with composing an atomic
// claim-then-spawn the way this notifier needs. See
// spawnClaimedChildSession's own doc comment, and coalesce.go's own
// CreateOrJoin, for the identical "claim a row, then create a session
// under that SAME lock" precedent this mirrors. SpawnChildSession itself
// is unchanged and still exported for a future caller with no
// already-open transaction of its own.

// sentinelFixBranchPrefix names Deliver's own generated, distinct fix-
// branch names -- confirmed-finding fix, see Deliver's own doc comment
// below for the full "why". Deliberately mirrors internal/domain/gitstate.
// sessionBranchPrefix's own "narvi/" convention (a fixed, invented prefix,
// vanishingly unlikely to collide with any real branch a human or
// automation might have already created) rather than inventing an
// unrelated one.
const sentinelFixBranchPrefix = "narvi/sentinel-fix/"

// errRolloutRefused is spawnClaimedChildSession's own sentinel for "the
// origin PR's own repo is not enrolled in §10's cohort rollout" (§10
// Phase 6, §32: CreateSessionError.RolloutRefusal, checked structurally,
// never by string-matching). Deliver checks for this specifically and
// maps it to the existing terminal-skip precedent (descriptionautofix.go:
// "return nil, and the outbox marks this row delivered, never retried")
// rather than the outbox's ordinary backoff/retry/dead-letter path --
// unlike a transient Postgres/GitHub failure, retrying an identical
// redelivery of this SAME outbox row would reproduce this exact same
// refusal every time, since repo_settings.sessions_enabled does not
// change between redeliveries. See Deliver's own doc comment for the
// full "why".
var errRolloutRefused = errors.New("outboxworker: sentinelAutoFixNotifier: repo not enrolled in cohort rollout")

// sentinelFixBranchName derives the distinct upstream branch name Deliver
// creates (via SourceControl.CreateBranch) and has the fix child session
// check out and push to -- NEVER the origin PR's own head branch. Keyed
// off sentinelFixID (sentinel_fixes.id, already unique per (repo, PR)):
// deterministic and idempotent -- a redelivered outbox entry for the SAME
// claim computes the SAME branch name every time, never a fresh one per
// attempt (load-bearing for CreateBranch's own idempotent "already
// exists" handling, and for a retried Deliver to converge on the SAME
// child-session repo config rather than a different one each attempt).
func sentinelFixBranchName(sentinelFixID string) string {
	return sentinelFixBranchPrefix + sentinelFixID
}

// sentinelAutoFixNotifier implements ports.Notifier for
// ports.NotificationKindSentinelAutoFix.
type sentinelAutoFixNotifier struct {
	pool           *pgxpool.Pool
	sessions       *postgres.SessionStore
	turns          *postgres.TurnStore
	environments   *postgres.EnvironmentStore
	auditLog       *postgres.AuditLogStore
	registry       *sessionactor.Registry
	sentinelFixes  *postgres.SentinelFixStore
	reviewFindings *postgres.ReviewFindingStore
	// sourceControl/githubBotToken (confirmed-finding fix) let Deliver
	// create the fix session's own distinct upstream branch (see this
	// file's own Deliver doc comment) BEFORE ever spawning the child
	// session -- the SAME bot-attributed, static credential
	// createSentinelFixPRBestEffort (pushpr.go) already authenticates its
	// own fix-PR-creation calls with, never a per-user OAuth token (this
	// session has no human creator to decrypt one for).
	sourceControl  ports.SourceControl
	githubBotToken string
	timeouts       platform.Timeouts
	// epistemicCheckDefault (F6, adversarial review) is the SAME
	// platform.Config.EpistemicCheckDefault value every other
	// CreateSessionOnTx-reaching caller in this codebase now threads
	// through -- spawnClaimedChildSession's own httpapi.CreateSessionOnTx
	// call below is an ordinary (never review-session) build session, so
	// no F7-style hardcoded-false carve-out applies here.
	epistemicCheckDefault bool
	// rolloutMode/repoSettings (§10 Phase 6, §32) are the SAME
	// two REQUIRED httpapi.CreateSessionOnTx parameters every other
	// caller now threads through -- spawnClaimedChildSession's own
	// CreateSessionOnTx call below needs both. See Deliver's own updated
	// doc comment for how a rollout refusal here maps to the existing
	// "confirmed negative -> nil, never retried" terminal-skip precedent
	// (descriptionautofix.go), never the outbox's ordinary backoff/retry/
	// dead-letter path.
	rolloutMode  platform.RolloutMode
	repoSettings *postgres.RepoSettingsStore
}

var _ ports.Notifier = (*sentinelAutoFixNotifier)(nil)

// NewSentinelAutoFixNotifier builds a ports.Notifier for
// ports.NotificationKindSentinelAutoFix -- called once by cmd/control-
// plane/main.go's own kind->Notifier map assembly, mirroring every other
// notifier constructor's own identical "called exactly once" precedent.
// sourceControl/githubBotToken/timeouts are the SAME instances/values
// production wiring already constructs for every other GitHub-flavored
// notifier (e.g. githubapi.NewVerdictNotifier's own sourceControl/
// cfg.GitHubBotToken, cmd/control-plane/main.go).
func NewSentinelAutoFixNotifier(
	pool *pgxpool.Pool,
	sessions *postgres.SessionStore,
	turns *postgres.TurnStore,
	environments *postgres.EnvironmentStore,
	auditLog *postgres.AuditLogStore,
	registry *sessionactor.Registry,
	sentinelFixes *postgres.SentinelFixStore,
	reviewFindings *postgres.ReviewFindingStore,
	sourceControl ports.SourceControl,
	githubBotToken string,
	timeouts platform.Timeouts,
	epistemicCheckDefault bool,
	rolloutMode platform.RolloutMode,
	repoSettings *postgres.RepoSettingsStore,
) ports.Notifier {
	return &sentinelAutoFixNotifier{
		pool: pool, sessions: sessions, turns: turns, environments: environments,
		auditLog: auditLog, registry: registry, sentinelFixes: sentinelFixes, reviewFindings: reviewFindings,
		sourceControl: sourceControl, githubBotToken: githubBotToken, timeouts: timeouts,
		epistemicCheckDefault: epistemicCheckDefault,
		rolloutMode:           rolloutMode,
		repoSettings:          repoSettings,
	}
}

// sentinelAutoFixPromptText builds the fix session's own deterministic,
// server-rendered first prompt (never a raw pass-through of the finding's
// own agent-authored text alone) -- names the specific finding(s) this
// session exists to remediate, and states the two constraints §17.2
// requires explicitly (test/doc files only; build mode, no plan-mode
// gate) so the agent's own first turn has an honest, actionable brief
// even before it inspects the origin diff itself.
func sentinelAutoFixPromptText(descriptions []string) string {
	var b strings.Builder
	b.WriteString("You are a sentinel-auto-fix remediation session (Narvi, §17). ")
	b.WriteString("Your ONLY job is to fix the following coverage/doc-drift finding(s) from the origin pull request's own review, by adding the missing test(s) or updating the stale documentation -- nothing else:\n\n")
	for _, d := range descriptions {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	b.WriteString("\nYour write access is restricted to test and documentation files only. Do not modify any other file. Once your fix is complete and any relevant tests pass, push your branch -- a pull request against the origin branch will be opened automatically.")
	return b.String()
}

// Deliver implements ports.Notifier: unmarshals payload, checks the
// cheap, non-atomic idempotency fast path (a child session already
// spawned for this claim -- skip straight to the MarkFixPending tail,
// never re-spawn), otherwise atomically claims the sentinel_fixes row and
// spawns the child session together (spawnClaimedChildSession), then
// records the winning child session back onto every finding this
// delivery's own payload addresses (markFindingsFixPending).
//
// # Confirmed-finding fix: the fix session needs its OWN branch
//
// payload.OriginHeadBranch is captured once, at claim time (reviewverdict.
// go), as the LITERAL value the eventual fix PR's own Base will be
// assigned to (ports.SentinelAutoFixPayload's own doc comment) -- a real
// re-review confirmed that this same string was ALSO being handed
// straight to the child session's own repos[].branch, unchanged. Since a
// session's repos[].branch is the ONE field that decides both what a
// fresh boot's own `git clone --branch <name>` checks out AND what the
// eventual `git push` sends back to the SAME-named remote ref
// (internal/sandboxagent/gitclone.CloneAll / cmd/sandbox-agent's own
// pushOneRepo -- neither ever invents a distinct branch for a fresh
// clone), that meant the fix session cloned, committed onto, and pushed
// back to the ORIGIN PR's own live branch -- silently fast-forwarding a
// still-open, still-under-review pull request with an unreviewed
// automated commit -- and its OWN eventual fix-PR CreatePR call
// (createSentinelFixPRBestEffort, pushpr.go) was doomed to Head == Base,
// which GitHub's real API rejects outright ("no commits between X and
// X"), so the stacked-fix PR could never actually open at all.
//
// The fix: BEFORE ever spawning the child session, resolve
// payload.OriginHeadBranch's own CURRENT commit SHA (SourceControl.
// ResolveBranchSHA) and create a brand-new branch ref at that exact SHA
// (SourceControl.CreateBranch, sentinelFixBranchName's own deterministic
// name) -- content-identical to the origin branch's own tip at claim time,
// but a name that exists ONLY for this fix session to check out, commit
// onto, and push. Everything downstream (the boot-time clone/checkout,
// the eventual push, and createSentinelFixPRBestEffort's own Head:
// pushed.Branch) then automatically uses this NEW, distinct name with NO
// further changes anywhere else -- fix.OriginHeadBranch (the eventual PR's
// own Base) is completely untouched, so Head != Base by construction, and
// the origin PR's own branch is never touched by this flow at all.
//
// A failure to resolve the origin branch's SHA or create the new branch
// is returned as a real error (never silently falls back to
// payload.OriginHeadBranch, which would reproduce the exact bug this
// fixes) -- the outbox worker's own existing backoff/retry machinery
// retries this delivery later; nothing user-visible has happened yet (no
// child session, no push, no PR), so a retry is safe.
//
// # Audit fix: atomic dedupe guard (spawn + claim-write, one transaction)
//
// Before this fix, the ONLY dedupe guard was a plain FixChildSessionID.
// Valid check, and the row was only marked spawned AFTER
// httpapi.SpawnChildSession had already committed ITS OWN, separate
// transaction and fired TriggerDispatch -- two writes, not atomic. Outbox
// delivery is at-least-once (builder.go's own recordFailure reschedules on
// any returned error): if deliverCtx's own OutboxDeliveryTimeout expired
// (or a transient pool error, or the pod died) BETWEEN those two writes, a
// redelivered/retried Deliver call would still see FixChildSessionID.Valid
// == false, createFixBranch would be a harmless no-op (githubapi.
// CreateBranch treats 422 "already exists" as success), and the spawn
// would run AGAIN -- a genuine second sandbox session, pushing to the SAME
// deterministic sentinelFixBranchName, for the SAME sentinel_fixes row.
//
// Fixed by spawnClaimedChildSession below: the row's own SELECT ... FOR
// UPDATE lock (SentinelFixStore.LockForUpdate), the session insert
// (httpapi.CreateSessionOnTx, called INLINE on that SAME transaction --
// never httpapi.SpawnChildSession, which would need its OWN, separate
// transaction), and the fix_child_session_id write
// (SentinelFixStore.UpdateChildSession) all run on ONE transaction --
// mirroring internal/adapters/inbound/github's own coalesce.go
// CreateOrJoin (EnsureRow+LockForUpdate, then CreateSessionOnTx inline,
// then the guard write, then commit) for the structurally identical
// "claim a row, then create a session under that SAME lock" problem, and
// CreateSessionOnTx's own doc comment (create.go), which names exactly
// this shape ("an atomic per-resource claim lock ... before ever reaching
// this function") as the reason it takes an already-open tx rather than
// owning one itself. A concurrent or redelivered claimant blocks on the
// FOR UPDATE lock until the winner's transaction resolves: a commit means
// it observes FixChildSessionID already valid and never spawns; a
// rollback (a crash/cancellation before commit, or an error partway
// through) leaves NO trace at all -- Postgres aborts the whole
// transaction, including the row lock -- so a following retry starts
// completely clean, never wedged in a half-claimed state. TriggerDispatch
// still fires strictly AFTER commit, exactly like every other
// CreateSessionOnTx caller (create.go's own CreateSessionCore, coalesce.
// go's own WINNER path) -- firing it before commit would risk dispatching
// against a session a subsequent rollback then makes disappear.
//
// A caller that has ALREADY spawned -- either the cheap fast-path check
// below, or spawnClaimedChildSession losing its own FOR UPDATE race --
// does NOT return early: Deliver always still runs markFindingsFixPending
// using whichever fix_child_session_id actually won. This matters for two
// distinct reasons: (1) Finding 2 below -- an earlier attempt may have
// spawned successfully but failed partway through ITS OWN
// markFindingsFixPending tail, and only a following redelivery gets a
// chance to finish it; (2) reviewverdict.go's own SentinelFixStore.Claim
// only sets fix_child_session_id HERE, in this notifier, never at claim
// time -- so two DIFFERENT qualifying findings posted concurrently on the
// SAME PR can each legitimately enqueue their OWN
// NotificationKindSentinelAutoFix outbox row against the SAME
// sentinel_fixes id, each carrying its OWN FindingIdentityHashes, and the
// second one to reach spawnClaimedChildSession never spawns anything
// itself but must still mark its own findings fix_pending.
func (n *sentinelAutoFixNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	if notification.Kind != ports.NotificationKindSentinelAutoFix {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: unrecognized notification kind %q", notification.Kind)
	}

	var payload ports.SentinelAutoFixPayload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: decode payload: %w", err)
	}

	var fixID pgtype.UUID
	if err := fixID.Scan(payload.SentinelFixID); err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: malformed sentinelFixId %q: %w", payload.SentinelFixID, err)
	}

	fix, err := n.sentinelFixes.GetByID(ctx, fixID)
	if err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: get sentinel_fixes row: %w", err)
	}

	fixChildSessionID := fix.FixChildSessionID
	if !fixChildSessionID.Valid {
		// Cheap, non-atomic fast path: skips createFixBranch's two GitHub
		// round trips and the claim transaction entirely on the common
		// "nothing left to do" redelivery. NOT the correctness guard --
		// see spawnClaimedChildSession's own doc comment for the real,
		// atomic one this falls through to whenever this read is stale or
		// simply wrong (a race against a concurrent claimant).
		spawned, err := n.spawnClaimedChildSession(ctx, payload)
		if err != nil {
			if errors.Is(err, errRolloutRefused) {
				// (§10 Phase 6, §32): terminal-skip, mirroring
				// descriptionautofix.go's own "confirmed negative -> nil,
				// never retried" precedent -- see errRolloutRefused's own
				// doc comment for the full "why this is not the ordinary
				// backoff/retry/dead-letter path". markFindingsFixPending
				// is deliberately NOT called below: no fix child session
				// was ever created, so there is nothing real to record --
				// every addressed finding is left exactly as it was
				// (still 'open'), not falsely marked 'fix_pending'.
				platform.Logger(ctx).Warn("outboxworker: sentinelAutoFixNotifier: fix child session refused: repo not enrolled in cohort rollout; skipping, never retried",
					"repo", payload.RepoFullName, "origin_pr_number", payload.OriginPRNumber)
				return nil
			}
			return err
		}
		fixChildSessionID = spawned
	}

	return n.markFindingsFixPending(ctx, payload, fixChildSessionID)
}

// spawnClaimedChildSession creates the fix session's own distinct
// upstream branch (createFixBranch, OUTSIDE any transaction -- a real
// outbound GitHub API call must never hold a Postgres transaction open,
// mirroring coalesce.go's own identical "network call always outside any
// tx" discipline), then atomically claims the sentinel_fixes row (a
// SELECT ... FOR UPDATE lock, SentinelFixStore.LockForUpdate) and spawns
// the child session on ONE transaction -- see Deliver's own "Audit fix:
// atomic dedupe guard" doc comment above for the full "why".
//
// Returns the child session id that won the claim: either the one THIS
// call just created, or -- if another claimant (a concurrent Deliver call
// for a genuinely different outbox row targeting the SAME claim, or a
// redelivery of this SAME row that raced an earlier, still-in-flight
// attempt of itself) already committed one first -- that winner's own id,
// read back under the SAME lock. Never spawns twice: only the caller that
// observes FixChildSessionID.Valid == false under the lock proceeds to
// httpapi.CreateSessionOnTx.
func (n *sentinelAutoFixNotifier) spawnClaimedChildSession(ctx context.Context, payload ports.SentinelAutoFixPayload) (pgtype.UUID, error) {
	var parentSessionID pgtype.UUID
	if err := parentSessionID.Scan(payload.OriginReviewSessionID); err != nil {
		return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: malformed originReviewSessionId %q: %w", payload.OriginReviewSessionID, err)
	}

	branch, err := n.createFixBranch(ctx, payload)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: create fix session's own upstream branch: %w", err)
	}

	prompt := sentinelAutoFixPromptText(payload.FindingDescriptions)
	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Prompt:      restdtos.CreateSessionRequestPrompt(&prompt),
		Repos: []restdtos.CreateSessionRequestReposElem{
			{
				Name: payload.RepoName,
				Url:  payload.RepoCloneURL,
				// Branch here is the CHILD SESSION's own head branch to
				// check out and push FROM -- confirmed-finding fix: this is
				// now the brand-new, distinct branch createFixBranch just
				// created (content-identical to the origin branch's own tip
				// at claim time, so the fix session's own commits still
				// necessarily apply on top of the origin diff, exactly what
				// a stacked fix requires, §17.2) -- NEVER payload.
				// OriginHeadBranch's own literal value, which is reserved
				// for the eventual fix PR's own Base (pushpr.go's
				// createSentinelFixPRBestEffort) and must stay a genuinely
				// DIFFERENT branch from this one for that PR to ever open
				// at all.
				Branch: restdtos.CreateSessionRequestReposElemBranch(&branch),
			},
		},
	}

	tx, err := n.pool.Begin(ctx)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: begin claim-and-spawn tx: %w", err)
	}
	committed := false
	// Rollback is a safety net for every return path other than a
	// successful Commit below -- mirrors coalesce.go's own CreateOrJoin
	// and httpapi's own create.go identical pattern.
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	txFixes := n.sentinelFixes.WithTx(tx)

	// Locks the claim row for the rest of THIS transaction -- any
	// concurrent or redelivered claimant for the SAME (repoFullName,
	// originPRNumber) blocks here until this transaction commits or rolls
	// back. See LockForUpdate's own doc comment (sentinelfixes_store.go)
	// and this method's own doc comment above.
	locked, err := txFixes.LockForUpdate(ctx, payload.RepoFullName, payload.OriginPRNumber)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: lock sentinel_fixes row: %w", err)
	}
	if locked.FixChildSessionID.Valid {
		// Lost the race under the lock -- another claimant already spawned
		// and committed first (see this method's own doc comment for the
		// two distinct ways that can happen). Never spawn a second child
		// session for the SAME claim: roll back (the deferred Rollback
		// above handles it; committed is still false -- there is nothing
		// to undo anyway, since createFixBranch above is idempotent and
		// this transaction has not written anything) and report the
		// winner's own id.
		return locked.FixChildSessionID, nil
	}

	// provenanceTag is a local copy: httpapi.ChildSessionOptions.
	// ProvenanceTag wants a *string, and provenance.SentinelAutoFix is a
	// Go const -- its address cannot be taken directly.
	provenanceTag := provenance.SentinelAutoFix
	created, hasPrompt, cerr := httpapi.CreateSessionOnTx(ctx, tx, n.sessions, n.turns, n.environments, n.auditLog, req, pgtype.UUID{}, n.epistemicCheckDefault, n.rolloutMode, n.repoSettings, httpapi.ChildSessionOptions{
		ParentSessionID: parentSessionID,
		SpawnDepth:      1,
		ProvenanceTag:   &provenanceTag,
	})
	if cerr != nil {
		if cerr.RolloutRefusal {
			// (§10 Phase 6, §32): a PERMANENT policy refusal --
			// the origin PR's own repo is not enrolled -- never a
			// transient failure. errRolloutRefused lets Deliver (this
			// notifier's own entry point) distinguish this from every
			// other error this function can return, structurally
			// (errors.Is), never by string-matching cerr.Message.
			return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: spawn child session: %w", errRolloutRefused)
		}
		return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: spawn child session: %s", cerr.Message)
	}

	if _, err := txFixes.UpdateChildSession(ctx, locked.ID, created.ID); err != nil {
		return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: record child session on sentinel_fixes: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return pgtype.UUID{}, fmt.Errorf("outboxworker: sentinelAutoFixNotifier: commit claim-and-spawn tx: %w", err)
	}
	committed = true

	// Fire-and-forget, OUTSIDE the transaction above, and ONLY if a prompt
	// was actually created -- mirrors every other CreateSessionOnTx
	// caller's own post-commit TriggerDispatch sequencing (create.go's own
	// CreateSessionCore, coalesce.go's own WINNER path). hasPrompt is
	// always true in practice here (sentinelAutoFixPromptText always
	// returns non-empty text), but checked anyway, matching every other
	// caller's own identical gate.
	if hasPrompt {
		httpapi.TriggerDispatch(ctx, n.registry, created.ID)
	}

	return created.ID, nil
}

// markFindingsFixPending records fixChildSessionID onto every finding
// payload.FindingIdentityHashes addresses (§17.3: suppresses the manual
// apply-suggestion action for each). Called from Deliver both right after
// a fresh spawn and on the "someone else already spawned" path -- always
// with whichever fix_child_session_id actually won the claim (see
// Deliver's own doc comment for why this must run in both cases).
//
// Finding 2 audit fix: a genuine per-finding store failure (anything but
// pgx.ErrNoRows) is now COLLECTED and returned, never silently discarded.
// Before this fix, this loop's own `continue` discarded it with a doc
// comment claiming it was "logged by the delivery worker's own caller
// (builder.go's attempt)" -- that claim was false: builder.go's attempt
// only logs what Deliver itself RETURNS, and this error never used to
// propagate there at all. A transient Postgres error on one finding used
// to leave it (and, since the old code `continue`d rather than stopped,
// every finding after it that ALSO failed) stuck in its pre-fix state
// forever, with the child session already running and the outbox row
// marked delivered -- and nothing logged it.
//
// Every hash is still attempted even after an earlier one fails in the
// SAME call (mirrors the original loop's own "one finding's own row must
// not block the rest of the batch" isolation) -- but now the accumulated
// real failures are joined and returned once the loop finishes, so
// builder.go's own recordFailure retries the WHOLE Deliver call. That
// retry is safe: spawnClaimedChildSession never spawns twice (Deliver's
// own doc comment), and MarkReviewFindingFixPending's own guard (status
// IN ('open', 'fix_pending'), queries/reviewfindings.sql) makes
// re-running this SAME write harmless even for a finding this loop
// already reached successfully on an earlier, partially-failed attempt --
// either it is still 'fix_pending' with the SAME fixChildSessionID (a
// pure no-op rewrite) or it has since progressed past fix_pending
// (fix_open/fix_merged/fix_applied, e.g. the fix session finished and
// pushed before the retry ran), in which case the guard makes THIS a
// no-op pgx.ErrNoRows too, never a regression back to fix_pending.
//
// A bare pgx.ErrNoRows (this finding's own row having since disappeared,
// never having qualified, or already past fix_pending -- see the guard
// above) is still a benign, expected no-op, exactly as before.
func (n *sentinelAutoFixNotifier) markFindingsFixPending(ctx context.Context, payload ports.SentinelAutoFixPayload, fixChildSessionID pgtype.UUID) error {
	var errs []error
	for _, hash := range payload.FindingIdentityHashes {
		if _, err := n.reviewFindings.MarkFixPending(ctx, payload.RepoFullName, payload.OriginPRNumber, hash, fixChildSessionID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			errs = append(errs, fmt.Errorf("mark finding %q fix-pending: %w", hash, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: %w", errors.Join(errs...))
	}
	return nil
}

// createFixBranch resolves payload.OriginHeadBranch's own current commit
// SHA and creates a brand-new, distinct branch ref at that exact SHA --
// see Deliver's own doc comment above for the full "why". Returns the new
// branch's own name on success. Bounded by n.timeouts.RepoSHAResolutionTimeout
// for each of its two real outbound GitHub API calls, mirroring
// internal/app/sessionactor/pushpr.go's own resolvePRBaseBranch precedent
// for the identical class of call.
func (n *sentinelAutoFixNotifier) createFixBranch(ctx context.Context, payload ports.SentinelAutoFixPayload) (string, error) {
	if n.sourceControl == nil {
		return "", errors.New("no SourceControl configured")
	}

	if err := reposource.CheckRepoHost(payload.RepoCloneURL, ports.SupportedSourceControlHosts()...); err != nil {
		return "", fmt.Errorf("repo url does not name a supported source-control host: %w", err)
	}
	owner, repoName, err := reposource.ParseOwnerRepo(payload.RepoCloneURL)
	if err != nil {
		return "", fmt.Errorf("parse owner/repo from clone url: %w", err)
	}

	shaCtx, cancel := context.WithTimeout(ctx, n.timeouts.RepoSHAResolutionTimeout)
	sha, _, err := n.sourceControl.ResolveBranchSHA(shaCtx, ports.ResolveBranchSHASpec{
		Owner:  owner,
		Repo:   repoName,
		Branch: payload.OriginHeadBranch,
		Token:  n.githubBotToken,
	})
	cancel()
	if err != nil {
		return "", fmt.Errorf("resolve origin head branch %q current sha: %w", payload.OriginHeadBranch, err)
	}

	newBranch := sentinelFixBranchName(payload.SentinelFixID)

	createCtx, cancel := context.WithTimeout(ctx, n.timeouts.RepoSHAResolutionTimeout)
	err = n.sourceControl.CreateBranch(createCtx, ports.CreateBranchSpec{
		Owner:  owner,
		Repo:   repoName,
		Branch: newBranch,
		SHA:    sha,
		Token:  n.githubBotToken,
	})
	cancel()
	if err != nil {
		return "", fmt.Errorf("create branch %q at sha %q: %w", newBranch, sha, err)
	}

	return newBranch, nil
}
