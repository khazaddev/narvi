// This file (reviewverdict.go) implements Step 47's ("server-side
// verdict", §8.2/§5.2/§21.2) own VERDICT-POSTING TOOL: POST
// /sessions/{sessionID}/review/verdict. This is the ONLY sanctioned way a
// review session's output reaches its pull request as a comment or formal
// review -- internal/app/sessionactor/outboxenqueue.go's own github-origin
// branch (Step 47's own RAW-COMMENT BLOCKING) no longer enqueues anything
// on its own, so a review session that never calls this endpoint simply
// posts nothing.
//
// # Why an HTTP endpoint, not a genuine OpenCode/LLM function-call tool
//
// The review agent is expected to invoke this the SAME way sandbox-agent's
// own git-credential-helper POSTs to CP's /sessions/{id}/scm-credentials
// (internal/adapters/inbound/httpapi/scmcredentials.go's own doc comment):
// an authenticated HTTP call, from inside the sandbox, using the SAME
// sandbox bearer token/gen-fencing scheme that endpoint and snapshot-mint
// (snapshotmint.go) already establish for "agent-initiated, server-
// validated" actions. Nothing in this codebase's AgentRuntime port (§4.2)
// or sandbox WS protocol (§6.1) defines a native LLM function-calling
// mechanism today (§7's own "no port change" precedent for adapter-local
// features), and inventing one from scratch -- registering a JSON-schema
// tool with OpenCode's own real tool/MCP surface, translating a NEW kind
// of tool_call/tool_result pair over the wire -- is real, novel scope this
// Step's own brief does not ask for and the existing wire contracts do not
// anticipate. Reusing the established sandbox-bearer-authenticated-
// endpoint pattern gives "the agent calls it with typed fields, validated
// server-side" (this Step's own central requirement) without touching the
// sandbox WS protocol or the OpenCode adapter at all -- the review turn's
// own prompt (review.RenderTurnPrompt, Step 46) is the natural place to
// instruct the agent HOW to call this endpoint (URL, bearer token, gen
// header, JSON shape), exactly like it already instructs the agent about
// pre-fetched diff/stack context. A genuine native tool-call integration
// remains a plausible LATER refinement once OpenCode's own real custom-
// tool/MCP configuration surface is confirmed against the pinned binary
// (mirroring §7's own contract-test discipline) -- not invented here on
// spec.
//
// # Server-computed Shippable -- the single most important invariant
//
// This endpoint NEVER accepts Shippable from the caller (restdtos.
// PostReviewVerdictRequest has no such field at all) -- it always
// recomputes it via review.ComputeShippable, through reviewpost.
// BuildVerdict, exactly matching review.Verdict's own CONTRACT. Nothing
// here, or anywhere downstream, ever parses a POSTED comment/review back
// into a Verdict -- the tool's payload (this request body) IS the
// structured verdict, and this handler's own job is validating it,
// authoritatively computing Shippable/the formal-review event/the synced
// label, and enqueueing exactly one outbox row for delivery -- the real
// GitHub network calls (formal review + label sync) happen entirely at
// delivery time, inside internal/adapters/outbound/githubapi.
// VerdictNotifier, never synchronously in this request path.

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/provenance"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// PostReviewVerdict backs POST /sessions/{sessionID}/review/verdict (note:
// no /api prefix -- a sandbox-to-CP endpoint, not a browser-facing REST
// route, mirroring scm-credentials/snapshot-mint exactly). Outcome table:
//
//  1. sessionID does not parse as a UUID, or no sandbox row exists for it
//     -> 404 (mirrors scmcredentials.go/snapshotmint.go's own identical
//     "malformed and nonexistent both mean no such session" precedent --
//     this caller is sandbox-agent/agent code, never a browser).
//  2. Authorization: Bearer <token> missing/malformed -> 401.
//  3. sandbox.IsDeadSandboxStatus(sandboxRow.Status) -> 410 (same ordering
//     as scmcredentials.go/snapshotmint.go: checked before the gen/token
//     comparisons, since a terminalized sandbox's last-known-live
//     token_hash/gen are no longer meaningful to compare against).
//  4. The presented X-Sandbox-Gen header is missing/malformed, or parses
//     but does not equal sandboxRow.Gen -> 403 (§9.3 scenario #6 parity).
//  5. The presented bearer token fails verifySandboxBearerToken -> 401.
//  6. This session has no github_pr_sessions row at all (pgx.ErrNoRows) --
//     meaningless call, this session is not a review session -> 400
//     (mirrors reviewretrigger.go's own identical "no PR to act on" 400).
//  7. Malformed request body (fails to decode as restdtos.
//     PostReviewVerdictRequest -- the generated type's own UnmarshalJSON
//     already rejects a missing required field, an out-of-enum value, a
//     negative filesChanged, or an empty summary) -> 400.
//  8. The decoded, syntactically-valid body still fails
//     reviewpost.ValidateVerdictInput (today: only a whitespace-only
//     summary reaches this far -- every other check above already caught
//     it at JSON-decode time; kept as real defense in depth, not dead
//     code, and the one remaining gap the schema alone cannot close) ->
//     400.
//  9. Otherwise -> 201 with restdtos.PostReviewVerdictResponse, having
//     enqueued exactly one ports.NotificationKindGitHubVerdict outbox row
//     (internal/adapters/outbound/githubapi.VerdictNotifier delivers it:
//     the formal review, then the label sync).
//
// Step 48 ("sentinels + suggestions", §17/§22.1) extends this handler,
// never replaces it: after building the verdict, it ALSO builds
// []reviewpost.Finding from req.Findings (optional, additive -- see
// restdtos.PostReviewVerdictRequest's own doc comment), computes each
// finding's identity server-side, and upserts review_findings -- all
// inside the SAME transaction as the outbox write below (pool.Begin ...
// tx.Commit), never a second, independently-committed write. When a
// posted finding names a sentinel kind (coverage/docs_drift) AND the
// repo's own sentinel_autofix_enabled toggle is on AND this session is
// NOT itself a sentinel-auto-fix child session (§17.1's "no recursion"
// rule), a SECOND outbox row (ports.NotificationKindSentinelAutoFix) is
// enqueued in the SAME transaction, claiming (or reusing an
// already-claimed) sentinel_fixes row for this PR.
func PostReviewVerdict(
	pool *pgxpool.Pool,
	sandboxes *postgres.SandboxStore,
	sessions *postgres.SessionStore,
	prSessions *postgres.GitHubPRSessionStore,
	repoSettings *postgres.RepoSettingsStore,
	reviewFindings *postgres.ReviewFindingStore,
	sentinelFixes *postgres.SentinelFixStore,
	outbox *postgres.OutboxStore,
	botHandle string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var sessionID pgtype.UUID
		if err := sessionID.Scan(chi.URLParam(r, "sessionID")); err != nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		ctx = platform.WithSessionID(ctx, sessionID.String())
		logger := platform.Logger(ctx)

		token, ok := bearerTokenFromHeader(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or malformed authorization header")
			return
		}

		sandboxRow, err := sandboxes.Get(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: review-verdict: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: review-verdict: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable verdict-posting credential for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		// This action is meaningful ONLY for a session that IS a GitHub PR
		// review session -- mirrors reviewretrigger.go's own identical
		// reverse lookup/400 precedent.
		prSession, err := prSessions.GetBySessionID(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "this session has no associated GitHub pull request to post a verdict on")
				return
			}
			logger.Error("httpapi: review-verdict: get github pr session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		owner, repo, ok := reposource.SplitFullName(prSession.RepoFullName)
		if !ok {
			logger.Error("httpapi: review-verdict: repo_full_name not in owner/repo shape", "repo_full_name", prSession.RepoFullName)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.PostReviewVerdictRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
			return
		}

		input := reviewpost.VerdictInput{
			RiskLevel:         review.RiskLevel(req.RiskLevel),
			Premise:           review.PremiseState(req.Premise),
			FilesChanged:      req.FilesChanged,
			TestsCoverage:     review.TestsCoverageState(req.TestsCoverage),
			DocsDrift:         review.DocsDriftState(req.DocsDrift),
			ProposedShippable: review.ProposedShippable(req.ProposedShippable),
			Summary:           req.Summary,
		}
		for _, tag := range req.BlastRadius {
			input.BlastRadius = append(input.BlastRadius, review.Tag(tag))
		}
		for _, f := range req.Findings {
			input.Findings = append(input.Findings, findingInputFromWire(f))
		}

		if err := reviewpost.ValidateVerdictInput(input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		verdict := reviewpost.BuildVerdict(input)
		findings := reviewpost.BuildFindings(input)

		// blockOnHighRisk/sentinelAutofixEnabled (§21.2, §17.1): both
		// admin, per-repo, strict-boolean settings -- a missing row OR any
		// read error defaults BOTH to false (fail-closed, mirroring
		// §24.5's own identical "if the setting cannot be read... treated
		// as OFF" precedent for a comparable per-repo policy flag). A
		// genuine read error is logged but never blocks verdict posting --
		// this is a policy nuance, not a precondition for the tool call to
		// succeed at all.
		blockOnHighRisk := false
		sentinelAutofixEnabled := false
		if settings, settingsErr := repoSettings.Get(ctx, prSession.RepoFullName); settingsErr != nil {
			if !errors.Is(settingsErr, pgx.ErrNoRows) {
				logger.Warn("httpapi: review-verdict: read repo settings failed, defaulting blockOnHighRisk/sentinelAutofixEnabled to false", "error", settingsErr)
			}
		} else {
			blockOnHighRisk = settings.BlockOnHighRisk
			sentinelAutofixEnabled = settings.SentinelAutofixEnabled
		}

		event := reviewpost.ComputeFormalReviewEvent(verdict.Shippable, verdict.RiskLevel, blockOnHighRisk)
		syncedLabel := reviewpost.RiskLabel(verdict.RiskLevel)
		body := reviewpost.RenderVerdictComment(verdict, findings, req.Summary, botHandle, syncedLabel)

		payload, err := json.Marshal(githubapi.VerdictPayload{
			Owner:     owner,
			Repo:      repo,
			PRNumber:  int(prSession.PrNumber),
			Event:     string(event),
			Body:      body,
			RiskLevel: string(verdict.RiskLevel),
		})
		if err != nil {
			logger.Error("httpapi: review-verdict: marshal outbox payload failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Correlation ID propagation -- mirrors internal/app/sessionactor/
		// outboxenqueue.go's own identical "read from ctx if present, else
		// NULL" convention exactly.
		var correlationID *string
		if id, ok := platform.CorrelationIDFromContext(ctx); ok && id != "" {
			correlationID = &id
		}

		// Step 48: is the sentinel-auto-fix flow even a CANDIDATE for this
		// verdict at all -- the toggle is on AND at least one posted
		// finding names a sentinel kind? Computed BEFORE opening the
		// transaction below (a pure, in-memory check) so the "fetch the
		// origin session's own repos/provenance_tag" read below is only
		// ever paid for when it could actually matter.
		autoFixCandidate := sentinelAutofixEnabled && hasSentinelFinding(findings)

		var originHeadBranch, repoName, repoCloneURL string
		if autoFixCandidate {
			sessionRow, sessErr := sessions.Get(ctx, sessionID)
			var repos []restdtos.CreateSessionRequestReposElem
			var reposErr error
			if sessErr == nil {
				reposErr = json.Unmarshal(sessionRow.Repos, &repos)
			}

			switch {
			case sessErr != nil:
				logger.Warn("httpapi: review-verdict: read session row for sentinel-auto-fix eligibility failed, skipping trigger", "error", sessErr)
				autoFixCandidate = false
			case provenance.IsSentinelAutoFix(sessionRow.ProvenanceTag):
				// §17.1's own "no recursion" rule: a PR opened by a
				// sentinel-auto-fix child session is never itself
				// eligible to trigger another, regardless of what its own
				// verdict finds.
				autoFixCandidate = false
			case reposErr != nil || len(repos) == 0 || repos[0].Branch == nil || *repos[0].Branch == "":
				logger.Warn("httpapi: review-verdict: could not determine origin head branch from session repos, skipping sentinel-auto-fix trigger",
					"error", reposErr)
				autoFixCandidate = false
			default:
				originHeadBranch = *repos[0].Branch
				repoName = repos[0].Name
				repoCloneURL = repos[0].Url
			}
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: review-verdict: begin tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		findingsTx := reviewFindings.WithTx(tx)
		findingIdentityHashes := make([]string, 0, len(findings))
		for _, f := range findings {
			if _, upsertErr := findingsTx.Upsert(ctx, sqlcgen.UpsertReviewFindingParams{
				RepoFullName: prSession.RepoFullName,
				PrNumber:     prSession.PrNumber,
				IdentityHash: f.IdentityHash,
				SentinelKind: sentinelKindColumn(f.SentinelKind),
				Severity:     string(f.Severity),
				FilePath:     f.FilePath,
				Line:         lineColumn(f.Line),
				Description:  f.Description,
				SuggestedFix: f.SuggestedFix,
			}); upsertErr != nil {
				logger.Error("httpapi: review-verdict: upsert review finding failed", "error", upsertErr)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			findingIdentityHashes = append(findingIdentityHashes, f.IdentityHash)
		}

		if _, err := outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
			SessionID:     sessionID,
			Kind:          string(ports.NotificationKindGitHubVerdict),
			Payload:       payload,
			CorrelationID: correlationID,
		}); err != nil {
			logger.Error("httpapi: review-verdict: enqueue outbox entry failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if autoFixCandidate {
			claim, claimErr := sentinelFixes.WithTx(tx).Claim(ctx, prSession.RepoFullName, prSession.PrNumber, sessionID, originHeadBranch)
			if claimErr != nil {
				logger.Error("httpapi: review-verdict: claim sentinel_fixes row failed", "error", claimErr)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			// FixChildSessionID.Valid means a fix is ALREADY in flight for
			// this PR (an earlier qualifying finding claimed this same
			// row) -- never enqueue a second, redundant spawn.
			if !claim.FixChildSessionID.Valid {
				var hashes, descriptions []string
				for _, f := range findings {
					if f.SentinelKind != nil {
						hashes = append(hashes, f.IdentityHash)
						descriptions = append(descriptions, f.Description)
					}
				}
				autoFixPayload, marshalErr := json.Marshal(ports.SentinelAutoFixPayload{
					SentinelFixID:         claim.ID.String(),
					RepoFullName:          prSession.RepoFullName,
					OriginPRNumber:        prSession.PrNumber,
					OriginReviewSessionID: sessionID.String(),
					OriginHeadBranch:      originHeadBranch,
					RepoName:              repoName,
					RepoCloneURL:          repoCloneURL,
					FindingIdentityHashes: hashes,
					FindingDescriptions:   descriptions,
				})
				if marshalErr != nil {
					logger.Error("httpapi: review-verdict: marshal sentinel-auto-fix outbox payload failed", "error", marshalErr)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				if _, err := outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
					SessionID:     sessionID,
					Kind:          string(ports.NotificationKindSentinelAutoFix),
					Payload:       autoFixPayload,
					CorrelationID: correlationID,
				}); err != nil {
					logger.Error("httpapi: review-verdict: enqueue sentinel-auto-fix outbox entry failed", "error", err)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
			}
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: review-verdict: commit tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusCreated, restdtos.PostReviewVerdictResponse{
			Shippable:             restdtos.PostReviewVerdictResponseShippable(verdict.Shippable),
			FormalReviewEvent:     restdtos.PostReviewVerdictResponseFormalReviewEvent(event),
			SyncedLabel:           syncedLabel,
			FindingIdentityHashes: findingIdentityHashes,
		})
	}
}

// findingInputFromWire converts one restdtos.PostedFinding (the wire
// shape) into reviewpost.FindingInput -- the one place this conversion
// happens, so a future field addition to either shape only ever needs
// updating here.
func findingInputFromWire(f restdtos.PostedFinding) reviewpost.FindingInput {
	in := reviewpost.FindingInput{
		Severity:    review.RiskLevel(f.Severity),
		FilePath:    f.FilePath,
		Description: f.Description,
	}
	if f.SentinelKind != nil {
		k := reviewpost.SentinelKind(*f.SentinelKind)
		in.SentinelKind = &k
	}
	if f.Line != nil {
		line := int(*f.Line)
		in.Line = &line
	}
	if f.SuggestedFix != nil {
		in.SuggestedFix = f.SuggestedFix
	}
	return in
}

// hasSentinelFinding reports whether any of findings names a sentinel
// kind (coverage/docs_drift) -- §17.1: "no other sentinel or finding type
// triggers this [sentinel-auto-fix flow]".
func hasSentinelFinding(findings []reviewpost.Finding) bool {
	for _, f := range findings {
		if f.SentinelKind != nil {
			return true
		}
	}
	return false
}

// sentinelKindColumn converts a reviewpost.Finding's own *SentinelKind
// into review_findings.sentinel_kind's own *string column shape.
func sentinelKindColumn(k *reviewpost.SentinelKind) *string {
	if k == nil {
		return nil
	}
	s := string(*k)
	return &s
}

// lineColumn converts a reviewpost.Finding's own *int Line into
// review_findings.line's own *int32 column shape.
func lineColumn(line *int) *int32 {
	if line == nil {
		return nil
	}
	l := int32(*line)
	return &l
}
