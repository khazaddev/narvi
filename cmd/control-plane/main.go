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
	githubingress "github.com/khazaddev/narvi/internal/adapters/inbound/github"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	identitylinkhttp "github.com/khazaddev/narvi/internal/adapters/inbound/identitylink"
	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/llm"
	"github.com/khazaddev/narvi/internal/adapters/outbound/modal"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/app/imagebuild"
	"github.com/khazaddev/narvi/internal/app/intentclassifier"
	"github.com/khazaddev/narvi/internal/app/outboxworker"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/reconciler"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
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

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open postgres pool: %w", err)
	}
	defer pool.Close()

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
	// immediately below.
	registry, err := sessionactor.NewRegistry(ctx, pool, cfg.Timeouts, hub, commander, sandboxProvider, cfg.PublicBaseURL,
		sourceControl, cfg.TokenEncryptionKey, cfg.OpenCodeRuntimeVersion)
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
	linearAgentSessionStore := postgres.NewLinearAgentSessionStore(pool)

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
	// imagebuild's own doc.go for what it does and why.
	builder, err := imagebuild.NewBuilder(imageBuildStore, pool, sandboxProvider, cfg.Timeouts)
	if err != nil {
		return fmt.Errorf("construct image builder: %w", err)
	}

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
	router.Post("/sessions/{sessionID}/scm-credentials",
		httpapi.ScmCredentials(sessionStore, sandboxStore, identityStore, cfg.TokenEncryptionKey, cfg.Timeouts))

	// snapshot-mint (Step 22, "snapshots & restore", design decision 2):
	// deliberately mounted OUTSIDE /api/sessions and outside auth.
	// Middleware entirely, mirroring scm-credentials immediately above
	// exactly (see that handler's own doc comment, and
	// httpapi/snapshotmint.go's own) -- another sandbox-bearer-token-
	// authenticated route, not a browser-facing one.
	router.Post("/sessions/{sessionID}/snapshot",
		httpapi.SnapshotMint(sandboxStore, sandboxProvider))

	// Slack ingress (Step 33, §8.10): deliberately mounted OUTSIDE
	// /api/sessions and outside auth.Middleware entirely -- Slack itself
	// is the caller here, authenticated via its own request-signing
	// scheme (X-Slack-Signature/X-Slack-Request-Timestamp), not a
	// narvi_auth_session cookie. See internal/adapters/inbound/slack's
	// own doc.go for the full request-handling writeup.
	router.Post("/webhooks/slack", slack.NewHandler(slack.Deps{
		Pool:             pool,
		Sessions:         sessionStore,
		Turns:            turnStore,
		Environments:     environmentStore,
		Registry:         registry,
		Deliveries:       webhookDeliveryStore,
		Threads:          slackThreadSessionStore,
		AuditLog:         auditLogStore,
		IntentClassifier: intentClassifierSvc,
		SigningSecret:    cfg.SlackSigningSecret,
		BotToken:         cfg.SlackBotToken,
		DefaultRepoName:  cfg.SlackDefaultRepoName,
		DefaultRepoURL:   cfg.SlackDefaultRepoURL,
		TimestampWindow:  cfg.Timeouts.WebhookTimestampFreshnessWindow,
		SlackAPIBaseURL:  slackAPIBaseURL,
		AckTimeout:       cfg.Timeouts.SlackAckTimeout,
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
		SigningSecret:       cfg.SlackSigningSecret,
		Timeouts:            cfg.Timeouts,
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
		},
		webhookDeliveryStore,
		githubingress.Config{
			WebhookSecret: cfg.GitHubWebhookSecret,
			BotHandle:     cfg.GitHubBotHandle,
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
		r.Patch("/{userID}/role", httpapi.UpdateMemberRole(pool, userStore, auditLogStore))
		r.Post("/{userID}/identities", httpapi.LinkMemberIdentity(pool, userStore, identityStore, auditLogStore))
		r.Delete("/{userID}/identities/{identityID}", httpapi.UnlinkMemberIdentity(pool, identityStore, auditLogStore))
	})
	router.Route("/api/audit-log", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/", httpapi.ListAuditLog(auditLogStore))
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
		r.Post("/", httpapi.CreateSession(pool, sessionStore, turnStore, environmentStore, auditLogStore, registry, intentClassifierSvc))
		r.Get("/{sessionID}", httpapi.GetSession(sessionStore))
		r.Get("/{sessionID}/events", httpapi.ListEvents(sessionStore, eventStore))
		r.Get("/{sessionID}/artifacts", httpapi.ListArtifacts(sessionStore, artifactStore))
		r.Post("/{sessionID}/ws-token", httpapi.MintWSToken(sessionStore, wsTokenStore, cfg.Timeouts))
		// turns (Step 28, "turn recovery", §8.7): the relaunch-and-resume
		// REST API -- enqueues a new turn on an existing session, 409 if
		// one is already in flight. See httpapi/turn.go's own doc comment.
		r.Post("/{sessionID}/turns", httpapi.CreateTurn(pool, sessionStore, turnStore, participantStore, auditLogStore, registry))
		// plans (Step 37, "plan mode, web", §8.1/§12.2 item 3): the
		// approve/reject HITL actions -- see httpapi/planapprove.go's own
		// doc comment for the full sequencing. outboxStore/
		// linearAgentSessionStore (Step 38, "plan mode, cross-channel") feed
		// DecidePlanOnTx's own cross-channel-notify step (decideplan.go).
		r.Post("/{sessionID}/plans/{planId}/approve", httpapi.ApprovePlan(pool, sessionStore, turnStore, planStore, participantStore, outboxStore, linearAgentSessionStore, auditLogStore, registry))
		r.Post("/{sessionID}/plans/{planId}/reject", httpapi.RejectPlan(pool, sessionStore, turnStore, planStore, participantStore, outboxStore, linearAgentSessionStore, auditLogStore))
	})

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
	// workspace connection; see internal/adapters/inbound/linear's own
	// doc.go for why role-gating this to admins specifically is left to
	// Step 39).
	router.Route("/auth/linear", func(r chi.Router) {
		r.Use(auth.Middleware(userSessionStore, userStore))
		r.Get("/install", linear.NewInstallHandler(linearOAuthConfig, cfg.Timeouts, secureCookies))
		r.Get("/callback", linear.NewInstallCallbackHandler(linearOAuthConfig, linearClient, linearInstallationStore, cfg.TokenEncryptionKey, secureCookies))
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
		// AuditLog/IdentityLink (Step 39, "identities + full RBAC", §13.2/
		// §13.3).
		AuditLog:     auditLogStore,
		IdentityLink: appIdentityLinkDeps,
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
	// cfg.TokenEncryptionKey) -- never a token cached in this map itself.
	// slackNotifier/planSlackNotifier are constructed earlier, alongside
	// outboxStore -- see that construction site's own doc comment for why.
	githubNotifier := githubapi.NewBotNotifier(sourceControl, cfg.GitHubBotToken)
	linearNotifier := outboxworker.NewLinearNotifier(linearClient, linearInstallationStore, cfg.TokenEncryptionKey)

	// outboxStore is constructed earlier, alongside linearAgentSessionStore
	// -- see that construction site's own doc comment for why.
	outboxBuilder, err := outboxworker.NewBuilder(outboxStore, pool, map[ports.NotificationKind]ports.Notifier{
		ports.NotificationKindSlack:             slackNotifier,
		ports.NotificationKindGitHub:            githubNotifier,
		ports.NotificationKindLinear:            linearNotifier,
		ports.NotificationKindSlackPlanApproval: planSlackNotifier,
		ports.NotificationKindSlackPlanDecided:  planSlackNotifier,
	}, cfg.Timeouts)
	if err != nil {
		return fmt.Errorf("construct outbox delivery worker: %w", err)
	}

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
