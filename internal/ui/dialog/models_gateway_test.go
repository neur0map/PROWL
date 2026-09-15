package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/csync"
	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/ui/styles"
)

func gatewayTestConfig(entries ...config.ProviderConfig) *config.Config {
	providers := csync.NewMap[string, config.ProviderConfig]()
	for _, e := range entries {
		providers.Set(e.ID, e)
	}
	return &config.Config{Providers: providers}
}

// TestGatewayRowIsOfferedBeforeSetup is the one-click claim: the row must be
// in the picker before the gateway has ever run, because selecting it is the
// setup step. Requiring a CLI invocation first would leave a new user with no
// way to discover it.
func TestGatewayRowIsOfferedBeforeSetup(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	m := &Models{modelType: ModelTypeLarge}
	var selected string

	group, ok := m.gatewayGroup(&sty, gatewayTestConfig(), config.SelectedModel{},
		map[string]*ModelItem{}, &selected)

	require.True(t, ok, "the gateway must be offered with no config at all")
	require.Contains(t, group.Title, gateway.ProviderName)
	require.False(t, group.configured, "an unregistered gateway must not claim to be configured")
	require.NotNil(t, group.accentFrom, "the row is meant to stand out from ordinary providers")
	require.NotNil(t, group.accentTo)

	ids := make([]string, 0, len(group.Items))
	for _, item := range group.Items {
		ids = append(ids, item.model.ID)
	}
	require.Contains(t, ids, gateway.ModelAuto, "the default router alias must be selectable")

	// The real contract: every id offered here must be one the gateway can
	// resolve. `auto:small` and `auto:large` sat in this picker answering 400,
	// because the router knows sort axes and named sets, not sizes.
	axes := map[string]struct{}{}
	for _, axis := range gateway.GlobalSortAliases() {
		axes["auto:"+axis] = struct{}{}
	}
	for _, id := range ids {
		if id == gateway.ModelAuto {
			continue
		}
		_, ok := axes[id]
		require.True(t, ok, "%s is offered in the picker but the router cannot resolve it", id)
	}
}

// TestGatewayRowReflectsConfiguredState covers the badge a user reads to tell
// whether setup already happened.
func TestGatewayRowReflectsConfiguredState(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	m := &Models{modelType: ModelTypeLarge}
	cfg := gatewayTestConfig(gateway.ProviderConfigFor("http://127.0.0.1:8787/v1", "tok"))

	group, ok := m.gatewayGroup(&sty, cfg, config.SelectedModel{}, map[string]*ModelItem{}, new(string))
	require.True(t, ok)
	require.True(t, group.configured, "a registered gateway must show as configured")
}

// TestGatewayRowMarksTheCurrentSelection keeps the picker's highlight correct
// when the gateway is already the active model.
func TestGatewayRowMarksTheCurrentSelection(t *testing.T) {
	t.Parallel()

	sty := styles.RyokutonePantera()
	m := &Models{modelType: ModelTypeLarge}
	cfg := gatewayTestConfig(gateway.ProviderConfigFor("http://127.0.0.1:8787/v1", "tok"))

	var selected string
	_, ok := m.gatewayGroup(&sty, cfg, config.SelectedModel{
		Provider: gateway.ProviderID, Model: gateway.ModelAuto,
	}, map[string]*ModelItem{}, &selected)

	require.True(t, ok)
	require.NotEmpty(t, selected, "the active gateway model must be pre-selected")
}
