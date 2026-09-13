# Development entry points. `make help` lists them.

SHELL := /bin/bash
COMPOSE_DEV := docker compose -f deployments/docker-compose.db.yml
DEV_DB_PORT ?= 5433
DEV_PDF_PORT ?= 3000
COMPOSE     := docker compose -f deployments/docker-compose.yml --env-file .env

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "};{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- database --

.PHONY: db-up
db-up: ## Start the local development database and PDF renderer
	$(COMPOSE_DEV) up -d --wait

.PHONY: db-down
db-down: ## Stop the local development database and PDF renderer
	$(COMPOSE_DEV) down

.PHONY: db-reset
db-reset: ## Destroy and recreate the development database (deletes all local data)
	$(COMPOSE_DEV) down -v
	$(COMPOSE_DEV) up -d --wait

.PHONY: db-shell
db-shell: ## Open psql against the development database
	$(COMPOSE_DEV) exec postgres psql -U invoice -d invoice

.PHONY: migrate
migrate: ## Apply pending migrations
	go run ./apps/api -migrate

.PHONY: migrate-status
migrate-status: ## Show applied and pending migrations
	go run ./apps/api -migrate-status

# --------------------------------------------------------------------- run --

.PHONY: api
api: ## Run the API against .env
	go run ./apps/api

.PHONY: web
web: ## Run the web dev server
	cd apps/web && npm run dev

.PHONY: install-web
install-web: ## Install web dependencies
	cd apps/web && npm install

# ------------------------------------------------------------------ checks --

.PHONY: test
test: ## Run Go tests with the race detector (database tests skip without TEST_DATABASE_URL)
	go test -race ./...

.PHONY: test-db
test-db: ## Create the test database and run the integration tests against it
	$(COMPOSE_DEV) exec -T postgres psql -U invoice -d postgres \
		-c "DROP DATABASE IF EXISTS invoice_test;" \
		-c "CREATE DATABASE invoice_test OWNER invoice;"
	TEST_DATABASE_URL="postgres://invoice:invoice@localhost:$(DEV_DB_PORT)/invoice_test?sslmode=disable" \
	TEST_GOTENBERG_URL="http://localhost:$(DEV_PDF_PORT)" \
		go test -race ./tests/

.PHONY: test-web
test-web: ## Typecheck and build the web app
	cd apps/web && npm run build

.PHONY: lint
lint: ## Vet Go and typecheck TypeScript
	go vet ./...
	gofmt -l apps internal migrations tests
	cd apps/web && npm run typecheck

.PHONY: check
check: lint test test-web ## Everything CI runs (add test-db for the database tests)

# ------------------------------------------------------------------ deploy --

.PHONY: up
up: ## Start the full Docker Compose stack
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Stop the full stack
	$(COMPOSE) down

.PHONY: logs
logs: ## Follow stack logs
	$(COMPOSE) logs -f
