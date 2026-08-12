// This file (reviewretrigger.go) implements Step 65's ("review: automatic
// re-review on new commits", §24) own SECOND, automatic trigger for a
// review session's own turns -- alongside, never replacing, Step 46's
// existing manual label/button re-trigger (internal/adapters/inbound/
// httpapi/reviewretrigger.go), which this file's own logic never touches.
//
// # Where this fits in the pipeline
//
// internal/adapters/inbound/github/pullrequestsynchronize.go (§24.1) is
// this feature's own ingress: it re-arms the TimerReviewRetriggerDebounce
// named timer (command.go) DIRECTLY, bypassing this Actor's mailbox
// entirely (command.go's own Command sum type has no member for an
// inbound "new commits pushed" signal), in the SAME transaction as its
// own upsert of github_pr_sessions.pending_retrigger_head_sha (migrations/
// 000075). Only the timer's later FIRING reaches this Actor, exactly like
// every other named timer (§2) -- handleReviewRetriggerDebounceTimer below
// is that fire handler, dispatched from timerfired.go's own
// handleTimerFired switch.
//
// # Why this needs a network call outside any transaction
//
// Deciding whether to enqueue a turn (§24.3 steps 1-3) needs only
// Postgres reads; ACTUALLY enqueueing one (step 4) needs a fresh,
// live-fetched diff/head-sha (internal/app/reviewcontext.Fetch, the SAME
// "diff provably anchored to a live GetPullRequest call" guarantee every
// other review-trigger path already gets, §62 review finding C2's own
// fix) -- a real outbound GitHub API call. This mirrors dispatch.go's own
// established "plan inside a transact, real network call OUTSIDE any
// transaction, result written back in a fresh transact" shape
// (planDispatch/executeSpawn/executeDispatch) rather than holding a
// Postgres transaction open across a network round trip: this handler
// reads its own decision inside one a.transact call
// (readReviewRetriggerState), fetches the diff (if the decision calls for
// it) with no transaction open at all, then writes the outcome back
// inside a SECOND, fresh a.transact call.
//
// # Why the writes are each their own guarded UPDATE
//
// github_pr_sessions.pending_retrigger_head_sha has exactly two writers:
// this Actor (clearing it) and the synchronize webhook handler (setting
// it, from OUTSIDE the actor, on every new push). A new push can land in
// the gap between this handler's own read (readReviewRetriggerState) and
// its own write (the second a.transact call below) -- clearing the
// column unconditionally in that race would silently drop the NEWER
// push's own still-pending re-review need. Every clear here is therefore
// a compare-and-swap (postgres.GitHubPRSessionStore.
// ClearPendingRetriggerHeadSHA, CLAUDE.md/§11's own "guarded UPDATE ...
// WHERE for cross-writer transitions" idiom): pgx.ErrNoRows means a
// newer synchronize event already won the race, and this handler simply
// leaves that newer event's own value (and its own freshly re-armed
// timer) alone, deleting only the ONE timer row it itself claimed --
// timerfired.go's own re-arm-or-delete contract is satisfied either way,
// since the newer event's own re-arm already covers what this handler
// chose not to touch.

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/reviewcontext"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// reviewAutoRetriggerBudget is §24.6's own per-PR budget on the AUTOMATIC
// re-review path only -- a human's manual label/button re-trigger
// (internal/adapters/inbound/httpapi/reviewretrigger.go) is never subject
// to it and always works, regardless of this count (§24.6: "two
// independent tracks by design"). A plain package constant, not a
// platform.Timeouts field or a repo_settings column: this is a COUNT, not
// a time.Duration (mailboxBufferSize's own identical "does not belong in
// platform.Timeouts" precedent, actor.go), and §24.6 itself only asks for
// a documented, reasoned default, not a per-repo override surface. Not
// given an explicit figure in the plan; chosen as 10 -- the plan's own
// proposed figure (§24.6: "propose 10 per PR").
const reviewAutoRetriggerBudget = 10

// autoRetriggerPromptText is this feature's own fixed, deterministically-
// synthesized turn prompt -- a webhook-driven re-review carries no human
// text at all, so any wording here is rendered server-side, never
// model-generated, mirroring httpapi's own manualRetriggerPromptText
// constant exactly in kind (a plain, fixed string) -- worded distinctly
// only so a maintainer reading turns.prompt later can tell which of this
// session's re-trigger paths actually produced a given turn.
const autoRetriggerPromptText = "Automatic re-review triggered: new commits were pushed to this pull request."

