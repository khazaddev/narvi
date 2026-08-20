.PHONY: build vet fmt tidy lint test test-integration \
	test-integration-group-1 test-integration-group-2 test-integration-group-3 test-integration-group-4 \
	contracts-generate contracts-check dev \
	web-typecheck web-lint web-check-dto-types web-test web-build web-check

build:
	go build ./...

vet:
	go vet ./...

fmt:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "gofmt needs to be run on:"; \
		echo "$$files"; \
		exit 1; \
	fi

tidy:
	go mod tidy

lint:
	golangci-lint run ./...
	go run ./tools/lint/narvichecks ./...

test:
	go test -race ./...

# test-integration runs with -p 1 (serialize package test binaries) --
# deliberately, not for correctness but for HOST RESOURCE CONTENTION: every
# DB-touching package spins up its own throwaway Postgres container via
# testcontainers-go (each package's own newTestPool/newTestPoolPair helper,
# ~20 of them repo-wide). internal/adapters/inbound/httpapi's own
# integration suite -- by far the heaviest single-package container churn
# in the repo, at the time these CI hangs happened -- used to spin one up
# per test function (~170 of them); it now starts exactly ONE for its
# whole test binary instead (see internal/adapters/inbound/httpapi/
# sharedpool_integration_test.go's own top doc comment), which is why
# group 1 below (httpapi alone) no longer needs the same PEAK-load
# reasoning this comment originally documented it for -- it is kept at
# -p 1 anyway simply because -p has no effect on a single package.
# Three separate CI runs (30831633470, 30834918806, 30838285218 -- see
# fix/sse-broadcast-race's own commit history), from BEFORE that change,
# each hung for the full Go 10-minute test-binary panic timeout on a
# DIFFERENT testcontainers-go internal call (ContainerStart, then
# wait.(*LogStrategy).WaitUntilReady, then wait.(*HostPortStrategy).
# WaitUntilReady/Exec) -- three genuinely different code paths, all
# inside the same third-party library's own Docker-daemon-facing
# machinery, all in the SAME package (httpapi). A per-call
# context.WithTimeout and, later, an independent goroutine-plus-watchdog
# race (see sharedpool_integration_test.go's own startSharedTestContainer
# doc comment, formerly httpapi_integration_test.go's own newTestPool)
# were each verified, directly and locally, to correctly bound this exact
# call in isolation -- yet the SAME construct still failed to cut the
# call off in real CI. The common thread across all three is HOST-LEVEL
# contention, not a single fixable call site: by default `go test ./...`
# runs multiple packages' own test binaries concurrently (bounded by
# GOMAXPROCS), each spinning up its own container(s) via the SAME Docker
# daemon on the SAME constrained runner, under -race's own substantial
# CPU/memory overhead on top -- plausibly severe enough, in aggregate, to
# make even independent per-process timers unreliable, not just Docker
# itself slow. -p 1 trades a longer, fully-serialized wall-clock run for
# dramatically lower PEAK concurrent Docker/host load, directly targeting
# that shared root cause rather than another per-call timeout mechanism
# -- still the right default for the OTHER ~19 packages in this target,
# even though httpapi's own contribution to that peak is now much smaller.
test-integration:
	go test -tags=integration -race -p 1 ./...

# test-integration-group-N -- CI (.github/workflows/ci.yml) runs these four
# targets as separate matrix legs on separate runner VMs (each with its own,
# uncontended Docker daemon) instead of one `test-integration` on a single
# runner, to claw back the wall-clock `-p 1` gave up above WITHOUT
# reintroducing the same-host container contention that caused the hangs
# -p 1 fixed in the first place. See ci.yml's own comment on the
# `test-integration` job for the bin-packing rationale, the timing data
# it's based on, and how these four groups were chosen.
#
# Groups 2-4 run at -p 2 (group 1 is httpapi alone -- a single package, so
# -p has no effect there, left at -p 1). This was verified, not assumed:
# group 3 alone ran 5 independent real CI repeats at -p 2 (4m56s-5m13s,
# vs. a 7m32s -p 1 baseline -- a consistent ~33% improvement) with zero
# hang recurrence before this was extended to groups 2 and 4 as well.
# Groups 2 and 4 were not individually put through that same 5-repeat
# verification -- the extension rests on group 3's result plus the same
# per-VM isolation PR #133's own job-matrix split already provides (no
# cross-group Docker daemon contention, only WITHIN-group contention is
# still possible at -p 2). If a hang symptom (a "test timed out after
# 10m0s" panic) resurfaces on group 2 or 4 specifically, that's a real
# signal this extension went too far for those groups' own package mix --
# drop that one group back to -p 1 rather than reverting all of them.
#
# Groups 1-3 are deliberately short, explicit package lists -- the handful
# of packages heavy enough to matter for balancing. Group 4 is everything
# else, computed from `go list -tags=integration ./...` (the same package
# universe `go test -tags=integration ./...` would use -- test/resilience,
# for instance, only exists as a package at all under that tag) rather than
# hand-listed, so a newly added package is automatically covered (in group
# 4) the moment it exists, instead of silently never running in CI until
# someone remembers to add it to a group.
INTEGRATION_MODULE := github.com/khazaddev/narvi

