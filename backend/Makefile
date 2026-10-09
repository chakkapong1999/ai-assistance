# Run from backend/. `make up` starts Postgres via the compose file at the repo root.
DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/ai_review?sslmode=disable
MIGRATE      ?= docker run --rm --network host -v "$(CURDIR)/migrations:/migrations" migrate/migrate:v4.18.1 -path=/migrations -database "$(DATABASE_URL)"

# Loads backend/.env (if present) into the environment for run-* targets.
# Real environment variables still win over the file.
LOADENV = set -a; [ -f .env ] && . ./.env; set +a;

.PHONY: up down migrate migrate-river migrate-down run-api run-worker test vet fmt build

up:
	docker compose -f ../docker-compose.yml up -d postgres

down:
	docker compose -f ../docker-compose.yml down

migrate:
	$(MIGRATE) up

migrate-river:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/rivermigrate up

migrate-down:
	$(MIGRATE) down 1

run-api:
	@$(LOADENV) DATABASE_URL="$${DATABASE_URL:-$(DATABASE_URL)}" go run ./cmd/server --mode=api

run-worker:
	@$(LOADENV) DATABASE_URL="$${DATABASE_URL:-$(DATABASE_URL)}" go run ./cmd/server --mode=worker

run-backfill:
	@$(LOADENV) DATABASE_URL="$${DATABASE_URL:-$(DATABASE_URL)}" go run ./cmd/server --mode=backfill

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

build:
	CGO_ENABLED=0 go build -o bin/server ./cmd/server
