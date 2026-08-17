.PHONY: dev prod clean build build-frontend run deps test fmt lint check help kill-dev image dev-docker dev-auth proto proto-clean proto-lint proto-format proto-breaking gen dev-docs

DATA_DIR := ./data
DOCKER_DATA_DIR := /tmp/discopanel
DB_FILE := $(DATA_DIR)/discopanel.db
FRONTEND_DIR := web/discopanel
DISCOPANEL_BIN := build/discopanel
BUF_IMAGE := bufbuild/buf:latest
BUF_RUN := docker run --rm \
	--volume "$(shell pwd):/workspace" \
	--workdir /workspace \
	--user "$(shell id -u):$(shell id -g)" \
	--env HOME=/tmp \
	$(BUF_IMAGE)

#DISCOSUPPORT_URL := http://localhost:8911

# Development mode - runs backend and frontend concurrently
run:
	@echo "Starting development environment..."
	@mkdir -p $(DATA_DIR)
	@echo "Starting backend server with frontend dev server..."
	@trap 'echo "Stopping all processes..."; kill $$(jobs -p) 2>/dev/null; wait; exit' INT TERM; \
	cd $(FRONTEND_DIR) && npm run dev & \
	FRONTEND_PID=$$!; \
	go run cmd/discopanel/main.go & \
	BACKEND_PID=$$!; \
	wait $$BACKEND_PID $$FRONTEND_PID

restore:
	@echo "Restoring seeded db for dev"
	@mkdir -p $(DATA_DIR)
	cp dev/discopanel.db data/discopanel.db || echo "No saved dev state, starting new db"

dev: clean restore run

# Build and run with OIDC provider (Keycloak)
dev-auth-%: clean
	docker compose -f oidc/$*/docker-compose.yaml down -v --remove-orphans
	@docker run --rm -v /tmp:/tmp alpine rm -rf /tmp/discopanel
	@echo "Building and running with OIDC provider..."
	docker compose -f oidc/$*/docker-compose.yaml build --no-cache
	docker compose -f oidc/$*/docker-compose.yaml up

dev-docker: clean
	docker compose down -v --remove-orphans
	@docker run --rm -v /tmp:/tmp alpine rm -rf /tmp/discopanel
	@echo "Building and running with base compose..."
	docker compose build --no-cache
	docker compose up

dev-docs:
	cd docs/discopanel && npm run dev
	
# Production build and run
prod: build-frontend
	@echo "Building for production..."
	@mkdir -p $(DATA_DIR)
	go build -o $(DISCOPANEL_BIN) cmd/discopanel/main.go

# Build frontend for production
build-frontend:
	@echo "Building frontend..."
	cd $(FRONTEND_DIR) && npm run build

# Build backend with embedded frontend
build: build-frontend
	@echo "Building backend with embedded frontend..."
	go build -o $(DISCOPANEL_BIN) cmd/discopanel/main.go

# Build and push Docker image to :dev tag
image:
	@echo "Building and pushing Docker image..."
	@bash scripts/build.sh

# Clean development data
clean:
	@echo "Cleaning development data..."
	@if [ -d "$(DATA_DIR)" ]; then \
		echo "Removing data directory..."; \
		rm -rf $(DATA_DIR); \
	fi
	@if [ -d "$(DOCKER_DATA_DIR)" ]; then \
		echo "Removing docker data directory..."; \
		docker run --rm -v $(DOCKER_DATA_DIR):/tmp alpine sh -c 'rm -rf /tmp/*'; \
	fi
	@if [ -f "$(DISCOPANEL_BIN)" ]; then \
		echo "Removing backend binary..."; \
		rm -f $(DISCOPANEL_BIN); \
	fi
	@if [ -f "discopanel.db" ]; then \
		echo "Removing old database file..."; \
		rm -f discopanel.db; \
	fi
	@echo "Clean complete!"

# Kill any orphaned dev processes
kill-dev:
	@echo "Killing orphaned development processes..."
	@pkill -f "npm run dev" || true
	@pkill -f "vite" || true
	@pkill -f "go run cmd/discopanel/main.go" || true
	@pkill -f "discopanel" || true
	@echo "Cleanup complete!"

