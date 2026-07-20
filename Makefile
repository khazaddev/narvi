.PHONY: build vet fmt tidy lint test test-integration contracts-generate contracts-check dev

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

test-integration:
	go test -tags=integration -race ./...

# dev is a LOCAL DEV convenience only (docker-compose.dev.yml), distinct
# from the self-host production story (§12.1: "one binary + Postgres") —
# this just brings up a throwaway local Postgres and runs `narvi serve`
# against it. Every env var below is an obviously-fake, dev-only value
# supplied inline by this recipe so platform.Load() (internal/platform/
# config.go) succeeds: the 3 HMAC secrets, the GitHub OAuth credentials,
# NARVI_PUBLIC_BASE_URL (matches config.go's own defaultHTTPAddr, ":8080"),
# NARVI_TOKEN_ENCRYPTION_KEY (a real base64 encoding of exactly 32 random
# bytes -- Load() rejects anything else for AES-256-GCM), one signup
# allowlist mechanism (NARVI_ALLOWED_GITHUB_ORGS, satisfying Load()'s
# OR-of-three-allowlists requirement), and the Modal base URL/auth token
# (a syntactically valid URL with a real host, but nothing actually
# listens there -- spawning a real sandbox locally needs further setup;
# this only restores "make dev boots", not "make dev's Modal calls work").
# Config.Load() still requires every one of these unconditionally in Go —
# nothing is made "optional in development" there.
dev:
	docker compose -f docker-compose.dev.yml up -d --wait
	NARVI_DATABASE_URL=postgres://narvi:narvi@localhost:5432/narvi?sslmode=disable \
	NARVI_HMAC_SANDBOX_SECRET=dev-only-insecure-sandbox-secret \
	NARVI_HMAC_BOTS_SECRET=dev-only-insecure-bots-secret \
	NARVI_HMAC_WEBHOOK_SECRET=dev-only-insecure-webhook-secret \
	NARVI_GITHUB_CLIENT_ID=dev-github-client-id-placeholder \
	NARVI_GITHUB_CLIENT_SECRET=dev-github-client-secret-placeholder \
	NARVI_PUBLIC_BASE_URL=http://localhost:8080 \
	NARVI_TOKEN_ENCRYPTION_KEY=X4x5GAK5D4bwFxg5fEzToXLfPfe2XwZp8U3CR/Pl1Z4= \
	NARVI_ALLOWED_GITHUB_ORGS=dev-org-placeholder \
	NARVI_MODAL_BASE_URL=http://localhost:9999 \
	NARVI_MODAL_AUTH_TOKEN=dev-modal-token-placeholder \
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