// autoRetriggerBudgetExhaustedBodyFormat renders §24.6's own one-time
// budget-exhausted notice -- a fixed, deterministic template (never
// model-generated), embedding reviewpost.RerunGuidance's own identical
// server-side re-run phrasing every OTHER posted verdict already carries
// (§5.2), so this notice is recognizable by the SAME deterministic
// mention-detection fallback path.
const autoRetriggerBudgetExhaustedBodyFormat = "Automatic re-review has reached its budget for this pull request (%d automatic re-reviews). Further pushes will not trigger another automatic re-review.\n\n%s"

// reviewRetriggerDecision is readReviewRetriggerState's own pure output --
// carries exactly what handleReviewRetriggerDebounceTimer needs to decide
// what to do next, without holding a transaction open across the network
// call §24.3 step 4 may require (see this file's own top comment).
type reviewRetriggerDecision struct {
	// action is one of the reviewRetriggerAction* constants below.
	action reviewRetriggerAction

	repoFullName            string
	prNumber                int32
	pendingHeadSHA          string
	autoRetriggerCount      int32
	budgetNoticeAlreadySent bool
	latestVerdictRiskLevel  string
}

type reviewRetriggerAction int

const (
	// reviewRetriggerActionDeleteOnly covers every case with nothing left
	// to clear: no github_pr_sessions row at all, no pending head sha, or
	// the per-repo opt-in is off/unreadable (§24.3 step 1's own fail-
	// closed branch, which the plan does not ask to also clear
	// pending_retrigger_head_sha -- see readReviewRetriggerState's own
	// doc comment).
	reviewRetriggerActionDeleteOnly reviewRetriggerAction = iota
	// reviewRetriggerActionHeadsMatch is §24.3 step 3: the pending head
	// sha already equals the latest posted verdict's own head sha --
	// clear pending_retrigger_head_sha, delete the timer, done.
	reviewRetriggerActionHeadsMatch
	// reviewRetriggerActionFetchFailed is handleReviewRetriggerDebounceTimer's
	// own downgrade of reviewRetriggerActionEnqueue when the live
	// reviewcontext.Fetch call could not resolve a head sha (§24's own
	// fail-closed "never guess-and-dispatch" direction) -- clear
	// pending_retrigger_head_sha, delete the timer, spend no budget.
	// Deliberately a SEPARATE action from reviewRetriggerActionDeleteOnly
	// below (which does NOT clear pending_retrigger_head_sha): a
	// transient fetch failure must still let a LATER firing retry against
	// a clean slate, unlike the opt-in-off case, which intentionally
	// leaves the stale pending value for whenever the repo opts back in.
	reviewRetriggerActionFetchFailed
	// reviewRetriggerActionBudgetExhausted is §24.6's own ceiling: clear
	// pending_retrigger_head_sha, delete the timer, and (the FIRST time
	// only) post the one-time budget-exhausted notice.
	reviewRetriggerActionBudgetExhausted
	// reviewRetriggerActionEnqueue is §24.3 step 4's "otherwise" branch:
	// fetch a fresh diff/head sha and enqueue a real review turn.
	reviewRetriggerActionEnqueue
)

