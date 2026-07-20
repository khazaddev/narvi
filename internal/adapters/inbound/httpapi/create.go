package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// CreateSession backs POST /api/sessions (§6.3), mounted (Step 20, "auth
// v1") behind internal/adapters/inbound/auth.Middleware -- see doc.go's own
// updated writeup. Decodes restdtos.CreateSessionRequest from a body
// bounded by http.MaxBytesReader(maxRequestBodyBytes) -- an oversized body
// surfaces as *http.MaxBytesError, reported as 413; any other decode
// failure (malformed JSON) is 400. repos' own schema-level minItems:1 is
// not enforced by Go's plain json.Unmarshal, so it is checked explicitly
// here -- 400 on an empty list.
//
// Each repo's own Name/Url/Branch is then validated via
// internal/domain/reposource's exported, plain-string validators -- the
// SAME package/functions internal/sandboxagent/gitclone's own
// validateRepoSpec already calls at actual git-invocation time. This is
// the trust-boundary half of defense in depth: without it, a malformed
// repo spec sat in Postgres past a 201 and only surfaced as a confusing,
// delayed spawn failure deep inside the sandbox agent. Validated here, in
// order, stopping at the first failure (matching validateRepoSpec's own
// stop-at-first-failure precedent): Name (it later reaches
// filepath.Join(workspaceDir, repo.Name) in gitclone, so an unvalidated
// Name is exactly as path-traversal-shaped a risk as an unvalidated Url/
// Branch), then Url, then Branch -- but ONLY when Branch is non-nil (nil
// means "use the repo's own default branch", exactly gitclone's own
// precedent for this identical nullable field). This runs entirely
// BEFORE pool.Begin below, so a rejected repo spec never reaches Postgres
// at all. gitclone's own validation at the deep git-invocation site is
// left completely unchanged -- sandbox-agent must never trust what it
// receives, even from a layer that validates first.
//
// Step 21 ("e2e happy path") update: req.Repos is now actually PERSISTED
// (marshaled to the sessions.repos JSONB column -- design decision 1,
// migrations/000018_session_repos.up.sql). When req.Prompt is non-nil, a
// Turn row is ALSO inserted, in the SAME Postgres transaction as the
// session insert (mirroring internal/adapters/inbound/auth's own
// createUserAndIdentity pool.Begin/WithTx/Commit pattern exactly, so a
// failure partway through never leaves an orphaned session with no turn
// or vice versa). After a successful commit, GetOrSpawn + Send(
// EnsureDispatched{}) run SYNCHRONOUSLY but are still "fire and forget" in
// the sense that matters: GetOrSpawn only hydrates local actor state (a
// few fast Postgres round trips, no external network call) and Send only
// enqueues into the actor's own mailbox -- the SLOW work (a real
// SandboxProvider.CreateSandbox call, if a spawn decision fires) happens
// entirely on the actor's own already-running background goroutine,
// AFTER this handler has already returned its 201. No naked goroutine is
// spun up here for that reason -- see this func's own inline comment at
// the call site.
func CreateSession(pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, registry *sessionactor.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		createdBy, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

		var req restdtos.CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		if len(req.Repos) < 1 {
			writeError(w, http.StatusBadRequest, "repos must be non-empty")
			return
		}

		// Validate every repo's Name/Url/Branch BEFORE any Postgres write
		// (pool.Begin below) -- see this func's own doc comment above.
		// Stops at the first failure, in order, matching gitclone's own
		// validateRepoSpec precedent exactly; does not attempt to
		// collect/report every failure across every repo at once.
		for i, repo := range req.Repos {
			if err := reposource.ValidateRepoName(repo.Name); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("repos[%d].name: %s", i, err))
				return
			}
			if err := reposource.ValidateRepoURL(repo.Url); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("repos[%d].url: %s", i, err))
				return
			}
			if repo.Branch != nil {
				if err := reposource.ValidateBranch(*repo.Branch); err != nil {
					writeError(w, http.StatusBadRequest, fmt.Sprintf("repos[%d].branch: %s", i, err))
					return
				}
			}
		}

		reposJSON, err := json.Marshal(req.Repos)
		if err != nil {
			logger.Error("httpapi: marshal repos failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: begin create-session tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// Rollback is a safety net for every return path other than a
		// successful Commit below -- same pattern as internal/adapters/
		// inbound/auth's own createUserAndIdentity and app/sessionactor's
		// own transact.
		defer func() { _ = tx.Rollback(ctx) }()

		created, err := sessions.WithTx(tx).Create(ctx, sqlcgen.CreateSessionParams{
			Title:       (*string)(req.Title),
			SpawnSource: sqlcgen.SessionSpawnSource(req.SpawnSource),
			CreatedBy:   createdBy,
			Repos:       reposJSON,
		})
		if err != nil {
			logger.Error("httpapi: create session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		hasPrompt := req.Prompt != nil
		if hasPrompt {
			if _, err := turns.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
				SessionID: created.ID,
				Status:    sqlcgen.TurnStatusPending,
				Prompt:    (*string)(req.Prompt),
				ModelID:   (*string)(req.ModelId),
				PlanMode:  req.PlanMode,
			}); err != nil {
				logger.Error("httpapi: create turn failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: commit create-session tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if hasPrompt {
			// Fire-and-forget: GetOrSpawn hydrates local actor state (fast,
			// no external network call) and Send only enqueues into the
			// actor's own mailbox -- the actual spawn/dispatch decision
			// (including any real CreateSandbox network call) runs
			// entirely on the actor's own background goroutine, not on
			// this request's own goroutine, so this does not block the
			// 201 response on how long that decision takes.
			actor, spawnErr := registry.GetOrSpawn(ctx, created.ID)
			if spawnErr != nil {
				logger.Warn("httpapi: GetOrSpawn after session create failed", "error", spawnErr)
			} else if sendErr := actor.Send(ctx, sessionactor.EnsureDispatched{}); sendErr != nil {
				logger.Warn("httpapi: send EnsureDispatched after session create failed", "error", sendErr)
			}
		}

		writeJSON(w, http.StatusCreated, sessionToDTO(created))
	}
}
