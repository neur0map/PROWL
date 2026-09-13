package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/neur0map/prowl/internal/agent/notify"
	"github.com/neur0map/prowl/internal/agent/tools"
	"github.com/neur0map/prowl/internal/db"
	"github.com/neur0map/prowl/internal/goals"
	"github.com/neur0map/prowl/internal/message"
	"github.com/neur0map/prowl/internal/session"
	"github.com/stretchr/testify/require"
)

type goalScriptModel struct {
	finishStreamModel
	mode    string
	calls   atomic.Int32
	entered chan struct{}
	inspect func(fantasy.Call)
}

func (m *goalScriptModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if len(call.Tools) == 0 {
		return (&finishStreamModel{text: "Retain the original request and active objective."}).Stream(ctx, call)
	}
	n := m.calls.Add(1)
	if m.inspect != nil {
		m.inspect(call)
	}
	if m.mode == "cancel" {
		close(m.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if n > 4 {
		return nil, errors.New("goal failed to stop")
	}
	return func(yield func(fantasy.StreamPart) bool) {
		reason := fantasy.FinishReasonStop
		toolName := ""
		if m.mode == "complete" && n == 2 {
			toolName = "goal"
		}
		if m.mode == "denied" {
			toolName = "blocked"
		}
		if toolName != "" {
			reason = fantasy.FinishReasonToolCalls
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: "control", ToolCallName: toolName, ToolCallInput: `{"op":"complete"}`}) {
				return
			}
		} else {
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "text"}) {
				return
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: "A normal assistant stop is not goal completion."}) {
				return
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "text"}) {
				return
			}
		}
		if m.mode == "filtered" {
			reason = fantasy.FinishReasonContentFilter
		}
		yield(fantasy.StreamPart{
			Type: fantasy.StreamPartTypeFinish, FinishReason: reason,
			Usage: fantasy.Usage{InputTokens: 10, OutputTokens: 3, CacheCreationTokens: 2, CacheReadTokens: 100},
		})
	}, nil
}

func newGoalTestAgent(t *testing.T, model fantasy.LanguageModel) (*sessionAgent, *goals.Service, session.Session) {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	service := goals.NewService(q)
	t.Cleanup(service.Shutdown)
	sess, err := sessions.Create(t.Context(), "Goal lifecycle")
	require.NoError(t, err)
	blocked := fantasy.NewAgentTool("blocked", "A denied operation.", func(ctx context.Context, _ tools.GoalParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return tools.NewPermissionDeniedResponse(), nil
	})
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{Model: model, CatwalkCfg: catwalk.Model{ContextWindow: 65536}},
		SmallModel: Model{Model: &finishStreamModel{text: "Goal lifecycle"}},
		Sessions:   sessions, Messages: messages, Goals: service, IsYolo: true,
		Tools: []fantasy.AgentTool{tools.NewGoalTool(service), blocked},
	}).(*sessionAgent)
	t.Cleanup(sa.CancelAll)
	return sa, service, sess
}

func TestGoalRunContinuesUntilExplicitCompletion(t *testing.T) {
	t.Parallel()
	model := &goalScriptModel{mode: "complete"}
	sa, service, sess := newGoalTestAgent(t, model)
	large := sa.largeModel.Get()
	large.ModelCfg.Provider, large.ModelCfg.Model = "fake", "goal-script"
	large.CatwalkCfg.CostPer1MIn = 10
	large.CatwalkCfg.CostPer1MInCached = 12.5
	large.CatwalkCfg.CostPer1MOutCached = 1
	large.CatwalkCfg.CostPer1MOut = 20
	sa.largeModel.Set(large)
	_, err := service.Apply(t.Context(), sess.ID, goals.Request{Op: "set", Objective: "Complete and verify the requested work"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "Start the goal", NonInteractive: true})
	require.NoError(t, err)
	g, err := service.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, goals.Complete, g.Status)
	require.Equal(t, int64(30), g.TokensUsed, "cache reads and post-completion output do not consume the goal budget")
	charged, err := sa.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.000855, charged.Cost, 1e-12,
		"all three provider requests are billable, including cached input and the final reply after goal completion")
	msgs, err := sa.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	var userPrompts []string
	for _, msg := range msgs {
		if msg.Role == message.User {
			userPrompts = append(userPrompts, msg.Content().Text)
		}
	}
	require.Equal(t, []string{"Start the goal"}, userPrompts, "synthetic continuations must not become user requests")
}

