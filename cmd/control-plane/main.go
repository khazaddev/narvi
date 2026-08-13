// Command control-plane is the Narvi control plane binary: config, wiring,
// migrations, HTTP+WS server. Config loading + validation landed in PR-02
// (§5.4); structured logging + OTel bootstrap landed in PR-03 (§5.3); PR-06
// adds the real dev-loop server: a Postgres pool + boot-time migrations, a
// chi router with a real /health, and errgroup-managed graceful shutdown
// (§5.2, §10-P0). The full REST/WS API lands in later PRs.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver, used only for the golang-migrate handle below
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/inbound/auth"
	"github.com/khazaddev/narvi/internal/adapters/inbound/automationwebhook"
	githubingress "github.com/khazaddev/narvi/internal/adapters/inbound/github"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	identitylinkhttp "github.com/khazaddev/narvi/internal/adapters/inbound/identitylink"
	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	"github.com/khazaddev/narvi/internal/adapters/outbound/chatgptoauth"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/llm"
	"github.com/khazaddev/narvi/internal/adapters/outbound/modal"
	"github.com/khazaddev/narvi/internal/adapters/outbound/objstore"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/rwx"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/automation"
	"github.com/khazaddev/narvi/internal/app/automerge"
	"github.com/khazaddev/narvi/internal/app/chatgptlink"
	"github.com/khazaddev/narvi/internal/app/chatgptrefresh"
	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/app/digest"
	"github.com/khazaddev/narvi/internal/app/findingposition"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/app/imagebuild"
	"github.com/khazaddev/narvi/internal/app/intentclassifier"
	"github.com/khazaddev/narvi/internal/app/outboxworker"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/reconciler"
	"github.com/khazaddev/narvi/internal/app/releasereview"
	appreviewverdict "github.com/khazaddev/narvi/internal/app/reviewverdict"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/app/uploadsweep"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// githubAPIBaseURL is GitHub's own real REST API base, passed to
// auth.NewCallbackHandler's own apiBaseURL parameter in production wiring.
// That parameter exists specifically so internal/adapters/inbound/auth's
// own tests can override it with a local httptest.Server standing in for
// GitHub's API — this constant is the ONLY place the real
// "https://api.github.com" literal appears in this binary's wiring.
const githubAPIBaseURL = "https://api.github.com"

// linearAPIBaseURL is Linear's own real API base (its GraphQL endpoint and
// OAuth2 token endpoint both live under this host), passed to
// linearapi.New's own apiBaseURL parameter in production wiring -- the
// ONLY place this literal appears in this binary's wiring, mirroring
// githubAPIBaseURL's own identical precedent immediately above (Step 34,
// "Linear ingress", §8.10).
const linearAPIBaseURL = "https://api.linear.app"

// slackAPIBaseURL is Slack's own real Web API base, passed to
// slack.Deps.SlackAPIBaseURL in production wiring (Step 33, "Slack
// ingress") -- the ONLY place this literal appears in this binary's
// wiring, mirroring githubAPIBaseURL's own identical precedent exactly.
const slackAPIBaseURL = "https://slack.com/api"

// This is intentionally a bare-bones dispatch, not a flag-parsing library:
// there is exactly one subcommand today ("serve"). Anything else prints a
// one-line usage message to stderr and exits non-zero.
func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: control-plane serve")
		os.Exit(1)
	}

	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// serve loads config, wires logging/OTel (unchanged from PR-02/PR-03),