// handleReviewRetriggerDebounceTimer implements the
// TimerReviewRetriggerDebounce named timer (§24.2/§24.3). See this file's
// own top comment for the two-phase (read-decide, then act) shape and why
// every write here is a guarded UPDATE.
func (a *Actor) handleReviewRetriggerDebounceTimer(ctx context.Context) error {
	decision, err := a.readReviewRetriggerState(ctx)
	if err != nil {
		return err
	}
	if decision == nil {
		// readReviewRetriggerState already fully handled (and logged) a
		// terminal case -- e.g. a stale/misrouted timer with no
		// github_pr_sessions row at all -- and left nothing further to do.
		return nil
	}

	var reviewCtx review.PreFetchedContext
	if decision.action == reviewRetriggerActionEnqueue {
		reviewCtx = a.fetchAutoRetriggerReviewContext(ctx, decision.repoFullName, decision.prNumber)
		if reviewCtx.HeadSHA == "" {
			// §24's own fail-closed direction: an error path in this
			// feature must never guess-and-dispatch. Without a live,
			// provably-fresh head sha to anchor turns.review_head_sha to,
			// enqueueing a turn here would produce a review that can
			// never honestly be recorded in review_verdicts (Step 62's
			// NOT NULL head_sha column) -- downgrade to a plain no-op
			// this cycle: clear pending_retrigger_head_sha (a fresh
			// synchronize event, or the next debounce firing once the
			// fetch succeeds, will try again), delete the timer, spend
			// no budget.
			a.logger.Warn("sessionactor: review_retrigger_debounce: could not resolve a live head sha for this PR, declining to enqueue an automatic re-review turn this cycle",
				"repo_full_name", decision.repoFullName, "pr_number", decision.prNumber)
			decision.action = reviewRetriggerActionFetchFailed
		}
	}

	enqueued, err := a.finishReviewRetrigger(ctx, decision, reviewCtx)
	if err != nil {
		return err
	}

	if enqueued {
		if dispatchErr := a.handleEnsureDispatched(ctx); dispatchErr != nil {
			a.logger.Warn("sessionactor: ensure-dispatched after automatic re-review enqueue failed", "error", dispatchErr)
		}
	}
	return nil
}

// readReviewRetriggerState is handleReviewRetriggerDebounceTimer's own
// read-and-decide phase (§24.3 steps 1-3) -- a single a.transact call, no
// writes, no network calls. Returns (nil, nil) for every terminal case it
// fully handles and commits itself (no github_pr_sessions row at all, or
// no pending head sha to act on) -- deleting the timer inline rather than
// returning a decision the caller would have to special-case identically.
func (a *Actor) readReviewRetriggerState(ctx context.Context) (*reviewRetriggerDecision, error) {
	var decision *reviewRetriggerDecision

	err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		prSession, err := a.stores.githubPRSession.WithTx(tx).GetBySessionID(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// A stale or misrouted timer -- there is no PR identity
				// to act on at all. timerfired.go's own re-arm-or-delete
				// contract still applies: delete it.
				a.logger.Warn("sessionactor: review_retrigger_debounce fired for a session with no github_pr_sessions row, deleting timer")
				return a.deleteTimer(ctx, tx, TimerReviewRetriggerDebounce)
			}
			return fmt.Errorf("sessionactor: get github pr session: %w", err)
		}

		var pendingHeadSHA string
		if prSession.PendingRetriggerHeadSha != nil {
			pendingHeadSHA = *prSession.PendingRetriggerHeadSha
		}
		if pendingHeadSHA == "" {
			// Nothing pending to debounce (already cleared by an earlier
			// firing, or a leftover timer from before this column
			// existed) -- nothing to clear, just delete.
			return a.deleteTimer(ctx, tx, TimerReviewRetriggerDebounce)
		}

		// §24.3 step 1: re-read the per-repo opt-in, fail closed on any
		// read error OR a missing row -- mirrors appreviewverdict.
		// AutoMergeEnabled's own identical fail-closed shape exactly
		// (internal/app/reviewverdict/config.go).
		enabled := false
		if settings, settingsErr := a.stores.repoSettings.WithTx(tx).Get(ctx, prSession.RepoFullName); settingsErr != nil {
			if !errors.Is(settingsErr, pgx.ErrNoRows) {
				a.logger.Warn("sessionactor: review_retrigger_debounce: read repo settings failed, treating auto-retrigger-review as OFF",
					"error", settingsErr, "repo_full_name", prSession.RepoFullName)
			}
		} else {
			enabled = settings.AutoRetriggerReviewEnabled
		}
		if !enabled {
			// §24.3 step 1's own wording is exact: "the timer is simply
			// dropped" -- pending_retrigger_head_sha is deliberately left
			// untouched (a later opt-in flip, or simply the next real
			// push's own fresh synchronize event, re-arms a working timer
			// again; leaving the stale value costs nothing since it is
			// only ever read, never trusted blindly, the next time a
			// timer for this PR fires).
			a.logger.Info("sessionactor: review_retrigger_debounce: auto-retrigger-review is off (or unreadable) for this repo, dropping timer without re-review",
				"repo_full_name", prSession.RepoFullName, "pr_number", prSession.PrNumber)
			return a.deleteTimer(ctx, tx, TimerReviewRetriggerDebounce)
		}

		// §24.3 step 2: the latest posted verdict for this PR -- the
		// SAME per-PR GetLatestReviewVerdict reduction §21.1 defines and
		// every other caller of "the latest verdict for this PR" already
		// reuses (queries/reviewverdicts.sql).
		var verdictHeadSHA, verdictRiskLevel string
		if latest, verdictErr := a.stores.reviewVerdict.WithTx(tx).GetLatest(ctx, prSession.RepoFullName, prSession.PrNumber); verdictErr != nil {
			if !errors.Is(verdictErr, pgx.ErrNoRows) {
				return fmt.Errorf("sessionactor: get latest review verdict: %w", verdictErr)
			}
			// No verdict ever posted for this PR -- verdictHeadSHA stays
			// "", which can never equal a real pendingHeadSHA, so the
			// comparison below correctly falls through to "otherwise".
		} else {
			verdictHeadSHA = latest.HeadSha
			verdictRiskLevel = latest.RiskLevel
		}

		base := reviewRetriggerDecision{
			repoFullName:            prSession.RepoFullName,
			prNumber:                prSession.PrNumber,
			pendingHeadSHA:          pendingHeadSHA,
			autoRetriggerCount:      prSession.AutoRetriggerCount,
			budgetNoticeAlreadySent: prSession.AutoRetriggerBudgetNoticeSentAt.Valid,
			latestVerdictRiskLevel:  verdictRiskLevel,
		}

		switch {
		case pendingHeadSHA == verdictHeadSHA:
			// §24.3 step 3: a race already reviewed this exact sha (a
			// manual re-trigger, or an earlier automatic one) -- nothing
			// to do.
			base.action = reviewRetriggerActionHeadsMatch
		case prSession.AutoRetriggerCount >= reviewAutoRetriggerBudget:
			base.action = reviewRetriggerActionBudgetExhausted
		default:
			base.action = reviewRetriggerActionEnqueue
		}
		decision = &base
		return nil
	})
	if err != nil {
		return nil, err
	}
	return decision, nil
}

