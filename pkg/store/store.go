package store

import (
	"time"

	"github.com/naugustin/opamp-server/pkg/config"
)

// Mode represents server operating mode
type Mode string

const (
	ModeDevelopment Mode = "development"
	ModeProduction  Mode = "production"
)

// OverrideMetadata stores metadata for an active temporary Redis override
type OverrideMetadata struct {
	Target       string    `json:"target"`
	Path         string    `json:"path"`
	Author       string    `json:"author"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	TTLSeconds   int64     `json:"ttl_seconds"`
	ConfigYAML   string    `json:"config_yaml"`
	PreviousHash string    `json:"previous_hash,omitempty"`
}

// TemporaryOverrideStore manages TTL-based overrides
type TemporaryOverrideStore interface {
	GetOverride(target string) (map[string]interface{}, error)
	SetOverride(target string, cfg map[string]interface{}, ttl time.Duration, meta OverrideMetadata) error
	DeleteOverride(target string) error
	ListOverrides() ([]OverrideMetadata, error)
}

// BaseOverlayStore provides the base and overlay configs from filesystem, Git, or ConfigMaps
type BaseOverlayStore interface {
	config.ConfigStore
	config.EnvRoleStore
	config.HierarchyStore

	// WriteConfig is only permitted in ModeDevelopment
	WriteConfig(layer string, name string, content []byte) error
}

// AppStore is the master store unifying overlays and temporary overrides
type AppStore interface {
	config.ConfigStore
	config.EnvRoleStore
	config.HierarchyStore
	TemporaryOverrideStore

	Mode() Mode
	WriteConfig(layer string, name string, content []byte) error
	GetGitEditURL(layer, name string) string
}
