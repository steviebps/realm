# realm — developer tasks
# Run `make help` for a summary.

BINARY := realm
UI_DIR  := http/realm-ui

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary (UI not embedded)
	go build -o $(BINARY) .

.PHONY: ui
ui: ## Build the React UI (produces $(UI_DIR)/dist)
	cd $(UI_DIR) && npm ci && npm run build

.PHONY: ui-test
ui-test: ## Lint and test the React UI (tests need Node 22.18 or later)
	cd $(UI_DIR) && npm ci && npm run lint && npm test

.PHONY: build-ui
build-ui: ui ## Build the UI, then the binary with the UI embedded
	go build -ldflags "-s -w" -tags=ui -o $(BINARY) .

.PHONY: run
run: ## Run the server in dev mode (bigcache, port 8080)
	go run . server --dev

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: test-race
test-race: ## Run all tests with the race detector (CI gate)
	go test -race ./...

.PHONY: sdk-test
sdk-test: ## Typecheck and test the TypeScript SDK
	cd sdk/typescript && npm ci && npm run typecheck && npm test && npm run build

.PHONY: e2e
e2e: ## Run scripts/e2e.sh against a freshly built server
	go build -o $(BINARY) .
	cd sdk/typescript && npm ci
	./scripts/e2e.sh ./$(BINARY)

.PHONY: bench
bench: ## Run benchmarks
	go test -run '^$$' -bench . -benchmem ./...

.PHONY: docker
docker: ## Build the Docker image (UI embedded) as realm:dev
	docker build -t realm:dev .

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -w -s .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (see .golangci.yml)
	golangci-lint run

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY) $(BINARY).exe server.log
	rm -rf $(UI_DIR)/dist
