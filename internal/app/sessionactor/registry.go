package sessionactor

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// meterName is this package's own OTel meter name (Step 27, "mocking +
// contract drift" -- the contract_drift_detected counter, below, is the
// first metric this package registers) -- mirrors app/reconciler's and
// app/imagebuild's own "narvi/<package>" convention exactly.
const meterName = "narvi/sessionactor"

// storeBundle bundles the store handles this package's Registry and every
// Actor it hydrates share -- built once in NewRegistry, then referenced
// (never rebuilt) everywhere else. Each store is pool-scoped by default;
// callers needing a transaction-scoped one call its WithTx method (see
// actor.go's transact and timerpump.go's claimDueTimers).
type storeBundle struct {
	session *postgres.SessionStore
	turn    *postgres.TurnStore
	sandbox *postgres.SandboxStore
	timer   *postgres.TimerStore
	event   *postgres.EventStore

	// identity and artifact are Step 21's ("e2e happy path") own additions
	// -- both used only by pushpr.go's createPRBestEffort: identity to
	// decrypt the session's own creator's GitHub OAuth access token,
	// artifact to record a successfully created PR as an artifact row (the
	// ONLY durable place a PR's URL is written -- see that file's own doc
	// comment for why this is what makes it visible to a client at all,
	// with no new wire contract needed).
	identity *postgres.IdentityStore
	artifact *postgres.ArtifactStore

	// user is Step 39's ("identities + full RBAC", §13.3) own addition --
	// pushpr.go's own createPRBestEffort uses it for the viewer guard
	// ("viewers never gain PR-reviewer attribution or git identity on
	// session artifacts"): a defense-in-depth check of the session
	// creator's CURRENT role, distinct from (and in addition to)
	// domain/authz.Authorize already refusing a viewer at session-creation
	// time (httpapi.CreateSession) -- this second check catches a role
	// downgraded to viewer AFTER a session was already created by a
	// non-viewer, which Authorize's own create-time check cannot.
	user *postgres.UserStore

	// imageBuild is Step 26's ("image builds") own addition -- used by
	// dispatch.go/imageresolve.go's resolveAndSetImage to look up an
	// already-built image by fingerprint, and to best-effort upsert a
	// pending tracking row on any miss (internal/app/imagebuild's own
	// background loop is this table's OTHER writer, via its own
	// independently-constructed *postgres.ImageBuildStore -- see
	// cmd/control-plane/main.go).
	imageBuild *postgres.ImageBuildStore

	// environment and contractDrift are Step 27's ("mocking + contract
	// drift") own additions, mirroring imageBuild's own addition for Step
	// 26 exactly: environment is used by dispatch.go/contractdrift.go's
	// checkContractDrift to read a spawn/restore plan's own Environment
	// row (MockConfigured, ContractsPath) back by id; contractDrift reads/
	// best-effort-upserts the per-repo contract_drift_snapshots row that
	// same function compares against.
	environment   *postgres.EnvironmentStore
	contractDrift *postgres.ContractDriftStore

	// outbox, slackThreadSession, githubPRSession, and linearAgentSession
	// are Step 35's ("outbox delivery", §5.1) own additions: outbox is
	// where pushpr.go's own completeProcessingTurn writes exactly one
	// notification row per non-'web'-origin session's turn completion,
	// inside that SAME transaction (§5.1: "written in the same tx as the
	// state change"); the other three are the REVERSE (session_id ->
	// origin-channel-address) lookups that write needs to know WHERE to
	// route that notification -- each mirrors imageBuild/environment's own
	// identical "added by the Step that first needs it" precedent.
	outbox             *postgres.OutboxStore
	slackThreadSession *postgres.SlackThreadSessionStore
	githubPRSession    *postgres.GitHubPRSessionStore
	linearAgentSession *postgres.LinearAgentSessionStore

	// plan is Step 37's ("plan mode, web", §8.1/§12.2 item 3) own
	// addition: pushpr.go's own completeProcessingTurn calls
	// recordPlanIfNeeded (planrecord.go) right after persisting a turn's
	// terminal state, inside that SAME transaction, exactly mirroring
	// outbox's own precedent above.
	plan *postgres.PlanStore

	// auditLog is an audit-fix batch's own addition (completeness/
	// observability finding against planrecord.go): recordPlanIfNeeded now
	// writes a "plan.superseded" audit_log row, in the SAME transaction, for
	// every prior awaiting_approval plan a new plan-mode turn's completion
	// supersedes -- mirroring httpapi.DecidePlanOnTx's own identical
	// "audit_log written in the same tx as the change" precedent (§13.3) for
	// a real human/bot decision, now extended to this automatic,
	// system-triggered transition too.
	auditLog *postgres.AuditLogStore

	// sentinelFix/reviewFinding are Step 48's ("sentinels + suggestions",
	// §17) own additions -- pushpr.go's own createSentinelFixPRBestEffort
	// reads sentinelFix (by this Actor's own session id, the FIX session)
	// to learn the origin PR's own head branch/number, then writes both
	// stores back once the fix PR actually opens.
	sentinelFix   *postgres.SentinelFixStore
	reviewFinding *postgres.ReviewFindingStore
}