INTEGRATION_GROUP_1 := $(INTEGRATION_MODULE)/internal/adapters/inbound/httpapi

INTEGRATION_GROUP_2 := \
	$(INTEGRATION_MODULE)/internal/app/sessionactor \
	$(INTEGRATION_MODULE)/internal/app/outboxworker \
	$(INTEGRATION_MODULE)/internal/app/actorauthz \
	$(INTEGRATION_MODULE)/internal/app/reconciler

INTEGRATION_GROUP_3 := \
	$(INTEGRATION_MODULE)/internal/adapters/inbound/slack \
	$(INTEGRATION_MODULE)/internal/adapters/outbound/opencode \
	$(INTEGRATION_MODULE)/internal/app/imagebuild \
	$(INTEGRATION_MODULE)/internal/adapters/inbound/auth \
	$(INTEGRATION_MODULE)/cmd/sandbox-agent \
	$(INTEGRATION_MODULE)/test/resilience

test-integration-group-1:
	go test -tags=integration -race -p 1 $(INTEGRATION_GROUP_1)

test-integration-group-2:
	go test -tags=integration -race -p 2 $(INTEGRATION_GROUP_2)

test-integration-group-3:
	go test -tags=integration -race -p 2 $(INTEGRATION_GROUP_3)

test-integration-group-4:
	@tmp="$$(mktemp)"; \
	printf '%s\n' $(INTEGRATION_GROUP_1) $(INTEGRATION_GROUP_2) $(INTEGRATION_GROUP_3) > "$$tmp"; \
	pkgs="$$(go list -tags=integration ./... | grep -vxF -f "$$tmp")"; \
	rm -f "$$tmp"; \
	go test -tags=integration -race -p 2 $$pkgs

