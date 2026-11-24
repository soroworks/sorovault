.DEFAULT_GOAL := help

BINARY      := sorovault
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# Port 55433 keeps the test database clear of the compose stack on 5432, so
# `make test-all` does not disturb a running dev environment.
TEST_PG_PORT      ?= 55433
TEST_PG_CONTAINER := sorovault-test-pg
TEST_DATABASE_URL ?= postgres://sorovault:sorovault@localhost:$(TEST_PG_PORT)/sorovault?sslmode=disable

# Print a formatted list of all documented targets. Run `make` or `make help`
# to see which targets are available and what each one does.
.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

# Compile the sorovault binary into ./bin. Run this before distributing or
# deploying; the output is a trimmed binary with the version embedded from the
# nearest git tag.
.PHONY: build
build: ## Build the binary into ./bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

# Run the full test suite without requiring a database or network. This is the
# fastest feedback loop and is what `make check` runs before linting.
.PHONY: test
test: ## Run the tests (no database or network needed)
	go test -race ./...

# Run the full test suite including the Postgres store conformance suite. Spins
# up a throwaway Docker container on port $(TEST_PG_PORT) and tears it down
# afterwards. Requires Docker. Prefer `make test` for quick iteration and this
# for a full pre-PR check.
.PHONY: test-all
test-all: ## Run the tests including the Postgres store suite
	@docker rm -f $(TEST_PG_CONTAINER) >/dev/null 2>&1 || true
	@docker run -d --rm --name $(TEST_PG_CONTAINER) \
		-e POSTGRES_USER=sorovault -e POSTGRES_PASSWORD=sorovault -e POSTGRES_DB=sorovault \
		-p $(TEST_PG_PORT):5432 postgres:16-alpine >/dev/null
	@echo "waiting for postgres..."
	@for i in $$(seq 1 30); do \
		docker exec $(TEST_PG_CONTAINER) pg_isready -U sorovault >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race ./...; \
		status=$$?; docker rm -f $(TEST_PG_CONTAINER) >/dev/null 2>&1; exit $$status

# Run the TypeScript attestation verifier against the shared Go test vectors.
# Requires Node 22.18 or later (node --test is used directly). Run after
# changing clients/typescript/verify.ts or the vector fixtures.
.PHONY: test-ts
test-ts: ## Test the TypeScript attestation verifier against the Go vectors (needs Node 22.18+)
	node --test clients/typescript/verify.test.ts

# Run the tests and write a coverage profile to coverage.out, then print the
# total coverage percentage. Useful for spotting coverage regressions before
# opening a PR.
.PHONY: cover
cover: ## Write a coverage profile and print the total
	go test -coverprofile=coverage.out -covermode=atomic ./...
	@go tool cover -func=coverage.out | tail -1

# Keep this in lockstep with .github/workflows/ci.yml. Running through `go run`
# makes the version the source of truth rather than whichever binary happens
# to be on PATH.
GOLANGCI_LINT_VERSION ?= v2.13.2
GOLANGCI_LINT         := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Run golangci-lint at the pinned version. The version is kept in lockstep with
# .github/workflows/ci.yml so local runs and CI use identical checks. No binary
# install is needed; `go run` fetches the pinned version the first time.
.PHONY: lint
lint: ## Run the pinned golangci-lint
	$(GOLANGCI_LINT) run

# Run the live smoke test against the Stellar testnet. Requires the environment
# variable SOROVAULT_LIVE=1 and a working RPC endpoint. This test makes real
# network calls and is opt-in; it is not run by `make test` or `make test-all`.
.PHONY: test-live
test-live: ## Run the opt-in live smoke test against testnet (requires SOROVAULT_LIVE=1)
	@go test -run TestLiveSmokeTest -v ./internal/stellar...

# Run lint and test together. This is the gate that must pass before opening a
# PR; it is what CI runs on every push.
.PHONY: check
check: lint test ## Lint and test — run before opening a PR

# Format all Go source files in-place with gofmt. Run this before committing
# or opening a PR; `make lint` will catch unformatted files.
.PHONY: fmt
fmt: ## Format the code
	gofmt -w .

# Tidy go.mod and go.sum to match the imports in the source tree. Run after
# adding or removing a dependency, or if `go mod verify` reports a mismatch.
.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

# Regenerate the golden ABI fixture for the zkvote test contract, together with
# the TypeScript client, JSON schemas and Go client built from it. Run whenever
# the spec decoder, the TypeScript codegen or the Go codegen changes, to keep
# the checked-in fixtures in sync.
.PHONY: golden
golden: ## Regenerate the golden ABI fixture, the clients built from it and the JSON schemas
	go test ./internal/spec -run TestDecodeFixture -update
	go test ./internal/codegen -run TestGoldenZKVote -update
	go test ./internal/codegen -run TestGoldenZKVoteSchemas -update
	go test ./internal/codegen -run TestGoldenGoClient -update
	go test ./internal/codegen -run TestGoldenPythonClient -update

# Start a full local development stack: Postgres, schema migrations and the
# sorovault server, all via Docker Compose. The binary is rebuilt on each run.
# Use `make down` to stop and `make clean-data` to also wipe the database.
.PHONY: up
up: ## Start Postgres, run migrations and serve, via docker compose
	docker compose up --build

# Stop the Docker Compose stack started by `make up`. The database volume is
# kept, so a subsequent `make up` picks up where it left off.
.PHONY: down
down: ## Stop the compose stack
	docker compose down

# Stop the Docker Compose stack and delete its database volume. Use this when
# you want a clean slate: migrations will re-run from scratch on the next
# `make up`.
.PHONY: clean-data
clean-data: ## Stop the compose stack and delete its database volume
	docker compose down -v

# Apply all pending schema migrations to the database named in $DATABASE_URL.
# Safe to run against an up-to-date database (golang-migrate is a no-op then).
# In Helm this runs automatically as a pre-install/pre-upgrade Job.
.PHONY: migrate
migrate: ## Apply migrations to $$DATABASE_URL
	go run ./cmd/$(BINARY) migrate

# Start the JSON API and browse UI against the database in $DATABASE_URL.
# Requires a migrated database; run `make migrate` or `make up` first.
.PHONY: run
run: ## Run the server against $$DATABASE_URL
	go run ./cmd/$(BINARY) serve

# Remove the ./bin directory and coverage.out. Run this to get a clean build
# output, for example before packaging or when switching branches.
.PHONY: clean
clean: ## Remove build output
	rm -rf bin coverage.out
