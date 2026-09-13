package model

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/agent/notify"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/reasoning"
	"github.com/neur0map/prowl/internal/session"
	"github.com/neur0map/prowl/internal/workspace"
	"github.com/stretchr/testify/require"
)

func reasoningTestUI() *UI {
	u := newTestUI()
	u.agentReady = true
	u.agentModel = workspace.AgentModel{
		CatwalkCfg: catwalk.Model{CanReason: true, ReasoningLevels: []string{"low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium"},
		ModelCfg:   config.SelectedModel{Provider: "test", Model: "test", ReasoningEffort: reasoning.Auto},
	}
	u.session = &session.Session{ID: "current"}
	u.setEditorPrompt(false)
	return u
}

func TestUltrathinkPaintsOnlyVisibleProse(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, text, painted string
		width, height       int
	}{
		{"prose", "please ultrathink now", "ultrathink", 40, 4},
		{"code and paths", "`ultrathink` /tmp/ultrathink then ultrathink", "ultrathink", 60, 4},
		{"markup", "<tag>ultrathink</tag> ultrathink", "ultrathink", 50, 4},
		{"wrapped word", "compare alternatives ultrathink now", "ultrathink", 24, 6},
		{"split word", "ultrathink", "ultrathink", 12, 6},
		{"unicode", "界界 e\u0301 ultrathink now", "ultrathink", 40, 4},
		{"scrolled", "ultrathink\nfirst\nsecond\nthird\nfourth\nplain last", "", 40, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := reasoningTestUI()
			u.textarea.DynamicHeight = false
			u.textarea.SetWidth(tt.width)
			u.textarea.SetHeight(tt.height)
			u.textarea.SetValue(tt.text)
			u.textarea, _ = u.textarea.Update(nil)
			for range strings.Count(tt.text, "\n") {
				u.textarea.CursorDown()
			}
			before := u.textarea.View()
			after := u.editorTextareaView()
			require.Equal(t, tt.text, u.textarea.Value(), "painting must not modify the submitted prompt")
			require.Equal(t, ansi.Strip(before), ansi.Strip(after), "painting must preserve the visible text and layout")

			width, height := renderedBounds(before, ansi.GraphemeWidth)
			original := uv.NewScreenBuffer(width, height)
			painted := uv.NewScreenBuffer(width, height)
			uv.NewStyledString(before).Draw(&original, original.Bounds())
			uv.NewStyledString(after).Draw(&painted, painted.Bounds())
			var changed strings.Builder
			for y := range height {
				for x := range width {
					a, b := original.Line(y).At(x), painted.Line(y).At(x)
					if a == nil || b == nil {
						require.Equal(t, a, b)
						continue
					}
					require.Equal(t, a.Content, b.Content)
					require.Equal(t, a.Width, b.Width)
					require.Equal(t, a.Style.Bg, b.Style.Bg, "selection background must survive")
					if !reflect.DeepEqual(a.Style.Fg, b.Style.Fg) {
						changed.WriteString(b.Content)
						require.NotZero(t, b.Style.Attrs&uv.AttrBold)
					}

				}
			}
			require.Equal(t, tt.painted, changed.String(), "only the intended visible keyword may change color")
		})
	}
}

func TestReasoningDisplayIsScopedToActiveRequest(t *testing.T) {
	t.Parallel()
	u := reasoningTestUI()
	base, _ := u.reasoningDisplay(&u.agentModel)
	u.textarea.SetValue("ultrathink solve the race")
	preview, high := u.reasoningDisplay(&u.agentModel)
	require.True(t, high)
	require.Contains(t, preview, "X-High")
	u.textarea.Reset()
	restored, _ := u.reasoningDisplay(&u.agentModel)
	require.Equal(t, base, restored)

	started := notify.Notification{SessionID: "current", ReasoningTurnID: "first", ReasoningMode: reasoning.Ultrathink, ReasoningEffort: "xhigh", ModelID: "test", ProviderID: "test"}
	u.handleReasoningChanged(started)
	active, high := u.reasoningDisplay(&u.agentModel)
	require.True(t, high)
	require.Contains(t, active, "X-High")
	u.textarea.SetValue("a plain queued question")
	stillActive, _ := u.reasoningDisplay(&u.agentModel)
	require.Equal(t, active, stillActive, "a draft cannot change the running request")

	other := started
	other.SessionID = "another-session"
	other.ReasoningEffort = "low"
	u.handleReasoningChanged(other)
	unchanged, _ := u.reasoningDisplay(&u.agentModel)
	require.Equal(t, active, unchanged)

	next := started
	next.ReasoningTurnID, next.ReasoningMode, next.ReasoningEffort = "second", reasoning.Auto, "low"
	u.handleReasoningChanged(next)
	ended := started
	ended.ReasoningEffort = ""
	u.handleReasoningChanged(ended)
	newActive, high := u.reasoningDisplay(&u.agentModel)
	require.False(t, high)
	require.Contains(t, newActive, "Low", "a stale end cannot erase the next request")
	ended.ReasoningTurnID = "second"
	u.handleReasoningChanged(ended)
	restored, _ = u.reasoningDisplay(&u.agentModel)
	require.Equal(t, base, restored)
	require.Equal(t, reasoning.Auto, u.agentModel.ModelCfg.ReasoningEffort)
}