// opens the Postgres pool and applies embedded migrations, then runs the
// chi-routed HTTP server until SIGINT/SIGTERM, shutting down gracefully
// within Timeouts.ShutdownGracePeriod. The listen goroutine and the
// shutdown-watcher goroutine are both launched via errgroup.Group.Go —
// never a bare `go` statement (§11: no naked goroutines).
func serve() error {
	cfg, err := platform.Load()
	if err != nil {
		return err
	}

	logger := platform.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownOTel, err := platform.SetupOTel(ctx, "narvi-control-plane")
	if err != nil {
		return err
	}
	defer func() {
		// Deliberately a fresh background context, not ctx: by the time
		// this deferred call runs, ctx may already be canceled (that's
		// exactly what triggers shutdown below), and a canceled context
		// would make the flush itself fail immediately.
		if err := shutdownOTel(context.Background()); err != nil {
			slog.Error("otel shutdown failed", "error", err)
		}
	}()

	pool, err := postgres.NewPoolWithMaxConns(ctx, cfg.DatabaseURL, cfg.DBPoolMaxConns)
	if err != nil {
		return fmt.Errorf("open postgres pool: %w", err)
	}
	defer pool.Close()
	// Logged at boot, not just documented, so an operator sizing a
	// deployment (or diagnosing a hang) has the resolved value in hand
	// without reading source -- see platform.Config.DBPoolMaxConns's own
	// doc comment for why this number matters independently of host core
	// count.
	slog.Info("narvi control-plane: postgres pool configured", "max_conns", pool.Config().MaxConns)

	if err := applyMigrations(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// hub is the single shared piece of state connecting the app-layer
	// actor to the adapter-layer client sockets (§6.2's "→ broadcast
	// stream", Step 19): constructed once here, then threaded through to
	// BOTH sessionactor.NewRegistry (as the ports.EventBroadcaster every
	// Actor's successful transact commits to) and wshub.NewClientHandler
	// (so it can register/unregister each subscribed connection) -- see
	// internal/adapters/inbound/wshub's own Hub doc comment.
	hub := wshub.NewHub()

	// commander is the ports.SandboxCommander every Actor uses to push an
	// outbound command (a dispatched turn's prompt) to a session's live
	// sandbox WS connection (Step 21, "e2e happy path", design decision
	// 4) -- constructed once here, then threaded through to BOTH
	// sessionactor.NewRegistry (as the port) and wshub.NewSandboxHandler
	// (so it can Register each connection as it completes its handshake),
	// mirroring hub's own dual-threading immediately above exactly.
	commander := wshub.NewSandboxRegistry(cfg.Timeouts)

	// sandboxProvider is the real internal/adapters/outbound/modal.
	// Provider (Step 21 is its first real production caller anywhere in
	// this codebase -- see that package's own doc.go for the "no real
	// Modal account reachable from this codebase's own tests/CI" caveat;
	// a real deploy of this binary must point NARVI_MODAL_BASE_URL/
	// NARVI_MODAL_AUTH_TOKEN at an actual Modal account, or a mock
	// standing in for one).
	sandboxProvider, err := modal.New(modal.Config{
		BaseURL:        cfg.ModalBaseURL,
		AuthToken:      cfg.ModalAuthToken,
		Timeouts:       cfg.Timeouts,
		EgressProxyURL: cfg.ModalEgressProxyURL,
	})
	if err != nil {
		return fmt.Errorf("construct modal provider: %w", err)
	}

	// sourceControl is the ports.SourceControl every Actor's own
	// createPRBestEffort (internal/app/sessionactor/pushpr.go) calls
	// CreatePR on once a push_complete event arrives -- constructed once
	// here, exactly mirroring modal.New's own real-adapter-in-production-
	// wiring precedent immediately above. httpClient is nil (defaults to
	// http.DefaultClient, see githubapi.New's own doc comment) since each
	// individual CreatePR call is already bounded by its own caller-
	// supplied context deadline (platform.Timeouts.PRCreateTimeout), not a
	// package-level http.Client.Timeout.
	sourceControl := githubapi.New(nil, githubAPIBaseURL)

	// Registry/wshub are wired into the real binary for the first time in
	// Step 18 -- an intended, natural consequence of that: the timer pump
	// (already built in Step 11) becomes genuinely live here for the first
	// time too, run via the errgroup below. Step 19 adds the client-hub
	// half (hub above) and the store handles its own handlers/REST
	// endpoints need. Step 21 adds commander/sandboxProvider/
	// cfg.PublicBaseURL/sourceControl/cfg.TokenEncryptionKey -- see
	// internal/app/sessionactor.NewRegistry's own doc comment for what
	// each is used for. Step 27 ("mocking + contract drift") makes
	// NewRegistry fallible (constructs the contract_drift_detected OTel
	// counter), mirroring recon/builder's own identical error handling
	// immediately below. Step 49 ("handoff-readiness sentinel") adds
	// diffFetcher -- the SAME sourceControl *githubapi.Adapter instance
	// passed a second time, satisfying sessionactor.PRDiffFetcher exactly
	// like it already satisfies the github inbound handler's own
	// reviewcontext.Fetcher below (DiffFetcher: sourceControl) -- never a
	// second, independently-constructed client. Step 65 ("review:
	// automatic re-review on new commits") adds ReviewDiffFetcher --
	// the SAME sourceControl instance a THIRD time, satisfying
	// reviewcontext.Fetcher directly (GetPullRequest/GetCompareDiff are
	// both real *githubapi.Adapter methods) -- plus GitHubBotHandle/
	// GitHubBotToken, bundled into sessionactor.RegistryOptions (see that
	// type's own doc comment for why this is a trailing options struct,
	// not more positional parameters).
	registry, err := sessionactor.NewRegistry(ctx, pool, cfg.Timeouts, hub, commander, sandboxProvider, cfg.PublicBaseURL,
		sourceControl, cfg.TokenEncryptionKey, cfg.OpenCodeRuntimeVersion, sourceControl, cfg.EpistemicCheckDefault,
		sessionactor.RegistryOptions{
			GitHubBotToken:    cfg.GitHubBotToken,
			GitHubBotHandle:   cfg.GitHubBotHandle,
			ReviewDiffFetcher: sourceControl,
		})
	if err != nil {
		return fmt.Errorf("construct session actor registry: %w", err)
	}
	sessionStore := postgres.NewSessionStore(pool)
	turnStore := postgres.NewTurnStore(pool)
	sandboxStore := postgres.NewSandboxStore(pool)
	eventStore := postgres.NewEventStore(pool)
	artifactStore := postgres.NewArtifactStore(pool)
	wsTokenStore := postgres.NewWSTokenStore(pool)
	environmentStore := postgres.NewEnvironmentStore(pool)
	imageBuildStore := postgres.NewImageBuildStore(pool)

	// automationStore/automationInvocationStore/automationRunStore are
	// Step 51's ("automations: engine", §3.5) own three tables -- see
	// internal/app/automation's own doc.go for the full engine writeup.
	// Constructed here, alongside every other core store, so the engine
	// below (and any future Step 52 trigger-evaluation caller) can share
	// them rather than each constructing its own copy.
	automationStore := postgres.NewAutomationStore(pool)
	automationInvocationStore := postgres.NewAutomationInvocationStore(pool)
	automationRunStore := postgres.NewAutomationRunStore(pool)

	// planStore/participantStore are Step 37's ("plan mode, web", §8.1)
	// own additions, backing the two new approve/reject REST endpoints
	// below (internal/adapters/inbound/httpapi/planapprove.go).
	planStore := postgres.NewPlanStore(pool)
	participantStore := postgres.NewParticipantStore(pool)

	// auditLogStore is Step 39's ("identities + full RBAC", §13.3) own
	// addition -- every Authorize-gated state change (CreateSession,
	// CreateTurn, ApprovePlan/RejectPlan below) writes one audit_log row
	// on the SAME transaction as the change itself; threaded into every
	// GitHub/Slack/Linear ingress Deps struct too (below), so bot-
	// attributed session/turn/plan-decision writes get the identical
	// treatment (actor_user_id NULL), not a second, REST-only code path.
	auditLogStore := postgres.NewAuditLogStore(pool)

	// outboxStore/linearAgentSessionStore are constructed here (rather than
	// down where the outbox delivery worker/Linear ingress blocks live
	// below) because Step 38's ("plan mode, cross-channel", §8.1/§13.3) own
	// httpapi.DecidePlanOnTx -- shared by the /api/sessions plan approve/
	// reject routes immediately below AND by internal/adapters/inbound/
	// {slack,linear}'s own new plan-decision entry points -- needs both, to
	// enqueue this Step's own cross-channel-notify outbox rows.
	outboxStore := postgres.NewOutboxStore(pool)
	// releaseManifestPendingStore (blocking-finding fix #1, "release PR
	// review", §15.2) is the durable release_manifest_pending queue --
	// the github Config below writes to it inline (a single, fast
	// INSERT, before its own webhook ack); releasereview.Worker (started
	// alongside every other background loop below) is what later claims
	// and actually runs the manifest check -- see
	// migrations/000050_release_manifest_pending.up.sql's own doc
	// comment for the full "why" this exists as its own table/loop,
	// never folded into outboxStore/outboxworker itself.
	releaseManifestPendingStore := postgres.NewReleaseManifestPendingStore(pool)
	linearAgentSessionStore := postgres.NewLinearAgentSessionStore(pool)

	// blobStore/uploadSweeper (Step 58, "uploads, blob storage & the
	// in-sandbox download_file tool", §28.7) are constructed ONLY when
	// cfg.ObjectStorage is non-nil -- mirrors cfg.RWXAccessToken's own
	// "absent = feature off" precedent below, one level deeper: with no
	// object-storage config present, blobStore stays a nil
	// ports.BlobStore, and every upload mint endpoint (route registration
	// below) returns its own structured "uploads not configured" error
	// rather than failing to boot. Constructed here, early, rather than
	// down where RWX's own optional adapter lives: unlike RWX (needed only
	// by the outbox notifier map), blobStore is threaded into the upload
	// routes registered further down in this same function, so it must
	// exist before that point.
	var blobStore ports.BlobStore
	var objStore *objstore.Store
	var uploadSweeper *uploadsweep.Sweeper
	if cfg.ObjectStorage != nil {
		objStore, err = objstore.New(objstore.Config{
			Endpoint:        cfg.ObjectStorage.Endpoint,
			PublicEndpoint:  cfg.ObjectStorage.PublicEndpoint,
			Region:          cfg.ObjectStorage.Region,
			Bucket:          cfg.ObjectStorage.Bucket,
			AccessKeyID:     cfg.ObjectStorage.AccessKeyID,
			SecretAccessKey: cfg.ObjectStorage.SecretAccessKey,
			UsePathStyle:    cfg.ObjectStorage.UsePathStyle,
			Timeouts:        cfg.Timeouts,
		})
		if err != nil {
			return fmt.Errorf("construct object storage adapter: %w", err)
		}
		blobStore = objStore

		uploadSweeper, err = uploadsweep.NewSweeper(pool, artifactStore, eventStore, outboxStore, sandboxStore, hub, cfg.Timeouts)
		if err != nil {
			return fmt.Errorf("construct upload abandonment sweeper: %w", err)
		}
	}

	// slackNotifier/planSlackNotifier are constructed here (rather than
	// down where the outbox delivery worker block lives below) because the
	// new Slack interactivity route (right below) ALSO needs a real
	// *slackapi.Client, for its own synchronous chat.update/views.open
	// calls -- one client, reused for both the outbox notifier registration
	// and this route, mirroring how registry/commander are each
	// constructed once and threaded through multiple call sites elsewhere
	// in this same function.
	slackNotifier := slackapi.New(nil, slackAPIBaseURL, cfg.SlackBotToken)
	planSlackNotifier := outboxworker.NewPlanSlackNotifier(slackNotifier, planStore)

	// webhookDeliveryStore is Step 31's own provider-agnostic dedupe claim,
	// shared across Steps 32/33/34's own GitHub/Slack/Linear ingress (see
	// the Linear ingress block below, which reuses this SAME store rather
	// than constructing its own). slackThreadSessionStore (Step 33, "Slack
	// ingress", §8.10) is the thread<->session mapping (see
	// internal/adapters/inbound/slack's own doc.go); githubPRSessionStore
	// (Step 32, "GitHub ingress", §8.2) is the per-PR review-session
	// coalescing claim (see internal/adapters/inbound/github's own doc.go).
	webhookDeliveryStore := postgres.NewWebhookDeliveryStore(pool)
	slackThreadSessionStore := postgres.NewSlackThreadSessionStore(pool)
	githubPRSessionStore := postgres.NewGitHubPRSessionStore(pool)
	// repoSettingsStore (Step 47, "server-side verdict", §8.2/§21.2) backs
	// the admin repo-settings REST routes below AND the verdict-posting
	// tool's own blockOnHighRisk read (reviewverdict.go) -- one store,
	// shared, never a second independently-constructed copy.
	repoSettingsStore := postgres.NewRepoSettingsStore(pool)
	// timerStore (Step 65, "review: automatic re-review on new commits",
	// §24.1) backs the synchronize webhook lane's own DIRECT, actor-
	// bypassing session_timers write below (githubingress.Config.Timers)
	// -- a standalone instance over the SAME pool every other store here
	// already shares (sessionactor.Registry constructs its own, separate
	// *postgres.TimerStore internally, never exported, so this webhook
	// handler needs its own).
	timerStore := postgres.NewTimerStore(pool)
	// providerCredentialStore (Step 53, "provider credential injection",
	// §25.1/§25.3) backs the 3 scoped management CRUD route groups below
	// AND the sandbox-facing delivery endpoint (providercredentialsdelivery.go)
	// -- one store, shared, never a second independently-constructed copy.
	providerCredentialStore := postgres.NewProviderCredentialStore(pool)
	// chatGPTLinkAttemptStore/chatGPTDeviceFlow (Step 59, "models: Codex
	// via ChatGPT-account OAuth", §29.3/§29.5/§29.9) back the self-service
	// link-flow REST routes (chatgptlink.go) AND the refresh pump
	// (chatgptrefresh) -- one store/client each, shared, never a second
	// independently-constructed copy. chatGPTDeviceFlow's own httpClient
	// deliberately does NOT set http.Client.Timeout -- chatgptoauth.Client
	// bounds every call itself via a per-call context.WithTimeout wrap
	// (platform.Timeouts.ChatGPTOAuthHTTPClientTimeout), mirroring
	// internal/adapters/outbound/opencode's own doJSONTimeout precedent
	// (see that package's own client.go doc comment).
	chatGPTLinkAttemptStore := postgres.NewChatGPTLinkAttemptStore(pool)
	chatGPTDeviceFlow := chatgptoauth.New(http.DefaultClient, chatgptoauth.DefaultBaseURL, cfg.Timeouts.ChatGPTOAuthHTTPClientTimeout)
	chatGPTLinkDeps := chatgptlink.Deps{
		Pool:                pool,
		LinkAttempts:        chatGPTLinkAttemptStore,
		ProviderCredentials: providerCredentialStore,
		AuditLog:            auditLogStore,
		DeviceFlow:          chatGPTDeviceFlow,
		TokenEncryptionKey:  cfg.TokenEncryptionKey,
		Timeouts:            cfg.Timeouts,
	}
	// reviewFindingStore/sentinelFixStore (Step 48, "sentinels +
	// suggestions", §17/§22.1) back the verdict-posting tool's own
	// per-finding upsert + sentinel-auto-fix claim (reviewverdict.go), the
	// rebut/apply-suggestion endpoints (reviewfindings.go), and re-review
	// reconciliation (internal/app/reviewcontext) -- one store each,
	// shared, never a second independently-constructed copy.
	reviewFindingStore := postgres.NewReviewFindingStore(pool)
	sentinelFixStore := postgres.NewSentinelFixStore(pool)
	// falsePositivePatternStore (Step 63, "review: learned false-positive
	// patterns", §22.2/§22.3/§22.4) backs the GitHub capture command, the
	// advisory-injection fetch (internal/app/reviewcontext), and the
	// audit-view/retire REST endpoints -- one store, shared, never a
	// second independently-constructed copy.
	falsePositivePatternStore := postgres.NewFalsePositivePatternStore(pool)
	// reviewVerdictStore/autoApprovalOutcomeStore/digestSendStateStore
	// (Step 62, "review verdict persistence, analytics, digest &
	// automated approval", §21) back the verdict-posting tool's own
	// review_verdicts insert (reviewverdict.go), the real auto-approval
	// eligibility engine's own latest-verdict read (both decision-inbox
	// call sites, internal/app/decisioninbox), the contradiction-rate
	// calibration read model, and the daily digest -- one store each,
	// shared, never a second independently-constructed copy.
	reviewVerdictStore := postgres.NewReviewVerdictStore(pool)
	autoApprovalOutcomeStore := postgres.NewAutoApprovalOutcomeStore(pool)
	digestSendStateStore := postgres.NewDigestSendStateStore(pool)
	reviewVerdictDeps := appreviewverdict.Deps{
		ReviewVerdicts:       reviewVerdictStore,
		RepoSettings:         repoSettingsStore,
		ReviewFindings:       reviewFindingStore,
		AutoApprovalOutcomes: autoApprovalOutcomeStore,
		Timeouts:             cfg.Timeouts,
	}
	// digestChannelStore (Step 62, §21.3) backs internal/app/digest's own
	// channel-discovery step -- constructed here, alongside its own
	// sibling stores, though the digest.Deps/automerge.Deps bundles that
	// actually use it are assembled further below, once decisionInboxDeps
	// (automerge.Deps embeds the full decisioninbox.Deps) exists.
	digestChannelStore := postgres.NewDigestChannelStore(pool)
	// workflowStore (Step 55, "workflow execution engine", §25.6) backs
	// the generic step-outcome-posting tool (workflowstepoutcome.go) --
	// sessionactor's own Registry constructs its OWN WorkflowStore
	// internally (newStoreBundle, registry.go), and createTurnLocked
	// constructs one inline from pool (turn.go's own doc comment explains
	// why: avoiding a cascading signature change to CreateTurnCore's many
	// callers) -- this is a THIRD, independent instance, exactly like
	// sandboxStore/sessionStore above are each already constructed once
	// per real top-level consumer rather than shared across unrelated
	// ones.
	workflowStore := postgres.NewWorkflowStore(pool)
	// githubActorLinkNoticeStore (batch fix/deny-unlinked-github-actors)
	// backs the GitHub webhook ingress's own anti-spam dedupe for the
	// "please sign in" reply posted to an unlinked commenter's denied
	// mention (see githubingress.Config.LinkNotices' own doc comment,
	// below).
	githubActorLinkNoticeStore := postgres.NewGitHubActorLinkNoticeStore(pool)

	// intentClassifierSvc is Step 36's own real classifier (§8.3, §18):
	// llm.New resolves cfg.IntentClassifierProvider against this
	// codebase's own small provider registry (internal/adapters/outbound/
	// llm's own doc.go) -- Anthropic is the one real adapter this Step
	// ships; an unrecognized provider name never fails process boot (see
	// that package's own registry.go), it simply makes every future
	// Classify call fall back with FallbackReasonUnsupportedProvider,
	// exactly like a misconfigured model string would. promptTemplateStore
	// backs the DB-editable prompt templates (§18.6); intentClassifierSvc
	// composes both plus sessionStore's own write-once persistence
	// (UpdateIntentDecisionIfNull) and cfg.IntentClassifierActiveSurfaces'
	// own permanent shadow-vs-active gate (§18.5).
	promptTemplateStore := postgres.NewPromptTemplateStore(pool)
	intentLLM := llm.New(llm.Config{
		Provider: cfg.IntentClassifierProvider,
		APIKey:   cfg.AnthropicAPIKey,
		Timeout:  cfg.Timeouts.IntentClassifierLLMTimeout,
	})
	intentClassifierSvc := intentclassifier.New(
		intentLLM,
		cfg.IntentClassifierProvider,
		cfg.IntentClassifierModel,
		promptTemplateStore,
		sessionStore,
		cfg.IntentClassifierActiveSurfaces,
	)

	// findingRelocationResolver is Step 63's own §22.1.1 relocation
	// fallback (internal/app/findingposition) -- reuses intentLLM (the
	// SAME already-constructed ports.LLM client/config intentClassifierSvc
	// above uses, never a second, independently-configured adapter) and
	// the SAME cfg.IntentClassifierProvider/Model values, per that
	// package's own doc comment: both this call and intent classification
	// are small, structured, non-agentic utility calls, the identical
	// KIND of call, so there is no reason to introduce a second
	// provider/model configuration surface for it.
	findingRelocationResolver := findingposition.New(intentLLM, cfg.IntentClassifierProvider, cfg.IntentClassifierModel)

	// recon is Step 25's ("reconciler + GC", §5.3) process-wide
	// provider-reconciliation/orphan-GC loop, run below via the errgroup
	// exactly once per process -- constructed from the SAME sandboxStore/
	// sandboxProvider/cfg.Timeouts already built above for everything
	// else, mirroring how registry/commander were threaded through rather
	// than built twice. See internal/app/reconciler's own doc.go for what
	// it does and why.
	recon, err := reconciler.NewReconciler(sandboxStore, sandboxProvider, cfg.Timeouts)
	if err != nil {
		return fmt.Errorf("construct reconciler: %w", err)
	}

	// builder is Step 26's ("image builds", §8.5-note/§10-P2) own
	// process-wide background image-build loop, run below via the
	// errgroup exactly once per process -- constructed from the SAME
	// sandboxProvider/cfg.Timeouts already built above, mirroring recon's
	// own construction immediately above exactly. See internal/app/
	// imagebuild's own doc.go for what it does and why. Step 42 ("warm
	// boot: refresh pump + hook policy", §19.2) adds the trailing
	// sourceControl/cfg.GitHubImageBuildToken pair: the SAME *githubapi.
	// Adapter instance already constructed above (for CreatePR/
	// ResolveBranchSHA/ResolveContractsFingerprint) plus the new
	// platform-level credential (deliberately DISTINCT from
	// cfg.GitHubBotToken -- see platform.Config.GitHubImageBuildToken's own
	// doc comment), both consulted only by the freshness pump's own
	// per-repo tip-SHA resolution and by claim-time SHA resolution for a
	// repo-bearing build (attempt) -- never by anything on the spawn path
	// itself.
	builder, err := imagebuild.NewBuilder(imageBuildStore, pool, sandboxProvider, cfg.Timeouts,
		sourceControl, cfg.GitHubImageBuildToken)
	if err != nil {
		return fmt.Errorf("construct image builder: %w", err)
	}

	// automationEngine is Step 51's ("automations: engine", §3.5) own
	// process-wide background automation engine, run below via the
	// errgroup exactly once per process -- constructed from the SAME
	// sessionStore/turnStore/environmentStore/auditLogStore/registry/pool
	// already built above for everything else, mirroring builder's/recon's
	// own construction immediately above exactly. See internal/app/
	// automation's own doc.go for what it does and why. registry is the
	// SAME *sessionactor.Registry every other CreateSessionOnTx caller in
	// this file already threads through (e.g. the GitHub/Slack/Linear
	// ingress Deps below) -- a run's own session dispatch reuses that
	// identical TriggerDispatch path, never a second one.
	automationEngine := automation.NewEngine(
		automationStore, automationInvocationStore, automationRunStore,
		sessionStore, turnStore, environmentStore, auditLogStore,
		pool, registry, cfg.Timeouts, cfg.EpistemicCheckDefault,
	)

	// The 3 stores backing Step 20's ("auth v1", §13.1/§13.4) own GitHub
	// OAuth login, backend-issued session cookies, and route middleware --
	// see internal/adapters/inbound/auth's own doc.go for the full writeup.
	userStore := postgres.NewUserStore(pool)
	identityStore := postgres.NewIdentityStore(pool)
	userSessionStore := postgres.NewUserSessionStore(pool)

	// identityLinkPromptStore/appIdentityLinkDeps are Step 39's own
	// ("identities + full RBAC", §13.2) auto-linking wiring -- threaded
	// into every Slack/Linear ingress Deps struct below (so a first event
	// from an unknown provider identity auto-links or creates a magic-link
	// prompt instead of always falling back to bot attribution) AND into
	// the magic-link consume route's own Deps further down. One shared
	// identitylink.Deps value, built once here from the SAME userStore/
	// identityStore/auditLogStore every other Step 20/39 caller already
	// uses -- never a second, independently-constructed copy of any of
	// them.
	identityLinkPromptStore := postgres.NewIdentityLinkPromptStore(pool)
	appIdentityLinkDeps := identitylink.Deps{
		Pool:          pool,
		Users:         userStore,
		Identities:    identityStore,
		LinkPrompts:   identityLinkPromptStore,
		AuditLog:      auditLogStore,
		PublicBaseURL: cfg.PublicBaseURL,
		PromptTTL:     cfg.Timeouts.IdentityLinkPromptTTL,
	}

	oauthConfig := auth.NewGitHubOAuthConfig(*cfg)
	allowlist := auth.AllowlistConfig{
		EmailDomains: cfg.AllowedEmailDomains,
		GitHubOrgs:   cfg.AllowedGitHubOrgs,
		Emails:       cfg.AllowedEmails,
	}
	// A cookie marked Secure is simply never sent over plain http://,
	// which is exactly what a local dev loop needs relaxed — everywhere
	// else (staging, production) it must always be true (§13.1, see
	// internal/platform/authcookie.go's own doc comment).
	secureCookies := cfg.Stage != platform.StageDevelopment

	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Use(platform.CorrelationIDMiddleware)
	// Deliberately NOT chi's own middleware.Logger/RequestID: PR-03 already
	// built our own correlation-id + platform.Logger(ctx) convention above,
	// and stacking chi's competing convention on top would give every
	// request two different request-identity mechanisms.
	router.Get("/health", healthHandler(pool, cfg.Timeouts))
	router.Get("/sessions/{sessionID}/ws", wshub.NewHandler(
		wshub.NewSandboxHandler(registry, sandboxStore, commander, cfg.Timeouts),
		wshub.NewClientHandler(registry, sessionStore, turnStore, sandboxStore, eventStore, artifactStore, wsTokenStore, hub, cfg.Timeouts),
	))

	// scm-credentials (Step 21, "e2e happy path", design decision 8):
	// deliberately mounted OUTSIDE /api/sessions and outside auth.
	// Middleware entirely -- a sandbox-bearer-token-authenticated
	// endpoint, not a browser-facing one (see that handler's own doc
	// comment in internal/adapters/inbound/httpapi/scmcredentials.go).
	// githubPRSessionStore/cfg.GitHubBotToken (Step 47 audit remediation)
	// are the SAME instances review/verdict below already uses, so a
	// review session mints the SAME bot credential either way, never the
	// creator's own personal OAuth token.
	router.Post("/sessions/{sessionID}/scm-credentials",
		httpapi.ScmCredentials(sessionStore, sandboxStore, identityStore, userStore, githubPRSessionStore, cfg.GitHubBotToken, cfg.TokenEncryptionKey, cfg.Timeouts))

	// provider-credentials (Step 53, "provider credential injection",
	// §25.1/§25.3): deliberately mounted OUTSIDE /api/sessions and outside
	// auth.Middleware entirely, mirroring scm-credentials immediately above
	// exactly (see httpapi/providercredentialsdelivery.go's own doc
	// comment) -- another sandbox-bearer-token-authenticated route, not a
	// browser-facing one.
	router.Post("/sessions/{sessionID}/provider-credentials",
		httpapi.ProviderCredentialsDelivery(sessionStore, sandboxStore, providerCredentialStore, cfg.TokenEncryptionKey))

	// snapshot-mint (Step 22, "snapshots & restore", design decision 2):
	// deliberately mounted OUTSIDE /api/sessions and outside auth.
	// Middleware entirely, mirroring scm-credentials immediately above
	// exactly (see that handler's own doc comment, and
	// httpapi/snapshotmint.go's own) -- another sandbox-bearer-token-
	// authenticated route, not a browser-facing one.
	router.Post("/sessions/{sessionID}/snapshot",
		httpapi.SnapshotMint(sandboxStore, sandboxProvider))

	// review/verdict (Step 47, "server-side verdict", §8.2/§5.2): the
	// verdict-posting TOOL -- deliberately mounted OUTSIDE /api/sessions
	// and outside auth.Middleware entirely, mirroring scm-credentials/
	// snapshot-mint immediately above exactly (see httpapi/reviewverdict.go's
	// own doc comment for the full "why an HTTP endpoint, sandbox-bearer
	// authenticated, not a browser route" reasoning). repoSettingsStore/
	// outboxStore are the SAME instances every other caller above already
	// uses; cfg.GitHubBotHandle is the SAME handle GitHub ingress's own
	// mention detector already matches against (githubingress.Config.
	// BotHandle above) -- the rendered re-run guidance (internal/domain/
	// reviewpost.RerunGuidance) is built to be recognized by that SAME
	// regex (§5.2).
	router.Post("/sessions/{sessionID}/review/verdict",
		httpapi.PostReviewVerdict(pool, sandboxStore, sessionStore, githubPRSessionStore, repoSettingsStore, reviewFindingStore, sentinelFixStore, outboxStore, reviewVerdictStore, turnStore, cfg.GitHubBotHandle, cfg.GitHubBotToken, sourceControl, findingRelocationResolver, cfg.Timeouts))

	// workflow/step-outcome (Step 55, "workflow execution engine", §25.6):
	// the GENERIC step-outcome-posting tool -- deliberately mounted
	// OUTSIDE /api/sessions and outside auth.Middleware entirely,
	// mirroring review/verdict immediately above exactly (see
	// httpapi/workflowstepoutcome.go's own doc comment). Unlike
	// review/verdict, no §25.8 built-in workflow's own prompt is wired to
	// actually call this in this Step -- it exists as real, callable
	// infrastructure for whichever future non-built-in workflow step
	// needs it (this Step's own generic engine + tool, never a specific
	// audit workflow).
	router.Post("/sessions/{sessionID}/workflow/step-outcome",
		httpapi.PostWorkflowStepOutcome(sandboxStore, workflowStore))

	// turn/epistemic-outcome (Step 61, "builder epistemic pre-action
	// check", §20.2): the devil's-advocate preamble's own required
	// structured-signal-reporting tool -- deliberately mounted OUTSIDE
	// /api/sessions and outside auth.Middleware entirely, mirroring
	// review/verdict and workflow/step-outcome immediately above exactly
	// (see httpapi/epistemicoutcome.go's own doc comment).
	router.Post("/sessions/{sessionID}/turn/epistemic-outcome",
		httpapi.PostEpistemicOutcome(sandboxStore, turnStore))

	// uploads mint/confirm/content (Step 58, "uploads, blob storage & the
	// in-sandbox download_file tool", §28.4/§28.5): deliberately mounted
	// OUTSIDE /api/sessions and outside auth.Middleware entirely, mirroring
	// scm-credentials/snapshot-mint/review-verdict/workflow-step-outcome
	// immediately above exactly -- the download_file tool's and the
	// agent-produced-upload direction's own sandbox-bearer endpoints.
	// blobStore/objectStorage may be nil (cfg.ObjectStorage absent, feature
	// off) -- each handler's own core returns a structured "uploads not
	// configured" error in that case rather than failing to boot.
	router.Post("/sessions/{sessionID}/uploads",
		httpapi.MintUpload(sandboxStore, artifactStore, blobStore, cfg.ObjectStorage, cfg.Timeouts))
	router.Post("/sessions/{sessionID}/uploads/{uploadID}/complete",
		httpapi.ConfirmUpload(sandboxStore, pool, artifactStore, eventStore, outboxStore, hub, blobStore, cfg.ObjectStorage))
	router.Get("/sessions/{sessionID}/uploads/{uploadID}/content",
		httpapi.UploadContent(sandboxStore, artifactStore, blobStore, cfg.ObjectStorage, cfg.Timeouts))

	// Slack ingress (Step 33, §8.10): deliberately mounted OUTSIDE
	// /api/sessions and outside auth.Middleware entirely -- Slack itself
	// is the caller here, authenticated via its own request-signing
	// scheme (X-Slack-Signature/X-Slack-Request-Timestamp), not a
	// narvi_auth_session cookie. See internal/adapters/inbound/slack's
	// own doc.go for the full request-handling writeup.
	router.Post("/webhooks/slack", slack.NewHandler(slack.Deps{
		Pool:         pool,
		Sessions:     sessionStore,
		Turns:        turnStore,
		Environments: environmentStore,
		Registry:     registry,
		Deliveries:   webhookDeliveryStore,
		Threads:      slackThreadSessionStore,
		AuditLog:     auditLogStore,
		// Plans (Step 37/38 follow-up fix, §8.1): the SAME planStore
		// instance every other caller above already uses -- handleEvent's
		// own awaiting-plan gate/verdict/revise-prefix check (handler.go)
		// needs this to find a mapped session's own awaiting_approval plan,
		// if any, exactly like Linear's identical Deps.Plans wiring below.
		Plans: planStore,
		// Outbox/LinearAgentSessions (this batch's own addition, "honour a
		// typed plan verdict"): handlePlanVerdict's own httpapi.DecidePlan
		// call (handler.go) needs these exactly like the interactivity
		// route's own identical Outbox/LinearAgentSessions wiring
		// immediately below -- the SAME outboxStore/linearAgentSessionStore
		// instances every other caller of DecidePlan already uses, never a
		// second, independently-constructed copy.
		Outbox:              outboxStore,
		LinearAgentSessions: linearAgentSessionStore,
		// Participants (this Step's own SECOND fix-pass addition,
		// "identities + full RBAC", §13.2/§13.3): the SAME participantStore
		// instance every other caller (the interactivity route immediately
		// below, Linear's own Deps) already uses, never a second,
		// independently-constructed copy.
		Participants:     participantStore,
		IntentClassifier: intentClassifierSvc,
		// EpistemicCheckDefault (Step 61, §20.4): the SAME platform.Config
		// value every other CreateTurnCore-reaching caller below also
		// receives.
		EpistemicCheckDefault: cfg.EpistemicCheckDefault,
		SigningSecret:         cfg.SlackSigningSecret,
		BotToken:              cfg.SlackBotToken,
		DefaultRepoName:       cfg.SlackDefaultRepoName,
		DefaultRepoURL:        cfg.SlackDefaultRepoURL,
		TimestampWindow:       cfg.Timeouts.WebhookTimestampFreshnessWindow,
		SlackAPIBaseURL:       slackAPIBaseURL,
		AckTimeout:            cfg.Timeouts.SlackAckTimeout,
		// IdentityLink/SlackClient/Timeouts (Step 39, "identities + full
		// RBAC", §13.2): SlackClient reuses the SAME slackNotifier
		// instance already constructed above (for the outbox delivery
		// worker and the interactivity route immediately below), never a
		// third, independently-constructed client.
		IdentityLink: appIdentityLinkDeps,
		SlackClient:  slackNotifier,
		Timeouts:     cfg.Timeouts,
	}))

	// Slack INTERACTIVITY ingress (Step 38, "plan mode, cross-channel",
	// §8.1/§13.3) -- a SEPARATE route from the Events API ingress
	// immediately above (structurally different payload shape; see
	// internal/adapters/inbound/slack/interactive.go's own top doc comment
	// for the real, external "Interactivity & Shortcuts" App-config step
	// this route requires before Slack ever sends it anything). Mounted
	// OUTSIDE auth.Middleware entirely, mirroring the Events API route
	// exactly -- authenticated via Slack's own request signature, not a
	// cookie.
	router.Post("/webhooks/slack/interactive", slack.NewInteractivityHandler(slack.InteractiveDeps{
		Pool:                pool,
		Sessions:            sessionStore,
		Turns:               turnStore,
		Plans:               planStore,
		Outbox:              outboxStore,
		LinearAgentSessions: linearAgentSessionStore,
		Registry:            registry,
		SlackClient:         slackNotifier,
		AuditLog:            auditLogStore,
		IdentityLink:        appIdentityLinkDeps,
		// Participants (Step 39, "identities + full RBAC", §13.2/§13.3):
		// the SAME participantStore instance Step 37's own REST plan
		// approve/reject endpoints already use (constructed once, above),
		// never a second, independently-constructed copy.
		Participants: participantStore,
		// EpistemicCheckDefault (Step 61, §20.4): see slack.Deps' own
		// identical field above -- this route's own CreateTurnCore call
		// always names planMode=true, so this value never actually
		// changes behavior here today (§20.3), but is threaded for the
		// same "correct by construction" reason documented on
		// InteractiveDeps.EpistemicCheckDefault itself.
		EpistemicCheckDefault: cfg.EpistemicCheckDefault,
		SigningSecret:         cfg.SlackSigningSecret,
		Timeouts:              cfg.Timeouts,
	}))

	// GitHub webhook ingress (Step 32, "GitHub ingress", §8.2): mounted
	// OUTSIDE auth.Middleware entirely, mirroring scm-credentials/
	// snapshot-mint immediately above exactly -- this route authenticates
	// via GitHub's own HMAC webhook signature, not a browser cookie. See
	// internal/adapters/inbound/github's own doc.go for the full
	// verify -> dedupe-claim -> parse -> detect -> per-PR-coalesce
	// sequencing.
	router.Post("/webhooks/github", githubingress.NewHandler(
		&githubingress.SessionCoalescer{
			Pool:             pool,
			PRSessions:       githubPRSessionStore,
			Sessions:         sessionStore,
			Turns:            turnStore,
			Environments:     environmentStore,
			Registry:         registry,
			IntentClassifier: intentClassifierSvc,
			AuditLog:         auditLogStore,
			// Identities/Users/Participants (batch fix/audit-github-actor-
			// rbac): the SAME identityStore/userStore/participantStore
			// instances every other caller above already uses (Step 20's
			// own auth wiring, Step 37's own plan approve/reject
			// endpoints), never a second, independently-constructed copy.
			Identities:   identityStore,
			Users:        userStore,
			Participants: participantStore,
			// Plans (Step 37/38 follow-up fix, §8.1): the SAME planStore
			// instance every other caller above already uses -- threaded
			// through to CreateTurnForBot's own awaiting-plan gate.
			Plans: planStore,
			// F7 correction (adversarial review, Step 61): SessionCoalescer
			// no longer has an EpistemicCheckDefault field -- both of its
			// own CreateSessionOnTx/CreateTurnForBot call sites now
			// hardcode false instead (coalesce.go's own doc comment on the
			// removed field explains the full "why": every session/turn
			// this package creates or joins is a PR review session, never a
			// build turn, so the platform's real epistemic-check default
			// must never reach it).
		},
		webhookDeliveryStore,
		githubingress.Config{
			WebhookSecret: cfg.GitHubWebhookSecret,
			BotHandle:     cfg.GitHubBotHandle,
			// ReReviewLabel/DiffFetcher (Step 46, "review sessions", §8.2):
			// the manual re-trigger-via-label lane's own configured label
			// name, and the SAME *githubapi.Adapter instance already
			// constructed above (sourceControl) as PullRequests/Comments --
			// never a second, independently-constructed copy -- now ALSO
			// wired as this Step's own diff/stack pre-fetch source.
			ReReviewLabel: cfg.GitHubReReviewLabel,
			DiffFetcher:   sourceControl,
			// ReviewFindings (Step 48, §22.1): the SAME reviewFindingStore
			// instance every other caller above already uses.
			ReviewFindings: reviewFindingStore,
			// FalsePositivePatterns (Step 63, §22.3): the SAME
			// falsePositivePatternStore instance every other caller
			// (RetriggerReview, the capture/lifecycle endpoints below)
			// already uses.
			FalsePositivePatterns: falsePositivePatternStore,
			// FalsePositivePatternCapture (Step 63, §22.2): the SAME
			// falsePositivePatternStore instance, satisfying this
			// structurally different (write) interface.
			FalsePositivePatternCapture: falsePositivePatternStore,
			// BotToken/PullRequests (batch fix/audit-github-pr-payload-
			// correctness, H5 audit fix): resolve an issue_comment
			// mention's TRUE head branch/repo via one authenticated
			// GET /repos/{owner}/{repo}/pulls/{number} call. sourceControl
			// is the SAME *githubapi.Adapter instance already constructed
			// above for CreatePR/ResolveBranchSHA/ResolveContractsFingerprint
			// -- never a second, independently-constructed copy -- and
			// cfg.GitHubBotToken is the SAME bot credential githubNotifier
			// (below) already authenticates its own PostIssueComment calls
			// with, never a per-commenter credential.
			BotToken:     cfg.GitHubBotToken,
			PullRequests: sourceControl,
			// Comments (Step 37/38 follow-up fix, Finding 1; also posts
			// batch fix/deny-unlinked-github-actors' own "please sign in"
			// reply): the SAME *githubapi.Adapter instance as
			// PullRequests/sourceControl above -- never a second,
			// independently-constructed copy.
			Comments: sourceControl,
			Timeouts: cfg.Timeouts,
			// PublicBaseURL/LinkNotices (batch fix/deny-unlinked-github-
			// actors): PublicBaseURL is the SAME base identitylink.
			// BuildMagicLinkURL already uses (appIdentityLinkDeps above),
			// never a second, independently-configured base. LinkNotices
			// is a freshly constructed store over the SAME pool every
			// other store here already shares -- see
			// githubActorLinkNoticeStore's own construction below.
			PublicBaseURL: cfg.PublicBaseURL,
			LinkNotices:   githubActorLinkNoticeStore,
			// SentinelFixes/RepoSettings/AuditLog (Step 48, §17.4/§17.5):
			// the SAME instances every other caller above already uses.
			SentinelFixes: sentinelFixStore,
			RepoSettings:  repoSettingsStore,
			AuditLog:      auditLogStore,
			// PendingChecks/ReleaseLabel/ReleaseBranchPattern (Step 50,
			// "release PR review", §15; PendingChecks itself is
			// blocking-finding fix #1): releaseManifestPendingStore is the
			// SAME instance constructed above, alongside outboxStore --
			// the webhook handler only ever writes ONE cheap row here now;
			// the actual check (ListMergedBetween, sourceControl, and
			// outboxStore's own release_manifest row) runs LATER, on
			// releaseManifestWorker's own background loop (started below,
			// alongside every other background loop), never inline on
			// this request's own context.
			PendingChecks:        releaseManifestPendingStore,
			ReleaseLabel:         cfg.GitHubReleaseLabel,
			ReleaseBranchPattern: cfg.GitHubReleaseBranchPattern,
			// Timers (Step 65, §24.1): the standalone timerStore instance
			// constructed above, backing this lane's own direct,
			// actor-bypassing review_retrigger_debounce timer arm.
			Timers: timerStore,
		},
	))

	// Auth routes (§13.1/§13.4, Step 20): how a session is obtained/
	// discarded in the first place, so — obviously — mounted OUTSIDE any
	// auth gate. See internal/adapters/inbound/auth's own doc.go for the
	// full routes/outcome-table writeup.
	router.Get("/auth/github/login", auth.NewLoginHandler(oauthConfig, cfg.Timeouts, secureCookies))
	router.Get("/auth/github/callback", auth.NewCallbackHandler(
		pool,
		oauthConfig,
		userStore,
		identityStore,
		auditLogStore,
		userSessionStore,
		allowlist,
		cfg.InitialAdminEmails,
		cfg.TokenEncryptionKey,
		cfg.Timeouts,
		secureCookies,
		githubAPIBaseURL,
	))
	router.Post("/auth/logout", auth.NewLogoutHandler(userSessionStore, secureCookies))

	// /auth/identity-link/{nonce}: the magic-link consume flow (Step 39,
	// "identities + full RBAC", §13.2 step 4's own "connect your account"
	// link) -- deliberately mounted OUTSIDE auth.Middleware entirely, like
	// the auth routes immediately above: this handler authenticates the
	// visitor ITSELF (auth.Authenticate), redirecting through the SAME
	// GitHub OAuth login flow above (with its own ?next= carrying them
	// back here) when they aren't signed in yet, rather than the bare 401
	// Middleware would give a not-yet-authenticated request. See internal/
	// adapters/inbound/identitylink's own doc.go for the complete design.
	router.Get("/auth/identity-link/{nonce}", identitylinkhttp.NewConsumeHandler(identitylinkhttp.Deps{
		UserSessions:    userSessionStore,
		Users:           userStore,
		AppIdentityLink: appIdentityLinkDeps,
	}))

	// /api/members, /api/audit-log (Step 39, "identities + full RBAC",
	// §13.2/§13.3): the backend-only members API -- list members (with
	// role, linked identities, pending-link state), an admin-only
	// role-change endpoint, admin manual link/unlink of an identity, and a
	// read endpoint over audit_log. Gated behind auth.Middleware (a "must
	// be logged in" gate) exactly like /api/sessions above; each handler
	// itself renders the REAL admin-only §13.3 verdict via
	// domain/authz.Authorize. The actual Settings -> Members UI is Phase 7
	// and out of scope here -- see httpapi/members.go's own doc comment.
	router.Route("/api/members", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.ListMembers(userStore, identityStore, identityLinkPromptStore))
		r.Patch("/{userID}/role", httpapi.UpdateMemberRole(pool, userStore, identityStore, auditLogStore))
		r.Post("/{userID}/identities", httpapi.LinkMemberIdentity(pool, userStore, identityStore, auditLogStore))
		r.Delete("/{userID}/identities/{identityID}", httpapi.UnlinkMemberIdentity(pool, identityStore, auditLogStore))
	})

	// /api/me/chatgpt-link (Step 59, "models: Codex via ChatGPT-account
	// OAuth", §29.3/§29.9): self-service link/status/unlink -- gated
	// behind auth.Middleware exactly like /api/members above; each
	// handler renders the real authz.ActionLinkChatGPTAccount verdict
	// (own-aware, satisfied unconditionally here since every one of these
	// three handlers only ever acts on the caller's OWN userID, never a
	// path parameter naming a different user -- see chatgptlink.go's own
	// doc comment for why there is no admin "/api/members/{userID}/
	// chatgpt-link" surface yet).
	router.Route("/api/me/chatgpt-link", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/", httpapi.StartChatGPTLink(chatGPTLinkDeps))
		r.Get("/", httpapi.GetChatGPTLinkStatus(chatGPTLinkDeps))
		r.Delete("/", httpapi.DeleteChatGPTLink(chatGPTLinkDeps))
	})

	// /api/models (Step 59, "models: Catalog", §8 item 8/§29/§25.2) --
	// mounted exactly like /api/members above: gated behind auth.
	// Middleware only, with the handler itself rendering the real
	// authz.ActionViewAnalytics verdict (everyone including viewer).
	router.Route("/api/models", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.GetModelCatalog())
	})

	// /api/admin/shadow-compare (Step 59, "shadow-comparison tooling for
	// review", §9.4/§18.5) -- mounted exactly like /api/members above:
	// gated behind auth.Middleware, with the handler itself rendering the
	// real authz.ActionViewShadowComparison verdict (admin/maintainer
	// only).
	router.Route("/api/admin/shadow-compare", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.GetShadowComparison(turnStore))
	})
	router.Route("/api/audit-log", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.ListAuditLog(auditLogStore))
	})

	// /api/decision-inbox (Step 60, "decision inbox: read model + API",
	// §16 -- Phase 5 half: read model + endpoints; the UI is Phase 7).
	// decisionInboxDeps bundles every Postgres store the read model
	// aggregates (internal/app/decisioninbox.Build's own doc comment: a
	// read model over plans/sessions/automations/outbox/review_findings/
	// sentinel_fixes/artifacts, all already constructed above for their
	// own existing purposes) plus decisionInboxSCMCache, the §16.2 short-
	// TTL cache wrapping the SAME sourceControl instance every other
	// GitHub-facing route already shares.
	decisionInboxSCMCache := decisioninbox.NewSCMCache(sourceControl, cfg.Timeouts)
	decisionInboxDeps := decisioninbox.Deps{
		Plans:              planStore,
		Sessions:           sessionStore,
		Participants:       participantStore,
		Automations:        automationStore,
		Outbox:             outboxStore,
		ReviewFindings:     reviewFindingStore,
		SentinelFixes:      sentinelFixStore,
		Artifacts:          artifactStore,
		Identities:         identityStore,
		SCMCache:           decisionInboxSCMCache,
		TokenEncryptionKey: cfg.TokenEncryptionKey,
		Timeouts:           cfg.Timeouts,
		ReviewVerdict:      reviewVerdictDeps,
	}

	// automergeWorker/digestPump (Step 62, §21.2 stage 2/§21.3): both
	// started below, alongside every other background loop, through the
	// SAME errgroup (§11: no naked goroutine). automergeWorker reuses
	// decisionInboxDeps in full (internal/app/decisioninbox.
	// RevalidateForAutoMerge, its own re-validation call, needs every
	// store that function already depends on) plus sourceControl/
	// cfg.GitHubBotToken -- the bot credential, since a background
	// worker has no clicking human's own token to reuse (see
	// automerge.Deps' own doc comment).
	automergeWorker := automerge.New(automerge.Deps{
		DecisionInbox: decisionInboxDeps,
		SourceControl: sourceControl,
		AuditLog:      auditLogStore,
		BotToken:      cfg.GitHubBotToken,
		Timeouts:      cfg.Timeouts,
	})
	digestPump := digest.New(digest.Deps{
		Channels:      digestChannelStore,
		SendState:     digestSendStateStore,
		Outbox:        outboxStore,
		ReviewVerdict: reviewVerdictDeps,
		Timeouts:      cfg.Timeouts,
	})

	router.Route("/api/decision-inbox", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.ListDecisionInbox(decisionInboxDeps))
		r.Post("/merge", httpapi.MergePullRequest(decisionInboxDeps, sourceControl, auditLogStore))
	})

	// /api/intent-templates, /api/intent-templates/preview (audit finding
	// M5, completeness): postgres.PromptTemplateStore's own Upsert method
	// (constructed above, alongside intentClassifierSvc) had ZERO callers
	// anywhere in this codebase until this batch -- these two routes are
	// its first ones. Gated behind auth.Middleware (a "must be logged in"
	// gate) exactly like /api/members above; each handler itself renders
	// the REAL admin-only §13.3 verdict via domain/authz.Authorize
	// (authz.ActionActivatePromptTemplate -- row 6's own "prompt-template
	// activation" action, itself likewise unused anywhere before this
	// batch). See httpapi/classifiertemplates.go's own doc comment for the
	// full design.
	router.Route("/api/intent-templates", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/preview", httpapi.PreviewIntentTemplate())
		r.Post("/", httpapi.UpsertIntentTemplate(pool, promptTemplateStore, auditLogStore))
	})

	// REST routes the UI needs (§6.3, Step 19's own plan row: "create/get/
	// events/artifacts", + ws-token named separately by §6.2), all gated
	// behind auth.Middleware as of Step 20 — see
	// internal/adapters/inbound/httpapi/doc.go's own updated writeup. This
	// is a "must be logged in" gate only: it does not apply to /health or
	// to GET /sessions/{sessionID}/ws above, which already has its OWN,
	// type-specific auth (the sandbox half's header-bearer-token handshake
	// and the client half's own post-upgrade ws-token subscribe message,
	// both Step 18/19's own precedent, untouched) — gating the WS UPGRADE
	// itself behind a cookie check would break the sandbox-agent's own
	// connection, which carries no cookie at all, only its own
	// Authorization header.
	router.Route("/api/sessions", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/", httpapi.CreateSession(pool, sessionStore, turnStore, environmentStore, auditLogStore, registry, intentClassifierSvc, cfg.EpistemicCheckDefault))
		r.Get("/{sessionID}", httpapi.GetSession(sessionStore))
		r.Get("/{sessionID}/events", httpapi.ListEvents(sessionStore, eventStore))
		r.Get("/{sessionID}/artifacts", httpapi.ListArtifacts(sessionStore, artifactStore))
		// uploads (Step 58, "uploads, blob storage & the in-sandbox
		// download_file tool", §28.4/§28.5): the browser twins of the
		// sandbox-bearer mint/confirm/content endpoints registered outside
		// /api above. mint/confirm are gated by authz.ActionUploadToSession
		// (the same §13.3 row as prompting, checked inside each handler);
		// content/download is gated by session visibility only (a download
		// is a read, so a read-only viewer may) -- mirrors ListArtifacts/
		// ListEvents immediately above, no separate Authorize call.
		r.Post("/{sessionID}/uploads", httpapi.MintUploadAPI(sessionStore, participantStore, artifactStore, blobStore, cfg.ObjectStorage, cfg.Timeouts))
		r.Post("/{sessionID}/uploads/{uploadID}/complete", httpapi.ConfirmUploadAPI(sessionStore, participantStore, pool, artifactStore, eventStore, outboxStore, sandboxStore, hub, blobStore, cfg.ObjectStorage))
		r.Get("/{sessionID}/uploads/{uploadID}/content", httpapi.UploadContentAPI(sessionStore, artifactStore, blobStore, cfg.ObjectStorage, cfg.Timeouts))
		r.Post("/{sessionID}/ws-token", httpapi.MintWSToken(sessionStore, wsTokenStore, cfg.Timeouts))
		// turns (Step 28, "turn recovery", §8.7): the relaunch-and-resume
		// REST API -- enqueues a new turn on an existing session, 409 if
		// one is already in flight. See httpapi/turn.go's own doc comment.
		r.Post("/{sessionID}/turns", httpapi.CreateTurn(pool, sessionStore, turnStore, planStore, participantStore, auditLogStore, registry, intentClassifierSvc, cfg.ObjectStorage, cfg.EpistemicCheckDefault))
		// plans (Step 37, "plan mode, web", §8.1/§12.2 item 3): the
		// approve/reject HITL actions -- see httpapi/planapprove.go's own
		// doc comment for the full sequencing. outboxStore/
		// linearAgentSessionStore (Step 38, "plan mode, cross-channel") feed
		// DecidePlanOnTx's own cross-channel-notify step (decideplan.go).
		r.Post("/{sessionID}/plans/{planId}/approve", httpapi.ApprovePlan(pool, sessionStore, turnStore, planStore, participantStore, outboxStore, linearAgentSessionStore, auditLogStore, registry, cfg.EpistemicCheckDefault))
		r.Post("/{sessionID}/plans/{planId}/reject", httpapi.RejectPlan(pool, sessionStore, turnStore, planStore, participantStore, outboxStore, linearAgentSessionStore, auditLogStore, cfg.EpistemicCheckDefault))
		// Audit-fix batch (completeness/discoverability, M3): the read half
		// plans/{planId}/approve|reject above was always missing -- a web
		// client had no way to ever discover a planId to approve. See
		// httpapi/plans.go's own doc comment.
		r.Get("/{sessionID}/plans", httpapi.ListPlans(sessionStore, planStore))
		// review/retrigger (Step 46, "review sessions", §8.2's own manual
		// re-trigger-via-BUTTON surface, §12.2 item 2's "re-run action") --
		// see httpapi/reviewretrigger.go's own doc comment. githubPRSessionStore/
		// sourceControl/cfg.GitHubBotToken are the SAME instances the
		// GitHub webhook ingress wiring above already constructs, never a
		// second, independently-constructed copy.
		r.Post("/{sessionID}/review/retrigger", httpapi.RetriggerReview(pool, sessionStore, turnStore, planStore, auditLogStore, registry, githubPRSessionStore, sourceControl, reviewFindingStore, falsePositivePatternStore, cfg.GitHubBotToken, cfg.Timeouts))
		// review/findings/{identityHash}/rebut + apply-suggestion (Step 48,
		// "sentinels + suggestions", §12.2 item 2/§22.1) -- maintainer+
		// only (authz.ActionEditReviewVerdict, checked inside each
		// handler). identityStore/sourceControl/cfg.TokenEncryptionKey are
		// the SAME instances every other caller above already uses --
		// ApplySuggestion decrypts the ACTING (authenticated) maintainer's
		// own GitHub token, never the session creator's.
		r.Post("/{sessionID}/review/findings/{identityHash}/rebut", httpapi.RebutReviewFinding(sessionStore, githubPRSessionStore, reviewFindingStore, auditLogStore))
		r.Post("/{sessionID}/review/findings/{identityHash}/apply-suggestion", httpapi.ApplySuggestion(sessionStore, githubPRSessionStore, reviewFindingStore, identityStore, sourceControl, cfg.TokenEncryptionKey, cfg.Timeouts))
	})

	// /api/workflow-runs/{runId}/steps/{stepRunId}/decide (Step 56, "workflow
	// HITL gate + circuit breaker", §25.9/§25.10/§25.11): the HITL
	// approve/reject/revise verdict endpoint -- see httpapi/decideworkflowstep.go's
	// own doc comment for the full sequencing. slackThreadSessionStore/
	// linearAgentSessionStore/githubPRSessionStore/outboxStore are the SAME
	// instances every other caller above already uses -- notification
	// destination resolution reuses those exact reverse-lookup stores,
	// never a second, independently-constructed copy.
	router.Route("/api/workflow-runs", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/{runId}/steps/{stepRunId}/decide", httpapi.DecideWorkflowStep(pool, sessionStore, turnStore, participantStore, workflowStore, slackThreadSessionStore, linearAgentSessionStore, githubPRSessionStore, outboxStore, registry, cfg.EpistemicCheckDefault))
	})

	// /api/repos/{owner}/{repo}/settings (Step 47, "server-side verdict",
	// §8.2/§21.2): admin-only read/write of a repo's own blockOnHighRisk
	// policy flag -- see httpapi/reposettings.go's own doc comment. Mounted
	// behind auth.Middleware like every other browser-facing REST route in
	// this package (unlike review/verdict below, this is an admin
	// configuring a setting, not the sandbox agent calling a tool).
	router.Route("/api/repos/{owner}/{repo}/settings", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.GetRepoSettings(repoSettingsStore, reviewVerdictDeps))
		r.Put("/", httpapi.PutRepoSettings(repoSettingsStore))
	})

	// /api/repos/{owner}/{repo}/false-positive-patterns (Step 63, "review:
	// learned false-positive patterns", §22.4): the audit-view/retire
	// lifecycle surface -- see httpapi/falsepositivepatterns.go's own doc
	// comment. Capture itself (§22.2) has no REST route at all; it is the
	// GitHub webhook's own dispatch-before-router `false positive:
	// <reason>` command instead. Gated by authz.
	// ActionManageFalsePositivePatterns (maintainer+, §13.3 row 5) and
	// mounted behind auth.Middleware, mirroring every other browser-facing
	// REST route in this package.
	router.Route("/api/repos/{owner}/{repo}/false-positive-patterns", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.ListFalsePositivePatterns(falsePositivePatternStore))
		r.Post("/{patternID}/retire", httpapi.RetireFalsePositivePattern(falsePositivePatternStore, auditLogStore))
	})

	// /api/repos/{owner}/{repo}/auto-approval-settings,
	// /api/repos/{owner}/{repo}/auto-merge (Step 62, §21.2): TWO further,
	// separately-gated routes -- see httpapi/reposettings.go's own
	// PutAutoApprovalSettings/PutAutoMergeToggle doc comments for why
	// these are not folded into PUT /settings above (a maintainer
	// authorized only for the auto-approval-config row, §13.3 row 5,
	// must never be forced through that endpoint's own admin-only gates,
	// row 6, just to reach it).
	router.Route("/api/repos/{owner}/{repo}/auto-approval-settings", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Put("/", httpapi.PutAutoApprovalSettings(repoSettingsStore, reviewVerdictDeps))
	})
	router.Route("/api/repos/{owner}/{repo}/auto-merge", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Put("/", httpapi.PutAutoMergeToggle(repoSettingsStore, reviewVerdictDeps))
	})

	// /api/repos/{owner}/{repo}/auto-retrigger-review (Step 65, §24.5): a
	// further, separately-gated route mirroring auto-merge above -- see
	// httpapi/reposettings.go's own PutAutoRetriggerReviewToggle doc
	// comment.
	router.Route("/api/repos/{owner}/{repo}/auto-retrigger-review", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Put("/", httpapi.PutAutoRetriggerReviewToggle(repoSettingsStore))
	})

	// /api/repos/{owner}/{repo}/description-autofix (Step 67, §26.2): a
	// further, separately-gated route mirroring auto-retrigger-review
	// above -- see httpapi/reposettings.go's own
	// PutDescriptionAutofixToggle doc comment.
	router.Route("/api/repos/{owner}/{repo}/description-autofix", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Put("/", httpapi.PutDescriptionAutofixToggle(repoSettingsStore))
	})

	// /api/repos/{owner}/{repo}/review-analytics (Step 62, §21.1):
	// read-only GET over the three analytics rollups (timeseries,
	// top-risk-driver breakdown, "Review finding outcomes" KPI) -- see
	// httpapi/reviewanalytics.go's own doc comment. Gated by the existing
	// authz.ActionViewAnalytics (§13.3 row 1: every role, including
	// viewer), unlike every §21.2 write-side route above.
	router.Route("/api/repos/{owner}/{repo}/review-analytics", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.GetReviewAnalytics(reviewVerdictDeps))
	})

	// /api/repos/{owner}/{repo}/provider-credentials,
	// /api/environments/{environmentID}/provider-credentials,
	// /api/provider-credentials (Step 53, "provider credential injection",
	// §25.1/§25.3): the 3 scope-partitioned CRUD route groups over
	// provider_credentials -- see httpapi/providercredentials.go's own doc
	// comment for the full route table and RBAC-per-scope rationale. Each
	// handler renders its own §13.3 verdict via domain/authz.Authorize
	// (ActionManageRepoSecrets/ActionManageEnvSecrets/
	// ActionManageGlobalSecrets respectively) -- mounted behind
	// auth.Middleware like every other browser-facing REST route in this
	// package (unlike provider-credentials' OWN sandbox-facing sibling
	// above, this is an admin/maintainer configuring a secret, not the
	// sandbox agent fetching one).
	router.Route("/api/repos/{owner}/{repo}/provider-credentials", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/", httpapi.CreateRepoProviderCredential(providerCredentialStore, cfg.TokenEncryptionKey))
		r.Get("/", httpapi.ListRepoProviderCredentials(providerCredentialStore))
		r.Put("/{credentialID}", httpapi.UpdateRepoProviderCredentialValue(providerCredentialStore, cfg.TokenEncryptionKey))
		r.Delete("/{credentialID}", httpapi.DeleteRepoProviderCredential(providerCredentialStore))
	})
	router.Route("/api/environments/{environmentID}/provider-credentials", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/", httpapi.CreateEnvironmentProviderCredential(providerCredentialStore, cfg.TokenEncryptionKey))
		r.Get("/", httpapi.ListEnvironmentProviderCredentials(providerCredentialStore))
		r.Put("/{credentialID}", httpapi.UpdateEnvironmentProviderCredentialValue(providerCredentialStore, cfg.TokenEncryptionKey))
		r.Delete("/{credentialID}", httpapi.DeleteEnvironmentProviderCredential(providerCredentialStore))
	})
	router.Route("/api/provider-credentials", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/", httpapi.CreateGlobalProviderCredential(providerCredentialStore, cfg.TokenEncryptionKey))
		r.Get("/", httpapi.ListGlobalProviderCredentials(providerCredentialStore))
		r.Put("/{credentialID}", httpapi.UpdateGlobalProviderCredentialValue(providerCredentialStore, cfg.TokenEncryptionKey))
		r.Delete("/{credentialID}", httpapi.DeleteGlobalProviderCredential(providerCredentialStore))
	})

	// /api/automations (Step 52, "automations: triggers & extras", §8.4):
	// the CRUD surface Step 51 ("automations: engine") never built --
	// automationStore is the SAME instance automationEngine (constructed
	// above) already uses, never a second, independently-constructed copy.
	// Create/Pause/Resume/RotateAutomationWebhookToken/
	// RevokeAutomationWebhookToken are further gated inside each handler
	// itself via domain/authz.Authorize(actor, authz.ActionManageAutomations,
	// ...) (admin/maintainer only); Get/List carry no further RBAC beyond
	// "must be logged in" -- see httpapi/automations.go's own doc comment
	// for why. The webhook-token rotate/revoke pair (review fix: "webhook
	// token has no rotation/revocation/expiry") is mounted here, inside
	// this SAME already-authenticated block, rather than as a separate
	// top-level route -- it manages a sub-resource of an existing
	// automation, exactly like pause/resume above.
	router.Route("/api/automations", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Post("/", httpapi.CreateAutomation(automationStore))
		r.Get("/", httpapi.ListAutomations(automationStore))
		r.Get("/{automationID}", httpapi.GetAutomation(automationStore))
		r.Post("/{automationID}/pause", httpapi.PauseAutomation(automationStore))
		r.Post("/{automationID}/resume", httpapi.ResumeAutomation(automationStore))
		r.Post("/{automationID}/webhook-token", httpapi.RotateAutomationWebhookToken(automationStore))
		r.Delete("/{automationID}/webhook-token", httpapi.RevokeAutomationWebhookToken(automationStore))
	})

	// /webhooks/automations/{automationID} (Step 52, §8.4's own "webhook-
	// facing API surface"): deliberately mounted OUTSIDE auth.Middleware
	// entirely, mirroring /webhooks/linear's own precedent immediately
	// below -- this is authenticated by a per-automation bearer token
	// (internal/adapters/inbound/automationwebhook's own doc comment),
	// never a browser cookie. automationStore/automationInvocationStore
	// are the SAME instances automationEngine already uses -- see that
	// package's own doc comment for why this handler lives in its own
	// adapter package rather than httpapi (an import-cycle constraint,
	// not a style preference).
	router.Post("/webhooks/automations/{automationID}", automationwebhook.NewHandler(automationStore, automationInvocationStore))

	// Linear ingress (Step 34, "Linear ingress", §8.10) -- see
	// internal/adapters/inbound/linear's own doc.go for the full design.
	// Kept as one self-contained block, separate from the auth/REST
	// sections above, to keep this Step's own diff to this shared file
	// minimal (Steps 32/33's own GitHub/Slack ingress land their own
	// analogous blocks here independently, in separate worktrees).
	linearOAuthConfig := linear.NewOAuthConfig(*cfg)
	linearClient := linearapi.New(nil, linearAPIBaseURL)
	// linearAgentSessionStore is constructed earlier, alongside outboxStore
	// -- see that construction site's own doc comment for why.
	linearInstallationStore := postgres.NewLinearInstallationStore(pool)

	// /auth/linear/install + /auth/linear/callback: the workspace OAuth
	// connection flow (§8.10's own "OAuth" scope) -- mounted behind
	// auth.Middleware (a signed-in Narvi user must initiate/complete a
	// workspace connection) AND, additionally, gated admin-only inside
	// each handler itself via domain/authz.ActionManageIntegrations (see
	// internal/adapters/inbound/linear's own authz.go for the full
	// reasoning -- a confirmed audit finding: this was never actually
	// role-gated, despite an earlier doc comment here deferring it to a
	// later Step that never added it).
	router.Route("/auth/linear", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/install", linear.NewInstallHandler(linearOAuthConfig, cfg.Timeouts, secureCookies))
		r.Get("/callback", linear.NewInstallCallbackHandler(linearOAuthConfig, linearClient, pool, linearInstallationStore, auditLogStore, cfg.TokenEncryptionKey, secureCookies))
	})

	// /webhooks/linear: Linear's own real AgentSessionEvent webhook --
	// deliberately mounted OUTSIDE auth.Middleware entirely, mirroring
	// scm-credentials/snapshot-mint's own precedent above exactly: this
	// is authenticated by Linear's own webhook signature (verified inside
	// the handler itself), never a browser cookie.
	router.Post("/webhooks/linear", linear.NewWebhookHandler(linear.Deps{
		Pool:               pool,
		Sessions:           sessionStore,
		Turns:              turnStore,
		Environments:       environmentStore,
		Registry:           registry,
		Deliveries:         webhookDeliveryStore,
		AgentSessions:      linearAgentSessionStore,
		Installations:      linearInstallationStore,
		LinearClient:       linearClient,
		IntentClassifier:   intentClassifierSvc,
		WebhookSecret:      []byte(cfg.LinearWebhookSecret),
		TokenEncryptionKey: cfg.TokenEncryptionKey,
		DefaultRepoName:    cfg.LinearDefaultRepoName,
		DefaultRepoURL:     cfg.LinearDefaultRepoURL,
		Timeouts:           cfg.Timeouts,
		// Plans/Outbox (Step 38, "plan mode, cross-channel", §8.1/§13.3):
		// handlePrompted's own new plan-verdict keyword check.
		Plans:  planStore,
		Outbox: outboxStore,
		// AuditLog/IdentityLink/Participants (Step 39, "identities + full
		// RBAC", §13.2/§13.3): Participants is the SAME participantStore
		// instance Step 37's own REST plan approve/reject endpoints already
		// use (constructed once, above), never a second, independently-
		// constructed copy.
		AuditLog:     auditLogStore,
		IdentityLink: appIdentityLinkDeps,
		Participants: participantStore,
		// EpistemicCheckDefault (Step 61, §20.4): the SAME platform.Config
		// value every other CreateTurnCore-reaching caller above also
		// receives.
		EpistemicCheckDefault: cfg.EpistemicCheckDefault,
	}))

	// Outbox delivery worker (Step 35, "outbox delivery", §5.1/§9.3
	// scenario 9): three real ports.Notifier implementations, one per
	// NotificationKind, assembled into a single kind->Notifier routing map
	// -- see internal/app/outboxworker's own doc.go for the full pump
	// design this Builder runs. slackNotifier is a NEW, separate client
	// from internal/adapters/inbound/slack's own ackClient (that one is
	// Step 33's own synchronous in-thread ack, never reused here -- see
	// that package's own doc.go). githubNotifier wraps the SAME
	// sourceControl Adapter already constructed above (design decision:
	// BotNotifier is a sibling type over the same Adapter/doPost
	// machinery, not a second, independently-constructed client),
	// authenticated with the NEW, separate cfg.GitHubBotToken rather than
	// any per-session OAuth token (see internal/platform/config.go's own
	// gitHubBotTokenEnvVarName doc comment for why). linearNotifier looks
	// up each workspace's own real Linear API credential fresh, by
	// organization_id, at delivery time (linearInstallationStore +
	// cfg.TokenEncryptionKey) -- never a token cached in this map itself --
	// and, as of an audit-fix batch (finding M16, "completeness"), is
	// registered under BOTH NotificationKindLinear and
	// NotificationKindLinearProgress below (the SAME instance/Deliver
	// implementation for both -- see that type's own doc comment,
	// linearnotifier.go). slackNotifier/planSlackNotifier are constructed
	// earlier, alongside outboxStore -- see that construction site's own
	// doc comment for why.
	githubNotifier := githubapi.NewBotNotifier(sourceControl, cfg.GitHubBotToken)
	linearNotifier := outboxworker.NewLinearNotifier(linearClient, linearInstallationStore, cfg.TokenEncryptionKey)
	// githubVerdictNotifier (Step 47, "server-side verdict", §8.2) wraps
	// the SAME sourceControl *githubapi.Adapter instance every other
	// GitHub-flavored notifier/caller above already uses, authenticated
	// with the SAME cfg.GitHubBotToken githubNotifier itself uses --
	// posting a verdict is a bot-attributed action exactly like posting
	// the (now-blocked-for-review-sessions) generic outcome comment used
	// to be, never a per-commenter credential.
	githubVerdictNotifier := githubapi.NewVerdictNotifier(sourceControl, cfg.GitHubBotToken)
	// sentinelAutoFixNotifier (Step 48, "sentinels + suggestions", §17.2)
	// spawns the child session -- reviewFindingStore/sentinelFixStore are
	// the SAME instances every other caller above already uses.
	sentinelAutoFixNotifier := outboxworker.NewSentinelAutoFixNotifier(pool, sessionStore, turnStore, environmentStore, auditLogStore, registry, sentinelFixStore, reviewFindingStore, sourceControl, cfg.GitHubBotToken, cfg.Timeouts, cfg.EpistemicCheckDefault)
	// handoffNotifier (Step 49, "handoff-readiness sentinel", §14.4) posts
	// the handoff-readiness comment and applies the "handoff" label on a
	// scoped session's PR -- the SAME sourceControl/cfg.GitHubBotToken
	// every other GitHub-flavored notifier above already uses.
	handoffNotifier := githubapi.NewHandoffNotifier(sourceControl, cfg.GitHubBotToken)
	// releaseManifestNotifier (Step 50, "release PR review", §15.2) posts
	// the release manifest check's own summary comment -- the SAME
	// sourceControl/cfg.GitHubBotToken every other GitHub-flavored
	// notifier above already uses.
	releaseManifestNotifier := githubapi.NewReleaseManifestNotifier(sourceControl, cfg.GitHubBotToken)
	// descriptionAutofixNotifier (Step 67, "review digest: description
	// adequacy + graduated remediation", §26.2) re-verifies Narvi-
	// authorship and this repo's own descriptionAutofix flag, fresh, at
	// delivery time, then rewrites a Narvi-authored PR's own body -- the
	// SAME repoSettingsStore/artifactStore/sourceControl/cfg.GitHubBotToken
	// every other caller above already uses.
	descriptionAutofixNotifier := outboxworker.NewDescriptionAutofixNotifier(repoSettingsStore, artifactStore, sourceControl, cfg.GitHubBotToken, cfg.Timeouts)

	// outboxStore is constructed earlier, alongside linearAgentSessionStore
	// -- see that construction site's own doc comment for why.
	outboxNotifiers := map[ports.NotificationKind]ports.Notifier{
		ports.NotificationKindSlack:             slackNotifier,
		ports.NotificationKindGitHub:            githubNotifier,
		ports.NotificationKindLinear:            linearNotifier,
		ports.NotificationKindLinearProgress:    linearNotifier,
		ports.NotificationKindSlackPlanApproval: planSlackNotifier,
		ports.NotificationKindSlackPlanDecided:  planSlackNotifier,
		ports.NotificationKindGitHubVerdict:     githubVerdictNotifier,
		ports.NotificationKindSentinelAutoFix:   sentinelAutoFixNotifier,
		ports.NotificationKindHandoffSentinel:   handoffNotifier,
		ports.NotificationKindReleaseManifest:   releaseManifestNotifier,
		// Step 67 ("review digest: description adequacy + graduated
		// remediation", §26.2): re-verifies Narvi-authorship and this
		// repo's own descriptionAutofix flag, fresh, at delivery time,
		// then rewrites a Narvi-authored PR's own body.
		ports.NotificationKindGitHubDescriptionAutofix: descriptionAutofixNotifier,
		// Step 56 ("workflow HITL gate + circuit breaker", §25.9): a
		// workflow step awaiting decision, or a run escalating to
		// needs_review, notifies a human via whichever of these three the
		// originating session supports (internal/app/workflowengine's own
		// enqueueWorkflowNotice, notify.go). Slack/Linear reuse the SAME
		// planSlackNotifier/linearNotifier instances already registered
		// above, each now handling a THIRD kind (see those types' own
		// updated Deliver switch); GitHub reuses the SAME githubNotifier
		// instance too -- BotNotifier.Deliver never inspects notification.
		// Kind at all, so registering it under a second key needs no new
		// githubapi code whatsoever.
		ports.NotificationKindSlackWorkflowDecision:  planSlackNotifier,
		ports.NotificationKindLinearWorkflowDecision: linearNotifier,
		ports.NotificationKindGitHubWorkflowDecision: githubNotifier,
		// Step 62 ("review verdict persistence, analytics, digest &
		// automated approval", §21.3): the deterministic daily digest's
		// own two outbox kinds. digestSlackNotifier reuses the SAME
		// *slackapi.Client every other Slack notifier above already
		// uses; digestLinearNotifier takes no dependencies at all -- see
		// that type's own doc comment (outboxworker/digestlinearnotifier.go)
		// for why it always returns a clear, typed error rather than
		// actually delivering anything (no organization-level Linear post
		// capability exists in this codebase yet).
		ports.NotificationKindSlackDigest:  outboxworker.NewDigestSlackNotifier(slackNotifier),
		ports.NotificationKindLinearDigest: outboxworker.NewDigestLinearNotifier(),
	}

	// rwxPreviewNotifier/githubPreviewLinkNotifier (Step 57, "RWX provider
	// + previews", §4.1.1/§4.1.2) are registered ONLY when cfg.RWXAccessToken
	// is configured -- see that env var's own doc comment (platform/
	// config.go) for why this platform-wide credential is optional, unlike
	// Modal's/GitHub's own mandatory secrets: RWX previews are an
	// off-by-default, per-repo opt-in feature layered on top of it, and a
	// deployment that never turns previews on for any repo should not be
	// forced to configure a real RWX account just to boot. When absent, any
	// row enqueued for either of these two kinds (which requires a repo
	// admin to have separately opted in -- an operator misconfiguration,
	// since the two are meant to be configured together) dead-letters with
	// a clear, logged "no notifier registered for kind" error rather than
	// silently vanishing. githubPreviewLinkNotifier reuses the SAME
	// sourceControl *githubapi.Adapter instance and cfg.GitHubBotToken every
	// other GitHub-flavored notifier above already uses -- a preview link
	// is a system-generated fact about a commit, never attributed to any
	// individual PR author or reviewer.
	if cfg.RWXAccessToken != "" {
		rwxDispatchClient := rwx.NewDispatchClient(nil, "", cfg.RWXAccessToken)
		outboxNotifiers[ports.NotificationKindRWXPreviewDispatch] = rwx.NewPreviewNotifier(rwxDispatchClient)
		outboxNotifiers[ports.NotificationKindGitHubPreviewLink] = githubapi.NewPreviewLinkNotifier(sourceControl, cfg.GitHubBotToken)
	}

	// blob_delete (Step 58, §28.4) is registered ONLY when blobStore is
	// configured -- mirrors the RWX block immediately above exactly. When
	// absent, a blob_delete row (which can only ever be enqueued by
	// confirmUploadCore/uploadsweep, both of which are themselves
	// unreachable/inert without cfg.ObjectStorage) dead-letters with the
	// same clear, logged "no notifier registered for kind" error every
	// other unconfigured kind gets -- not a real-world case, since nothing
	// produces this kind's rows without the SAME config this notifier
	// itself requires, but handled identically rather than specially.
	if objStore != nil {
		outboxNotifiers[ports.NotificationKindBlobDelete] = objstore.NewBlobDeleteNotifier(objStore)
	}

	outboxBuilder, err := outboxworker.NewBuilder(outboxStore, pool, outboxNotifiers, cfg.Timeouts)
	if err != nil {
		return fmt.Errorf("construct outbox delivery worker: %w", err)
	}

	// releaseManifestWorker (blocking-finding fix #1, "release PR
	// review", §15.2) is the SEPARATE background loop that claims
	// release_manifest_pending rows (releaseManifestPendingStore,
	// constructed earlier alongside outboxStore) and runs the actual
	// manifest check (releasereview.Run) for each -- entirely decoupled
	// from any webhook request's own context/lifetime, started below via
	// this SAME errgroup as outboxBuilder/every other background loop.
	// deps mirrors what the pre-fix inline call used to pass directly:
	// the SAME sourceControl instance (it satisfies releasereview.
	// MergedPRLister directly) and the SAME outboxStore instance every
	// other enqueue site in this file already uses -- Run itself still
	// enqueues the check's own already-rendered comment there, unchanged
	// by this fix. cfg.GitHubBotToken is the SAME bot credential the
	// pre-fix inline call authenticated ListMergedBetween with -- never
	// persisted onto a release_manifest_pending row itself (see
	// releasereview.Enqueue's own doc comment).
	releaseManifestWorker := releasereview.NewWorker(releaseManifestPendingStore, releasereview.Deps{
		SourceControl: sourceControl,
		Outbox:        outboxStore,
		Timeouts:      cfg.Timeouts,
	}, cfg.GitHubBotToken, cfg.Timeouts)

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: router,
	}

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		slog.Info("narvi control-plane: listening", "addr", cfg.HTTPAddr, "stage", cfg.Stage)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		if err := registry.RunTimerPump(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			// RunTimerPump returns ctx.Err() (context.Canceled) on normal
			// shutdown -- that must NOT be treated as a fatal error the way
			// http.ErrServerClosed is already specially unwrapped for the
			// listener goroutine above; only a genuinely different error is
			// surfaced here.
			return fmt.Errorf("timer pump: %w", err)
		}
		return nil
	})

	// Audit-remediation (config/platform-hardening batch): purges expired
	// ws_tokens/user_sessions rows (neither is ever deleted otherwise --
	// see internal/adapters/outbound/postgres/expiredcleanup.go's own doc
	// comment). Started/shut down through this SAME errgroup as every
	// other background loop above -- no naked goroutine (§11).
	group.Go(func() error {
		if err := postgres.RunExpiredTokenCleanup(groupCtx, pool, cfg.Timeouts.ExpiredCredentialCleanupInterval); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("expired credential cleanup: %w", err)
		}
		return nil
	})

	// Step 25 ("reconciler + GC", §5.3): started/shut down through this
	// SAME errgroup as every other background loop above -- no naked
	// goroutine (§11) -- with the identical context.Canceled carve-out
	// RunTimerPump/RunExpiredTokenCleanup already establish for normal
	// shutdown.
	group.Go(func() error {
		if err := recon.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("reconciler: %w", err)
		}
		return nil
	})

	// Step 26 ("image builds"): started/shut down through this SAME
	// errgroup as every other background loop above -- no naked goroutine
	// (§11) -- with the identical context.Canceled carve-out RunTimerPump/
	// RunExpiredTokenCleanup/recon.Run each already establish for normal
	// shutdown.
	group.Go(func() error {
		if err := builder.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("image builder: %w", err)
		}
		return nil
	})

	// Step 35 ("outbox delivery", §5.1): started/shut down through this
	// SAME errgroup as every other background loop above -- no naked
	// goroutine (§11) -- with the identical context.Canceled carve-out
	// RunTimerPump/RunExpiredTokenCleanup/recon.Run/builder.Run each
	// already establish for normal shutdown.
	group.Go(func() error {
		if err := outboxBuilder.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("outbox delivery worker: %w", err)
		}
		return nil
	})

	// Blocking-finding fix #1 ("release PR review", §15.2): started/shut
	// down through this SAME errgroup as every other background loop
	// above -- no naked goroutine (§11) -- with the identical
	// context.Canceled carve-out every other background loop already
	// establishes for normal shutdown. Runs against groupCtx, the SAME
	// process-lifetime context every other background loop uses -- NEVER
	// any individual webhook request's own context, which is the entire
	// point of this fix (see releaseManifestWorker's own construction
	// site doc comment, and migrations/000050_release_manifest_pending.
	// up.sql's own doc comment, for the full "why").
	group.Go(func() error {
		if err := releaseManifestWorker.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("release manifest check worker: %w", err)
		}
		return nil
	})

	// Step 51 ("automations: engine", §3.5): started/shut down through
	// this SAME errgroup as every other background loop above -- no naked
	// goroutine (§11) -- with the identical context.Canceled carve-out
	// every other background loop already establishes for normal
	// shutdown. Engine.Run itself fans out its own three ticker loops
	// (fan-out, reconcile, sweep) via a further, internal errgroup -- see
	// internal/app/automation's own doc.go.
	group.Go(func() error {
		if err := automationEngine.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("automation engine: %w", err)
		}
		return nil
	})

	// Step 62 (§21.2 stage 2, "auto-merge"): started/shut down through
	// this SAME errgroup as every other background loop above -- no
	// naked goroutine (§11) -- with the identical context.Canceled
	// carve-out every other background loop already establishes for
	// normal shutdown.
	group.Go(func() error {
		if err := automergeWorker.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("auto-merge worker: %w", err)
		}
		return nil
	})

	// Step 62 (§21.3, "deterministic daily digest"): started/shut down
	// through this SAME errgroup as every other background loop above --
	// no naked goroutine (§11) -- with the identical context.Canceled
	// carve-out every other background loop already establishes for
	// normal shutdown.
	group.Go(func() error {
		if err := digestPump.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("digest pump: %w", err)
		}
		return nil
	})

	// Step 58 ("uploads, blob storage & the in-sandbox download_file
	// tool", §28.4): uploadSweeper is nil when cfg.ObjectStorage is absent
	// (feature off) -- started/shut down through this SAME errgroup as
	// every other background loop above -- no naked goroutine (§11) --
	// with the identical context.Canceled carve-out every other
	// background loop already establishes for normal shutdown.
	if uploadSweeper != nil {
		group.Go(func() error {
			if err := uploadSweeper.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("upload abandonment sweeper: %w", err)
			}
			return nil
		})
	}

	// Step 59 ("models: Codex via ChatGPT-account OAuth", §29.5):
	// chatGPTRefreshPump is the single control-plane refresher for every
	// linked ChatGPT account -- unconditional (unlike uploadSweeper above,
	// this needs no optional external dependency to be configured; it is
	// a plain, always-on ticker that simply finds zero rows to do until a
	// user actually links an account). Started/shut down through this
	// SAME errgroup as every other background loop above -- no naked
	// goroutine (§11) -- with the identical context.Canceled carve-out.
	chatGPTRefreshPump := chatgptrefresh.NewPump(providerCredentialStore, pool, chatGPTDeviceFlow, cfg.TokenEncryptionKey, cfg.Timeouts)
	group.Go(func() error {
		if err := chatGPTRefreshPump.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("chatgpt oauth refresh pump: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		<-groupCtx.Done()
		slog.Info("narvi control-plane: shutting down", "grace_period", cfg.Timeouts.ShutdownGracePeriod.String())

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeouts.ShutdownGracePeriod)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http server shutdown: %w", err)
		}

		// Registry.Shutdown cancels every live actor's run loop and waits
		// for all of them, plus the timer-pump goroutine above, to finish.
		// Its own errgroup.Wait() will very likely surface context.Canceled
		// from every actor whose run loop was still alive at shutdown time
		// -- expected/benign, not a real failure, so it gets the exact same
		// context.Canceled carve-out as the timer pump above; anything else
		// is a genuine shutdown failure.
		if err := registry.Shutdown(); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("session actor registry shutdown: %w", err)
		}
		return nil
	})

	return group.Wait()
}

