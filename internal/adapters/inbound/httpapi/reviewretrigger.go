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
// the authenticated caller fails the authz.ActionRetriggerReview check
// (§13.3 row 5: "edit review verdicts; re-trigger reviews; auto-approval
// eligibility config" -- admin/maintainer only, no member own/joined
// carve-out, unlike CreateTurn's own ActionPromptSession gate); otherwise
// 201 with the newly-queued turn, exactly like CreateTurn's own response
// shape.
//
// Audit fix: this endpoint previously authorized against
// authz.ActionPromptSession (the SAME own/joined-aware check CreateTurn's
// REST endpoint applies), which let a plain member re-trigger a review on
// any session they created or joined -- a privilege escalation against
// §13.3's own RBAC matrix, which reserves "re-trigger reviews" for
// admin/maintainer with no member carve-out at all (unlike row 2's
// ordinary prompt/create carve-out). ActionRetriggerReview has no
// allowIfOwned entry (domain/authz/authorize.go), so ownership/
// participation is never consulted here any more -- ownedOrJoined is no
// longer computed at all (it would be silently ignored by Authorize for
// this Action regardless, per Resource's own doc comment on fields an
// Action doesn't consult), avoiding a wasted Postgres participants read on
// every call.
func RetriggerReview(pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, plans *postgres.PlanStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, prSessions *postgres.GitHubPRSessionStore, diffFetcher reviewcontext.Fetcher, reviewFindings reviewcontext.FindingsFetcher, falsePositivePatterns reviewcontext.FalsePositivePatternsFetcher, botToken string, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)
		logger := platform.Logger(ctx)

		// Mirrors CreateTurn's own "404 before 403" sequencing (turn.go):
		// fetch the session first so a caller never learns "you can't
		// retrigger this" about a session that doesn't exist at all, THEN
		// render the authz verdict -- but the verdict itself is
		// authz.ActionRetriggerReview, admin/maintainer only (§13.3 row 5),
		// never CreateTurn's own own/joined-aware ActionPromptSession.
		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		if _, err := sessions.Get(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session for authorization failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if !authorize(w, r, authz.ActionRetriggerReview, authz.Resource{}) {
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
		// Step 63 (§22.3): prepend this repo's own currently-active
		// learned false-positive patterns BEFORE the already-answered
		// facts below -- "injected into every review pass, first pass and
		// re-review alike": a manual re-trigger is exactly a re-review
		// pass. Independent of diffFetcher (a review turn can still carry
		// the advisory block even with no diff fetcher wired).
		if falsePositivePatterns != nil {
			if advisory := reviewcontext.FetchFalsePositivePatterns(ctx, logger, falsePositivePatterns, prSession.RepoFullName); advisory != "" {
				prompt = advisory + prompt
			}
		}
		// Step 48 (§22.1): prepend this PR's own already-answered facts
		// (open+rebutted review_findings) BEFORE calling RenderTurnPrompt
		// -- prepended to, never replacing, the prose fallback above.
		// Independent of diffFetcher (a review turn can still get
		// reconciliation facts even with no diff fetcher wired).
		if reviewFindings != nil {
			if alreadyAnswered := reviewcontext.FetchAlreadyAnswered(ctx, logger, reviewFindings, prSession.RepoFullName, prSession.PrNumber); alreadyAnswered != "" {
				prompt = alreadyAnswered + prompt
			}
		}
		// reviewHeadSHA (§62 review finding C2, CRITICAL, fixed) is
		// captured here and threaded into CreateTurnCore below via
		// CreateTurnOptions.ReviewHeadSHA -- persisted onto THIS turn's
		// own row (turns.review_head_sha, set once at creation), never
		// written to a shared, mutable per-(repo,PR) column a LATER,
		// unrelated turn's own context-fetch could overwrite (the
		// previous github_pr_sessions.pending_head_sha design this fix
		// replaces -- see migrations/000072_turns_review_head_sha.up.sql's
		// own doc comment for the full "why").
		var reviewHeadSHA *string
		if diffFetcher != nil {
			if owner, repo, ok := reposource.SplitFullName(prSession.RepoFullName); ok {
				prCtx := reviewcontext.Fetch(ctx, logger, diffFetcher, timeouts, owner, repo, prSession.PrNumber, botToken, nil)
				prompt = review.RenderTurnPrompt(prompt, prCtx)
				if prCtx.HeadSHA != "" {
					reviewHeadSHA = &prCtx.HeadSHA
				}
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
		//
		// epistemicCheckDefault: hardcoded false, deliberately never the
		// operator's own platform.Config.EpistemicCheckDefault -- Step 61's
		// own devil's-advocate preamble (§20) is a BUILDER check ("domain/
		// turn: builder epistemic pre-action check", the Step's own title);
		// a review turn is never a build turn (this function creates
		// review-session turns, with review.RenderTurnPrompt's own
		// verdict-tool-instructions already appended above, an entirely
		// separate concern). Passing the real default here would leak the
		// preamble onto review turns with no code path ever exercising
		// ShouldInjectEpistemicPreamble's own planMode=false branch to stop
		// it (a review-retrigger turn is always planMode=false, never
		// true), so this call site is EXCLUDED at the source rather than
		// relying on some other gate downstream.
		created, _, cerr := CreateTurnCore(ctx, pool, sessions, turns, plans, auditLog, registry, sessionID, prompt, nil, false, false, actorUserID, AlwaysQueue, CreateTurnOptions{ReviewHeadSHA: reviewHeadSHA})
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
