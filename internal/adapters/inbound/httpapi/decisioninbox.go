// This file (decisioninbox.go) backs Step 60's own two REST endpoints
// ("decision inbox: read model + API", §16): GET /api/decision-inbox (the
// read model, ListDecisionInbox below) and POST /api/decision-inbox/merge
// (§16.2's own Merge endpoint, MergePullRequest below) -- the ONLY
// state-changing action this Step introduces, and the one place its own
// "read model, not new state" scope (aggregate.go's own doc comment)
// does not apply, since merging is inherently an action against GitHub,
// not a Postgres write.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/authz"
	decisioninboxdomain "github.com/khazaddev/narvi/internal/domain/decisioninbox"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// ListDecisionInbox backs GET /api/decision-inbox (§16.2/§16.3 -- Phase 5
// half: read model + endpoints). No authz.Authorize gate: this is a
// personalized READ of the caller's own pending decisions, the same
// "everyone, including viewer, read-only" posture §13.3 row 1 already
// grants view_sessions/view_analytics -- a viewer sees their own queue,
// just without the ability to act on any row that isn't already denied
// them by MergePullRequest's own RBAC check below. needs_attention rows
// are included only for an admin actor (§16.1's own parenthetical) --
// enforced inside decisioninbox.Build itself, never filtered here.
func ListDecisionInbox(deps decisioninbox.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		authUser, ok := platform.UserFromContext(ctx)
		if !ok {
			logger.Error("httpapi: no authenticated user in context (route not mounted behind auth.Middleware?)")
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		result, err := decisioninbox.Build(ctx, deps, actorUserID, authz.Role(authUser.Role), time.Now())
		if err != nil {
			logger.Error("httpapi: build decision inbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, decisionInboxResultToDTO(result))
	}
}

// decisionInboxResultToDTO converts the app-layer decisioninbox.Result
// into its REST wire shape (contracts/rest/v1/dtos.schema.json). A pure,
// side-effect-free mapping -- every field here already exists on result;
// this function invents nothing.
func decisionInboxResultToDTO(result decisioninbox.Result) restdtos.ListDecisionInboxResponse {
	items := make([]restdtos.DecisionInboxItem, len(result.Items))
	for i, it := range result.Items {
		items[i] = decisionInboxItemToDTO(it)
	}

	resp := restdtos.ListDecisionInboxResponse{
		Items:                     items,
		ScmAsOf:                   result.SCMAsOf,
		DecisionLatencySampleSize: result.DecisionLatencySampleSize,
		DecisionLatencyComputed:   result.DecisionLatencyComputed,
	}
	if result.DecisionLatencyComputed {
		seconds := result.DecisionLatencyMedian.Seconds()
		resp.DecisionLatencyMedianSeconds = &seconds
	}
	return resp
}

func decisionInboxItemToDTO(it decisioninbox.Item) restdtos.DecisionInboxItem {
	dto := restdtos.DecisionInboxItem{
		Kind:           restdtos.DecisionInboxItemKind(it.Kind),
		Title:          it.Title,
		EnteredQueueAt: it.EnteredQueueAt,
		AgeSeconds:     int(it.AgeSeconds),
		Stale:          it.Stale,
	}

	if it.RepoFullName != "" {
		dto.RepoFullName = &it.RepoFullName
	}
	if it.PRNumber != 0 {
		n := it.PRNumber
		dto.PrNumber = &n
	}
	if it.HTMLURL != "" {
		dto.HtmlUrl = &it.HTMLURL
	}
	if it.HeadSHA != "" {
		dto.HeadSha = &it.HeadSHA
	}
	if it.Provenance != nil {
		dto.ProvenanceKind = &restdtos.DecisionInboxItemProvenanceKind{Value: string(it.Provenance.Kind)}
		if it.Provenance.RepoFullName != "" {
			dto.ProvenanceRepoFullName = &it.Provenance.RepoFullName
		}
		if it.Provenance.Pattern != "" {
			dto.ProvenancePattern = &it.Provenance.Pattern
		}
	}
	if it.RiskLabel != "" {
		dto.RiskLabel = &it.RiskLabel
	}
	if it.Kind == decisioninboxdomain.KindReadyToMerge || it.Kind == decisioninboxdomain.KindNeedsReview {
		ciGreen := it.CIGreen
		dto.CiGreen = &ciGreen
		findings := it.Findings
		dto.Findings = &findings
		isHandoff := it.IsHandoff
		dto.IsHandoff = &isHandoff
	}

	if it.PlanID != "" {
		dto.PlanId = &it.PlanID
	}
	if it.SessionID != "" {
		dto.SessionId = &it.SessionID
	}
	if it.FailureReason != "" {
		dto.FailureReason = &it.FailureReason
	}
	if it.AutomationID != "" {
		dto.AutomationId = &it.AutomationID
	}
	if it.ArtifactSummary != "" {
		dto.ArtifactSummary = &it.ArtifactSummary
	}
	if it.OutboxID != "" {
		dto.OutboxId = &it.OutboxID
	}
	if it.OutboxKind != "" {
		dto.OutboxKind = &it.OutboxKind
	}
	if it.LastError != "" {
		dto.LastError = &it.LastError
	}

	return dto
}

// MergePullRequest backs POST /api/decision-inbox/merge (§16.2's own
// Merge endpoint; mockups.html decision 33: "Auto-approved still means
// human-merged... The Merge button re-validates CI, approval state, and
// RBAC server-side at click time; the rendered queue is never trusted as
// authority"). Sequence: decode + validate the request, resolve the
// caller's own decrypted GitHub token, LIVE-revalidate (decisioninbox.
// RevalidateForMerge -- never the cache), THEN authz.Authorize
// (ActionMergePR, OwnedOrJoined=true -- only ever set once revalidation
// itself has confirmed the PR is currently assigned to this actor), THEN
// call SourceControl.MergePR, THEN record the audit log. Any step failing
// stops the sequence -- an already-succeeded GitHub merge is never
// undone by a later step's own failure (only the audit-log write, which
// is best-effort and logged, not retried or rolled back, mirroring this
// codebase's own "the merge already happened, a logging failure must
// never claim otherwise" discipline).
func MergePullRequest(deps decisioninbox.Deps, sourceControl ports.SourceControl, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if sourceControl == nil {
			logger.Error("httpapi: merge pull request: no SourceControl configured")
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		authUser, ok := platform.UserFromContext(ctx)
		if !ok {
			logger.Error("httpapi: no authenticated user in context (route not mounted behind auth.Middleware?)")
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.MergePullRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}
		if req.RepoFullName == "" || req.PrNumber <= 0 {
			writeError(w, http.StatusBadRequest, "repoFullName and a positive prNumber are required")
			return
		}
		owner, repo, ok := reposource.SplitFullName(req.RepoFullName)
		if !ok {
			writeError(w, http.StatusBadRequest, "repoFullName must be shaped owner/repo")
			return
		}

		// pgx.ErrNoRows (no linked GitHub identity) is an expected, common
		// outcome -- never logged as an error, mirroring ApplySuggestion's
		// own identical "no usable git credential" 403 (reviewfindings.go).
		identity, err := deps.Identities.GetByUserAndProvider(ctx, actorUserID, sqlcgen.IdentityProviderGithub)
		if err != nil || identity.AccessTokenEncrypted == nil {
			writeError(w, http.StatusForbidden, "no usable git credential for this action")
			return
		}
		plaintextToken, err := platform.DecryptToken(deps.TokenEncryptionKey, identity.AccessTokenEncrypted)
		if err != nil {
			logger.Error("httpapi: decrypt actor's access token failed", "error", err)
			writeError(w, http.StatusForbidden, "no usable git credential for this action")
			return
		}
		// Never logged, here or anywhere it might propagate to -- mirrors
		// scmcredentials.go/reviewfindings.go's own identical discipline.
		token := string(plaintextToken)

		revalidateCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.GitHubListOpenPRsForUserTimeout)
		eligible, headSHA, reason, err := decisioninbox.RevalidateForMerge(revalidateCtx, deps, sourceControl, identity.ExternalID, req.RepoFullName, req.PrNumber, token)
		cancel()
		if err != nil {
			logger.Error("httpapi: revalidate pull request for merge failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !eligible {
			writeError(w, http.StatusConflict, reason)
			return
		}

		// RBAC, LAST -- only once live revalidation has itself confirmed
		// this PR is currently assigned to the actor does OwnedOrJoined
		// become true (authz.ActionMergePR's own doc comment,
		// internal/domain/authz/action.go): never trusted from the
		// client-rendered queue, exactly like every other fact this
		// handler re-checks.
		actor := authz.Actor{UserID: actorUserID.String(), Role: authz.Role(authUser.Role)}
		if err := authz.Authorize(actor, authz.ActionMergePR, authz.Resource{OwnedOrJoined: true}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				writeError(w, http.StatusForbidden, "not authorized to perform this action")
				return
			}
			logger.Error("httpapi: authz.Authorize failed", "error", err, "action", string(authz.ActionMergePR))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		mergeCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.GitHubMergePRTimeout)
		mergeSHA, err := sourceControl.MergePR(mergeCtx, ports.MergePRSpec{
			Owner: owner, Repo: repo, Number: req.PrNumber, HeadSHA: headSHA, Token: token,
		})
		cancel()
		if err != nil {
			var mergeErr *ports.MergePRError
			if errors.As(err, &mergeErr) {
				switch mergeErr.Status {
				case http.StatusMethodNotAllowed:
					writeError(w, http.StatusConflict, "GitHub reports this pull request is not currently mergeable")
				case http.StatusConflict:
					writeError(w, http.StatusConflict, "this pull request changed since it was last checked -- please retry")
				default:
					logger.Error("httpapi: merge pr failed", "error", err)
					writeError(w, http.StatusBadGateway, "merge failed")
				}
				return
			}
			logger.Error("httpapi: merge pr failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := auditlog.Record(ctx, auditLog, actorUserID, "merge_pr", "pull_request", fmt.Sprintf("%s#%d", req.RepoFullName, req.PrNumber), map[string]any{
			"repo_full_name":   req.RepoFullName,
			"pr_number":        req.PrNumber,
			"merge_commit_sha": mergeSHA,
		}); err != nil {
			// The merge already succeeded on GitHub -- a logging failure
			// here must never claim otherwise to the caller (mirrors
			// §17.5's own "the merge already happened" posture for the
			// system-initiated sentinel-fix merge's audit write).
			logger.Error("httpapi: record audit log for merge pr failed", "error", err)
		}

		writeJSON(w, http.StatusOK, restdtos.MergePullRequestResponse{
			Merged:         true,
			MergeCommitSha: mergeSHA,
			Message:        "Pull request merged",
		})
	}
}