func TestGoalRunStopsWithoutClaimingSuccess(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"budget", "filtered", "denied", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			model := &goalScriptModel{mode: mode, entered: make(chan struct{})}
			sa, service, sess := newGoalTestAgent(t, model)
			req := goals.Request{Op: "set", Objective: "Keep working until verified"}
			if mode == "budget" {
				cap := int64(20)
				req.TokenBudget = &cap
			}
			_, err := service.Apply(t.Context(), sess.ID, req)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "Start", NonInteractive: true})
				done <- err
			}()
			if mode == "cancel" {
				select {
				case <-model.entered:
					sa.Cancel(sess.ID)
				case <-ctx.Done():
					t.Fatal("model did not start")
				}
			}
			select {
			case err := <-done:
				if mode == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.NoError(t, err)
				}
			case <-ctx.Done():
				t.Fatal("goal did not stop")
			}
			g, err := service.Get(t.Context(), sess.ID)
			require.NoError(t, err)
			if mode == "budget" {
				require.Equal(t, goals.BudgetLimited, g.Status)
				require.Equal(t, int64(30), g.TokensUsed)
			} else {
				require.Equal(t, goals.Paused, g.Status)
			}
		})
	}
}

func TestGoalCompactionKeepsOneUserRequest(t *testing.T) {
	t.Parallel()
	type requestContext struct{ original, focus bool }
	var requests []requestContext
	model := &goalScriptModel{mode: "complete"}
	sa, service, sess := newGoalTestAgent(t, model)
	model.inspect = func(call fantasy.Call) {
		var observed requestContext
		for _, msg := range call.Prompt {
			for _, part := range msg.Content {
				if text, ok := part.(fantasy.TextPart); ok {
					observed.original = observed.original || text.Text == "Original request"
					observed.focus = observed.focus || text.Text == focusOnInstructions
				}
			}
		}
		requests = append(requests, observed)
	}
	sess, err := sa.sessions.SetFocusMode(t.Context(), sess.ID, session.FocusModeOn)
	require.NoError(t, err)
	large := sa.largeModel.Get()
	large.CatwalkCfg.ContextWindow = 100
	sa.largeModel.Set(large)
	_, err = service.Apply(t.Context(), sess.ID, goals.Request{Op: "set", Objective: "Finish the objective across compaction"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "Original request", NonInteractive: true})
	require.NoError(t, err)
	g, err := service.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, goals.Complete, g.Status)
	msgs, err := sa.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	var users []string
	for _, msg := range msgs {
		if msg.Role == message.User {
			users = append(users, msg.Content().Text)
		}
	}
	require.Equal(t, []string{"Original request"}, users)
	require.Equal(t, []requestContext{{original: true, focus: true}, {original: false, focus: true}}, requests,
		"compaction removes the old request but must retain the active focus policy on the automatic continuation")
}

func TestGoalWithoutControlToolPauses(t *testing.T) {
	t.Parallel()
	sa, service, sess := newGoalTestAgent(t, &goalScriptModel{mode: "complete"})
	sa.SetTools(nil)
	_, err := service.Apply(t.Context(), sess.ID, goals.Request{Op: "set", Objective: "Do not loop without a completion control"})
	require.NoError(t, err)
	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "Start", NonInteractive: true})
	require.NoError(t, err)
	g, err := service.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, goals.Paused, g.Status)
	require.Zero(t, g.TokensUsed)
}

func TestGoalStaleCompactionContinuationTerminates(t *testing.T) {
	t.Parallel()
	sa, service, sess := newGoalTestAgent(t, &goalScriptModel{mode: "complete"})
	previous, err := service.Apply(t.Context(), sess.ID, goals.Request{Op: "set", Objective: "Old objective"})
	require.NoError(t, err)
	_, err = service.Apply(t.Context(), sess.ID, goals.Request{Op: "set", Objective: "Replacement objective"})
	require.NoError(t, err)
	var terminal *notify.RunComplete
	_, err = sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID, Prompt: "Internal continuation", NonInteractive: true,
		goalContinuationID: previous.ID,
		OnComplete:         func(result notify.RunComplete) { terminal = &result },
	})
	require.NoError(t, err)
	require.NotNil(t, terminal, "clients must not wait forever for a discarded continuation")
	require.True(t, terminal.Cancelled)
	current, err := service.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.True(t, current.Active(), "discarding stale work must not pause the replacement")
	msgs, err := sa.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Empty(t, msgs, "an internal continuation must not become a user request")
}