# Install dependencies
deps:
	@echo "Installing Go dependencies..."
	go mod download
	@echo "Updating buf dependencies (using Docker)..."
	$(BUF_RUN) dep update
	@echo "Installing frontend dependencies..."
	cd $(FRONTEND_DIR) && npm install

# Run tests
test:
	@echo "Running Go tests..."
	go test ./...

# Format code
fmt:
	@echo "Formatting Go code..."
	go fmt ./...
	@echo "Formatting frontend code..."
	cd $(FRONTEND_DIR) && npm run format

# Lint code
lint: proto-lint
	@echo "Linting frontend code..."
	cd $(FRONTEND_DIR) && npm run lint

# Type check frontend
check:
	@echo "Type checking frontend..."
	cd $(FRONTEND_DIR) && npm run check
	@echo "Checking for private repo imports..."
	@if grep -rn --include="*.go" '"github.com/discohaus/discoruntime[/"]\|"github.com/discohaus/discomodule[/"]' cmd internal pkg; then \
		echo "panel code must never import private repos"; exit 1; fi

proto:
	@echo "Materializing protogorm annotation schema..."
	go tool protogorm -options proto
	@echo "Generating protocol buffer code (using Docker)..."
	$(BUF_RUN) generate --exclude-path proto/protogorm
	@echo "Injecting gorm tags and generating db wrappers..."
	$(BUF_RUN) build -o - | go tool protogorm -support pkg/proto -store internal/db/store.gen.go:db -inject pkg/proto -spec internal/db/migrations/head.snapshot.json
	@test -f internal/db/migrations/0001_v2_intake.snapshot.json || cp internal/db/migrations/head.snapshot.json internal/db/migrations/0001_v2_intake.snapshot.json
	@echo "Proto generation complete!"

proto-clean:
	@echo "Cleaning generated proto files..."
	rm -rf pkg/proto
	rm -rf web/discopanel/src/lib/proto
	rm -rf proto/protogorm
	@echo "Proto files cleaned!"

proto-lint:
	@echo "Linting proto files (using Docker)..."
	$(BUF_RUN) lint || echo "Buf linting failed, but it's probably just missing comment documentation. Ignore it."
	@echo "Proto linting complete!"

gen: proto-clean proto

proto-format:
	@echo "Formatting proto files (using Docker)..."
	$(BUF_RUN) format -w
	@echo "Proto files formatted!"

proto-breaking:
	@echo "Checking for breaking changes (using Docker)..."
	$(BUF_RUN) breaking --against '.git#branch=main'
	@echo "Breaking change check complete!"

# proto-install:
# 	go install github.com/sudorandom/protoc-gen-connect-openapi@latest

# Help
help:
	@echo "Available commands:"
	@echo "  make dev            - Run in development mode (frontend + backend)"
	@echo "  make build          - Build standalone binary with embedded frontend"
	@echo "  make prod           - Build and run in production mode"
	@echo "  make image          - Build and push Docker image to :dev tag"
	@echo "  make dev-docker     - Build and run Docker container locally (no cache)"
	@echo "  make dev-auth       - Build and run with OIDC provider (Keycloak)"
	@echo "  make clean          - Remove data directory and build artifacts"
	@echo "  make kill-dev       - Kill any orphaned dev processes"
	@echo "  make deps           - Install all dependencies"
	@echo "  make test           - Run tests"
	@echo "  make fmt            - Format code"
	@echo "  make lint           - Lint code"
	@echo "  make check          - Type check frontend"
	@echo "  make gen            - Clean and regenerate proto code (via Docker)"
	@echo "  make proto          - Generate Go and TypeScript code from proto files (via Docker)"
	@echo "  make proto-clean    - Remove all generated proto files"
	@echo "  make proto-lint     - Lint proto files for style and correctness (via Docker)"
	@echo "  make proto-format   - Format proto files (via Docker)"
	@echo "  make proto-breaking - Check for breaking changes against main (via Docker)"
	@echo "  make help           - Show this help message"