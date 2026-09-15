package api

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

// The chain adapter binds the ported chain resolution to one request.
//
// The failover loop asks only for "the next route, honouring what I have
// already skipped". Everything behind that question — resolving the model
// string, scoring the candidates, gating them, and choosing among a model's
// keys — lives here, so the loop stays a control structure rather than a
// router.

// buildChain resolves this request's candidates into the loop's view of them.
func (s *Server) buildChain(ctx context.Context, req *chatRequestBody) (gateway.Chain, error) {
	resolved, err := gateway.ResolveChain(s.engine.DB(), req.Model, s.routingStrategy(ctx))
	if err != nil {
		return nil, err
	}
	if len(resolved.Chain) == 0 {
		// Name the actual gap. "No keys configured" is wrong whenever the
		// operator has keys but none of them serves what the list points at,
		// which is the usual way a hand-built or subscription list ends up
		// empty.
		return nil, &gateway.ChainError{
			Status:  503,
			Message: gateway.ExplainUnroutableChain(s.engine.DB(), resolved.StrategyKey),
		}
	}

	scorer := s.newAxisScorer(ctx)
	ordered := gateway.OrderChain(
		resolved.Chain, resolved.OrderBy, gateway.Weights{}, true, scorer, s.engine.Penalties())

	return &requestChain{
		server:  s,
		entries: ordered,
		gate: gateway.RequestGate{
			EstimatedTokens: estimateTokens(req),
			RequireVision:   requestHasImages(req),
			RequireTools:    req.Params["tools"] != nil,
		},
		rng: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x9e3779b9)),
	}, nil
}

// requestChain is the per-request chain the loop walks. Ordering and skip
// state are both request-scoped, so nothing here is shared between requests.
type requestChain struct {
	server  *Server
	entries []gateway.ChainEntry
	gate    gateway.RequestGate
	rng     *rand.Rand

	mu sync.Mutex
}

// Route returns the next candidate that passes this request's gates, is not
// already skipped, and is not benched.
func (c *requestChain) Route(_ int, skip *gateway.SkipState) (gateway.Route, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// SkipState holds presence sets while RequestGate takes bool maps, so
	// convert once per call rather than once per candidate.
	gate := c.gate
	gate.SkipModels = make(map[int64]bool, len(skip.Models))
	for id := range skip.Models {
		gate.SkipModels[id] = true
	}
	gate.SkipPlatforms = make(map[string]bool, len(skip.Platforms))
	for name := range skip.Platforms {
		gate.SkipPlatforms[name] = true
	}

	for _, entry := range gateway.Eligible(c.entries, gate) {
		if entry.KeyID == nil {
			continue
		}
		keyID := *entry.KeyID
		if _, gone := skip.Keys[gateway.RouteKey{
			Platform: entry.Platform, ModelID: entry.ModelID, KeyID: keyID,
		}]; gone {
			continue
		}
		if _, benched := c.server.engine.Cooldowns().Active(
			gateway.QuotaKey(entry.Platform, entry.ModelID, keyID)); benched {
			continue
		}
		return gateway.Route{
			Platform:  entry.Platform,
			ModelID:   entry.ModelID,
			ModelDBID: entry.ModelDBID,
			KeyID:     keyID,
			BaseURL:   c.server.keyBaseURL(keyID),
			RPDLimit:  entry.RPDLimit,
			TPDLimit:  entry.TPDLimit,
		}, nil
	}

	return gateway.Route{}, &gateway.RouteError{
		Status:  503,
		Message: "every candidate is skipped, benched, or out of quota",
	}
}

// RoutableKeys is the full key set a model-level bench must span, ignoring the
// transient gates: benching a model on only the key that just failed would
// leave the others to hit the same broken model.
func (c *requestChain) RoutableKeys(modelDBID int64) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	seen := map[int64]bool{}
	var out []int64
	for _, entry := range c.entries {
		if entry.ModelDBID != modelDBID || entry.KeyID == nil || seen[*entry.KeyID] {
			continue
		}
		seen[*entry.KeyID] = true
		out = append(out, *entry.KeyID)
	}
	return out
}

// HasOtherUsableKey gates the model penalty. A sibling key that can still
// serve this model means the model is not what failed, so demoting it would
// punish the wrong thing.
func (c *requestChain) HasOtherUsableKey(modelDBID, failedKeyID int64, skipped map[gateway.RouteKey]struct{}) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, entry := range c.entries {
		if entry.ModelDBID != modelDBID || entry.KeyID == nil || !entry.Enabled {
			continue
		}
		keyID := *entry.KeyID
		if keyID == failedKeyID {
			continue
		}
		if _, gone := skipped[gateway.RouteKey{
			Platform: entry.Platform, ModelID: entry.ModelID, KeyID: keyID,
		}]; gone {
			continue
		}
		if _, benched := c.server.engine.Cooldowns().Active(
			gateway.QuotaKey(entry.Platform, entry.ModelID, keyID)); benched {
			continue
		}
		return true
	}
	return false
}

// routingStrategy reads the operator's choice. A malformed stored value falls
// back to the default rather than failing the request: the setting is not
// worth a 500.
func (s *Server) routingStrategy(ctx context.Context) gateway.RoutingStrategy {
	var raw string
	if err := s.engine.DB().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'routing_strategy'`).Scan(&raw); err != nil {
		return gateway.DefaultRoutingStrategy
	}
	return gateway.ParseRoutingStrategy(raw)
}

// requestHasImages reports whether any message carries image content, which is
// what gates vision-incapable models out of the chain.
func requestHasImages(req *chatRequestBody) bool {
	for _, message := range req.Messages {
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := m["type"].(string); kind == "image_url" || kind == "image" {
				return true
			}
		}
	}
	return false
}
