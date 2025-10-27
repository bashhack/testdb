# Check for .env file and include it
ifneq (,$(wildcard .env))
	include .env
	export
endif

# % is a wildcard character that matches anything, so if make doesn't find a rule
# for a target, it uses this rule. The @ beneath is a no-op command. Using these
# together allows us to define a rule that matches anything, but doesn't do anything.
# This is particularly useful when you have targets that take arbitrary user input.
%:
	@:

# ============================================================================= #
# HELPERS
# ============================================================================= #

## help: Print this help message
.PHONY: help
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

.PHONY: confirm
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

# ============================================================================= #
# DEVELOPMENT
# ============================================================================= #

## tools/check/tern: Check if tern is installed
.PHONY: tools/check/tern
tools/check/tern:
	@if ! command -v tern >/dev/null 2>&1; then \
		echo "Error: 'tern' is not installed."; \
		echo "Install with: go install github.com/jackc/tern/v2@latest"; \
		echo "Or run: make tools/install"; \
		exit 1; \
	fi

## tools/check/goose: Check if goose is installed
.PHONY: tools/check/goose
tools/check/goose:
	@if ! command -v goose >/dev/null 2>&1; then \
		echo "Error: 'goose' is not installed."; \
		echo "Install with: go install github.com/pressly/goose/v3/cmd/goose@latest"; \
		echo "Or run: make tools/install"; \
		exit 1; \
	fi

## tools/check/migrate: Check if migrate is installed
.PHONY: tools/check/migrate
tools/check/migrate:
	@if ! command -v migrate >/dev/null 2>&1; then \
		echo "Error: 'migrate' is not installed."; \
		echo "Install with: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"; \
		echo "Or run: make tools/install"; \
		exit 1; \
	fi

## tools/install: Install optional migration tools (tern, goose, and migrate)
.PHONY: tools/install
tools/install:
	@echo 'Installing migration tools...'
	@echo 'Installing tern...'
	@go install github.com/jackc/tern/v2@latest
	@echo 'Installing goose...'
	@go install github.com/pressly/goose/v3/cmd/goose@latest
	@echo 'Installing migrate...'
	@go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	@echo '✅ Migration tools installed'

# ============================================================================= #
# DOCUMENTATION
# ============================================================================= #

## docs/serve: Serve Go documentation locally with pkgsite
.PHONY: docs/serve
docs/serve:
	@echo 'Serving documentation at http://localhost:3030...'
	@if ! command -v pkgsite >/dev/null 2>&1; then \
		echo "Installing pkgsite..."; \
		go install golang.org/x/pkgsite/cmd/pkgsite@latest; \
	fi
	pkgsite -http localhost:3030

# ============================================================================= #
# DATABASES
# ============================================================================= #

POSTGRES_COMPOSE_FILE := docker-compose.test.yml
POSTGRES_SERVICE := test-postgres
POSTGRES_HOST := localhost
POSTGRES_PORT := 5432
POSTGRES_USER := postgres
POSTGRES_PASSWORD := postgres
POSTGRES_DB := testdb

.PHONY: check_pgcli
check_pgcli:
	@if ! command -v pgcli >/dev/null 2>&1; then \
		echo "Error: 'pgcli' is not installed. Please install pgcli first."; \
		echo "Consider 'brew install pgcli' and visit https://www.pgcli.com for more info."; \
		exit 1; \
	fi

define wait_for_postgres
	@echo "Waiting for Postgres to be ready..."
	@attempts=0; \
	while ! docker compose -f $(1) exec -T $(2) pg_isready -U $(3) -d $(4) >/dev/null 2>&1; do \
		attempts=$$((attempts+1)); \
		if [ $$attempts -gt 30 ]; then \
			echo "❌ Postgres failed to become ready after 30 seconds"; \
			echo "Try 'make $(5)' for a fresh start"; \
			exit 1; \
		fi; \
		echo "⏳ Waiting... ($$attempts/30)"; \
		sleep 1; \
	done; \
	echo "✅ Postgres is ready!"
endef

## test/db/start: Start PostgreSQL test database
.PHONY: test/db/start
test/db/start:
	@echo 'Starting test database...'
	docker compose -f $(POSTGRES_COMPOSE_FILE) up -d
	$(call wait_for_postgres,$(POSTGRES_COMPOSE_FILE),$(POSTGRES_SERVICE),$(POSTGRES_USER),$(POSTGRES_DB),test/db/restart)

