# Orchestrator Platform Makefile
# Provides commands for database and application management

.PHONY: help clean FORCE
.PHONY: db/% backend/% agent/% proto/%
.PHONY: test test/integration test-docker

# Docker Compose v2 is the supported tool (the `docker compose` plugin). A
# host that still carries only the v1 binary can run `make COMPOSE=docker-compose`.
COMPOSE ?= docker compose

# Force target to ensure pattern rules always execute
FORCE:

# Default target
help:
	@echo "Orchestrator Platform - Available commands:"
	@echo ""
	@echo "Database commands:"
	@echo "  db/up       - Start PostgreSQL database with Docker Compose"
	@echo "  db/down     - Stop PostgreSQL database"
	@echo "  db/restart  - Restart PostgreSQL database"
	@echo "  db/logs     - Show database logs"
	@echo "  db/shell    - Connect to database shell"
	@echo "  clean       - Clean up containers and volumes"
	@echo ""
	@echo "Backend commands:"
	@echo "  Use 'make backend/<command>' where <command> is one of:"
	@$(MAKE) -C backend help 2>/dev/null | grep -E "^  [a-z]" | sed 's/^  /    /' || echo "    (run 'make backend/help' for details)"
	@echo ""
	@echo "Agent commands:"
	@echo "  Use 'make agent/<command>' where <command> is one of:"
	@$(MAKE) -C agent help 2>/dev/null | grep -E "^  [a-z]" | sed 's/^  /    /' || echo "    (run 'make agent/help' for details)"
	@echo ""
	@echo "Proto commands:"
	@echo "  Use 'make proto/<command>' where <command> is one of:"
	@$(MAKE) -C proto help 2>/dev/null | grep -E "^  [a-z]" | sed 's/^  /    /' || echo "    (run 'make proto/help' for details)"
	@echo ""
	@echo "Test commands:"
	@echo "  test              - Run the Go modules' unit tests"
	@echo "  test/integration  - Run the DSN-gated tests against the local Postgres"
	@echo "  test-docker       - Run the Go modules' tests in Docker"
	@echo "  eeprom/test       - Run the eeprom-signer input-validation tests"
	@echo ""
	@echo "Release commands:"
	@echo "  release/agent     - Build local agent release with GoReleaser"
	@echo "  release/backend   - Build local backend Docker image"
	@echo "  release/frontend  - Build local frontend Docker image"
	@echo "  release/all       - Build all local releases"
	@echo ""
	@echo "Agent package commands:"
	@echo "  agent/deb-arm     - Build ARM Linux Debian package for agent"
	@echo ""
	@echo "Note: Use 'make <module>/help' for detailed help from each module."

# Database commands
db/up:
	@echo "Starting PostgreSQL database..."
	$(COMPOSE) up -d postgres
	@echo "Waiting for database to be ready..."
	@for i in $$(seq 1 30); do \
		if $(COMPOSE) exec postgres pg_isready -U postgres -d orchestrator >/dev/null 2>&1; then \
			echo "Database is ready!"; \
			exit 0; \
		fi; \
		echo "Waiting for database... ($$i/30)"; \
		sleep 1; \
	done; \
	echo "Database failed to start within 30 seconds"; \
	exit 1

db/down:
	@echo "Stopping PostgreSQL database..."
	$(COMPOSE) down

db/restart: db/down db/up

db/logs:
	$(COMPOSE) logs -f postgres

db/shell:
	$(COMPOSE) exec postgres psql -U postgres -d orchestrator

# Cleanup
clean:
	@echo "Cleaning up..."
	$(COMPOSE) down -v
	@echo "Cleanup complete!"

# Backend commands (generic pattern - passes command to backend Makefile)
# FORCE ensures this always runs even if a file with the same name exists
backend/%: FORCE
	@# Remove file if it exists (Make might think it's a file target)
	@if [ -f "$@" ]; then rm -f "$@"; fi
	$(MAKE) -C backend $*

# Agent commands (generic pattern - passes command to agent Makefile)
# FORCE ensures this always runs even if a file with the same name exists
agent/%: FORCE
	@# Remove file if it exists (Make might think it's a file target)
	@if [ -f "$@" ]; then rm -f "$@"; fi
	$(MAKE) -C agent $*

# Proto commands (generic pattern - passes command to proto Makefile)
# FORCE ensures this always runs even if a file with the same name exists
proto/%: FORCE
	@# Remove file if it exists (Make might think it's a file target)
	@if [ -f "$@" ]; then rm -f "$@"; fi
	$(MAKE) -C proto $*

# Test commands
.PHONY: test test-docker
test:
	@echo "Running all tests..."
	$(MAKE) -C backend test
	$(MAKE) -C agent test
	cd tools/entitlement-signer && go test ./...

# Integration tests against a real Postgres.
#
# The DSN-gated tests (every backend package that names BACKEND_TEST_DSN) SKIP
# when it is unset — the state under plain `make test`. This target supplies
# it, one package at a time since they share the database.
#
# Nothing here touches a live database. A scratch database is created on the
# local Docker Postgres (host port 5434, from docker-compose.yml), used, and
# dropped again.
test/integration:
	@echo "Starting Postgres for integration tests..."
	@$(MAKE) db/up
	@echo "Creating the scratch database..."
	@$(COMPOSE) exec -T postgres psql -U postgres -d orchestrator -v ON_ERROR_STOP=1 \
		-c "DROP DATABASE IF EXISTS backend_test" -c "CREATE DATABASE backend_test" >/dev/null
	@echo "Running backend integration tests..."
	@cd backend && pkgs=$$(grep -rl 'BACKEND_TEST_DSN' --include='*_test.go' . | xargs -n1 dirname | sort -u) && \
		BACKEND_TEST_DSN="$(BACKEND_TEST_DSN_VALUE)" go test -p 1 $$pkgs ; \
		backend_rc=$$? ; \
		cd $(CURDIR) ; \
		echo "Dropping the scratch database..." ; \
		$(COMPOSE) exec -T postgres psql -U postgres -d orchestrator \
			-c "DROP DATABASE IF EXISTS backend_test" >/dev/null 2>&1 ; \
		exit $$backend_rc

BACKEND_TEST_DSN_VALUE ?= host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable

test-docker:
	@echo "Running the Go modules' tests in Docker..."
	$(MAKE) -C backend test-docker
	$(MAKE) -C agent test-docker

# eeprom-signer input-validation / injection tests. Stdlib unittest, no
# pytest — the signer image installs no test tooling. Needs only python3 and
# bash; the GNU-sed-only s///e escalation test SKIPS on BSD sed (macOS) and
# runs on Linux.
.PHONY: eeprom/test
eeprom/test:
	@echo "Running eeprom-signer tests..."
	python3 -m unittest discover -s eeprom-signer/tests

# Release commands
.PHONY: release/agent release/backend release/frontend release/all
release/agent:
	@echo "Building local agent release with GoReleaser..."
	@cd agent && goreleaser build --snapshot --clean

release/backend:
	@echo "Building local backend Docker image..."
	@cd backend && docker build -t orchestrator-backend:local -f Dockerfile .

release/frontend:
	@echo "Building local frontend Docker image..."
	@cd frontend && docker build -t orchestrator-frontend:local -f Dockerfile .

release/all: release/agent release/backend release/frontend
	@echo "All local releases built successfully!"