// fetchAutoRetriggerReviewContext live-fetches this PR's own current
// diff/stack, pinned to a freshly-resolved head sha (internal/app/
// reviewcontext.Fetch -- the SAME assembly point every other review-
// trigger path already uses), OUTSIDE any Postgres transaction (this
// file's own top comment). A nil a.reviewDiffFetcher (not configured for
// this deployment/test) degrades identically to a live fetch failure --
// review.PreFetchedContext's own honest zero value, HeadSHA == "".
func (a *Actor) fetchAutoRetriggerReviewContext(ctx context.Context, repoFullName string, prNumber int32) review.PreFetchedContext {
	if a.reviewDiffFetcher == nil {
		a.logger.Warn("sessionactor: review_retrigger_debounce: no review diff fetcher configured, cannot resolve a live head sha",
			"repo_full_name", repoFullName, "pr_number", prNumber)
		return review.PreFetchedContext{}
	}
	owner, repo, ok := reposource.SplitFullName(repoFullName)
	if !ok {
		a.logger.Warn("sessionactor: review_retrigger_debounce: repo_full_name not in owner/repo shape",
			"repo_full_name", repoFullName)
		return review.PreFetchedContext{}
	}
	return reviewcontext.Fetch(ctx, a.logger, a.reviewDiffFetcher, a.timeouts, owner, repo, prNumber, a.githubBotToken, nil)
}

