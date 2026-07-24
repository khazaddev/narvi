// This file (pushpr.go) implements the two remaining halves of Step 21's
// ("e2e happy path") own end-to-end wiring that design decisions 3/6/7/8/9
// alone do not connect: (1) completing whichever turn is currently
// Processing when a REAL execution_complete event arrives from the
// sandbox (§3.3: "On terminal event: complete turn... re-derive session
// status, dispatch next pending" -- the turn-lifecycle half of §3.3 that
// domain/turn.TriggerComplete/TriggerFail already exist for, per their own
// doc comments, but that nothing outside this file's own tests ever
// called before this Step, since no turn could ever reach Processing in
// production before this Step's own dispatch.go existed either); (2)
// actually sending the `push` command once a turn completes successfully,
// and actually calling ports.SourceControl.CreatePR once the resulting
// push_complete event arrives -- the wiring gap an independent verifier's
// review of this Step found: ports.SourceControl/githubapi.Adapter were
// built and unit-tested in isolation but never constructed or invoked
// anywhere in the running system.
//
// Both halves are invoked from handleSandboxEvent (sandboxevent.go):
// completeProcessingTurn runs INSIDE that function's own transact (a
// turn's terminal transition is a real state write, subject to the exact
// same epoch-fencing/commit-or-rollback discipline every other write in
// this package already gets); sendPushBestEffort and createPRBestEffort
// both run AFTER that transact has already committed, and never inside
// any Postgres transaction of their own for their OWN network calls
// (SandboxCommander.SendCommand is a bounded WS write; SourceControl.
// CreatePR is a real outbound HTTP call to GitHub) -- exactly the same
// "a real network call must never hold a Postgres transaction open"
// discipline dispatch.go's own executeSpawn already established for
// SandboxProvider.CreateSandbox. createPRBestEffort's own artifact-record
// write (recordPRArtifact) is a SEPARATE, small, fresh transact, mirroring
// executeSpawn's own "network call, then a second small transact records
// the outcome" shape precisely.
//
// # Explicitly out of scope (do not expand this file's job)
//
// A "cancelled" real execution_complete outcome is handled (turn.
// TriggerCancel, StateCancelled) since it is one of ExecutionComplete.
// Outcome's own three enum values and leaving it unhandled would silently
// drop a real terminal event -- but no push is attempted for it (only a
// genuine "completed" outcome has anything to push). Building out a
// fuller stop/cancel-driven UX beyond "the turn correctly reaches
// Cancelled" is not this Step's own job.
//
// This file never touches internal/domain/gitstate (Step 29's own job:
// stash/checkout/pop, dirty-tree reconciliation) -- sendPushBestEffort's
// own push is a plain push of whatever branch the session's own repos
// config already named at spawn time, matching design decision 7's own
// "no pre-existing dirty state to reconcile in the happy path" scoping
// exactly. A repo whose own repos[].branch was left null ("use the repo's
// default base branch") is honestly skipped here -- see
// sendPushBestEffort's own doc comment for why this Step cannot resolve
// that case.

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// placeholderPRBaseBranch is the CreatePRSpec.Base value this Step uses
// for every PR it opens -- mirroring dispatch.go's own placeholderBaseImage
// precedent exactly: neither the sessions.repos JSONB column (design
// decision 1) nor SessionConfigReposElem carries a "base"/default-branch
// field distinct from the branch actually checked out, and resolving the
// SCM's real default branch would mean either persisting it at
// session-creation time (a schema change this Step's own brief does not
// ask for) or an extra GitHub API round trip this Step's own scope does
// not otherwise need -- neither is built here. "main" is used instead as
// an honest, clearly-named placeholder; a real per-repo default-branch
// resolution is a natural follow-up for whichever later Step first needs
// PRs against a non-"main" default branch.
const placeholderPRBaseBranch = "main"

