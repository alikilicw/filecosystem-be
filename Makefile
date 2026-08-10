.DEFAULT_GOAL := help

ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: infra-up
infra-up: ## Start mongodb and rabbitmq
	docker compose up -d

.PHONY: infra-down
infra-down: ## Stop the infrastructure containers
	docker compose down

.PHONY: infra-reset
infra-reset: ## Stop the infrastructure and delete its volumes
	docker compose down -v

.PHONY: apps-up
apps-up: ## Build and run the API and workers in containers
	docker compose --profile apps up -d --build

.PHONY: logs
logs: ## Follow container logs
	docker compose --profile apps logs -f

.PHONY: run
run: ## Run the API from source
	go run ./cmd/api

.PHONY: build
build: ## Compile the API binary into bin/
	go build -o bin/api ./cmd/api

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: tidy
tidy: ## Sync go.mod and go.sum
	go mod tidy

.PHONY: vet
vet: ## Run go vet
	go vet ./...
