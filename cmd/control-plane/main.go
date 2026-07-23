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
	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/modal"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/imagebuild"
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
		Pool:            pool,
		Sessions:        sessionStore,
		Turns:           turnStore,
		Environments:    environmentStore,
		Registry:        registry,
		Deliveries:      webhookDeliveryStore,
		Threads:         slackThreadSessionStore,
		SigningSecret:   cfg.SlackSigningSecret,
		BotToken:        cfg.SlackBotToken,
		DefaultRepoName: cfg.SlackDefaultRepoName,
		DefaultRepoURL:  cfg.SlackDefaultRepoURL,
		TimestampWindow: cfg.Timeouts.WebhookTimestampFreshnessWindow,
		SlackAPIBaseURL: slackAPIBaseURL,
		AckTimeout:      cfg.Timeouts.SlackAckTimeout,
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
			Pool:         pool,
			PRSessions:   githubPRSessionStore,
			Sessions:     sessionStore,
			Turns:        turnStore,
			Environments: environmentStore,
			Registry:     registry,
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
		r.Post("/", httpapi.CreateSession(pool, sessionStore, turnStore, environmentStore, registry))
		r.Get("/{sessionID}", httpapi.GetSession(sessionStore))
		r.Get("/{sessionID}/events", httpapi.ListEvents(sessionStore, eventStore))
		r.Get("/{sessionID}/artifacts", httpapi.ListArtifacts(sessionStore, artifactStore))
		r.Post("/{sessionID}/ws-token", httpapi.MintWSToken(sessionStore, wsTokenStore, cfg.Timeouts))
		// turns (Step 28, "turn recovery", §8.7): the relaunch-and-resume
		// REST API -- enqueues a new turn on an existing session, 409 if
		// one is already in flight. See httpapi/turn.go's own doc comment.
		r.Post("/{sessionID}/turns", httpapi.CreateTurn(pool, sessionStore, turnStore, registry))
	})

	// Linear ingress (Step 34, "Linear ingress", §8.10) -- see
	// internal/adapters/inbound/linear's own doc.go for the full design.
	// Kept as one self-contained block, separate from the auth/REST
	// sections above, to keep this Step's own diff to this shared file
	// minimal (Steps 32/33's own GitHub/Slack ingress land their own
	// analogous blocks here independently, in separate worktrees).
	linearOAuthConfig := linear.NewOAuthConfig(*cfg)
	linearClient := linearapi.New(nil, linearAPIBaseURL)
	linearAgentSessionStore := postgres.NewLinearAgentSessionStore(pool)
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
		WebhookSecret:      []byte(cfg.LinearWebhookSecret),
		TokenEncryptionKey: cfg.TokenEncryptionKey,
		DefaultRepoName:    cfg.LinearDefaultRepoName,
		DefaultRepoURL:     cfg.LinearDefaultRepoURL,
		Timeouts:           cfg.Timeouts,
	}))

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
