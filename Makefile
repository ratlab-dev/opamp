.PHONY: build run dev test clean docker-build docker-up docker-down docker-logs build-agent build-all run-supervisor

# Build production server binary to bin/
build:
	go build -o ./bin/opamp-server ./cmd/server

# Build OpAMP agent binary to bin/
build-agent:
	go build -o ./bin/opamp-agent ./cmd/agent

# Build both server and agent binaries
build-all: build build-agent

# Run OpAMP supervisor script to manage OpenTelemetry Collector
# Usage: make run-supervisor [OTEL_BIN=/path/to/otelcol] [ROLE=mac-dev] [ENV=dev]
run-supervisor: build-agent
	./run-supervisor.sh $(if $(OTEL_BIN),-b $(OTEL_BIN),)

# Run standard compiled binary
run: build
	./bin/opamp-server -port 4320 -mode development -config-dir ./examples/configs

# Hot-reload development using air
dev:
	air -c .air.toml

# Run all tests (unit tests and Godog BDD features)
test:
	go test -v ./...

# Build Docker image for server
docker-build:
	docker compose build

# Start server and OpenObserve single instance with disk storage
docker-up:
	docker compose up -d

# Stop docker compose stack
docker-down:
	docker compose down

# Follow logs of docker compose services
docker-logs:
	docker compose logs -f

# Clean build artifacts
clean:
	rm -rf ./bin ./tmp
