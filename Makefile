.DEFAULT_GOAL := help

ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: s3-cors
s3-cors: ## Allow browser uploads to S3_BUCKET (edit deploy/s3-cors.json for your origins)
	aws s3api put-bucket-cors --bucket $(S3_BUCKET) --cors-configuration file://deploy/s3-cors.json
	aws s3api get-bucket-cors --bucket $(S3_BUCKET)

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