// finishReviewRetrigger is handleReviewRetriggerDebounceTimer's own act
// phase -- a single, fresh a.transact call applying decision (enriched
// with reviewCtx when decision.action ==
// reviewRetriggerActionEnqueue) -- see this file's own top comment for
// why every write here is a guarded UPDATE, and why this is a SEPARATE
// transaction from readReviewRetriggerState's. Returns whether a turn was
// actually enqueued (the caller's own signal to call
// handleEnsureDispatched afterward).
func (a *Actor) finishReviewRetrigger(ctx context.Context, decision *reviewRetriggerDecision, reviewCtx review.PreFetchedContext) (bool, error) {
	enqueued := false

	err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		switch decision.action {
		case reviewRetriggerActionDeleteOnly:
			return a.deleteTimer(ctx, tx, TimerReviewRetriggerDebounce)

		case reviewRetriggerActionHeadsMatch, reviewRetriggerActionFetchFailed:
			if err := a.clearPendingRetriggerHeadSHAGuarded(ctx, tx, decision); err != nil {
				return err
			}
			return a.deleteTimer(ctx, tx, TimerReviewRetriggerDebounce)

		case reviewRetriggerActionBudgetExhausted:
			if !decision.budgetNoticeAlreadySent {
				if _, markErr := a.stores.githubPRSession.WithTx(tx).MarkAutoRetriggerBudgetNoticeSent(ctx, decision.repoFullName, decision.prNumber); markErr != nil {
					if !errors.Is(markErr, pgx.ErrNoRows) {
						return fmt.Errorf("sessionactor: mark auto-retrigger budget notice sent: %w", markErr)
					}
					// ErrNoRows: already marked by an earlier firing --
					// this actor processes one command at a time for
					// this session (§2), so in practice this branch is
					// unreachable, but the guard is honored defensively
					// like every other guarded write in this file.
				} else if err := a.enqueueAutoRetriggerBudgetExhaustedNotice(ctx, tx, decision); err != nil {
					return err
				}
			}
			if err := a.clearPendingRetriggerHeadSHAGuarded(ctx, tx, decision); err != nil {
				return err
			}
			return a.deleteTimer(ctx, tx, TimerReviewRetriggerDebounce)

		case reviewRetriggerActionEnqueue:
			if err := a.insertAutoRetriggerTurn(ctx, tx, decision, reviewCtx); err != nil {
				return err
			}
			if _, err := a.stores.githubPRSession.WithTx(tx).IncrementAutoRetriggerCount(ctx, decision.repoFullName, decision.prNumber); err != nil {
				return fmt.Errorf("sessionactor: increment auto retrigger count: %w", err)
			}
			if err := a.clearPendingRetriggerHeadSHAGuarded(ctx, tx, decision); err != nil {
				return err
			}
			enqueued = true
			return a.deleteTimer(ctx, tx, TimerReviewRetriggerDebounce)

		default:
			return fmt.Errorf("sessionactor: unhandled review-retrigger action %d", decision.action)
		}
	})
	if err != nil {
		return false, err
	}
	return enqueued, nil
}

// clearPendingRetriggerHeadSHAGuarded is the one shared "clear, tolerating
// a race" call every reviewRetriggerAction branch above ends with -- see
// this file's own top comment for the compare-and-swap this performs and
// why pgx.ErrNoRows is an expected, harmless outcome, never an error.
func (a *Actor) clearPendingRetriggerHeadSHAGuarded(ctx context.Context, tx pgx.Tx, decision *reviewRetriggerDecision) error {
	if _, err := a.stores.githubPRSession.WithTx(tx).ClearPendingRetriggerHeadSHA(ctx, decision.repoFullName, decision.prNumber, decision.pendingHeadSHA); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("sessionactor: clear pending retrigger head sha: %w", err)
		}
		a.logger.Info("sessionactor: review_retrigger_debounce: pending_retrigger_head_sha already moved on (a newer push raced in), leaving it for that push's own timer",
			"repo_full_name", decision.repoFullName, "pr_number", decision.prNumber)
	}
	return nil
}