func newStoreBundle(pool *pgxpool.Pool) storeBundle {
	return storeBundle{
		session:            postgres.NewSessionStore(pool),
		turn:               postgres.NewTurnStore(pool),
		sandbox:            postgres.NewSandboxStore(pool),
		timer:              postgres.NewTimerStore(pool),
		event:              postgres.NewEventStore(pool),
		identity:           postgres.NewIdentityStore(pool),
		artifact:           postgres.NewArtifactStore(pool),
		user:               postgres.NewUserStore(pool),
		imageBuild:         postgres.NewImageBuildStore(pool),
		environment:        postgres.NewEnvironmentStore(pool),
		contractDrift:      postgres.NewContractDriftStore(pool),
		outbox:             postgres.NewOutboxStore(pool),
		slackThreadSession: postgres.NewSlackThreadSessionStore(pool),
		githubPRSession:    postgres.NewGitHubPRSessionStore(pool),
		linearAgentSession: postgres.NewLinearAgentSessionStore(pool),
		plan:               postgres.NewPlanStore(pool),
		auditLog:           postgres.NewAuditLogStore(pool),
		sentinelFix:        postgres.NewSentinelFixStore(pool),
		reviewFinding:      postgres.NewReviewFindingStore(pool),
	}
}

// Registry is the process-wide supervisor of session actors (§2's "one
// goroutine + mailbox per active session", scoped to THIS process --
// other pods run their own independent Registry against the same
// Postgres). At most one live *Actor per session id exists in this
// process's actors map at any time; Registry's own mutex is what makes
// that true within a process, while the Postgres advisory lock
// (hydrateAndAcquire, hydrate.go) is what makes it true ACROSS processes.
type Registry struct {
	mu     sync.Mutex
	actors map[pgtype.UUID]*Actor

	pool     *pgxpool.Pool
	timeouts platform.Timeouts
	stores   storeBundle

	// broadcaster is threaded through to every Actor this Registry
	// hydrates (§6.2's "→ broadcast stream", made real via
	// internal/adapters/inbound/wshub's *Hub -- see
	// internal/app/ports.EventBroadcaster's own doc comment for the full
	// rationale). May be nil (some tests construct a Registry without
	// one) -- Actor.broadcastPending already guards against that.
	broadcaster ports.EventBroadcaster

	// commander, provider, and publicBaseURL are Step 21's ("e2e happy
	// path") own additions, threaded through to every Actor this Registry
	// hydrates exactly the same way broadcaster already is -- see Actor's
	// own field doc comments (actor.go) for what each is used for. All
	// three may be nil/empty (some tests, e.g. the resilience test in
	// design decision 12, never exercise the spawn/dispatch path at all).
	commander     ports.SandboxCommander
	provider      ports.SandboxProvider
	publicBaseURL string

	// sourceControl and tokenEncryptionKey are Step 21's ("e2e happy
	// path") own remaining additions, threaded through to every Actor this
	// Registry hydrates exactly the same way commander/provider already
	// are: sourceControl is the ports.SourceControl every Actor's
	// createPRBestEffort (pushpr.go) calls CreatePR on once a push_complete
	// event arrives; tokenEncryptionKey decrypts the session creator's own
	// stored identities.access_token_encrypted (§13.1) to obtain the
	// plaintext OAuth token CreatePR needs (§8.11: "PR created with the
	// prompting user's OAuth token"). Both may be nil/empty (tests that
	// never exercise the push/PR path).
	sourceControl      ports.SourceControl
	tokenEncryptionKey []byte

	// openCodeRuntimeVersion is Step 26's ("image builds") own remaining
	// addition, threaded through to every Actor this Registry hydrates
	// exactly like sourceControl/tokenEncryptionKey already are: the
	// RuntimeVersion input to domain/imagebuild.Fingerprint (dispatch.go/
	// imageresolve.go's own resolveAndSetImage), sourced from
	// platform.Config.OpenCodeRuntimeVersion. May be empty (tests that
	// never exercise the image-resolution path) -- an empty runtime
	// version still fingerprints deterministically, it just means every
	// session's own fingerprint shares that one (test-only) value.
	openCodeRuntimeVersion string

	// githubBotToken is Step 48's ("sentinels + suggestions", §17.2) own
	// addition, threaded through to every Actor this Registry hydrates
	// exactly like the fields above: pushpr.go's own
	// createSentinelFixPRBestEffort uses this SAME static bot credential
	// (platform.Config.GitHubBotToken -- the identical one internal/
	// adapters/outbound/githubapi.BotNotifier/VerdictNotifier already
	// authenticate with) to open the fix PR, since a sentinel-auto-fix
	// child session has NO human creator of its own to decrypt an OAuth
	// token FOR (sessionRow.CreatedBy is invalid/NULL, SpawnChildSession's
	// own doc comment) -- the fix PR is a system-initiated action,
	// bot-attributed by design, mirroring §17.4's own "system-initiated,
	// not a delegated human one" framing for the eventual merge. May be
	// empty (tests that never exercise the sentinel-fix PR path).
	githubBotToken string

	// contractDriftDetected is Step 27's ("mocking + contract drift", §14.3)
	// own OTel counter, constructed exactly once here (NewRegistry), then
	// threaded through to every Actor this Registry hydrates -- mirroring
	// how every other Actor-shared field above is threaded, and mirroring
	// app/reconciler.NewReconciler's/app/imagebuild.NewBuilder's own
	// "construct the counter once, at construction time" precedent for the
	// counter itself. Incremented by dispatch.go/contractdrift.go's own
	// checkContractDrift whenever contractdrift.HasDrifted reports true for
	// a mock-configured Environment's repo.
	contractDriftDetected metric.Int64Counter

	// repoAccessCache is the audit fix's ("warm-boot image access control",
	// HIGH) own addition: the process-wide, in-memory TTL cache backing
	// resolveAndSetImage's own repo-access gate (imageresolve.go),
	// constructed exactly once here and threaded through to every Actor
	// this Registry hydrates -- mirroring how contractDriftDetected above
	// is already threaded through. See repoaccesscache.go's own top
	// comment for why this is keyed by (user, repo), not per-session, and
	// therefore must be shared across every Actor rather than owned by
	// any one of them.
	repoAccessCache *repoAccessCache

	// group tracks every actor's mailbox-loop goroutine, so evicted/
	// crashed actors are cleanly reaped and Shutdown can wait on all of
	// them. Deliberately the zero value, NOT errgroup.WithContext(...) --
	// see doc.go's Concurrency section: a shared cancel-on-first-error
	// context would let one session's failure tear down every OTHER
	// session's actor sharing this process, which is exactly what
	// single-session ownership must not allow.
	group errgroup.Group

	// lifecycleCtx is the parent context every actor's run loop derives
	// its own cancellation from -- intentionally the PROCESS's lifetime
	// (supplied once at construction), never any individual caller's
	// request-scoped context: an actor spawned to satisfy one inbound
	// request or one timer-pump delivery must keep running long after
	// that request's own context is done. Storing a context in a struct
	// field is unusual, but this is the recognized exception -- a
	// long-lived component's own lifetime signal, not a request-scoped
	// value threaded through call arguments.
	lifecycleCtx context.Context
	cancel       context.CancelFunc
}