// executionOutcomeTrigger maps a real, wire-level sandboxws.
// ExecutionComplete.Outcome to the domain/turn.Trigger that reports it --
// the ONLY three values ExecutionCompleteOutcome's own generated
// UnmarshalJSON accepts. ok is false for anything else (a value that
// somehow bypassed that validation) so callers never attempt a
// turn.Transition call with a fabricated trigger.
func executionOutcomeTrigger(outcome sandboxws.ExecutionCompleteOutcome) (turn.Trigger, bool) {
	switch outcome {
	case sandboxws.ExecutionCompleteOutcomeCompleted:
		return turn.TriggerComplete, true
	case sandboxws.ExecutionCompleteOutcomeFailed:
		return turn.TriggerFail, true
	case sandboxws.ExecutionCompleteOutcomeCancelled:
		return turn.TriggerCancel, true
	default:
		return 0, false
	}
}

// pushSignal is what completeProcessingTurn hands back to
// handleSandboxEvent when (and only when) a turn just completed
// successfully -- the actual SandboxCommander.SendCommand call happens
// AFTER the caller's own transact has committed (sendPushBestEffort),
// never inside it, since composing/sending that command needs nothing
// further from the database.
type pushSignal struct {
	gen   int
	repos []sessionconfig.SessionConfigReposElem
}

// completeProcessingTurn implements this file's own first half: given a
// real, already-schema-validated execution_complete event's raw wire
// bytes (already persisted verbatim by handleSandboxEvent's own
// appendRawEvent call, immediately before this is ever invoked), find
// whichever turn is currently Processing and drive it to its real
// terminal state, exactly mirroring handleTurnDeadlineTimer's own
// (timeout-triggered) shape -- re-deriving session status and deleting
// turn_deadline identically -- but WITHOUT ever calling
// turn.RequiresSyntheticExecutionComplete/appending a synthetic
// execution_complete: a REAL terminal event already arrived on the wire
// (this SandboxEvent itself), so no synthesis is ever needed here,
// regardless of which Trigger constant was used to compute the
// transition (RequiresSyntheticExecutionComplete's own doc comment
// reserves synthesis for CONTROL-PLANE-internal triggers with no real
// wire event of their own -- timeout/abandon/cancel-by-the-control-plane
// -- which this real-event-driven path never is).
//
// Returns (nil, nil) -- not an error -- for every case where there is
// genuinely nothing to complete: no turn currently Processing (a
// redelivery of an already-handled execution_complete, or one that
// arrived with nothing ever dispatched), or an unrecognized Outcome.
// Returns a non-nil *pushSignal only when the completed trigger was
// genuinely TriggerComplete (a failed/cancelled turn has nothing to
// push) AND the session names at least one repo.
func (a *Actor) completeProcessingTurn(ctx context.Context, tx pgx.Tx, sandboxRow sqlcgen.Sandbox, raw json.RawMessage) (*pushSignal, error) {
	var evt sandboxws.ExecutionComplete
	if err := json.Unmarshal(raw, &evt); err != nil {
		// Defensive, not fatal -- mirrors peekAckID's own doc comment
		// exactly ("a decode failure here is defensive, not expected"):
		// wshub's own read loop (internal/adapters/inbound/wshub/
		// dispatch.go) only peeks a small, permissive envelope struct
		// before ever constructing this SandboxEvent, it never validates
		// against sandboxws.ExecutionComplete's own stricter generated
		// UnmarshalJSON (required keys, enum/pattern checks) -- so a
		// genuinely malformed-per-schema execution_complete CAN reach this
		// point in production. Returning an error here would roll back the
		// caller's WHOLE transact, including the raw event this same
		// transact already persisted moments ago via appendRawEvent --
		// this file's own top comment (sandboxevent.go) promises "persist
		// ALWAYS, for every recognized event type" unconditionally, so a
		// decode failure here must never retroactively break that promise.
		a.logger.Warn("sessionactor: execution_complete failed schema decode; persisted verbatim, but no turn completion attempted",
			"error", err)
		return nil, nil
	}

	trig, ok := executionOutcomeTrigger(evt.Outcome)
	if !ok {
		a.logger.Warn("sessionactor: execution_complete carries an unrecognized outcome; ignoring",
			"outcome", string(evt.Outcome))
		return nil, nil
	}

	turns, err := a.stores.turn.WithTx(tx).ListForSession(ctx, a.sessionID)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: list turns: %w", err)
	}
	processing, ok := findProcessingTurn(turns)
	if !ok {
		a.logger.Warn("sessionactor: execution_complete arrived with no turn in processing; ignoring")
		return nil, nil
	}

	to, err := turn.Transition(turn.StateProcessing, trig)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: turn transition via real execution_complete (trigger %s): %w", trig, err)
	}

	now := time.Now()
	if _, err := a.stores.turn.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:          processing.ID,
		Status:      sqlcgen.TurnStatus(to),
		CompletedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("sessionactor: update turn status: %w", err)
	}

	failureReason, _ := turn.DeriveFailureReason(turn.StateProcessing, trig)
	if err := a.persistDerivedSessionStatus(ctx, tx, summariesWithOverride(turns, processing.ID, to, failureReason)); err != nil {
		return nil, err
	}
	if err := a.deleteTimer(ctx, tx, TimerTurnDeadline); err != nil {
		return nil, err
	}

	sessionRow, err := a.stores.session.WithTx(tx).Get(ctx, a.sessionID)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: get session: %w", err)
	}

	// Step 37 ("plan mode, web", §8.1/§12.2 item 3): a plan_mode=true turn
	// that just genuinely completed records exactly one new plans row, in
	// this SAME transaction -- see planrecord.go's own doc comment.
	// recordPlanIfNeeded itself is a no-op (nil, nil) for every other case
	// (plan_mode false, or trig != TriggerComplete). Deliberately called
	// BEFORE enqueueOutboxNotification below (Step 38, "plan mode,
	// cross-channel", reordering this Step's own predecessor): that call
	// needs to know whether a plan was just recorded (and its own id/
	// version) to route this turn's completion to the plan-approval-
	// request notification instead of the generic one.
	plan, err := a.recordPlanIfNeeded(ctx, tx, processing, trig)
	if err != nil {
		return nil, err
	}

	// Step 35 ("outbox delivery", §5.1): enqueue exactly one outbox
	// notification for THIS turn's completion, in the SAME transaction as
	// the state change above -- runs for every outcome (complete/fail/
	// cancel alike), unlike the push/PR path below which is success-only.
	// A no-op (never an error propagated to the caller) for a 'web'-origin
	// session, or a non-'web'-origin session whose own reverse-lookup row
	// is missing -- see enqueueOutboxNotification's own doc comment
	// (outboxenqueue.go).
	if err := a.enqueueOutboxNotification(ctx, tx, sessionRow, trig, failureReason, processing, plan); err != nil {
		return nil, err
	}

	if trig != turn.TriggerComplete {
		// A failed/cancelled turn has nothing to push -- §9.3's resilience
		// scenarios for THOSE outcomes are later Steps' own job (see this
		// file's own top comment), not this one's.
		return nil, nil
	}

	repos, err := reposFromJSON(sessionRow.Repos)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, nil
	}
	return &pushSignal{gen: int(sandboxRow.Gen), repos: repos}, nil
}

