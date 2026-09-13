package session

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/neur0map/prowl/internal/db"
	"github.com/neur0map/prowl/internal/pubsub"
)

// PromptCache records an immutable provider lease and its full committed TTL
// charge. Invalidating reuse does not refund or erase that historical charge.
type PromptCache struct {
	ID           string
	Key          string
	Resource     string
	SessionID    string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	Tokens       int64
	CreationCost float64
	StorageCost  float64
}

func (s *service) GetPromptCache(ctx context.Context, key string) (PromptCache, error) {
	item, err := s.q.GetPromptCache(ctx, db.GetPromptCacheParams{
		CacheKey: key, ExpiresAt: time.Now().Add(10 * time.Second).UnixNano(),
	})
	if err != nil {
		return PromptCache{}, err
	}
	return PromptCache{
		ID: item.ID, Key: item.CacheKey, Resource: item.Resource, SessionID: item.SessionID,
		CreatedAt: time.Unix(0, item.CreatedAt), ExpiresAt: time.Unix(0, item.ExpiresAt),
		Tokens: item.TokenCount, CreationCost: item.CreationCost, StorageCost: item.StorageCost,
	}, nil
}

func (s *service) SavePromptCache(ctx context.Context, cache PromptCache) error {
	cost := cache.CreationCost + cache.StorageCost
	if cache.ID == "" || cache.Key == "" || cache.Resource == "" || cache.SessionID == "" ||
		cache.CreatedAt.IsZero() || !cache.ExpiresAt.After(cache.CreatedAt) || cache.Tokens < 0 ||
		cache.CreationCost < 0 || cache.StorageCost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return fmt.Errorf("invalid prompt cache lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	inserted, err := queries.InsertPromptCache(ctx, db.InsertPromptCacheParams{
		ID: cache.ID, CacheKey: cache.Key, Resource: cache.Resource, SessionID: cache.SessionID,
		CreatedAt: cache.CreatedAt.UnixNano(), ExpiresAt: cache.ExpiresAt.UnixNano(),
		TokenCount: cache.Tokens, CreationCost: cache.CreationCost, StorageCost: cache.StorageCost,
	})
	if err != nil {
		return err
	}
	if inserted == 0 {
		return tx.Commit()
	}
	updated, err := s.addCost(ctx, queries, cache.SessionID, cost)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, session := range updated {
		s.Publish(pubsub.UpdatedEvent, session)
	}
	return nil
}

func (s *service) InvalidatePromptCache(ctx context.Context, id string) error {
	return s.q.InvalidatePromptCache(ctx, id)
}
