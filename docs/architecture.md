# OpAMP Architecture & Precedence Specification

This document details the architecture, configuration merging hierarchy, operating modes, and reproducibility guarantees of the OpAMP server.

---

## 1. Order of Override & Precedence

When rendering an effective configuration for an OpenTelemetry Collector or agent, the OpAMP server applies a strict 8-level precedence order:

```
[1] Base (Fleet Defaults: base.yaml)
  ↓
[2] Environment (e.g. environments/prod.yaml)
  ↓
[3] Cluster (e.g. clusters/us-east-1.yaml)
  ↓
[4] Subdomain (e.g. subdomains/payments.yaml)
  ↓
[5] Role (e.g. roles/database.yaml)
  ↓
[6] Environment-Role (e.g. environments/prod/roles/database.yaml)
  ↓
[7] Hostname (e.g. hosts/prod-db-01.yaml)
  ↓
[8] Temporary Redis Override (Highest Precedence - Enforced TTL)
```

### Precedence Rules:
1. **Base Configuration (`base.yaml`)**:
   - Defines the baseline OTLP receivers (`grpc: 4317`, `http: 4318`), default processors (`memory_limiter`, `batch`), and telemetry logging settings.
2. **Environment Overlays (`environments/<env>.yaml`)**:
   - Overrides exporter destinations (e.g. production vs staging backend gateways), adjusts batch timeouts and telemetry log levels.
3. **Cluster Overlays (`clusters/<cluster>.yaml`)**:
   - Adds regional resource attributes (e.g. AWS EC2, GCP, or Kubernetes cluster tags) and zone routing.
4. **Subdomain Overlays (`subdomains/<subdomain>.yaml`)**:
   - Adds compliance scrubbing or custom subdomain routing filters.
5. **Role Overlays (`roles/<role>.yaml`)**:
   - Injects workload-specific scrape receivers (e.g., PostgreSQL for `database`, Hostmetrics for `node-exporter`, Prometheus & log scraper for `clickhouse`).
6. **Environment-Role Overlays (`environments/<env>/roles/<role>.yaml`)**:
   - Allows fine-tuning a specific role within a specific environment (e.g., lower scrape intervals in production vs dev).
7. **Hostname Overlays (`hosts/<hostname>.yaml`)**:
   - Host-specific pin settings (e.g., custom local telemetry scrape port).
8. **Temporary Redis TTL Overrides (`redis:override:<target>`)**:
   - **Highest Precedence**: Overrides any matching key in GitOps.
   - Enforces an automatic Time-To-Live (TTL, e.g. 1h, 6h, 12h, 24h).
   - Once expired or revoked by an operator in the UI, configuration instantly and safely rolls back to GitOps.

---

## 2. Reproducible Rendering & Zero-Diff Restart Prevention

OpenTelemetry Collectors running in VMs or Kubernetes pods should **never restart** unless their effective configuration actually changes.

### How the Server Guarantees Zero-Diff:
1. **Canonical Map Sorting**:
   - All dictionary keys are marshaled in alphabetical order using `gopkg.in/yaml.v3`.
2. **Stable Cryptographic Hashing**:
   - The server computes `SHA-256(canonical_yaml_bytes)`.
   - Identical configuration inputs produce identical byte-for-byte SHA-256 hashes on every run.
3. **OpAMP RemoteConfigStatus Check**:
   - When an agent connects, it reports its `last_remote_config_hash`.
   - If `agent.last_remote_config_hash == computed_hash`:
     - The server responds with `RemoteConfig = nil`.
     - No configuration payload is transferred over the wire.
     - The collector is not commanded to restart or reload.

---

## 3. Storage Providers

### Local Directory Store (`LocalStore`)
- Used for local development and offline environments.
- Directory layout:
  - `base.yaml`
  - `environments/{prod,staging,dev}.yaml`
  - `clusters/{us-east-1}.yaml`
  - `subdomains/{payments}.yaml`
  - `roles/{database,node-exporter,clickhouse}.yaml`
  - `hosts/{hostname}.yaml`

### Kubernetes ConfigMaps Store (`ConfigMapStore`)
- In production, multiple Kubernetes ConfigMaps can be mounted as volumes to the container's filesystem (e.g., `/etc/opamp/configmaps/base`, `/etc/opamp/configmaps/roles`).
- The server scans all mounted volumes to assemble the hierarchy.

### Gitea GitOps Store (`GiteaClient` / `GitStore`)
- Fetches configurations directly from Gitea via REST API (`/api/v1/repos/{owner}/{repo}/raw/{path}`).
- Provides direct deep-links in the UI to open files in Gitea's web editor for GitOps PR workflows.
- Extensible to GitLab and GitHub.

---

## 4. Operating Modes

### Development Mode (`-mode development`)
- Allows operators to read and write directly to the local configuration repository from the UI.
- Direct filesystem writes are audited via webhooks.

### Production Mode (`-mode production`)
- Configuration filesystem and Git repositories are **strictly read-only**.
- Direct configuration writes are rejected with HTTP 403 Forbidden.
- The UI guides operators on making permanent changes via GitOps pull requests.
- Operators may only apply **TTL-based temporary overrides** stored in Redis.