# dev is a LOCAL DEV convenience only (docker-compose.dev.yml), distinct
# from the self-host production story (§12.1: "one binary + Postgres") —
# this just brings up a throwaway local Postgres and runs `narvi serve`
# against it. Every env var below is an obviously-fake, dev-only value
# supplied inline by this recipe so platform.Load() (internal/platform/
# config.go) succeeds: the 3 HMAC secrets, the GitHub OAuth credentials,
# the GitHub webhook secret + bot handle (Step 32, "GitHub ingress") and the
# GitHub bot token (Step 35, "outbox delivery"),
# NARVI_PUBLIC_BASE_URL (matches config.go's own defaultHTTPAddr, ":8080"),
# NARVI_TOKEN_ENCRYPTION_KEY (a real base64 encoding of exactly 32 random
# bytes -- Load() rejects anything else for AES-256-GCM), one signup
# allowlist mechanism (NARVI_ALLOWED_GITHUB_ORGS, satisfying Load()'s
# OR-of-three-allowlists requirement), the Modal base URL/auth token
# (a syntactically valid URL with a real host, but nothing actually
# listens there -- spawning a real sandbox locally needs further setup;
# this only restores "make dev boots", not "make dev's Modal calls work"),
# and Step 34's own Linear webhook secret/OAuth credentials/default repo
# (also placeholders -- nothing actually verifies a Linear webhook
# signature or exchanges a Linear OAuth code locally either), and
# (Step 33, "Slack ingress") the Slack signing secret/bot token pair --
# nothing actually listens for any of these locally, so a real Slack/
# Linear webhook or chat.postMessage/OAuth exchange still needs further
# setup, same caveat as Modal.
# Config.Load() still requires every one of these unconditionally in Go —
# nothing is made "optional in development" there.
# Step 58 ("uploads, blob storage & the in-sandbox download_file tool",
# §28.7) adds the NARVI_OBJECT_STORE_* block, pointed at
# docker-compose.dev.yml's own new minio service: the root user/password/
# bucket here match that file's own MINIO_ROOT_USER/MINIO_ROOT_PASSWORD and
# the minio-init service's own provisioned bucket name EXACTLY -- unlike
# every credential above, these are not placeholders, uploads actually
# work end to end against this local MinIO. NARVI_OBJECT_STORE_USE_PATH_STYLE
# is required true for MinIO (§28.7's own path-style toggle).
# minio-init lives on docker-compose.dev.yml's "init" profile (see that
# file's own comment): a one-shot that exits 0 after bucket creation,
# which `up --wait` would treat as failure if it were in the default stack.
dev:
	docker compose -f docker-compose.dev.yml up -d --wait
	docker compose -f docker-compose.dev.yml --profile init run --rm minio-init
	NARVI_STAGE=development \
	NARVI_DATABASE_URL=postgres://narvi:narvi@localhost:$${NARVI_DEV_PG_PORT:-5432}/narvi?sslmode=disable \
	NARVI_HMAC_SANDBOX_SECRET=dev-only-insecure-sandbox-secret \
	NARVI_HMAC_BOTS_SECRET=dev-only-insecure-bots-secret \
	NARVI_HMAC_WEBHOOK_SECRET=dev-only-insecure-webhook-secret \
	NARVI_GITHUB_CLIENT_ID=dev-github-client-id-placeholder \
	NARVI_GITHUB_CLIENT_SECRET=dev-github-client-secret-placeholder \
	NARVI_GITHUB_WEBHOOK_SECRET=dev-only-insecure-github-webhook-secret \
	NARVI_GITHUB_BOT_HANDLE=narvi-bot \
	NARVI_GITHUB_BOT_TOKEN=dev-github-bot-token-placeholder \
	NARVI_PUBLIC_BASE_URL=http://localhost:8080 \
	NARVI_TOKEN_ENCRYPTION_KEY=X4x5GAK5D4bwFxg5fEzToXLfPfe2XwZp8U3CR/Pl1Z4= \
	NARVI_ALLOWED_GITHUB_ORGS=dev-org-placeholder \
	NARVI_MODAL_BASE_URL=http://localhost:9999 \
	NARVI_MODAL_AUTH_TOKEN=dev-modal-token-placeholder \
	NARVI_LINEAR_WEBHOOK_SECRET=dev-linear-webhook-secret-placeholder \
	NARVI_LINEAR_CLIENT_ID=dev-linear-client-id-placeholder \
	NARVI_LINEAR_CLIENT_SECRET=dev-linear-client-secret-placeholder \
	NARVI_LINEAR_DEFAULT_REPO_NAME=narvi \
	NARVI_LINEAR_DEFAULT_REPO_URL=https://github.com/khazaddev/narvi \
	NARVI_SLACK_SIGNING_SECRET=dev-slack-signing-secret-placeholder \
	NARVI_SLACK_BOT_TOKEN=dev-slack-bot-token-placeholder \
	NARVI_ANTHROPIC_API_KEY=dev-anthropic-api-key-placeholder \
	NARVI_INTENT_CLASSIFIER_PROVIDER=anthropic \
	NARVI_INTENT_CLASSIFIER_MODEL=claude-haiku-4-5 \
	NARVI_OBJECT_STORE_ENDPOINT=http://localhost:$${NARVI_DEV_MINIO_PORT:-9000} \
	NARVI_OBJECT_STORE_REGION=us-east-1 \
	NARVI_OBJECT_STORE_BUCKET=narvi-dev-uploads \
	NARVI_OBJECT_STORE_ACCESS_KEY_ID=narvi \
	NARVI_OBJECT_STORE_SECRET_ACCESS_KEY=narvi-dev-secret \
	NARVI_OBJECT_STORE_USE_PATH_STYLE=true \
	go run ./cmd/control-plane serve

