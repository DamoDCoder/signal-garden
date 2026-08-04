.PHONY: help test test-race run demo build fmt vet check clean

# M0 needs only test and run. The proto, dev, reset, load, and replay targets
# in docs/local-development.md arrive with the milestone that makes them
# meaningful, rather than sitting here failing.

help: ## Show available targets
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests under the race detector
	go test -race ./...

run: ## Run a simulation with default controls
	go run ./cmd/signalgarden

demo: ## Run three contrasting scenarios back to back
	@echo "=== balanced ==="
	@go run ./cmd/signalgarden -seed 42 -ticks 40
	@echo "=== pest storm ==="
	@go run ./cmd/signalgarden -seed 42 -ticks 40 -rate 20 -rain 1 -growth 1 -pest 4
	@echo "=== pest storm, every event delivered twice ==="
	@go run ./cmd/signalgarden -seed 42 -ticks 40 -rate 20 -rain 1 -growth 1 -pest 4 -duplicate-every 1

build: ## Build the CLI into bin/
	go build -o bin/signalgarden ./cmd/signalgarden

fmt: ## Format all Go source
	go fmt ./...

vet: ## Run go vet
	go vet ./...

check: fmt vet test-race ## Format, vet, and test with the race detector

clean: ## Remove build output
	rm -rf bin/
