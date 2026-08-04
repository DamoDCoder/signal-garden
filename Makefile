.PHONY: help tools proto test test-race run live serve demo build fmt vet check clean

# The dev, reset, load, and replay targets in docs/local-development.md arrive
# with the milestone that makes them meaningful, rather than sitting here
# failing.

MODULE := github.com/damodbear/signal-garden
TOOLS := bin/tools
PROTO_FILES := $(shell find proto -name '*.proto')

help: ## Show available targets
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

tools: ## Build the pinned code generators into bin/tools
	@mkdir -p $(TOOLS)
	go build -o $(TOOLS)/ tool

# Plugin versions are pinned by the tool directives in go.mod, so generation is
# reproducible from a clean checkout. protoc itself and the vendored googleapis
# protos in third_party/ are the only inputs from outside the module.
proto: tools ## Generate Go gRPC and REST gateway code from proto/
	protoc -I proto -I third_party \
		--plugin=protoc-gen-go=$(TOOLS)/protoc-gen-go \
		--plugin=protoc-gen-go-grpc=$(TOOLS)/protoc-gen-go-grpc \
		--plugin=protoc-gen-grpc-gateway=$(TOOLS)/protoc-gen-grpc-gateway \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
		--grpc-gateway_out=. --grpc-gateway_opt=module=$(MODULE) \
		$(PROTO_FILES)

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests under the race detector
	go test -race ./...

run: ## Run a simulation with default controls
	go run ./cmd/signalgarden

live: ## Run live: a frame per tick, steered by typed commands
	go run ./cmd/signalgarden -live -ticks 0 -interval 300ms

serve: ## Serve gRPC on :9090 and the generated REST gateway on :8080
	go run ./cmd/signalgardend

demo: ## Run three contrasting scenarios back to back
	@echo "=== balanced ==="
	@go run ./cmd/signalgarden -seed 42 -ticks 40
	@echo "=== pest storm ==="
	@go run ./cmd/signalgarden -seed 42 -ticks 40 -rate 20 -rain 1 -growth 1 -pest 4
	@echo "=== pest storm, every event delivered twice ==="
	@go run ./cmd/signalgarden -seed 42 -ticks 40 -rate 20 -rain 1 -growth 1 -pest 4 -duplicate-every 1

build: ## Build the CLI and the server into bin/
	go build -o bin/signalgarden ./cmd/signalgarden
	go build -o bin/signalgardend ./cmd/signalgardend

fmt: ## Format all Go source
	go fmt ./...

vet: ## Run go vet
	go vet ./...

check: fmt vet test-race ## Format, vet, and test with the race detector

clean: ## Remove build output
	rm -rf bin/