// sendPushBestEffort implements this file's own second half's first step:
// once a turn has genuinely completed, best-effort send a real
// sandboxws.Push command for every repo the session names whose own
// repos[].branch is a known, explicit branch name. Never rolls anything
// back on failure (this runs AFTER handleSandboxEvent's own transact has
// already committed the turn's completion -- that fact is real regardless
// of whether a push command can be delivered) -- a log line is this Step's
// own honest, complete recourse, matching design decision 3b's own
// "SendCommand failing is logged, not retried by any new machinery"
// precedent for the symmetric dispatch-a-prompt case.
//
// A repo whose repos[].branch was left null ("use the repo's default base
// branch", per restdtos.CreateSessionRequestReposElem's own doc comment)
// is skipped, not guessed at: resolving which branch gitclone actually
// ended up checking out for such a repo would require either gitclone
// itself reporting back the real checked-out branch name (it does not) or
// a real `git symbolic-ref` round trip through the sandbox this Step's own
// wire contract has no command for -- an honest, documented gap, not a
// silent shortcut.
func (a *Actor) sendPushBestEffort(sessionID string, sig *pushSignal) {
	if sig == nil {
		return
	}
	if a.commander == nil {
		a.logger.Warn("sessionactor: turn completed but no SandboxCommander is configured; skipping push")
		return
	}

	repos := make([]sandboxws.PushReposElem, 0, len(sig.repos))
	for _, r := range sig.repos {
		if r.Branch == nil || *r.Branch == "" {
			a.logger.Warn("sessionactor: session repo has no explicit branch; skipping auto-push for it", "repo", r.Name)
			continue
		}
		repos = append(repos, sandboxws.PushReposElem{Name: r.Name, Branch: *r.Branch})
	}
	if len(repos) == 0 {
		return
	}

	push := sandboxws.Push{
		Type:      "push",
		MessageId: uuid.NewString(),
		SessionId: sessionID,
		Gen:       sig.gen,
		Repos:     repos,
	}
	payload, err := json.Marshal(push)
	if err != nil {
		a.logger.Error("sessionactor: marshal push command failed", "error", err)
		return
	}
	if err := a.commander.SendCommand(sessionID, payload); err != nil {
		a.logger.Warn("sessionactor: send push command failed", "error", err)
	}
}