## test/db/stop: Stop PostgreSQL test database
.PHONY: test/db/stop
test/db/stop:
	@echo 'Stopping test database...'
	docker compose -f $(POSTGRES_COMPOSE_FILE) down

## test/db/logs: Show PostgreSQL logs
.PHONY: test/db/logs
test/db/logs:
	docker compose -f $(POSTGRES_COMPOSE_FILE) logs -f $(POSTGRES_SERVICE)

## test/db/clean: Clean up Postgres containers and volumes (destructive)
.PHONY: test/db/clean
test/db/clean:
	@echo "⚠️  This will remove all Postgres data. Continue? [y/N] " && read ans && [ $${ans:-N} = y ]
	@echo "Cleaning up Postgres containers and volumes..."
	docker compose -f $(POSTGRES_COMPOSE_FILE) down -v
	@volumes=$$(docker volume ls -q -f name=testdb); \
	if [ -n "$$volumes" ]; then \
		docker volume rm $$volumes; \
	fi

## test/db/restart: Restart Postgres with fresh container and volume (destructive)
.PHONY: test/db/restart
test/db/restart: test/db/clean
	@echo "Starting fresh Postgres instance..."
	docker compose -f $(POSTGRES_COMPOSE_FILE) build --force-rm --no-cache $(POSTGRES_SERVICE)
	docker compose -f $(POSTGRES_COMPOSE_FILE) up -d $(POSTGRES_SERVICE)
	$(call wait_for_postgres,$(POSTGRES_COMPOSE_FILE),$(POSTGRES_SERVICE),$(POSTGRES_USER),$(POSTGRES_DB),test/db/restart)

## test/db/psql: Connect to Postgres database
.PHONY: test/db/psql
test/db/psql:
	@if ! docker compose -f $(POSTGRES_COMPOSE_FILE) exec $(POSTGRES_SERVICE) pg_isready -U $(POSTGRES_USER) > /dev/null 2>&1; then \
		echo "❌ Error: Postgres is not running. Please start it first with 'make test/db/start'"; \
		exit 1; \
	fi
	@echo "🔌 Connecting to Postgres..."
	docker compose -f $(POSTGRES_COMPOSE_FILE) exec $(POSTGRES_SERVICE) psql --username=$(POSTGRES_USER) --dbname=$(POSTGRES_DB)

## test/db/pgcli: Connect to Postgres database via pgcli
.PHONY: test/db/pgcli
test/db/pgcli: check_pgcli
	@if ! docker compose -f $(POSTGRES_COMPOSE_FILE) exec $(POSTGRES_SERVICE) pg_isready -U $(POSTGRES_USER) > /dev/null 2>&1; then \
		echo "❌ Error: Postgres is not running. Please start it first with 'make test/db/start'"; \
		exit 1; \
	fi
	@echo "🔌 Connecting to Postgres..."
	pgcli -h $(POSTGRES_HOST) -p $(POSTGRES_PORT) -U $(POSTGRES_USER) -d $(POSTGRES_DB)

## test/db/execute: Execute a SQL script against the database
.PHONY: test/db/execute
test/db/execute:
	@if [ -z "$(file)" ]; then \
		echo "❌ Error: SQL file not provided. Usage: make test/db/execute file=path/to/your/script.sql"; \
		exit 1; \
	fi
	@if [ ! -f "$(file)" ]; then \
		echo "❌ Error: SQL file '$(file)' not found"; \
		exit 1; \
	fi
	@if ! docker compose -f $(POSTGRES_COMPOSE_FILE) exec $(POSTGRES_SERVICE) pg_isready -U $(POSTGRES_USER) > /dev/null 2>&1; then \
		echo "❌ Error: Postgres is not running. Please start it first with 'make test/db/start'"; \
		exit 1; \
	fi
	@echo "Executing SQL script $(file)..."
	cat $(file) | docker compose -f $(POSTGRES_COMPOSE_FILE) exec -T $(POSTGRES_SERVICE) psql --username=$(POSTGRES_USER) --dbname=$(POSTGRES_DB)
	@echo "✅ SQL script executed successfully"

