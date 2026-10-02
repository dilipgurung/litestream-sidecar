BIN       := litestream-sidecar
CMD_DIR   := ./cmd/example
OUT_DIR   := ./bin
GO        := go
GO_FLAGS  :=

.PHONY: all build release run test test-integration test-all test-race test-cover vet lint fmt fmt-check tidy tidy-check clean help

## all: run all checks (vet, tidy check, test, race detector)
all: vet tidy-check test test-race

## build: compile the example binary
build:
	@mkdir -p $(OUT_DIR)
	$(GO) build $(GO_FLAGS) -o $(OUT_DIR)/$(BIN) $(CMD_DIR)

## release: compile with debug info stripped (smaller binary)
release:
	@mkdir -p $(OUT_DIR)
	$(GO) build $(GO_FLAGS) -ldflags="-s -w" -o $(OUT_DIR)/$(BIN) $(CMD_DIR)

## run: build and run the example (set DB_PATH, LITESTREAM_REPLICA_URL etc. via env)
run: build
	$(OUT_DIR)/$(BIN)

## test: run unit tests (fast, no external deps needed)
test:
	$(GO) test $(GO_FLAGS) ./...

## test-integration: run integration tests (requires litestream + sqlite3 CLIs)
test-integration:
	$(GO) test $(GO_FLAGS) -tags=integration -count=1 -v ./

## test-all: run unit + integration tests
test-all: test test-integration

## test-race: run unit tests with the race detector
test-race:
	$(GO) test $(GO_FLAGS) -race -count=1 ./...

## test-cover: run unit tests with coverage report
test-cover:
	@mkdir -p $(OUT_DIR)
	$(GO) test $(GO_FLAGS) -coverprofile=$(OUT_DIR)/coverage.out ./...
	$(GO) tool cover -html=$(OUT_DIR)/coverage.out -o $(OUT_DIR)/coverage.html
	@$(GO) tool cover -func=$(OUT_DIR)/coverage.out | tail -5

## vet: run go vet
vet:
	$(GO) vet ./...

## tidy: ensure go.mod and go.sum are clean
tidy:
	$(GO) mod tidy

## tidy-check: fail if go.mod or go.sum need updating
tidy-check:
	$(GO) mod tidy
	@if ! git diff --quiet -- go.mod go.sum 2>/dev/null; then \
		echo "❌ go.mod or go.sum are dirty after 'go mod tidy'. Run 'make tidy' and commit."; \
		exit 1; \
	fi
	@echo "✓ go.mod and go.sum are clean"

## lint: run golangci-lint if available, otherwise fall back to vet
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed — falling back to 'go vet'"; \
		$(GO) vet ./...; \
	fi

## fmt: run gofmt on all Go source files
fmt:
	gofmt -w .

## fmt-check: fail if any Go source file is not gofmt-formatted
fmt-check:
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "❌ The following files are not gofmt-formatted:"; \
		gofmt -l . | sed 's/^/  /'; \
		exit 1; \
	fi
	@echo "✓ All files are gofmt-formatted"

## clean: remove build artifacts
clean:
	rm -rf $(OUT_DIR)

## help: print this help message
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^## //p' ${MAKEFILE_LIST} | column -t -s ':'
	@echo ""
	@echo "Variables:"
	@echo "  GO       = $(GO)"
	@echo "  BIN      = $(BIN)"
	@echo "  OUT_DIR  = $(OUT_DIR)"
