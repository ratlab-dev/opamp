package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// EventType categorizes audit actions
type EventType string

const (
	EventTemporaryOverrideCreated EventType = "temporary_override.created"
	EventTemporaryOverrideUpdated EventType = "temporary_override.updated"
	EventTemporaryOverrideDeleted EventType = "temporary_override.deleted"
	EventTemporaryOverrideExpired EventType = "temporary_override.expired"
	EventConfigModified           EventType = "config.modified"
	EventAgentConfigSynced        EventType = "agent.config_synced"
)

// Actor identifies who triggered the action
type Actor struct {
	Identity  string `json:"identity"`
	IPAddress string `json:"ip_address,omitempty"`
}

// Target identifies what collector or configuration path is impacted
type Target struct {
	Hostname    string `json:"hostname,omitempty"`
	Environment string `json:"environment,omitempty"`
	Role        string `json:"role,omitempty"`
	Cluster     string `json:"cluster,omitempty"`
	Subdomain   string `json:"subdomain,omitempty"`
	Path        string `json:"path,omitempty"`
}

// EventDetails contains change specifics, hashes, and TTL
type EventDetails struct {
	TTLSeconds   int64  `json:"ttl_seconds,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Reason       string `json:"reason,omitempty"`
	PreviousHash string `json:"previous_hash,omitempty"`
	NewHash      string `json:"new_hash,omitempty"`
	DiffSnippet  string `json:"diff_snippet,omitempty"`
}

// AuditEvent is the standardized audit event structure
type AuditEvent struct {
	SpecVersion string       `json:"spec_version"`
	ID          string       `json:"id"`
	Timestamp   string       `json:"timestamp"`
	EventType   EventType    `json:"event_type"`
	Actor       Actor        `json:"actor"`
	Target      Target       `json:"target"`
	Action      string       `json:"action"`
	Details     EventDetails `json:"details"`
}

// NewEvent creates a standardized AuditEvent with timestamp and unique UUID
func NewEvent(eventType EventType, action string, actor Actor, target Target, details EventDetails) *AuditEvent {
	return &AuditEvent{
		SpecVersion: "1.0",
		ID:          uuid.New().String(),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		EventType:   eventType,
		Actor:       actor,
		Target:      target,
		Action:      action,
		Details:     details,
	}
}

// ComputeSignature calculates HMAC-SHA256 of the payload given a secret key
func ComputeSignature(payload []byte, secret string) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
