# Build stage
FROM golang:alpine AS builder

WORKDIR /app

# Install git and ca-certificates if needed for module fetching
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source tree
COPY . .

# Compile static binary for Linux
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/bin/opamp-server ./cmd/server

# Final runtime image
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/bin/opamp-server /app/opamp-server

# Copy default configuration directory
COPY examples/configs /app/configs

# Expose OpAMP server port (UI & WebSocket agent endpoint)
EXPOSE 4320

ENV OPAMP_PORT=4320
ENV OPAMP_MODE=development
ENV OPAMP_CONFIG_DIR=/app/configs

ENTRYPOINT ["/app/opamp-server"]