// createPRBestEffort implements this file's own second half's second
// step: once a push_complete event has already been persisted (generically,
// by handleSandboxEvent's own appendRawEvent, before this is ever
// invoked), open a pull request for each pushed repo using the session's
// own creator's decrypted GitHub OAuth token, and record each success as
// an artifact row (type "pr") -- the ONLY durable place a created PR's
// URL/number is ever written. This is deliberately what makes it visible
// to a client: GET /api/sessions/{id}/artifacts (Step 19) and the client
// WS hub's own SubscribedPayload.artifacts (§6.2) already exist and
// already read this exact table -- no new wire contract is invented here.
//
// Never rolls anything back and never retries: per ports.SourceControl's
// own doc comment, "no caller of this port retries or trips a
// circuit-breaker on a PR-creation failure yet" -- a failure here (no
// usable OAuth token, a GitHub API error, ...) is logged, and the happy
// path simply has no PR artifact to show for that repo, exactly like a
// push failure (push_error) already has no further automated recourse in
// this Step's own scope.
//
// The decrypted OAuth token is NEVER logged, here or anywhere it might
// propagate to.
func (a *Actor) createPRBestEffort(ctx context.Context, raw json.RawMessage) {
	if a.sourceControl == nil {
		a.logger.Warn("sessionactor: push_complete arrived but no SourceControl is configured; skipping PR creation")
		return
	}

	var evt sandboxws.PushComplete
	if err := json.Unmarshal(raw, &evt); err != nil {
		a.logger.Error("sessionactor: decode push_complete for PR creation failed", "error", err)
		return
	}
	if len(evt.Repos) == 0 {
		return
	}

	// A plain, pool-scoped read (not WithTx) -- gathering information for
	// an outbound API call is not a state mutation, so it needs none of
	// transact's own epoch-fencing; the same already-established "read
	// straight off the store" precedent internal/adapters/inbound/httpapi
	// handlers already use throughout.
	sessionRow, err := a.stores.session.Get(ctx, a.sessionID)
	if err != nil {
		a.logger.Error("sessionactor: get session for PR creation failed", "error", err)
		return
	}

	if !a.creatorMayGetPRAttribution(ctx, sessionRow.CreatedBy) {
		return // already logged by creatorMayGetPRAttribution
	}

	token, ok := a.decryptCreatorGitHubToken(ctx, sessionRow.CreatedBy)
	if !ok {
		return // already logged by decryptCreatorGitHubToken
	}

	repos, err := reposFromJSON(sessionRow.Repos)
	if err != nil {
		a.logger.Error("sessionactor: parse session repos for PR creation failed", "error", err)
		return
	}
	reposByName := make(map[string]sessionconfig.SessionConfigReposElem, len(repos))
	for _, r := range repos {
		reposByName[r.Name] = r
	}

	title := prTitle(sessionRow)

	for _, pushed := range evt.Repos {
		repoCfg, ok := reposByName[pushed.Name]
		if !ok {
			a.logger.Warn("sessionactor: push_complete named a repo not in this session's own repos", "repo", pushed.Name)
			continue
		}

		owner, repoName, err := parseOwnerRepo(repoCfg.Url)
		if err != nil {
			a.logger.Error("sessionactor: parse owner/repo from clone url for PR creation failed",
				"repo", pushed.Name, "error", err)
			continue
		}

		prCtx, cancel := context.WithTimeout(ctx, a.timeouts.PRCreateTimeout)
		ref, createErr := a.sourceControl.CreatePR(prCtx, ports.CreatePRSpec{
			Owner: owner,
			Repo:  repoName,
			Head:  pushed.Branch,
			Base:  placeholderPRBaseBranch,
			Title: title,
			Body:  prBody(pushed),
			Token: token,
		})
		cancel()
		if createErr != nil {
			a.logger.Error("sessionactor: create PR failed", "repo", pushed.Name, "error", createErr)
			continue
		}

		if err := a.recordPRArtifact(ctx, repoName, ref); err != nil {
			a.logger.Error("sessionactor: record PR artifact failed", "repo", pushed.Name, "error", err)
		}
	}
}

