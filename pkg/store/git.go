package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/naugustin/opamp-server/pkg/config"
	"gopkg.in/yaml.v3"
)

// GitProvider abstracts remote Git hosting services (Gitea, GitHub, GitLab)
type GitProvider interface {
	GetFile(ctx context.Context, path string) ([]byte, error)
	GetWebURL(path string) string
	GetEditURL(path string) string
	ProviderName() string
}

// GiteaConfig configures connection to a Gitea instance
type GiteaConfig struct {
	BaseURL string        // e.g. "https://gitea.example.internal" or "http://localhost:3000"
	Owner   string        // repository owner/organization, e.g. "devops"
	Repo    string        // repository name, e.g. "opamp-configs"
	Branch  string        // default branch, e.g. "main"
	Token   string        // optional personal access token
	Timeout time.Duration // HTTP timeout
}

// GiteaClient implements GitProvider for Gitea repositories
type GiteaClient struct {
	cfg        GiteaConfig
	httpClient *http.Client
}

func NewGiteaClient(cfg GiteaConfig) *GiteaClient {
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	return &GiteaClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (g *GiteaClient) ProviderName() string {
	return "gitea"
}

// GetFile fetches raw file content from Gitea via raw or contents API
func (g *GiteaClient) GetFile(ctx context.Context, path string) ([]byte, error) {
	// Try raw endpoint first: /api/v1/repos/{owner}/{repo}/raw/{filepath}?ref={branch}
	reqURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/raw/%s?ref=%s",
		g.cfg.BaseURL, g.cfg.Owner, g.cfg.Repo, strings.TrimPrefix(path, "/"), g.cfg.Branch)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if g.cfg.Token != "" {
		req.Header.Set("Authorization", "token "+g.cfg.Token)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return io.ReadAll(resp.Body)
	}

	// If raw failed with 404, fallback to contents API: /api/v1/repos/{owner}/{repo}/contents/{filepath}?ref={branch}
	if resp.StatusCode == http.StatusNotFound {
		contentsURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=%s",
			g.cfg.BaseURL, g.cfg.Owner, g.cfg.Repo, strings.TrimPrefix(path, "/"), g.cfg.Branch)
		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, contentsURL, nil)
		if err != nil {
			return nil, err
		}
		if g.cfg.Token != "" {
			req2.Header.Set("Authorization", "token "+g.cfg.Token)
		}
		resp2, err := g.httpClient.Do(req2)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()

		if resp2.StatusCode == http.StatusOK {
			var result struct {
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
			}
			if err := json.NewDecoder(resp2.Body).Decode(&result); err == nil {
				if result.Encoding == "base64" {
					cleanBase64 := strings.ReplaceAll(result.Content, "\n", "")
					return base64.StdEncoding.DecodeString(cleanBase64)
				}
				return []byte(result.Content), nil
			}
		}
	}

	return nil, fmt.Errorf("failed to fetch %s from gitea: HTTP %d", path, resp.StatusCode)
}

func (g *GiteaClient) GetWebURL(path string) string {
	return fmt.Sprintf("%s/%s/%s/src/branch/%s/%s",
		g.cfg.BaseURL, g.cfg.Owner, g.cfg.Repo, g.cfg.Branch, strings.TrimPrefix(path, "/"))
}

func (g *GiteaClient) GetEditURL(path string) string {
	return fmt.Sprintf("%s/%s/%s/_edit/%s/%s",
		g.cfg.BaseURL, g.cfg.Owner, g.cfg.Repo, g.cfg.Branch, strings.TrimPrefix(path, "/"))
}

// GitStore implements BaseOverlayStore backed by any GitProvider
type GitStore struct {
	provider GitProvider
}

func NewGitStore(provider GitProvider) *GitStore {
	return &GitStore{provider: provider}
}

func (s *GitStore) Provider() GitProvider {
	return s.provider
}

func (s *GitStore) readFile(path string) (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	data, err := s.provider.GetFile(ctx, path)
	if err != nil {
		// Try alternate .yml
		if filepath.Ext(path) == ".yaml" {
			altPath := path[:len(path)-5] + ".yml"
			data, err = s.provider.GetFile(ctx, altPath)
		}
		if err != nil {
			return nil, err
		}
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", path, err)
	}
	return m, nil
}

func (s *GitStore) GetBase() (map[string]interface{}, error) {
	return s.readFile("base.yaml")
}

func (s *GitStore) GetEnv(env string) (map[string]interface{}, error) {
	return s.readFile("environments/" + env + ".yaml")
}

func (s *GitStore) GetCluster(cluster string) (map[string]interface{}, error) {
	return s.readFile("clusters/" + cluster + ".yaml")
}

func (s *GitStore) GetSubdomain(subdomain string) (map[string]interface{}, error) {
	return s.readFile("subdomains/" + subdomain + ".yaml")
}

func (s *GitStore) GetRole(role string) (map[string]interface{}, error) {
	return s.readFile("roles/" + role + ".yaml")
}

func (s *GitStore) GetEnvRole(env, role string) (map[string]interface{}, error) {
	return s.readFile("environments/" + env + "/roles/" + role + ".yaml")
}

func (s *GitStore) GetHost(hostname string) (map[string]interface{}, error) {
	return s.readFile("hosts/" + hostname + ".yaml")
}

func (s *GitStore) GetByPath(path string) (map[string]interface{}, error) {
	return s.readFile(path)
}

func (s *GitStore) GetHierarchy() (*config.HierarchyConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	data, err := s.provider.GetFile(ctx, "hierarchy.yaml")
	if err != nil {
		return nil, nil
	}
	return config.ParseHierarchy(data)
}

func (s *GitStore) GetRedisOverride(target string) (map[string]interface{}, error) {
	return nil, nil
}

func (s *GitStore) WriteConfig(layer string, name string, content []byte) error {
	return ErrProductionReadOnly
}
