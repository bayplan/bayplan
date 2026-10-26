VERSION ?= dev

# The single entry point for development: `make help`
# lists the targets. Recipes with real logic (waiting for the backend, creating
# the dev admin, registering the test cluster) live in scripts/dev.sh.

.DEFAULT_GOAL := help
.PHONY: help build agent images test vet fmt lint check run frontend-build frontend-lint third-party \
	dev web db start restart status stop reencrypt reset-db clean audit

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-z0-9-]+:.*## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nAfter 'make dev': http://localhost:8080  ->  admin / devlocal12345\n"

## ─── Development environment (Postgres in Docker, backend and Vite on the host)

dev: ## Postgres + build + backend in the background, creates the admin, follows the log
	@scripts/dev.sh dev

web: ## Vite with hot reload on :5173 (another terminal; proxies /api and /ws to :8080)
	@scripts/dev.sh web

db: ## Start only Postgres
	@scripts/dev.sh db

start: ## Start the already-built backend in the background, without rebuilding
	@scripts/dev.sh backend

restart: ## Rebuild and restart the backend (Postgres must be up)
	@scripts/dev.sh restart

status: ## Status of Postgres, the backend (/readyz) and the synced clusters
	@scripts/dev.sh status

stop: ## Stop the backend and Postgres
	@scripts/dev.sh stop

reencrypt: ## Re-encrypt the dev DB with the active key (key rotation, see deploy/README.md)
	@scripts/dev.sh reencrypt

reset-db: ## Recreate Postgres from scratch (re-bootstrap on the next make dev)
	@scripts/dev.sh reset-db

clean: ## Purge Postgres (container + volume) and delete bin/
	@scripts/dev.sh clean

## ─── Build and checks

build: frontend-build ## Build frontend + backend into a single binary (bin/bayplan)
	cd backend && go build -ldflags="-X main.version=$(VERSION)" -o ../bin/bayplan ./cmd/server

run: build ## Build and run bin/bayplan in the foreground (needs the environment exported)
	./bin/bayplan

# Agent binary deployed into the managed clusters (agent connector).
agent: ## Build the agent binary (bin/bayplan-agent)
	cd backend && go build -o ../bin/bayplan-agent ./cmd/agent

# Container images (server and agent). VERSION is embedded in both:
# the server offers the agent the image of ITS OWN version.
images: ## Build the server and agent images with podman (VERSION=…)
	podman build -f deploy/Containerfile --build-arg VERSION=$(VERSION) -t localhost/bayplan:$(VERSION) .
	podman build -f deploy/Containerfile.agent --build-arg VERSION=$(VERSION) -t localhost/bayplan-agent:$(VERSION) .

test: ## Backend tests (go test ./...)
	cd backend && go test ./...

vet:
	cd backend && go vet ./...

# Fails if a file is not formatted: `gofmt -l` alone only lists them.
fmt:
	@cd backend && out="$$(gofmt -l .)" && if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: fmt vet frontend-lint ## gofmt + go vet + frontend lint (oxlint, locale keys)

check: lint test ## What CI runs: lint + tests

audit: ## Known vulnerabilities: govulncheck, npm audit and Trivy on IMAGES="…" (needs trivy)
	@scripts/security-scan.sh $(IMAGES)

# Installs the frontend dependencies when package-lock.json changes (or the
# first time): build, lint and Vite need them.
frontend/node_modules: frontend/package-lock.json
	cd frontend && npm ci
	@touch $@

# dist/.gitkeep is tracked so that a fresh clone compiles (go:embed all:dist
# needs a file) and test/vet/lint never need a frontend build first: recreate it
# after wiping the directory, or `git status` shows it deleted.
frontend-build: frontend/node_modules
	cd frontend && npm run build
	rm -rf backend/internal/web/dist
	mkdir -p backend/internal/web/dist
	cp -r frontend/dist/. backend/internal/web/dist/
	touch backend/internal/web/dist/.gitkeep

frontend-lint: frontend/node_modules
	cd frontend && npm run lint

# Regenerates THIRD_PARTY_NOTICES.md from the Go modules actually linked in
# and the production npm dependencies. Run it whenever dependencies change.
third-party: ## Regenerate THIRD_PARTY_NOTICES.md
	./deploy/third-party-notices.sh
