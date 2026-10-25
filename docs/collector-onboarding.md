# Collector Onboarding Guide

This document provides step-by-step guidance for onboarding OpenTelemetry Collectors, Prometheus Node Exporters, ClickHouse collectors, and custom agents into the OpAMP management control plane.

---

## 1. How OpAMP Works with Collectors

The **OpenTelemetry Agent Management Protocol (OpAMP)** enables centralized fleet configuration management for OpenTelemetry collectors. 

Collectors connect over WebSocket (`ws://<opamp-server>:4320/v1/opamp`) or HTTP (`http://<opamp-server>:4320/v1/opamp`).

### Zero-Diff Guarantee
Target VMs and pods **do not restart** unless the effective configuration hash changes:
1. The server compiles the effective configuration from base + overlays + temporary Redis overrides.
2. The server computes a deterministic SHA-256 hash.
3. If the agent's reported `last_remote_config_hash` matches the computed hash, the server sends **no configuration update**, guaranteeing zero restart cycles.

---

## 2. Onboarding Standard OpenTelemetry Collector

### Method A: Using the OpAMP Supervisor (Recommended)
The official OpAMP supervisor manages the lifecycle of the collector binary and applies configuration updates on the fly without downtime.

1. **Install OpAMP Supervisor & OTEL Collector**:
   ```bash
   curl -L https://github.com/open-telemetry/opamp-go/releases/latest/download/opamp-supervisor-linux-amd64 -o /usr/local/bin/opamp-supervisor
   chmod +x /usr/local/bin/opamp-supervisor
   ```

2. **Configure `/etc/opamp/supervisor.yaml`**:
   ```yaml
   server:
     endpoint: "ws://opamp-server.internal:4320/v1/opamp"

   capabilities:
     reports_effective_config: true
     accepts_remote_config: true

   agent:
     executable: "/usr/local/bin/otelcol-contrib"
     description:
       identifying_attributes:
         service.name: "io.opentelemetry.collector"
         host.name: "prod-db-01"
       non_identifying_attributes:
         deployment.environment: "prod"
         service.role: "database"
         k8s.cluster.name: "us-east-1"
   ```

3. **Start the Supervisor via Systemd**:
   ```bash
   sudo systemctl enable --now opamp-supervisor
   ```

---

## 3. Onboarding Prometheus Node Exporter

Instead of maintaining a separate standalone Node Exporter daemon, OpenTelemetry Collector natively supports node host metrics via the `hostmetrics` receiver.

### Configuration Hierarchy Overlay: `roles/node-exporter.yaml`
```yaml
receivers:
  hostmetrics:
    collection_interval: 10s
    scrapers:
      cpu:
      disk:
      filesystem:
      load:
      memory:
      network:
      process:

service:
  pipelines:
    metrics:
      receivers: [hostmetrics]
```

### Steps to Onboard a Host as a Node Exporter:
1. Deploy OpenTelemetry Collector on the target VM.
2. Set agent attribute `service.role: "node-exporter"` in the agent startup flags or supervisor config:
   ```yaml
   non_identifying_attributes:
     service.role: "node-exporter"
     host.name: "app-worker-01"
     deployment.environment: "prod"
   ```
3. The OpAMP server automatically applies:
   - `base.yaml` (OTLP pipelines, batch processors, exporters)
   - `environments/prod.yaml` (Production backend endpoints)
   - `roles/node-exporter.yaml` (Hostmetrics scrapers)
4. Result: Host CPU, Memory, Disk, and Network telemetry are scraped and streamed automatically.

---

## 4. Onboarding ClickHouse Collector

ClickHouse instances expose Prometheus metrics on port `9363` (by default) and output structured queries and error logs to `/var/log/clickhouse-server/`.