# contracts-generate regenerates every codegen target under contracts/gen from
# the JSON Schemas under /contracts (§6). Go output uses the go-jsonschema
# tool pinned by go.mod's `tool` directive (no separately-installed binary
# required); TS output uses contracts/scripts/generate-ts.mjs, which requires
# contracts/node_modules to already be installed (`cd contracts && npm ci`).
contracts-generate:
	go tool go-jsonschema contracts/sandbox-ws/v1/commands.schema.json contracts/sandbox-ws/v1/events.schema.json -p sandboxws -o contracts/gen/go/sandboxws/sandboxws.go
	go tool go-jsonschema contracts/client-ws/v1/protocol.schema.json -p clientws -o contracts/gen/go/clientws/clientws.go
	go tool go-jsonschema contracts/session-config/v1/session-config.schema.json -p sessionconfig -o contracts/gen/go/sessionconfig/sessionconfig.go
	go tool go-jsonschema contracts/rest/v1/dtos.schema.json -p restdtos -o contracts/gen/go/restdtos/restdtos.go
	cd contracts && npm run generate

# contracts-check (§9.2/§10 exit criterion "contracts round-trip green") is
# the drift check: snapshot contracts/gen, regenerate everything, fail if
# that changed anything versus the snapshot, then typecheck the TS output
# against contracts/tsconfig.json (which also covers the hand-written
# contracts/typecheck fixtures). Deliberately plain-diff based (not
# git-diff-based) so it works the same whether or not contracts/gen is
# already committed, and regardless of any git wrapper on PATH.
contracts-check:
	@tmp="$$(mktemp -d)" && \
	cp -R contracts/gen "$$tmp/gen-before" && \
	$(MAKE) contracts-generate && \
	if ! diff -ru "$$tmp/gen-before" contracts/gen; then \
		echo "contracts/gen is out of date with the schemas under /contracts (diff above): run 'make contracts-generate' and commit the result"; \
		rm -rf "$$tmp"; \
		exit 1; \
	fi; \
	rm -rf "$$tmp"
	cd contracts && npm run typecheck

# web-* targets (§12.1, "ui bootstrap"): the frontend's own equivalent of
# vet/lint/test/build above, over web/ (Vite + React + TanStack Query/
# Router) instead of the Go module. Each assumes `cd web && npm ci` has
# already been run -- mirrors contracts-generate/contracts-check's own
# identical assumption about contracts/node_modules (see that target's own
# comment) rather than auto-installing on every invocation.
#
# web-typecheck and web-build both regenerate src/routeTree.gen.ts first
# (web/package.json's own "generate-routes" script, via @tanstack/
# router-cli) -- that file is gitignored and normally produced by Vite's
# own dev/build lifecycle, but tsc needs it to already exist before either
# of THESE targets' own tsc pass runs, on a checkout where `vite dev`/
# `vite build` has never run yet.
web-typecheck:
	cd web && npm run typecheck

web-lint:
	cd web && npm run lint

# web-check-dto-types (§12.1: "no hand-written response types anywhere")
# enforces that no .ts/.tsx file under web/src redeclares a type/interface
# name contracts/gen/ts/*.ts already generates -- see
# web/scripts/check-no-dto-redeclaration.mjs's own top comment for why this
# specific, name-collision shape was chosen over a field-shape comparison.
web-check-dto-types:
	cd web && npm run check-dto-types

# web-test (§12.1's own data layer, Step 80: "WS transport -> event log ->
# reducer -> query invalidation"): typechecks web/src/**/__tests__ (its own
# standalone tsconfig.vitest.json -- see web/README.md's own "Testing"
# section for why that tree is not part of web-typecheck's own tsc -b
# project) and runs the real pipeline tests (vitest) -- transport.test.ts/
# sessionStream.test.ts drive the real transport/reducer against a real
# local fake WS server, not hand-made objects.
web-test:
	cd web && npm test

# web-build produces the web UI's static bundle, written DIRECTLY into
# internal/adapters/inbound/webui/dist/ (web/vite.config.ts's own outDir --
# see that file's own comment for why go:embed's own directive syntax rules
# out the more conventional web/dist here). Required before
# `go build -tags web_assets ./cmd/control-plane` can compile at all (see
# internal/adapters/inbound/webui's own doc comment) -- the single-binary
# `make dist` recipe that wires the two together end to end is a later
# Step's own scope, not built here.
web-build:
	cd web && npm run build

# web-check is a LOCAL convenience only (typecheck + lint + the DTO-name
# guard + test + build, in the order most likely to fail fast) -- CI
# (.github/workflows/ci.yml's own `web` job) runs the same npm scripts as
# separate steps instead, for per-concern failure attribution in the
# Actions UI rather than one opaque `make` invocation.
web-check: web-typecheck web-lint web-check-dto-types web-test web-build