// NewRegistry builds a Registry backed by pool. ctx represents the
// process's own lifetime; Shutdown cancels the context every spawned
// actor's run loop derives from. broadcaster is threaded through to every
// Actor this Registry hydrates (§6.2's "→ broadcast stream") -- may be
// nil, in which case every Actor simply never broadcasts (see
// Actor.broadcastPending).
//
// commander/provider/publicBaseURL are Step 21's ("e2e happy path") own
// additions (design decisions 3/4/6): commander is the
// ports.SandboxCommander every Actor's handleEnsureDispatched uses to push
// a dispatched turn's prompt to a live sandbox connection; provider is the
// ports.SandboxProvider every Actor's handleEnsureDispatched calls
// CreateSandbox on; publicBaseURL is this control plane's own externally-
// reachable http(s):// base URL, used to derive SessionConfig.
// ControlPlaneWsUrl for a freshly spawned sandbox. sourceControl and
// tokenEncryptionKey are this SAME Step's remaining additions (design
// decision 9): sourceControl is the ports.SourceControl every Actor's
// createPRBestEffort (pushpr.go) calls CreatePR on; tokenEncryptionKey
// decrypts the session creator's own stored GitHub OAuth access token for
// that same call. openCodeRuntimeVersion is Step 26's ("image builds") own
// addition: the RuntimeVersion input to every Actor's own image-fingerprint
// computation (dispatch.go/imageresolve.go). All six may be nil/empty --
// callers that never exercise the spawn/dispatch/push/PR/image-resolution
// path (e.g. the resilience test, design decision 12) can safely omit
// them.
//
// Step 27 ("mocking + contract drift") adds the contract_drift_detected
// OTel counter's construction here -- exactly once per Registry, mirroring
// app/reconciler.NewReconciler's/app/imagebuild.NewBuilder's own identical
// precedent (see each of their own doc comments) -- which is why NewRegistry
// now returns an error: construction can fail exactly the same way theirs
// can (an invalid/misconfigured MeterProvider), and that failure is
// propagated up through whatever already handles Reconciler/Builder
// construction errors today (cmd/control-plane/main.go).
func NewRegistry(
	ctx context.Context,
	pool *pgxpool.Pool,
	timeouts platform.Timeouts,
	broadcaster ports.EventBroadcaster,
	commander ports.SandboxCommander,
	provider ports.SandboxProvider,
	publicBaseURL string,
	sourceControl ports.SourceControl,
	tokenEncryptionKey []byte,
	openCodeRuntimeVersion string,
	githubBotToken ...string,
) (*Registry, error) {
	meter := otel.Meter(meterName)

	contractDriftDetected, err := meter.Int64Counter(
		"contract_drift_detected",
		metric.WithDescription("Number of times a mock-configured Environment's repo was found to have drifted from its own declared contracts/api spec (§14.3)."),
		metric.WithUnit("{repo}"),
	)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: construct contract_drift_detected counter: %w", err)
	}

	var botToken string
	if len(githubBotToken) > 0 {
		botToken = githubBotToken[0]
	}

	lifecycleCtx, cancel := context.WithCancel(ctx)
	return &Registry{
		actors:                 make(map[pgtype.UUID]*Actor),
		pool:                   pool,
		timeouts:               timeouts,
		stores:                 newStoreBundle(pool),
		broadcaster:            broadcaster,
		commander:              commander,
		provider:               provider,
		publicBaseURL:          publicBaseURL,
		sourceControl:          sourceControl,
		tokenEncryptionKey:     tokenEncryptionKey,
		openCodeRuntimeVersion: openCodeRuntimeVersion,
		githubBotToken:         botToken,
		contractDriftDetected:  contractDriftDetected,
		repoAccessCache:        newRepoAccessCache(),
		lifecycleCtx:           lifecycleCtx,
		cancel:                 cancel,
	}, nil
}

