package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/catwalk/pkg/catwalk"

	"github.com/neur0map/prowl/internal/config"
)

// ProviderID is how the gateway appears in Prowl's provider list.
const ProviderID = "prowl-gateway"

// ProviderName is the display name in the model picker.
const ProviderName = "Prowl Gateway"

// Routing aliases the gateway exposes as selectable models. Every id here
// must be one the gateway can actually resolve: `auto:small` and `auto:large`
// were offered in the picker for months and answered 400, because the router
// knows sort axes and named sets, not sizes.
const (
	ModelAuto         = "auto"
	ModelAutoSmart    = "auto:smart"
	ModelAutoFast     = "auto:fast"
	ModelAutoCheap    = "auto:cheap"
	ModelAutoReliable = "auto:reliable"
)

// Dir returns the gateway's state directory: <prowl data>/gateway.
func Dir() string {
	return filepath.Join(filepath.Dir(config.GlobalConfigData()), "gateway")
}

// DefaultBaseURL is where a gateway started with default flags listens.
func DefaultBaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/v1", DefaultPort)
}

// EnsureLocalIdentity creates just the state a client needs to address the
// gateway: its directory and local token. It deliberately does not open the
// key store or usage log, so the TUI can register the provider without
// starting a gateway.
func EnsureLocalIdentity(dir string) (baseURL, token string, err error) {
	if dir == "" {
		dir = Dir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create gateway directory: %w", err)
	}
	token, err = loadOrCreateToken(dir)
	if err != nil {
		return "", "", err
	}
	return DefaultBaseURL(), token, nil
}

// RoutingModels returns the alias models, richest first.
func RoutingModels() []catwalk.Model {
	return []catwalk.Model{
		{
			ID:                     ModelAuto,
			Name:                   "Auto (smart routing)",
			ContextWindow:          128_000,
			DefaultMaxTokens:       16_000,
			CanReason:              true,
			SupportsImages:         false,
			DefaultReasoningEffort: "medium",
			ReasoningLevels:        []string{"low", "medium", "high"},
		},
		{
			ID:               ModelAutoSmart,
			Name:             "Auto — most capable",
			ContextWindow:    128_000,
			DefaultMaxTokens: 16_000,
			CanReason:        true,
			ReasoningLevels:  []string{"low", "medium", "high"},
		},
		{
			ID:               ModelAutoFast,
			Name:             "Auto — fastest",
			ContextWindow:    128_000,
			DefaultMaxTokens: 16_000,
		},
		{
			ID:               ModelAutoCheap,
			Name:             "Auto — cheapest",
			ContextWindow:    128_000,
			DefaultMaxTokens: 16_000,
		},
		{
			ID:               ModelAutoReliable,
			Name:             "Auto — most reliable",
			ContextWindow:    128_000,
			DefaultMaxTokens: 16_000,
		},
	}
}

// setModelPrefix marks a picker entry that is one of the operator's own sets
// rather than a routing axis.
const setModelPrefix = "Set: "

// displaySetName restores a readable label: the routable id is lowercased, so
// a set the operator named "Deep work" comes back as "deep work".
func displaySetName(name string) string {
	runes := []rune(name)
	if len(runes) == 0 {
		return name
	}
	return string(unicode.ToUpper(runes[0])) + string(runes[1:])
}

// DiscoverSets asks a running gateway which named sets it can route, so the
// sets built in the dashboard are selectable in the TUI — switching set per
// task is the point of having them, and it must not require editing config.
// A gateway that is not running yields nothing rather than an error: the
// picker still offers the axes above.
func DiscoverSets(ctx context.Context, baseURL, token string) []catwalk.Model {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return nil
	}

	// Every sort axis, not just the ones this picker offers: the router
	// resolves more axes than are worth listing, and they are not sets.
	axes := map[string]struct{}{}
	for _, axis := range GlobalSortAliases() {
		axes[axis] = struct{}{}
	}

	var out []catwalk.Model
	for _, entry := range body.Data {
		if !strings.HasPrefix(entry.ID, "auto:") {
			continue
		}
		name := strings.TrimPrefix(entry.ID, "auto:")
		if _, isAxis := axes[name]; isAxis {
			continue
		}
		// The default list is what plain `auto` already routes, so offering
		// it again as a set is two entries for one behaviour.
		if strings.EqualFold(name, "default") {
			continue
		}
		out = append(out, catwalk.Model{
			ID:   entry.ID,
			Name: setModelPrefix + displaySetName(name),
			// A set's members vary, so the picker carries the same
			// conservative envelope `auto` does; the gateway enforces the
			// real per-model limits at dispatch.
			ContextWindow:    128_000,
			DefaultMaxTokens: 16_000,
			CanReason:        true,
			ReasoningLevels:  []string{"low", "medium", "high"},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ProviderConfigFor returns the provider entry that points Prowl at a running
func ProviderConfigFor(baseURL, token string) config.ProviderConfig {
	return config.ProviderConfig{
		ID:      ProviderID,
		Name:    ProviderName,
		BaseURL: baseURL,
		Type:    catwalk.TypeOpenAICompat,
		APIKey:  token,
		Models:  RoutingModels(),
	}
}

// Register writes the gateway provider into the global config so the model
// picker offers it. It is idempotent: selecting the gateway twice must not
// produce a second entry or disturb a user's other providers.
func Register(store *config.ConfigStore, baseURL, token string) error {
	if store == nil {
		return fmt.Errorf("config store is required to register the gateway")
	}
	entry := ProviderConfigFor(baseURL, token)
	// Best effort: a reachable gateway contributes the operator's own sets.
	if sets := DiscoverSets(context.Background(), baseURL, token); len(sets) > 0 {
		entry.Models = append(entry.Models, sets...)
	}
	if err := store.SetConfigField(config.ScopeGlobal, "providers."+ProviderID, entry); err != nil {
		return fmt.Errorf("register the gateway provider: %w", err)
	}
	return nil
}
