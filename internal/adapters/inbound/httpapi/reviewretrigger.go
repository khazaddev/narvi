// This file (reviewretrigger.go) implements Step 46's ("review sessions",
// §8.2) own manual re-trigger-via-BUTTON surface: POST
// /api/sessions/:id/review/retrigger (§12.2 item 2's own "re-run action"
// on the Code review view). This is the THIRD of Step 46's three manual
// re-trigger surfaces -- see internal/adapters/inbound/github's own doc.go
// for the other two (an @mention comment, and the new label lane) and for
// why all three are equally legitimate, deliberate human commands (§5.1).
//
// Unlike the other two, this handler never creates a brand-new review
// session -- it targets an ALREADY-KNOWN session_id (the URL itself), so
// there is no "first mention on this PR" ambiguity for
// github_pr_sessions' own atomic claim to resolve; this handler never
// touches that claim row at all. What it DOES reuse from Step 32/45/46's
// own existing machinery: CreateTurnCore (turn.go) with AlwaysQueue --
// the SAME policy coalesce.go's own REUSE branch uses via
// CreateTurnForBot (bot.go) -- so a manual re-review click behaves exactly
// like another @mention on the same PR: it always succeeds, queuing
// alongside whatever else is already Pending/Dispatched/Processing on this
// session, rather than the ordinary REST CreateTurn endpoint's own
// RejectIfOpen 409. A human clicking "re-run review" on a PR that already
// has other review turns in flight should queue behind them, not be told
// to try again later -- the two surfaces (@mention, button) are the same
// kind of command wearing a different UI.

package httpapi

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/reviewcontext"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/platform"
)

// manualRetriggerPromptText is this endpoint's own fixed, deterministically-
// synthesized turn prompt -- a REST button click carries no request body
// at all (this endpoint deliberately never accepts one: any re-run
// phrasing is rendered server-side, never client-supplied, mirroring §5.2's
// "any re-run phrasing a posted verdict recommends ... is rendered
// server-side from the verdict's own typed fields, never generated ... by
// a model" -- the same discipline applied here to a human's own explicit
// button click, which needs no model-generated wording at all). Mirrors
// internal/adapters/inbound/github's own labelRetriggerPromptText constant
// exactly in kind (a plain, fixed string, never model-generated) -- worded
// distinctly only so a maintainer reading turns.prompt later can tell
// which of Step 46's three manual surfaces actually triggered a given
// turn.
const manualRetriggerPromptText = "Manual re-review requested via the web review button."

// RetriggerReview backs POST /api/sessions/{sessionID}/review/retrigger.
// 404 if sessionID doesn't exist; 400 if it exists but was never created
// via a GitHub PR mention (github_pr_sessions has no reverse row for it --
// this action is meaningless for a session with no PR to review); 403 if
// the authenticated caller fails the SAME ActionPromptSession check
// CreateTurn's own REST endpoint already applies (turn.go); otherwise 201
// with the newly-queued turn, exactly like CreateTurn's own response shape.
func RetriggerReview(pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, plans *postgres.PlanStore, participants *postgres.ParticipantStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, prSessions *postgres.GitHubPRSessionStore, diffFetcher reviewcontext.Fetcher, botToken string, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)
		logger := platform.Logger(ctx)

		// Mirrors CreateTurn's own identical authorization sequencing
		// (turn.go) verbatim: fetch (404), compute ownedOrJoined, then the
		// SAME ActionPromptSession check -- a manual re-review is, at the
		// authorization layer, just another turn on an existing session.
		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session for authorization failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		ownedOrJoined := sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID
		if !ownedOrJoined {
			exists, err := participants.Exists(ctx, sessionRow.ID, actorUserID)
			if err != nil {
				logger.Error("httpapi: check participant for authorization failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			ownedOrJoined = exists
		}
		if !authorize(w, r, authz.ActionPromptSession, authz.Resource{OwnedOrJoined: ownedOrJoined}) {
			return
		}

		// This action is meaningful ONLY for a session that IS a GitHub PR
		// review session -- the reverse (session_id -> repo/PR) lookup Step
		// 35 ("outbox delivery") already added GitHubPRSessionStore for.
		// pgx.ErrNoRows here means sessionID was never created via a GitHub
		// PR mention/label -- a plain web/Slack/Linear session has no PR to
		// re-review, so this is a genuine 400, not a transient failure.
		prSession, err := prSessions.GetBySessionID(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "this session has no associated GitHub pull request to review")
				return
			}
			logger.Error("httpapi: look up github_pr_sessions by session id failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		prompt := manualRetriggerPromptText
		if diffFetcher != nil {
			if owner, repo, ok := reposource.SplitFullName(prSession.RepoFullName); ok {
				prCtx := reviewcontext.Fetch(ctx, logger, diffFetcher, timeouts, owner, repo, prSession.PrNumber, botToken, nil)
				prompt = review.RenderTurnPrompt(prompt, prCtx)
			} else {
				logger.Warn("httpapi: could not split repo_full_name into owner/repo, skipping pre-fetched review context",
					"repo_full_name", prSession.RepoFullName, "pr_number", prSession.PrNumber)
			}
		}

		// AlwaysQueue, NOT CreateTurn's own RejectIfOpen -- see this file's
		// own top doc comment for why a manual re-review click is treated
		// exactly like another @mention on the same PR (coalesce.go's own
		// REUSE branch), never CreateTurn's own single-relaunch-at-a-time
		// REST policy.
		created, _, cerr := CreateTurnCore(ctx, pool, sessions, turns, plans, auditLog, registry, sessionID, prompt, nil, false, actorUserID, AlwaysQueue)
		if cerr != nil {
			logger.Error("httpapi: retrigger review (create turn) failed", "status", cerr.Status, "message", cerr.Message)
			writeError(w, cerr.Status, cerr.Message)
			return
		}

		writeJSON(w, http.StatusCreated, restdtos.CreateTurnResponse{
			Id:     created.ID.String(),
			Status: restdtos.CreateTurnResponseStatus(created.Status),
		})
	}
}