// recordPRArtifact persists a successfully created PR as a "pr"-typed
// artifact row, inside its OWN small, fresh transact -- mirroring
// dispatch.go's own executeSpawn shape exactly ("a real network call
// already happened outside any transaction; a separate, small transact
// now records its outcome"). Deliberately still epoch-fenced by transact
// (the same mechanism every other write in this package gets) even though
// an artifact row is not session/turn/sandbox state -- transact's own
// fencing check does not care what a caller's fn writes, only that the
// actor invoking it is still the legitimate owner of the session.
func (a *Actor) recordPRArtifact(ctx context.Context, repoName string, ref ports.PRRef) error {
	metadata, err := json.Marshal(map[string]any{"repo": repoName, "number": ref.Number})
	if err != nil {
		return fmt.Errorf("sessionactor: marshal pr artifact metadata: %w", err)
	}

	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := a.stores.artifact.WithTx(tx).Create(ctx, sqlcgen.CreateArtifactParams{
			SessionID: a.sessionID,
			Type:      sqlcgen.ArtifactTypePr,
			Url:       ref.URL,
			Metadata:  metadata,
		}); err != nil {
			return fmt.Errorf("sessionactor: insert pr artifact: %w", err)
		}
		return nil
	})
}

// prTitle uses the session's own title if it has one (set at creation,
// restdtos.CreateSessionRequest.title), falling back to a generic,
// clearly-labeled title otherwise -- sessions created with no title are
// common (title is nullable) and must still produce a valid, non-empty PR
// title.
func prTitle(sessionRow sqlcgen.Session) string {
	if sessionRow.Title != nil && *sessionRow.Title != "" {
		return *sessionRow.Title
	}
	return "Narvi session " + sessionRow.ID.String()
}

// prBody builds a minimal, honest PR description -- this Step invents no
// richer changelog/summary mechanism than "which branch, which commit".
func prBody(pushed sandboxws.PushCompleteReposElem) string {
	return fmt.Sprintf("Automated changes from a Narvi session (branch %q, commit %s).", pushed.Branch, pushed.Sha)
}

// parseOwnerRepo extracts (owner, repo) from a git clone URL of the
// generic https://<host>/<owner>/<repo>[.git] shape. Deliberately generic,
// not GitHub-specific: ports.CreatePRSpec.Owner/Repo are generic
// source-control concepts (that port's own doc comment), and this exact
// path shape is common to GitHub/GitLab/Bitbucket alike -- even though
// internal/adapters/outbound/githubapi is the only real SourceControl
// implementation this Step builds.
func parseOwnerRepo(rawURL string) (owner, repo string, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parse repo clone url %q: %w", rawURL, err)
	}

	trimmed := strings.Trim(parsed.Path, "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo clone url %q does not have an /owner/repo path", rawURL)
	}
	return parts[0], parts[1], nil
}
