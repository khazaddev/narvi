package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/environment"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// scopedEnvironmentProvenanceTag is the provenance_tag value (§14.1: "carry
// a provenance tag ... so the label automation and the handoff sentinel
// (§14.4) can act on it without re-deriving intent") CreateSession writes
// onto a session's sessions.provenance_tag column whenever
// environment.RequiresProvenanceTag reports true for the Environment it just
// created. §14.1 does not specify an exact wire value, so this is this
// batch's own concrete choice -- a single fixed constant, not derived from
// anything about the request, since today there is exactly one reason a
// session ever carries a provenance tag at all (a non-empty pathScope).
const scopedEnvironmentProvenanceTag = "scoped_environment"

// defaultContractsPath is the contracts_path value CreateSession stores
// when a request's mockConfig is present but omits (or nulls)
// contractsPath -- Row 27's ("mocking + contract drift", §14.3) own
// concrete choice, matching §14.3's own "a shared contracts/api/*.{yaml,json}
// spec" example path exactly.
const defaultContractsPath = "contracts/api"

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
//
// Row 10 ("domain: Environment scoping", §14.1) update: req.PathScope is
// OPTIONAL -- absent or null leaves environment_id/provenance_tag both
// NULL, byte-for-byte today's existing unscoped behavior. When non-empty,
// internal/domain/environment.ValidatePathScope validates every pattern
// BEFORE any Postgres write, exactly the same trust-boundary precedent the
// repo validation above already established (reject with 400 on the
// first invalid pattern, never call pool.Begin on that path). When valid,
// a new environments row is inserted in the SAME transaction as the
// session itself, the session's environment_id is set to that row's id,
// and provenance_tag is set to scopedEnvironmentProvenanceTag whenever
// environment.RequiresProvenanceTag (the real domain function, not a
// re-derived local check) reports true for it.
//
// Row 27 ("mocking + contract drift", §14.3) update: req.MockConfig is a
// SECOND, independent optional Environment attribute alongside PathScope
// (§14.1: "an optional path_scope ... and an optional mock_config" -- two
// separate optional fields, not a package deal). An environments row is
// now created whenever EITHER hasPathScope OR hasMockConfig (mockConfig
// key present in the request body at all, even as {}) is true -- the
// pre-existing hasPathScope-only gate is widened to an OR, never narrowed.
// When hasMockConfig, contractsPath resolves to req.MockConfig.
// ContractsPath's own value when non-nil, otherwise the literal
// defaultContractsPath ("contracts/api") -- mock_configured is set true and
// contracts_path is set to that resolved value on the SAME environments
// row pathScope's own block already creates (or a freshly-created one, if
// mockConfig was supplied with no pathScope). provenance_tag's own
// RequiresProvenanceTag check is untouched -- it only ever depends on
// PathScope (environment.RequiresProvenanceTag's own doc comment), so a
// mockConfig-only Environment does not, by itself, cause a session to
// carry a provenance tag.
//
// Audit remediation (security-crosscutting lens): a caller-supplied
// mockConfig.contractsPath previously reached Postgres (and, downstream,
// a real outbound GitHub API request built by internal/adapters/outbound/
// githubapi.ResolveContractsFingerprint) with ZERO validation -- unlike
// pathScope, which ValidatePathScope already gated. Fixed below by running
// the newly added environment.ValidateContractsPath (this same batch's own
// addition, mirroring ValidatePathScope's own ".." rejection at minimum,
// plus rejecting "?"/"#" -- see that function's own doc comment) BEFORE
// pool.Begin, the SAME trust-boundary precedent every other request field
// this handler validates already follows.
//
// Step 31 ("webhook toolkit") update: everything this func used to do
// AFTER decoding the request body is now createSessionCore below -- a
// pure extraction, not a behavior change (every case this func's own doc
// comment above describes, and every existing test in this package's own
// _test.go files, is unchanged). The only two things that stay HERE,
// specific to the browser/REST path, are decoding the body off an actual
// *http.Request and requiring a real authenticated human caller via
// authenticatedUserID -- a future webhook ingress handler (Steps 32-34)
// calls createSessionCore directly with its own already-decoded request
// and a NULL createdBy (no cookie, no human), never this func.
func CreateSession(pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, environments *postgres.EnvironmentStore, registry *sessionactor.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

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

		created, cerr := createSessionCore(ctx, pool, sessions, turns, environments, registry, req, createdBy)
		if cerr != nil {
			writeError(w, cerr.status, cerr.message)
			return
		}

		writeJSON(w, http.StatusCreated, sessionToDTO(created))
	}
}

