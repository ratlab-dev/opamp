package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/naugustin/opamp-server/pkg/config"
	"gopkg.in/yaml.v3"
)

var (
	ErrProductionReadOnly = errors.New("cannot write configuration in production mode: repository/filesystem is read-only")
)

// LocalStore reads configurations from a local directory structure
type LocalStore struct {
	rootDir string
	mode    Mode
}

func NewLocalStore(rootDir string, mode Mode) *LocalStore {
	return &LocalStore{
		rootDir: rootDir,
		mode:    mode,
	}
}

func (s *LocalStore) RootDir() string {
	return s.rootDir
}

func (s *LocalStore) readFile(relPath string) (map[string]interface{}, error) {
	fullPath := filepath.Join(s.rootDir, relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Try .yml if .yaml failed
			if filepath.Ext(fullPath) == ".yaml" {
				altPath := fullPath[:len(fullPath)-5] + ".yml"
				data, err = os.ReadFile(altPath)
			}
		}
		if err != nil {
			return nil, err
		}
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", relPath, err)
	}
	return m, nil
}

func (s *LocalStore) GetBase() (map[string]interface{}, error) {
	return s.readFile("base.yaml")
}

func (s *LocalStore) GetEnv(env string) (map[string]interface{}, error) {
	return s.readFile(filepath.Join("environments", env+".yaml"))
}

func (s *LocalStore) GetCluster(cluster string) (map[string]interface{}, error) {
	return s.readFile(filepath.Join("clusters", cluster+".yaml"))
}

func (s *LocalStore) GetSubdomain(subdomain string) (map[string]interface{}, error) {
	return s.readFile(filepath.Join("subdomains", subdomain+".yaml"))
}

func (s *LocalStore) GetRole(role string) (map[string]interface{}, error) {
	return s.readFile(filepath.Join("roles", role+".yaml"))
}

func (s *LocalStore) GetEnvRole(env, role string) (map[string]interface{}, error) {
	return s.readFile(filepath.Join("environments", env, "roles", role+".yaml"))
}

func (s *LocalStore) GetHost(hostname string) (map[string]interface{}, error) {
	return s.readFile(filepath.Join("hosts", hostname+".yaml"))
}

func (s *LocalStore) GetByPath(path string) (map[string]interface{}, error) {
	return s.readFile(path)
}

func (s *LocalStore) GetHierarchy() (*config.HierarchyConfig, error) {
	fullPath := filepath.Join(s.rootDir, "hierarchy.yaml")
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return config.ParseHierarchy(data)
}

func (s *LocalStore) GetRedisOverride(target string) (map[string]interface{}, error) {
	// Base overlay store does not manage redis directly; handled by combined store
	return nil, nil
}

func (s *LocalStore) WriteConfig(layer string, name string, content []byte) error {
	if s.mode == ModeProduction {
		return ErrProductionReadOnly
	}

	var relPath string
	switch layer {
	case "base":
		relPath = "base.yaml"
	case "environments", "environment", "env":
		relPath = filepath.Join("environments", name+".yaml")
	case "clusters", "cluster":
		relPath = filepath.Join("clusters", name+".yaml")
	case "subdomains", "subdomain":
		relPath = filepath.Join("subdomains", name+".yaml")
	case "roles", "role":
		relPath = filepath.Join("roles", name+".yaml")
	case "hosts", "host":
		relPath = filepath.Join("hosts", name+".yaml")
	default:
		relPath = filepath.Join(layer, name+".yaml")
	}

	fullPath := filepath.Join(s.rootDir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}

	// Validate yaml before writing
	var tmp map[string]interface{}
	if err := yaml.Unmarshal(content, &tmp); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	return os.WriteFile(fullPath, content, 0644)
}
