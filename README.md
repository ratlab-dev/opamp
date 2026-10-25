# OpAMP Control Plane & OpenTelemetry Collector Supervisor

A lightweight, centralized OpenTelemetry Agent Management Protocol (**OpAMP**) server and supervisor system built in Go.

This repository enables fleet configuration management for OpenTelemetry Collectors with:
- **Hierarchical Configuration Overlays**: Base defaults + Environment + Cluster + Subdomain + Role + Host overrides.
- **Zero-Diff Restarts**: Collectors never reload or restart unless their effective configuration hash actually changes.
- **Live Supervisor Daemon**: Automatically receives remote configurations over WebSocket, validates and writes YAML to disk, and starts/reloads the target OpenTelemetry Collector binary.
- **Temporary Redis TTL Overrides**: Inject temporary configs (e.g., debug log levels, trace sampling) that automatically expire and revert back to GitOps.
- **Interactive Web UI**: Real-time fleet overview, effective configuration viewer, zero-drift verification, and live diff inspection.

---

## Architecture Overview

```
                      +-----------------------------+
                      |     OpAMP Control Plane     |
                      |   (http://localhost:4320)   |
                      +--------------+--------------+
                                     |
                         WebSocket   |  Remote Configs &
                      (v1/opamp)     |  Effective Status
                                     v
                 +---------------------------------------+
                 |          OpAMP Supervisor             |
                 |          (run-supervisor.sh)          |
                 +-------------------+-------------------+
                                     |
                Manages Process      | Writes Config File
                & Auto-Restarts      | (~/.opamp-agent/config.yaml)
                                     v
                       +---------------------------+
                       |  OpenTelemetry Collector  |
                       |     (otelcol-contrib)     |
                       +---------------------------+
```

---

## Prerequisites

1. **Go 1.22+** (if compiling the server or agent manually)
2. **OpenTelemetry Collector Binary** (`otelcol-contrib` or `otelcol`)
   - *macOS (Homebrew)*: `brew install opentelemetry-collector-contrib`
   - *Direct Download*: The included `run-supervisor.sh --download` automatically downloads the latest release for your platform (macOS/Linux, arm64/amd64).
3. **Docker & Docker Compose** (Optional: for running with OpenObserve)

---

## Quick Start Guide

### Step 1: Start the OpAMP Control Plane Server

Choose either local mode or Docker Compose:

#### Option A: Local Development (Recommended for quick testing)
```bash
# Build and run the server using example local GitOps configuration overlays
make run
```
The OpAMP server will start on `http://localhost:4320`, with WebSocket endpoint at `ws://127.0.0.1:4320/v1/opamp`.