// createSessionError carries the exact (status, message) pair the HTTP
// handler should surface for a createSessionCore failure -- a distinct
// type (rather than a plain error) so CreateSession's own writeError call
// sites, and every message they produce, stay byte-for-byte identical to
// what this codebase's existing tests already assert, before and after
// this Step 31 extraction.
type createSessionError struct {
	status  int
	message string
}

func (e *createSessionError) Error() string { return e.message }

// createSessionCore does everything CreateSession's own doc comment above
// describes AFTER decoding the request body: repo validation, pathScope/
// mockConfig validation, the transactional environment/session/(optional)
// turn writes, and the post-commit GetOrSpawn + EnsureDispatched dispatch.
//
// createdBy is a NULLABLE creator (pgtype.UUID with Valid == false stored
// as a genuine SQL NULL in sessions.created_by) -- matching sqlcgen.
// CreateSessionParams.CreatedBy's own pgtype.UUID nullability and this
// schema's own documented intent (contracts/rest/v1/dtos.schema.json:
// Session.createdBy, "Null for bot/automation-created sessions with no
// direct human user"). CreateSession (the HTTP handler above) always
// passes a Valid one today, since it still hard-requires
// authenticatedUserID -- but this core function itself never assumes
// that: a future webhook ingress caller (Steps 32-34) with no
// cookie-authenticated human passes an explicitly invalid pgtype.UUID{}
// here instead.
func createSessionCore(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, environments *postgres.EnvironmentStore, registry *sessionactor.Registry, req restdtos.CreateSessionRequest, createdBy pgtype.UUID) (sqlcgen.Session, *createSessionError) {
	logger := platform.Logger(ctx)

	if len(req.Repos) < 1 {
		return sqlcgen.Session{}, &createSessionError{http.StatusBadRequest, "repos must be non-empty"}
	}

	// Validate every repo's Name/Url/Branch BEFORE any Postgres write
	// (pool.Begin below) -- see CreateSession's own doc comment above.
	// Stops at the first failure, in order, matching gitclone's own
	// validateRepoSpec precedent exactly; does not attempt to
	// collect/report every failure across every repo at once.
	for i, repo := range req.Repos {
		if err := reposource.ValidateRepoName(repo.Name); err != nil {
			return sqlcgen.Session{}, &createSessionError{http.StatusBadRequest, fmt.Sprintf("repos[%d].name: %s", i, err)}
		}
		if err := reposource.ValidateRepoURL(repo.Url); err != nil {
			return sqlcgen.Session{}, &createSessionError{http.StatusBadRequest, fmt.Sprintf("repos[%d].url: %s", i, err)}
		}
		if repo.Branch != nil {
			if err := reposource.ValidateBranch(*repo.Branch); err != nil {
				return sqlcgen.Session{}, &createSessionError{http.StatusBadRequest, fmt.Sprintf("repos[%d].branch: %s", i, err)}
			}
		}
	}

	reposJSON, err := json.Marshal(req.Repos)
	if err != nil {
		logger.Error("httpapi: marshal repos failed", "error", err)
		return sqlcgen.Session{}, &createSessionError{http.StatusInternalServerError, "internal error"}
	}

	// pathScope is OPTIONAL (contracts/rest/v1/dtos.schema.json's
	// CreateSessionRequest.pathScope) -- req.PathScope may be nil
	// (absent) or point at a nil/empty slice (present but null/[]);
	// either way that means "unscoped", exactly today's existing
	// behavior. Only a genuinely non-empty pathScope triggers
	// validation + environment creation below.
	var pathScope []string
	if req.PathScope != nil {
		pathScope = []string(*req.PathScope)
	}
	hasPathScope := len(pathScope) > 0

	// Validate every pathScope pattern BEFORE any Postgres write (pool.
	// Begin below) -- the SAME trust-boundary precedent the repo
	// validation above already established: reject with 400 on the
	// first invalid pattern, in order, never reaching pool.Begin on
	// that path.
	if hasPathScope {
		if err := environment.ValidatePathScope(pathScope); err != nil {
			return sqlcgen.Session{}, &createSessionError{http.StatusBadRequest, fmt.Sprintf("pathScope: %s", err)}
		}
	}

	// mockConfig is OPTIONAL and INDEPENDENT of pathScope (row 27,
	// "mocking + contract drift", §14.3 -- see CreateSession's own doc
	// comment). hasMockConfig is true whenever the request body carried
	// a "mockConfig" key at all (req.MockConfig != nil), even as {} --
	// contractsPath resolves to the caller's own value when supplied,
	// otherwise defaultContractsPath.
	hasMockConfig := req.MockConfig != nil
	contractsPath := defaultContractsPath
	if hasMockConfig && req.MockConfig.ContractsPath != nil {
		contractsPath = *req.MockConfig.ContractsPath

		// Validate a caller-supplied contractsPath BEFORE any Postgres
		// write (pool.Begin below) -- the SAME trust-boundary precedent
		// pathScope's own ValidatePathScope call above already
		// established (audit remediation: contractsPath previously had
		// NO validation at all, unlike pathScope, even though it is
		// later interpolated into a real outbound GitHub API request,
		// internal/adapters/outbound/githubapi.
		// ResolveContractsFingerprint). defaultContractsPath itself is
		// never run through this check -- it is this handler's own
		// fixed, known-safe constant, not caller input.
		if err := environment.ValidateContractsPath(contractsPath); err != nil {
			return sqlcgen.Session{}, &createSessionError{http.StatusBadRequest, fmt.Sprintf("mockConfig.contractsPath: %s", err)}
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: begin create-session tx failed", "error", err)
		return sqlcgen.Session{}, &createSessionError{http.StatusInternalServerError, "internal error"}
	}
	// Rollback is a safety net for every return path other than a
	// successful Commit below -- same pattern as internal/adapters/
	// inbound/auth's own createUserAndIdentity and app/sessionactor's
	// own transact.
	defer func() { _ = tx.Rollback(ctx) }()

	// An environments row is inserted in this SAME transaction, BEFORE
	// the session row itself, so the session insert below can set
	// environment_id to it directly, whenever EITHER a non-empty
	// pathScope OR a present mockConfig was supplied -- matching
	// CreateSession's own doc comment (row 27's "either" gate, not "both
	// required"). environment_id/provenanceTag both stay their
	// pgtype/Go zero values (NULL) when NEITHER is present, identical
	// to every session created before this batch.
	var environmentID pgtype.UUID
	var provenanceTag *string
	if hasPathScope || hasMockConfig {
		var pathScopeJSON []byte
		if hasPathScope {
			var marshalErr error
			pathScopeJSON, marshalErr = json.Marshal(pathScope)
			if marshalErr != nil {
				logger.Error("httpapi: marshal pathScope failed", "error", marshalErr)
				return sqlcgen.Session{}, &createSessionError{http.StatusInternalServerError, "internal error"}
			}
		}

		var contractsPathCol *string
		if hasMockConfig {
			contractsPathCol = &contractsPath
		}

		env, envErr := environments.WithTx(tx).Create(ctx, sqlcgen.CreateEnvironmentParams{
			PathScope:      pathScopeJSON,
			MockConfigured: hasMockConfig,
			ContractsPath:  contractsPathCol,
		})
		if envErr != nil {
			logger.Error("httpapi: create environment failed", "error", envErr)
			return sqlcgen.Session{}, &createSessionError{http.StatusInternalServerError, "internal error"}
		}
		environmentID = env.ID

		// The real domain function, not a re-derived local boolean --
		// see CreateSession's own doc comment. Depends only on PathScope,
		// exactly like RequiresProvenanceTag's own doc comment says --
		// a mockConfig-only Environment never causes this to fire.
		if environment.RequiresProvenanceTag(environment.Environment{PathScope: pathScope}) {
			tag := scopedEnvironmentProvenanceTag
			provenanceTag = &tag
		}
	}

	created, err := sessions.WithTx(tx).Create(ctx, sqlcgen.CreateSessionParams{
		Title:         (*string)(req.Title),
		SpawnSource:   sqlcgen.SessionSpawnSource(req.SpawnSource),
		CreatedBy:     createdBy,
		Repos:         reposJSON,
		EnvironmentID: environmentID,
		ProvenanceTag: provenanceTag,
	})
	if err != nil {
		logger.Error("httpapi: create session failed", "error", err)
		return sqlcgen.Session{}, &createSessionError{http.StatusInternalServerError, "internal error"}
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
			return sqlcgen.Session{}, &createSessionError{http.StatusInternalServerError, "internal error"}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: commit create-session tx failed", "error", err)
		return sqlcgen.Session{}, &createSessionError{http.StatusInternalServerError, "internal error"}
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

	return created, nil
}
