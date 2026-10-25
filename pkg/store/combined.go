package store

import (
	"time"

	"github.com/naugustin/opamp-server/pkg/config"
)

// CombinedStore brings together the base/overlay provider and Redis temporary overrides
type CombinedStore struct {
	baseOverlay BaseOverlayStore
	redisStore  TemporaryOverrideStore
	mode        Mode
	gitProvider GitProvider
}

func NewCombinedStore(baseOverlay BaseOverlayStore, redisStore TemporaryOverrideStore, mode Mode, gitProvider GitProvider) *CombinedStore {
	return &CombinedStore{
		baseOverlay: baseOverlay,
		redisStore:  redisStore,
		mode:        mode,
		gitProvider: gitProvider,
	}
}

func (c *CombinedStore) Mode() Mode {
	return c.mode
}

func (c *CombinedStore) GetBase() (map[string]interface{}, error) {
	return c.baseOverlay.GetBase()
}

func (c *CombinedStore) GetEnv(env string) (map[string]interface{}, error) {
	return c.baseOverlay.GetEnv(env)
}

func (c *CombinedStore) GetCluster(cluster string) (map[string]interface{}, error) {
	return c.baseOverlay.GetCluster(cluster)
}

func (c *CombinedStore) GetSubdomain(subdomain string) (map[string]interface{}, error) {
	return c.baseOverlay.GetSubdomain(subdomain)
}

func (c *CombinedStore) GetRole(role string) (map[string]interface{}, error) {
	return c.baseOverlay.GetRole(role)
}

func (c *CombinedStore) GetEnvRole(env, role string) (map[string]interface{}, error) {
	return c.baseOverlay.GetEnvRole(env, role)
}

func (c *CombinedStore) GetHost(hostname string) (map[string]interface{}, error) {
	return c.baseOverlay.GetHost(hostname)
}

func (c *CombinedStore) GetHierarchy() (*config.HierarchyConfig, error) {
	if hs, ok := c.baseOverlay.(config.HierarchyStore); ok {
		return hs.GetHierarchy()
	}
	return nil, nil
}

func (c *CombinedStore) GetByPath(path string) (map[string]interface{}, error) {
	if hs, ok := c.baseOverlay.(config.HierarchyStore); ok {
		return hs.GetByPath(path)
	}
	return nil, nil
}

func (c *CombinedStore) GetRedisOverride(target string) (map[string]interface{}, error) {
	return c.redisStore.GetOverride(target)
}

func (c *CombinedStore) GetOverride(target string) (map[string]interface{}, error) {
	return c.redisStore.GetOverride(target)
}

func (c *CombinedStore) SetOverride(target string, cfg map[string]interface{}, ttl time.Duration, meta OverrideMetadata) error {
	return c.redisStore.SetOverride(target, cfg, ttl, meta)
}

func (c *CombinedStore) DeleteOverride(target string) error {
	return c.redisStore.DeleteOverride(target)
}

func (c *CombinedStore) ListOverrides() ([]OverrideMetadata, error) {
	return c.redisStore.ListOverrides()
}

func (c *CombinedStore) WriteConfig(layer string, name string, content []byte) error {
	if c.mode == ModeProduction {
		return ErrProductionReadOnly
	}
	return c.baseOverlay.WriteConfig(layer, name, content)
}

func (c *CombinedStore) GetGitEditURL(layer, name string) string {
	if c.gitProvider == nil {
		return ""
	}
	var path string
	switch layer {
	case "base":
		path = "base.yaml"
	case "environments", "environment", "env":
		path = "environments/" + name + ".yaml"
	case "clusters", "cluster":
		path = "clusters/" + name + ".yaml"
	case "subdomains", "subdomain":
		path = "subdomains/" + name + ".yaml"
	case "roles", "role":
		path = "roles/" + name + ".yaml"
	case "hosts", "host":
		path = "hosts/" + name + ".yaml"
	default:
		path = layer + "/" + name + ".yaml"
	}
	return c.gitProvider.GetEditURL(path)
}

func (c *CombinedStore) BaseOverlayStore() BaseOverlayStore {
	return c.baseOverlay
}

