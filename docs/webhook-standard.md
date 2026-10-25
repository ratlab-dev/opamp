# OpAMP Audit Webhook Standard (Specification v1.0)

This document specifies the standard payload format, HTTP headers, authentication, and behavior for audit webhooks dispatched by the OpAMP management server whenever configuration changes occur.

---

## 1. Overview

The OpAMP server dispatches HTTP `POST` requests to registered webhook endpoints upon any configuration modification, including:
- Creation, update, revocation, or expiration of temporary Redis TTL overrides.
- Permanent configuration saves (in development mode).
- Agent remote configuration offers and synchronization events.

---

## 2. HTTP Request Headers

| Header Name | Type | Description |
|---|---|---|
| `Content-Type` | String | `application/json` |
| `X-OpAMP-Audit-Event` | String | Unique event identifier (e.g., `temporary_override.created`) |
| `X-OpAMP-Audit-ID` | String | Unique UUIDv4 assigned to this specific event |
| `X-OpAMP-Audit-Timestamp` | String | UTC ISO 8601 timestamp of event generation |
| `X-OpAMP-Signature` | String | HMAC-SHA256 signature in hex: `sha256=<hex_digest>` |

---

## 3. HMAC-SHA256 Signature Verification

If a webhook secret is configured (`-webhook-secret` or `WEBHOOK_SECRET`), the server signs the raw JSON request body using HMAC-SHA256.

### Verifying Signature in Go:
```go
func VerifyWebhookSignature(payload []byte, secret string, signatureHeader string) bool {
    expectedPrefix := "sha256="
    if !strings.HasPrefix(signatureHeader, expectedPrefix) {
        return false
    }
    providedSig := signatureHeader[len(expectedPrefix):]

    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(payload)
    computedSig := hex.EncodeToString(mac.Sum(nil))

    return hmac.Equal([]byte(computedSig), []byte(providedSig))
}
```

### Verifying Signature in Python:
```python
import hmac, hashlib

def verify_signature(payload_bytes: bytes, secret: str, header_value: str) -> bool:
    if not header_value.startswith("sha256="):
        return False
    signature = header_value.split("=", 1)[1]
    expected = hmac.new(secret.encode(), payload_bytes, hashlib.sha256).hexdigest()
    return hmac.compare_digest(signature, expected)
```

---

## 4. Standard Event Payload Schema

```json
{
  "spec_version": "1.0",
  "id": "e2c39d89-b847-49f3-8b7c-0a612502ef40",
  "timestamp": "2026-10-10T04:45:00Z",
  "event_type": "temporary_override.created",
  "actor": {
    "identity": "alice@ops.internal",
    "ip_address": "10.0.4.12"
  },
  "target": {
    "hostname": "prod-db-01",
    "environment": "prod",
    "role": "database",
    "cluster": "us-east-1",
    "subdomain": "",
    "path": "redis:override:prod-db-01"
  },
  "action": "create_temporary_override",
  "details": {
    "ttl_seconds": 21600,
    "expires_at": "2026-10-10T10:45:00Z",
    "reason": "INC-4029: Enable debug logging for slow query triage",
    "previous_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "new_hash": "7a35b1d44c8f5336e1b6f6990d0b046a50672e81129e7943580a6d9ec4b51829",
    "diff_snippet": "service:\n  telemetry:\n    logs:\n      level: debug"
  }
}
```

---

## 5. Event Types Catalog

| Event Type | Description |
|---|---|
| `temporary_override.created` | An operator created a temporary override in Redis with a TTL |
| `temporary_override.updated` | An existing temporary override was updated with new configuration or extended TTL |
| `temporary_override.deleted` | An operator manually revoked a temporary override before its TTL expired |
| `temporary_override.expired` | A temporary override reached its TTL and was automatically expired |
| `config.modified` | Direct file write to configuration store (in development mode) |
| `agent.config_synced` | An agent received an updated configuration bundle |

---

## 6. Retry and Delivery Policy

1. **Timeout**: HTTP requests timeout after 5 seconds by default.
2. **Asynchronous Dispatch**: Webhook calls run in dedicated goroutines and do not block the UI or agent control loop.
3. **Audit History Ring Buffer**: The last 100 audit events are persisted in memory and accessible via `GET /api/audit` and the UI dashboard.