// GetOrSpawn returns the live local Actor for sessionID if this process
// already has one running, otherwise hydrates and starts a new one (§2:
// "hydration on demand"). Returns ErrSessionActorElsewhere if another
// owner already holds the session's advisory lock -- deliberately never
// blocks waiting for it, so a caller in a later Step can route the
// request to whichever pod actually holds the session rather than
// hanging on a lock that may not release for the rest of that actor's
// lifetime.
func (r *Registry) GetOrSpawn(ctx context.Context, sessionID pgtype.UUID) (*Actor, error) {
	if a := r.lookup(sessionID); a != nil {
		return a, nil
	}

	// Hydration + the advisory-lock attempt run WITHOUT the registry
	// mutex held: they are the slow, I/O-bound part, and the Postgres
	// advisory lock itself -- not this mutex -- is the mechanism that
	// must arbitrate two concurrent attempts for the SAME sessionID,
	// whether those two attempts come from two different processes, or
	// two goroutines in this same process each racing past the lookup
	// above before either has inserted into the map. Holding the mutex
	// across this whole sequence would needlessly serialize spawning of
	// completely UNRELATED sessions in this process, for no correctness
	// benefit.
	a, err := r.hydrateAndAcquire(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	// No re-check-the-map-after-hydrating race is possible here: the
	// Postgres advisory lock this call just won is the sole arbiter of
	// ownership, so by construction no OTHER goroutine (in this process
	// or any other) could have concurrently also won it and already
	// inserted a competing entry for sessionID.
	r.mu.Lock()
	r.actors[sessionID] = a
	r.mu.Unlock()

	r.group.Go(func() error {
		return a.run(r.lifecycleCtx)
	})

	return a, nil
}

func (r *Registry) lookup(sessionID pgtype.UUID) *Actor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.actors[sessionID]
}

// evict removes a from the registry's map, but only if a is still the
// entry on file for sessionID -- defensive against a (should-be-
// impossible, per GetOrSpawn's own reasoning) double-insert.
func (r *Registry) evict(sessionID pgtype.UUID, a *Actor) {
	r.mu.Lock()
	if cur, ok := r.actors[sessionID]; ok && cur == a {
		delete(r.actors, sessionID)
	}
	r.mu.Unlock()
}

// Shutdown cancels every live actor's run loop (each releases its
// advisory lock and evicts itself as it exits, per Actor.shutdown) and
// waits for all of them, plus any timer-pump goroutine started through
// this Registry's own group, to finish.
func (r *Registry) Shutdown() error {
	r.cancel()
	return r.group.Wait()
}
