package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// WebhookConfig contains webhook URL and optional HMAC secret
type WebhookConfig struct {
	URL     string        `json:"url"`
	Secret  string        `json:"secret"`
	Timeout time.Duration `json:"timeout"`
}

// Dispatcher dispatches audit events to webhooks and keeps an in-memory buffer
type Dispatcher struct {
	cfg        WebhookConfig
	httpClient *http.Client
	mu         sync.RWMutex
	history    []*AuditEvent
	maxHistory int
}

func NewDispatcher(cfg WebhookConfig) *Dispatcher {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &Dispatcher{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		history:    make([]*AuditEvent, 0, 100),
		maxHistory: 100,
	}
}

// Publish stores the event in memory and dispatches to webhook URL if configured
func (d *Dispatcher) Publish(ctx context.Context, event *AuditEvent) error {
	// Store in memory buffer
	d.mu.Lock()
	d.history = append([]*AuditEvent{event}, d.history...)
	if len(d.history) > d.maxHistory {
		d.history = d.history[:d.maxHistory]
	}
	d.mu.Unlock()

	// If no webhook URL configured, we are done
	if d.cfg.URL == "" {
		return nil
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal audit event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OpAMP-Audit-Event", string(event.EventType))
	req.Header.Set("X-OpAMP-Audit-ID", event.ID)
	req.Header.Set("X-OpAMP-Audit-Timestamp", event.Timestamp)

	if d.cfg.Secret != "" {
		sig := ComputeSignature(payload, d.cfg.Secret)
		req.Header.Set("X-OpAMP-Signature", "sha256="+sig)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook delivery failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook responded with non-2xx status: %d", resp.StatusCode)
	}

	return nil
}

// PublishAsync dispatches in the background without blocking callers
func (d *Dispatcher) PublishAsync(event *AuditEvent) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), d.cfg.Timeout)
		defer cancel()
		_ = d.Publish(ctx, event)
	}()
}

// GetRecentEvents returns up to limit recent audit events
func (d *Dispatcher) GetRecentEvents(limit int) []*AuditEvent {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 || limit > len(d.history) {
		limit = len(d.history)
	}

	result := make([]*AuditEvent, limit)
	copy(result, d.history[:limit])
	return result
}