// insertAutoRetriggerTurn is §24.3 step 4's own turn creation -- CANNOT
// call httpapi.CreateTurnForBot (Step 46's manual path): internal/app/
// sessionactor cannot import internal/adapters/inbound/httpapi (httpapi
// already imports sessionactor throughout its bot/create/turn/plan files;
// the reverse would be a compile-time import cycle), and createTurnLocked
// (the function CreateTurnForBot wraps) is unexported besides. This
// inserts the turn directly via a.stores.turn.Create -- the SAME
// store-level primitive createTurnLocked itself calls
// (internal/adapters/inbound/httpapi/turn.go) -- mirroring Step 46's
// manual path at the storage layer rather than calling through it.
//
// # Which of createTurnLocked's own extra logic is duplicated here, and why
//
// The audit-log write IS duplicated (below): recordPlanIfNeeded's own
// "plan.superseded" write (planrecord.go) already establishes the
// precedent that a system-triggered state change inside this package logs
// an audit_log row with an explicitly invalid actor_user_id
// (pgtype.UUID{}) -- an UNATTENDED action is, if anything, MORE valuable
// to have an audit trail for than a human-clicked one, not less.
//
// The awaiting-plan gate is DELIBERATELY NOT duplicated: it exists to
// stop an ordinary build/request turn from dispatching while a plan on
// the SAME session sits awaiting_approval. A review session's turns are
// ALWAYS created with planMode=false (this one, the manual retrigger,
// every @mention, every label retrigger) and recordPlanIfNeeded's own
// documented contract is that a plan_mode=false turn completing NEVER
// records a plans row at all (planrecord.go: "trig != turn.TriggerComplete
// || !processing.PlanMode" -- returns (nil, nil), nothing recorded) -- so
// no review session can EVER have an awaiting_approval plan row to gate
// against in the first place. Duplicating a check against a
// structurally-unreachable precondition would be dead code asserting a
// property that is already true by construction elsewhere, not a real
// safety net.
func (a *Actor) insertAutoRetriggerTurn(ctx context.Context, tx pgx.Tx, decision *reviewRetriggerDecision, reviewCtx review.PreFetchedContext) error {
	prompt := review.RenderTurnPrompt(autoRetriggerPromptText, reviewCtx)
	headSHA := reviewCtx.HeadSHA

	created, err := a.stores.turn.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
		SessionID:     a.sessionID,
		Status:        sqlcgen.TurnStatusPending,
		Prompt:        &prompt,
		PlanMode:      false,
		ReviewHeadSha: &headSHA,
	})
	if err != nil {
		return fmt.Errorf("sessionactor: insert automatic re-review turn: %w", err)
	}

	if err := auditlog.Record(ctx, a.stores.auditLog.WithTx(tx), pgtype.UUID{}, "turn.create", "turn", created.ID.String(), map[string]any{
		"session_id": a.sessionID.String(),
		"trigger":    "review_auto_retrigger",
		"head_sha":   headSHA,
		"repo":       decision.repoFullName,
		"pr_number":  decision.prNumber,
	}); err != nil {
		return fmt.Errorf("sessionactor: record turn.create audit log: %w", err)
	}
	return nil
}

// enqueueAutoRetriggerBudgetExhaustedNotice is §24.6's own one-time
// notice -- posted through the SAME verdict-posting mechanism every
// OTHER review-session write to a PR already goes through (Step 47's
// raw-comment blocking: this is the sanctioned way review-session content
// ever reaches a PR at all), never a raw comment. This is NOT a real
// review.Verdict -- no risk assessment happened -- so, deliberately
// unlike httpapi.PostReviewVerdict, this does NOT insert a review_verdicts
// row (that table's own doc comment, migrations/000067: "every column
// below is forwarded verbatim from a value already computed server-side"
// -- there is no honestly-computed RiskLevel/Shippable to forward here).
//
// RiskLevel in the payload is decision.latestVerdictRiskLevel -- this
// PR's own last REAL verdict's risk, not a fabricated new assessment --
// so VerdictNotifier's own label sync (ComputeLabelSync) resolves to a
// no-op (the PR's current label already reflects that same risk from the
// real verdict that posted it), rather than reviewpost.RiskLabel's own
// fail-conservative default (an empty/unrecognized RiskLevel renders as
// review:high-risk) mislabeling a PR this notice never actually assessed.
func (a *Actor) enqueueAutoRetriggerBudgetExhaustedNotice(ctx context.Context, tx pgx.Tx, decision *reviewRetriggerDecision) error {
	owner, repo, ok := reposource.SplitFullName(decision.repoFullName)
	if !ok {
		a.logger.Warn("sessionactor: review_retrigger_debounce: repo_full_name not in owner/repo shape, skipping budget-exhausted notice",
			"repo_full_name", decision.repoFullName)
		return nil
	}

	body := fmt.Sprintf(autoRetriggerBudgetExhaustedBodyFormat, reviewAutoRetriggerBudget, reviewpost.RerunGuidance(a.githubBotHandle))

	payload, err := json.Marshal(githubapi.VerdictPayload{
		Owner:     owner,
		Repo:      repo,
		PRNumber:  int(decision.prNumber),
		Event:     string(reviewpost.FormalReviewEventComment),
		Body:      body,
		RiskLevel: decision.latestVerdictRiskLevel,
	})
	if err != nil {
		return fmt.Errorf("sessionactor: marshal auto-retrigger budget-exhausted notice payload: %w", err)
	}

	if _, err := a.stores.outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: a.sessionID,
		Kind:      string(ports.NotificationKindGitHubVerdict),
		Payload:   payload,
	}); err != nil {
		return fmt.Errorf("sessionactor: insert github_verdict outbox entry for budget-exhausted notice: %w", err)
	}
	return nil
}
