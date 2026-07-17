.PHONY: build vet fmt tidy lint test

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