## test: Run all tests (requires database)
.PHONY: test
test: test/db/start
	@echo 'Running tests...'
	go test -v ./...

## test/postgres: Run PostgreSQL tests only
.PHONY: test/postgres
test/postgres: test/db/start
	@echo 'Running PostgreSQL tests...'
	cd postgres && go test -v

## test/parallel: Run tests in parallel
.PHONY: test/parallel
test/parallel: test/db/start
	@echo 'Running tests in parallel...'
	go test -v -parallel 10 ./...

## test/race: Run tests with race detector
.PHONY: test/race
test/race: test/db/start
	@echo 'Running tests with race detector...'
	go test -race -v ./...

# ============================================================================= #
# QUALITY CONTROL
# ============================================================================= #

## check_staticcheck: Check if staticcheck is installed
.PHONY: check_staticcheck
check_staticcheck:
	@if ! command -v staticcheck >/dev/null 2>&1; then \
		echo "Error: 'staticcheck' is not installed. Installing..."; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi

## check_golangci_lint: Check if golangci-lint is installed
.PHONY: check_golangci_lint
check_golangci_lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "Error: 'golangci-lint' is not installed. Installing..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi

## golangci: Run golangci-lint
.PHONY: golangci
golangci:
	$(MAKE) check_golangci_lint
	golangci-lint run ./...

## security/scan: Run security scan
.PHONY: security/scan
security/scan:
	@if ! command -v gosec >/dev/null 2>&1; then \
		echo "Error: 'gosec' is not installed. Installing..."; \
		go install github.com/securego/gosec/v2/cmd/gosec@latest; \
	fi
	gosec ./...

## lint: Run linters without tests
.PHONY: lint
lint:
	@echo 'Formatting code...'
	go fmt ./...
	@echo 'Vetting code...'
	go vet ./...
	$(MAKE) check_staticcheck
	staticcheck ./...
	$(MAKE) check_golangci_lint
	golangci-lint run ./...
	@echo '✅ Linting complete'

## audit: Tidy dependencies and format, vet and test all code
.PHONY: audit
audit:
	@echo 'Tidying and verifying module dependencies...'
	go mod tidy
	go mod verify
	@echo 'Formatting code...'
	go fmt ./...
	@echo 'Vetting code...'
	go vet ./...
	$(MAKE) check_staticcheck
	staticcheck ./...
	$(MAKE) check_golangci_lint
	golangci-lint run ./...
	@echo 'Running tests...'
	go test -short -vet=off ./...
	@echo '✅ Audit complete'

## audit/long: Run all tests including long-running tests (no -short flag)
.PHONY: audit/long
audit/long:
	@echo 'Running ALL tests including long-running tests...'
	@echo 'Tidying and verifying module dependencies...'
	@go mod tidy
	@go mod verify
	@echo 'Formatting code...'
	@go fmt ./...
	@echo 'Vetting code...'
	@go vet ./...
	@$(MAKE) check_staticcheck
	@staticcheck ./...
	@$(MAKE) check_golangci_lint
	@golangci-lint run ./...
	@echo 'Starting test database...'
	@$(MAKE) test/db/start
	@echo 'Running ALL tests (including long tests, 10 minute timeout, no cache, with race detection)...'
	@go test -race -count=1 -timeout=10m -vet=off ./...
	@echo '✅ Full audit complete (including long tests)'

## audit/security: Run security audit
.PHONY: audit/security
audit/security:
	@echo 'Checking for security vulnerabilities...'
	$(MAKE) security/scan
	@echo '✅ Security audit complete'

## vendor: Tidy and vendor dependencies
.PHONY: vendor
vendor:
	@echo 'Tidying and verifying module dependencies...'
	go mod tidy
	go mod verify
	@echo 'Vendoring dependencies...'
	go mod vendor

## coverage: Run test suite with coverage
.PHONY: coverage
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

## fmt: Format all code
.PHONY: fmt
fmt:
	@echo 'Formatting code...'
	go fmt ./...

## vet: Vet all code
.PHONY: vet
vet:
	@echo 'Vetting code...'
	go vet ./...

## tidy: Tidy module dependencies
.PHONY: tidy
tidy:
	@echo 'Tidying module dependencies...'
	go mod tidy
	go mod verify
