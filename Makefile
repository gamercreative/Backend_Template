.PHONY: help up down migrate migrate-down run build test check check-queries smoke docker-up docker-down docker-logs
.DEFAULT_GOAL := help

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-14s %s\n", $$1, $$2}'

up: ## start only PostgreSQL (docker compose) and wait until healthy
	docker compose up -d --wait db

down: ## stop every container (data is kept in the pgdata volume)
	docker compose down

docker-up: ## build and run db + migrations + server in Docker, wait until healthy
	docker compose up -d --build --wait

docker-down: ## stop the Docker stack
	docker compose down

docker-logs: ## follow the server logs
	docker compose logs -f app

migrate: ## apply db/migrations
	./scripts/migrate.sh up

migrate-down: ## revert the latest migration
	./scripts/migrate.sh down

run: ## run the server
	go run ./cmd/server

build: ## build bin/server
	mkdir -p bin
	go build -o bin/server ./cmd/server

test: ## go test ./...
	go test ./...

check: ## go build + go vet + go test
	go build ./...
	go vet ./...
	go test ./...

check-queries: ## validate SQL files and layering rules
	./scripts/check_queries.sh

smoke: ## curl every endpoint against a running server
	./scripts/smoke.sh
