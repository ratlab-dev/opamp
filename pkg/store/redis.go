package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
)

// RedisOverrideStore manages temporary overrides in Redis with TTLs
type RedisOverrideStore struct {
	client    *redis.Client
	prefix    string
	inMemMode bool
	inMemMu   sync.RWMutex
	inMemData map[string]inMemEntry
}

type inMemEntry struct {
	config    map[string]interface{}
	metadata  OverrideMetadata
	expiresAt time.Time
}

// NewRedisOverrideStore connects to a Redis instance, or falls back to in-memory if addr is empty
func NewRedisOverrideStore(redisAddr string, redisPassword string, db int) *RedisOverrideStore {
	store := &RedisOverrideStore{
		prefix:    "opamp:override:",
		inMemData: make(map[string]inMemEntry),
	}

	if redisAddr == "" {
		store.inMemMode = true
		return store
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		// Redis ping failed, fallback to in-memory mode with notice
		store.inMemMode = true
		return store
	}

	store.client = rdb
	return store
}

// NewInMemoryOverrideStore creates an in-memory TTL override store for testing/isolated environments
func NewInMemoryOverrideStore() *RedisOverrideStore {
	return &RedisOverrideStore{
		prefix:    "opamp:override:",
		inMemMode: true,
		inMemData: make(map[string]inMemEntry),
	}
}

func (s *RedisOverrideStore) IsInMemory() bool {
	return s.inMemMode
}

func (s *RedisOverrideStore) normalizeKey(target string) string {
	return strings.TrimSpace(target)
}

func (s *RedisOverrideStore) GetOverride(target string) (map[string]interface{}, error) {
	key := s.normalizeKey(target)
	if s.inMemMode {
		s.inMemMu.Lock()
		defer s.inMemMu.Unlock()
		entry, exists := s.inMemData[key]
		if !exists {
			return nil, nil
		}
		if time.Now().After(entry.expiresAt) {
			delete(s.inMemData, key)
			return nil, nil
		}
		return entry.config, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	configKey := s.prefix + "cfg:" + key
	data, err := s.client.Get(ctx, configKey).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to unmarshal redis override yaml: %w", err)
	}
	return m, nil
}

func (s *RedisOverrideStore) SetOverride(target string, cfg map[string]interface{}, ttl time.Duration, meta OverrideMetadata) error {
	key := s.normalizeKey(target)
	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal override config: %w", err)
	}

	now := time.Now()
	expiresAt := now.Add(ttl)
	meta.Target = key
	meta.CreatedAt = now
	meta.ExpiresAt = expiresAt
	meta.TTLSeconds = int64(ttl.Seconds())
	meta.ConfigYAML = string(yamlBytes)

	if s.inMemMode {
		s.inMemMu.Lock()
		defer s.inMemMu.Unlock()
		s.inMemData[key] = inMemEntry{
			config:    cfg,
			metadata:  meta,
			expiresAt: expiresAt,
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal override metadata: %w", err)
	}

	configKey := s.prefix + "cfg:" + key
	metaKey := s.prefix + "meta:" + key
	activeSetKey := s.prefix + "active_keys"

	pipe := s.client.Pipeline()
	pipe.Set(ctx, configKey, yamlBytes, ttl)
	pipe.Set(ctx, metaKey, metaBytes, ttl)
	pipe.SAdd(ctx, activeSetKey, key)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisOverrideStore) DeleteOverride(target string) error {
	key := s.normalizeKey(target)
	if s.inMemMode {
		s.inMemMu.Lock()
		defer s.inMemMu.Unlock()
		delete(s.inMemData, key)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	configKey := s.prefix + "cfg:" + key
	metaKey := s.prefix + "meta:" + key
	activeSetKey := s.prefix + "active_keys"

	pipe := s.client.Pipeline()
	pipe.Del(ctx, configKey, metaKey)
	pipe.SRem(ctx, activeSetKey, key)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisOverrideStore) ListOverrides() ([]OverrideMetadata, error) {
	now := time.Now()

	if s.inMemMode {
		s.inMemMu.Lock()
		defer s.inMemMu.Unlock()
		var list []OverrideMetadata
		for k, entry := range s.inMemData {
			if now.After(entry.expiresAt) {
				delete(s.inMemData, k)
				continue
			}
			meta := entry.metadata
			meta.TTLSeconds = int64(time.Until(entry.expiresAt).Seconds())
			list = append(list, meta)
		}
		return list, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	activeSetKey := s.prefix + "active_keys"
	keys, err := s.client.SMembers(ctx, activeSetKey).Result()
	if err != nil {
		return nil, err
	}

	var list []OverrideMetadata
	for _, key := range keys {
		metaKey := s.prefix + "meta:" + key
		data, err := s.client.Get(ctx, metaKey).Bytes()
		if err == redis.Nil {
			// Expired from redis keyspace
			s.client.SRem(ctx, activeSetKey, key)
			continue
		}
		if err != nil {
			continue
		}

		var meta OverrideMetadata
		if err := json.Unmarshal(data, &meta); err == nil {
			ttl, _ := s.client.TTL(ctx, metaKey).Result()
			if ttl <= 0 {
				s.client.SRem(ctx, activeSetKey, key)
				continue
			}
			meta.TTLSeconds = int64(ttl.Seconds())
			list = append(list, meta)
		}
	}

	return list, nil
}

// Client returns the underlying redis.Client if connected, or nil if in-memory
func (s *RedisOverrideStore) Client() *redis.Client {
	if s.inMemMode {
		return nil
	}
	return s.client
}

// PublishClusterEvent sends a message to all server nodes in the cluster
func (s *RedisOverrideStore) PublishClusterEvent(ctx context.Context, channel, payload string) error {
	if s.inMemMode || s.client == nil {
		return nil
	}
	return s.client.Publish(ctx, channel, payload).Err()
}

// SubscribeClusterEvents listens to cluster broadcast messages in a background goroutine
func (s *RedisOverrideStore) SubscribeClusterEvents(ctx context.Context, channel string, handler func(msg string)) {
	if s.inMemMode || s.client == nil {
		return
	}
	go func() {
		pubsub := s.client.Subscribe(ctx, channel)
		defer pubsub.Close()

		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				if msg != nil {
					handler(msg.Payload)
				}
			}
		}
	}()
}
