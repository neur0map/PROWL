package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/neur0map/prowl/internal/gateway/catalog"
)

// Settings are the gateway's non-secret configuration. They live beside the
type Settings struct {
	path string

	mu       sync.RWMutex
	data     settingsData
	fallback Strategy
}

type settingsData struct {
	Strategy Strategy `json:"strategy"`
	// Disabled lists providers the user switched off. Storing the exceptions
	// rather than the enabled set means a newly configured provider works
	// immediately instead of waiting to be added here.
	Disabled []string `json:"disabled_providers"`
	// Priority overrides the catalog order per provider; lower wins.
	Priority map[string]int `json:"priority,omitempty"`
	// Models overrides which model ids a provider exposes, for a user who
	// wants something the curated list does not carry.
	Models map[string][]string `json:"models,omitempty"`
}

const settingsFileName = "settings.json"

// LoadSettings reads settings from dir, returning usable defaults when no
// file exists yet.
func LoadSettings(dir string) (*Settings, error) {
	s := &Settings{
		path:     filepath.Join(dir, settingsFileName),
		fallback: StrategyCost,
		data:     settingsData{Strategy: StrategyCost},
	}
	blob, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(blob, &s.data); err != nil {
		return nil, err
	}
	if s.data.Strategy == "" {
		s.data.Strategy = StrategyCost
	}
	return s, nil
}

func (s *Settings) save() error {
	blob, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Strategy returns the active routing strategy.
func (s *Settings) Strategy() Strategy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.Strategy == "" {
		return s.fallback
	}
	return s.data.Strategy
}

// SetStrategy persists a new routing strategy.
func (s *Settings) SetStrategy(strategy Strategy) error {
	if !slices.Contains(Strategies(), strategy) {
		return errors.New("unknown routing strategy: " + string(strategy))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Strategy = strategy
	return s.save()
}

// Strategies lists every selectable strategy, cheapest-intent first.
func Strategies() []Strategy {
	return []Strategy{StrategyCost, StrategyLatency, StrategyHeadroom, StrategyPriority}
}

// ProviderEnabled reports whether a provider may be routed to.
func (s *Settings) ProviderEnabled(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !slices.Contains(s.data.Disabled, id)
}

// SetProviderEnabled turns a provider on or off for routing.
func (s *Settings) SetProviderEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	has := slices.Contains(s.data.Disabled, id)
	switch {
	case enabled && has:
		s.data.Disabled = slices.DeleteFunc(s.data.Disabled, func(v string) bool { return v == id })
	case !enabled && !has:
		s.data.Disabled = append(s.data.Disabled, id)
	default:
		return nil
	}
	return s.save()
}

// PriorityFor returns a provider's operator-declared rank, 0 when unset.
func (s *Settings) PriorityFor(id string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Priority[id]
}

// ModelsFor returns the models a provider should expose: the user's override
// when present, otherwise the catalog's curated list.
func (s *Settings) ModelsFor(p catalog.Provider) []catalog.Model {
	s.mu.RLock()
	override := slices.Clone(s.data.Models[p.ID])
	s.mu.RUnlock()

	if len(override) > 0 {
		out := make([]catalog.Model, 0, len(override))
		for _, id := range override {
			model := catalog.Model{ID: id, Name: id, Context: p.MaxContext}
			// Keep catalog metadata when the override names a known model.
			for _, known := range p.Models {
				if known.ID == id {
					model = known
					break
				}
			}
			out = append(out, model)
		}
		return out
	}
	return p.Models
}

// SetModels overrides the model list for a provider. An empty list restores
// the catalog default.
func (s *Settings) SetModels(id string, models []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Models == nil {
		s.data.Models = map[string][]string{}
	}
	if len(models) == 0 {
		delete(s.data.Models, id)
	} else {
		s.data.Models[id] = models
	}
	return s.save()
}
