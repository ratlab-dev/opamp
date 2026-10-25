package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/naugustin/opamp-server/pkg/config"
	"gopkg.in/yaml.v3"
)

// ConfigMapStore aggregates multiple mounted Kubernetes ConfigMap volume directories
type ConfigMapStore struct {
	mountPaths []string
}

// NewConfigMapStore initializes a store searching across multiple mounted ConfigMap paths
func NewConfigMapStore(mountPaths []string) *ConfigMapStore {
	return &ConfigMapStore{
		mountPaths: mountPaths,
	}
}

func (s *ConfigMapStore) findAndReadFile(relPath string, alternateNames ...string) (map[string]interface{}, error) {
	candidates := append([]string{relPath}, alternateNames...)

	for _, mount := range s.mountPaths {
		for _, cand := range candidates {
			fullPath := filepath.Join(mount, cand)
			data, err := os.ReadFile(fullPath)
			if err == nil {
				var m map[string]interface{}
				if err := yaml.Unmarshal(data, &m); err != nil {
					return nil, fmt.Errorf("error parsing %s: %w", fullPath, err)
				}
				return m, nil
			}
		}
	}

	return nil, os.ErrNotExist
}

func (s *ConfigMapStore) GetBase() (map[string]interface{}, error) {
	return s.findAndReadFile("base.yaml", "base.yml", "base/base.yaml")
}

func (s *ConfigMapStore) GetEnv(env string) (map[string]interface{}, error) {
	return s.findAndReadFile(
		filepath.Join("environments", env+".yaml"),
		filepath.Join("environments", env+".yml"),
		filepath.Join(env+".yaml"),
	)
}

func (s *ConfigMapStore) GetCluster(cluster string) (map[string]interface{}, error) {
	return s.findAndReadFile(
		filepath.Join("clusters", cluster+".yaml"),
		filepath.Join("clusters", cluster+".yml"),
		filepath.Join(cluster+".yaml"),
	)
}

func (s *ConfigMapStore) GetSubdomain(subdomain string) (map[string]interface{}, error) {
	return s.findAndReadFile(
		filepath.Join("subdomains", subdomain+".yaml"),
		filepath.Join("subdomains", subdomain+".yml"),
		filepath.Join(subdomain+".yaml"),
	)
}

func (s *ConfigMapStore) GetRole(role string) (map[string]interface{}, error) {
	return s.findAndReadFile(
		filepath.Join("roles", role+".yaml"),
		filepath.Join("roles", role+".yml"),
		filepath.Join(role+".yaml"),
	)
}

func (s *ConfigMapStore) GetEnvRole(env, role string) (map[string]interface{}, error) {
	return s.findAndReadFile(
		filepath.Join("environments", env, "roles", role+".yaml"),
		filepath.Join(env, "roles", role+".yaml"),
		filepath.Join(env+"-"+role+".yaml"),
	)
}

func (s *ConfigMapStore) GetHost(hostname string) (map[string]interface{}, error) {
	return s.findAndReadFile(
		filepath.Join("hosts", hostname+".yaml"),
		filepath.Join("hosts", hostname+".yml"),
		filepath.Join(hostname+".yaml"),
	)
}

func (s *ConfigMapStore) GetByPath(path string) (map[string]interface{}, error) {
	return s.findAndReadFile(path)
}

func (s *ConfigMapStore) GetHierarchy() (*config.HierarchyConfig, error) {
	m, err := s.findAndReadFile("hierarchy.yaml", "hierarchy.yml")
	if err != nil || m == nil {
		return nil, nil
	}
	bytes, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	return config.ParseHierarchy(bytes)
}

func (s *ConfigMapStore) GetRedisOverride(target string) (map[string]interface{}, error) {
	return nil, nil
}

func (s *ConfigMapStore) WriteConfig(layer string, name string, content []byte) error {
	return ErrProductionReadOnly
}
