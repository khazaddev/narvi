.PHONY: build vet fmt tidy lint test test-integration contracts-generate contracts-check

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