// applyMigrations runs the embedded migrations (migrations.FS) up against
// dsn, using the exact same iofs-source + golang-migrate/database/postgres
// pattern already proven in
// internal/adapters/outbound/postgres/postgres_integration_test.go.
// golang-migrate's Postgres driver takes its own internal advisory lock, so
// this is safe to call on every boot regardless of replica count — no extra
// locking needed here.
func applyMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration db handle: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			slog.Error("close migration db handle failed", "error", closeErr)
		}
	}()

	dbDriver, err := migratepg.WithInstance(db, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("migrate postgres driver: %w", err)
	}

	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrate iofs source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		return fmt.Errorf("migrate.NewWithInstance: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}

// healthResponse is the /health JSON body: {"status":"ok"} on success, or
// {"status":"unhealthy"} on failure. The underlying error is logged
// server-side only — pool.Ping's error text can include the DB user,
// database name, and host:port, which an unauthenticated caller has no
// business learning.
type healthResponse struct {
	Status string `json:"status"`
}

// healthHandler backs /health with a real pool.Ping bounded by
// timeouts.HealthCheckTimeout, so a stuck DB reports 503 within that bound
// rather than hanging the handler indefinitely — never panics (Recoverer
// is also mounted above it as a second line of defense), never hangs past
// the timeout.
func healthHandler(pool *pgxpool.Pool, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeouts.HealthCheckTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")

		if err := pool.Ping(ctx); err != nil {
			slog.Error("health handler: db ping failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			if encErr := json.NewEncoder(w).Encode(healthResponse{Status: "unhealthy"}); encErr != nil {
				slog.Error("health handler: encode unhealthy response", "error", encErr)
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		if encErr := json.NewEncoder(w).Encode(healthResponse{Status: "ok"}); encErr != nil {
			slog.Error("health handler: encode ok response", "error", encErr)
		}
	}
}