#### Option B: Docker Compose (Includes OpenObserve Log/Metric UI)
```bash
make docker-up
```
- OpAMP Dashboard: [http://localhost:4320](http://localhost:4320)
- OpenObserve UI: [http://localhost:5080](http://localhost:5080) (`admin@example.com` / `adminpassword`)

---

### Step 2: Obtain an OpenTelemetry Collector Binary

You need an OpenTelemetry Collector executable (typically `otelcol-contrib`). Choose any of the following:

- **Automatic Download via helper script**:
  ```bash
  ./run-supervisor.sh --download
  ```
  *(Downloads the matching official binary into `./bin/collector/otelcol-contrib`)*

- **macOS via Homebrew**:
  ```bash
  brew install opentelemetry-collector-contrib
  ```

- **Linux / Manual Download**:
  Download `otelcol-contrib` tarball from the [OpenTelemetry Collector Releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases).

---

### Step 3: Run the Supervisor to Start the OTEL Collector

Use the `run-supervisor.sh` script. The script automatically builds `./bin/opamp-agent` if it has not been compiled yet, verifies your collector binary, connects to the OpAMP server, receives the effective configuration, and starts the collector process.

#### 1. Simplest Usage (Given Binary Location as Positional Argument)
```bash
./run-supervisor.sh /usr/local/bin/otelcol-contrib
```
or if using Homebrew on Apple Silicon:
```bash
./run-supervisor.sh /opt/homebrew/bin/otelcol-contrib
```

#### 2. Using Options / Flags
```bash
./run-supervisor.sh -b /path/to/otelcol-contrib -r mac-dev -e dev
```

#### 3. Using Environment Variables
```bash
export OTEL_BIN="/usr/local/bin/otelcol-contrib"
export ROLE="mac-dev"
export OPAMP_SERVER_URL="ws://127.0.0.1:4320/v1/opamp"

./run-supervisor.sh
```

#### 4. Using the Makefile
```bash
make run-supervisor OTEL_BIN=/path/to/otelcol-contrib
```

#### 5. Simulation Mode (No Collector Binary)
If you run `./run-supervisor.sh` without a binary, the supervisor runs in **simulation mode**: it connects to the server, receives and applies configuration updates to `~/.opamp-agent/config.yaml`, and logs state transitions without spawning a child process.

---

## `run-supervisor.sh` Script Reference

The `run-supervisor.sh` script wraps the supervisor agent with user-friendly argument parsing, automatic binary discovery, and compilation.

### Usage Syntax
```bash
./run-supervisor.sh [options] [collector-binary-path]
```

### Options

| Flag | Description | Default |
| :--- | :--- | :--- |
| `[collector-binary-path]` | Positional path to OpenTelemetry Collector binary | Autodetected from `bin/collector/` or `$PATH` |
| `-b`, `--bin`, `--collector <path>` | Explicit path to OpenTelemetry Collector binary | None (or `$OTEL_BIN`) |
| `-s`, `--server <url>` | OpAMP WebSocket server endpoint | `ws://127.0.0.1:4320/v1/opamp` |
| `-r`, `--role <role>` | Topology role overlay | `mac-dev` |
| `-e`, `--env <env>` | Target environment (`dev`, `staging`, `prod`) | `dev` |
| `-c`, `--cluster <name>` | Cluster identifier | `local` |
| `-d`, `--subdomain <name>` | Optional subdomain scope | `""` |
| `-n`, `--hostname <name>` | Host identifier | System hostname |
| `-p`, `--config <path>` | Destination path for received collector YAML config | `~/.opamp-agent/config.yaml` |
| `--supervisor-bin <path>` | Custom path to the supervisor agent binary | `./bin/opamp-agent` (auto-built if missing) |
| `--download` | Automatically downloads the latest `otelcol-contrib` for current OS/Arch into `./bin/collector/` | - |
| `-h`, `--help` | Prints help and usage examples | - |

### Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `OTEL_BIN` | Path to collector binary | `""` |
| `OPAMP_SERVER_URL` | WebSocket server URL | `ws://127.0.0.1:4320/v1/opamp` |
| `ROLE` | Role overlay (`mac-dev`, `node-exporter`, `database`, `clickhouse`, `mac-work`) | `mac-dev` |
| `ENV` | Environment overlay name | `dev` |
| `CLUSTER` | Cluster overlay name | `local` |
| `CONFIG_PATH` | Managed collector config path | `~/.opamp-agent/config.yaml` |
| `SUPERVISOR_BIN` | Supervisor executable binary path | `./bin/opamp-agent` |

---

## How the Supervisor Works

1. **Persistent Identity**:
   Upon first launch, the supervisor generates a unique persistent UUID in `~/.opamp-agent/instance_id`.
2. **WebSocket Handshake**:
   The supervisor opens a WebSocket connection to the server (`/v1/opamp`) and reports identifying attributes (`service.name`, `host.name`) and topology attributes (`service.role`, `deployment.environment`, `k8s.cluster.name`).
3. **Config Assembly & Hash Comparison (Zero-Diff Guarantee)**:
   - The OpAMP server compiles the effective YAML from `base.yaml` + overlays.
   - The server computes the canonical SHA-256 hash.
   - If the supervisor reports that its applied config already has this exact hash, the server sends `RemoteConfig = nil`. **The collector does not restart.**
4. **Applying Configuration Updates**:
   When an updated hash arrives:
   - The supervisor writes the new YAML to `--config-path` (`~/.opamp-agent/config.yaml`).
   - If managing an active collector process, the supervisor safely terminates the old PID and executes:
     ```bash
     <otel-bin> --config ~/.opamp-agent/config.yaml
     ```
   - It streams the collector's stdout/stderr to the supervisor terminal.
   - It reports `APPLIED` status along with the new hash back to the server.
5. **Graceful Shutdown**:
   On receiving `SIGINT` or `SIGTERM` (e.g., `Ctrl+C`), the supervisor terminates the child OpenTelemetry Collector process cleanly before closing the OpAMP connection.

---

## Verifying in the Web Dashboard

1. Open **[http://localhost:4320](http://localhost:4320)** in your browser.
2. In the **Fleet Overview** table, you will see your connected agent with:
   - Status: `APPLIED` (Green badge)
   - Role: `mac-dev` (or selected role)
   - Effective Config Hash
   - Zero-Drift indicator
3. Click on the agent row to view the full rendered configuration.
4. **Test Temporary Redis TTL Overrides**:
   - In the dashboard, click **Add Temporary Override**.
   - Select a TTL (e.g., `1h`, `6h`, `24h`) and supply an override snippet (e.g., setting log level to `debug`).
   - Watch the supervisor console: the supervisor immediately receives the new config, updates the config file, and restarts the collector.
   - When the TTL expires or when you click **Revoke**, the configuration automatically rolls back to the GitOps baseline with zero manual intervention.

---

## Available Example Roles

The `./examples/configs/roles/` directory contains sample overlays:

- **`mac-dev`**: macOS log collection from `/var/log/system.log`, `/var/log/wifi.log`, and DiagnosticReports, forwarding to OpenObserve via `otlp_http`.
- **`node-exporter`**: Hostmetrics scraper (CPU, memory, disk, network, load, filesystem).
- **`database`**: PostgreSQL scrapers and database metrics.
- **`clickhouse`**: ClickHouse performance scraping and pipeline routing.

To run with any role:
```bash
./run-supervisor.sh -b /path/to/otelcol-contrib -r node-exporter
```

---

## Troubleshooting

### 1. Collector Binary Permission Denied
Ensure the binary has executable permissions:
```bash
chmod +x /path/to/otelcol-contrib
```
The `run-supervisor.sh` script automatically checks and adds execution permissions if required.

### 2. Connection Refused to Server (`ws://127.0.0.1:4320/v1/opamp`)
Make sure the OpAMP server is running:
```bash
make run
# or
make docker-up
```
Test port reachability:
```bash
curl -I http://127.0.0.1:4320/
```

### 3. File Log Permissions on macOS (`/var/log/system.log`)
If running the `mac-dev` role on macOS, `/var/log/system.log` may require elevated read access or Full Disk Access permissions in **System Settings > Privacy & Security > Full Disk Access** for your terminal.

---

## Development & Testing

```bash
# Run unit tests and Godog BDD test suites
make test

# Build both server and agent binaries
make build-all

# Clean binaries
make clean
```