### Configuration Hierarchy Overlay: `roles/clickhouse.yaml`
```yaml
receivers:
  prometheus/clickhouse:
    config:
      scrape_configs:
        - job_name: 'clickhouse-server'
          scrape_interval: 10s
          static_configs:
            - targets: ['localhost:9363']

  filelog/clickhouse:
    include: [/var/log/clickhouse-server/clickhouse-server.log]
    operators:
      - type: regex_parser
        regex: '^(?P<time>[0-9]{4}\.[0-9]{2}\.[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]+) \[(?P<thread_id>[0-9]+)\] <(?P<level>[A-Za-z]+)> (?P<logger>[A-Za-z0-9_]+): (?P<message>.*)$'
        timestamp:
          parse_from: attributes.time
          layout: '2006.01.02 15:04:05.000000'

service:
  pipelines:
    metrics:
      receivers: [prometheus/clickhouse]
    logs:
      receivers: [filelog/clickhouse]
```

### Enabling ClickHouse Prometheus Endpoint:
Ensure `/etc/clickhouse-server/config.xml` has:
```xml
<prometheus>
    <endpoint>/metrics</endpoint>
    <port>9363</port>
    <metrics>true</metrics>
    <events>true</events>
    <asynchronous_metrics>true</asynchronous_metrics>
</prometheus>
```

### Steps to Onboard:
1. Set the agent's role attribute to `service.role: "clickhouse"`.
2. The OpAMP server merges the ClickHouse metrics scraper and structured log tailer into the collector's configuration.

---

## 5. Onboarding Custom Go / Python Agents

Custom applications can integrate directly with the OpAMP server using the Go SDK (`github.com/open-telemetry/opamp-go/client`):

```go
package main

import (
	"context"
	"log"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func main() {
	opampClient := client.NewWebSocket(nil)
	
	settings := types.StartSettings{
		OpAMPServerURL: "ws://opamp-server.internal:4320/v1/opamp",
		InstanceUid:    []byte("custom-agent-01"),
		RemoteConfigStatus: &protobufs.RemoteConfigStatus{
			Status: protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		},
		Callbacks: types.Callbacks{
			OnMessage: func(ctx context.Context, msg *types.MessageData) {
				if msg.RemoteConfig != nil {
					log.Printf("Received new config with hash: %x", msg.RemoteConfig.ConfigHash)
				}
			},
		},
	}

	if err := opampClient.Start(context.Background(), settings); err != nil {
		log.Fatalf("Failed to start OpAMP client: %v", err)
	}
}
```

---

## 6. Onboarding macOS Workstations (`mac-work`) with OpenObserve

The `mac-work` role enables system log collection (`/var/log/system.log`) from development or workstation machines with filtered forwarding to OpenObserve.

### OpenObserve Exporter Architecture
OpenObserve is **OpenTelemetry-native** and accepts standard OTLP logs directly over HTTP via the standard `otlp_http` exporter (built into `otelcol-contrib`). No proprietary binary plugin is required.

### Configuration Hierarchy Overlay: `roles/mac-work.yaml`
```yaml
receivers:
  filelog/system_critical:
    include: [/var/log/system.log]
    start_at: end

processors:
  filter/critical_logs:
    error_mode: ignore
    logs:
      log_record:
        - 'severity_number < 17 and not IsMatch(body, "(?i)critical|fatal|emergency|alert|panic")'

exporters:
  otlp_http/openobserve:
    endpoint: http://localhost:5080/api/default
    headers:
      Authorization: "${env:OPENOBSERVE_AUTH}"
      stream-name: "default"
    tls:
      insecure: true
  debug/stdout:
    verbosity: detailed

service:
  pipelines:
    logs:
      receivers: [filelog/system_critical]
      processors: [memory_limiter, filter/critical_logs, batch]
      exporters: [otlp_http/openobserve, debug/stdout]
```

### Running the Stack via Docker Compose
To bring up the OpAMP server alongside an OpenObserve single instance with persistent disk storage:
```bash
make docker-up
# or: docker compose up -d
```

- **OpAMP Web Dashboard**: [http://localhost:4320](http://localhost:4320)
- **OpenObserve Console**: [http://localhost:5080](http://localhost:5080) (`admin@example.com` / `adminpassword`)

### Starting the OpAMP Agent for `mac-work`
```bash
./bin/opamp-agent -server-url ws://127.0.0.1:4320/v1/opamp -role mac-work
```

