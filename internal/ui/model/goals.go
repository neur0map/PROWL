package model

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/goals"
	"github.com/neur0map/prowl/internal/message"
	"github.com/neur0map/prowl/internal/session"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/dialog"
	"github.com/neur0map/prowl/internal/ui/util"
)

type goalWorkspace interface {
	ControlGoal(context.Context, string, goals.Request) (*goals.Goal, error)
}

type goalSnapshotMsg struct {
	generation uint64
	sessionID  string
	goal       *goals.Goal
	err        error
}
type goalCommandMsg struct {
	generation    uint64
	origin        string
	session       session.Session
	goal          *goals.Goal
	request       goals.Request
	prompt, draft string
	attachments   []message.Attachment
	err           error
}
type goalRunSubmittedMsg struct {
	generation        uint64
	sessionID, goalID string
	err               error
}

func (m *UI) loadGoal(sessionID string) tea.Cmd {
	ws, ok := m.com.Workspace.(goalWorkspace)
	if !ok {
		return nil
	}
	m.goalSnapshotGeneration++
	generation := m.goalSnapshotGeneration
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		g, err := ws.ControlGoal(ctx, sessionID, goals.Request{Op: "restore"})
		return goalSnapshotMsg{generation, sessionID, g, err}
	}
}

func (m *UI) runGoalCommand(req goals.Request, draft string, attachments []message.Attachment) tea.Cmd {
	ws, ok := m.com.Workspace.(goalWorkspace)
	if !ok {
		return m.slashError(draft, errors.New("this workspace does not support goals"))
	}
	if !m.hasSession() && req.Op != "create" && req.Op != "set" && req.Op != "guided" {
		if req.Op == "get" || req.Op == "show" {
			m.dialog.OpenDialog(dialog.NewGoal(m.com, nil, ""))
			return nil
		}
		return m.slashError(draft, errors.New("no goal is set"))
	}
	if m.goalRequestCancel != nil {
		if req.Op != "pause" && req.Op != "drop" {
			return m.slashError(draft, errors.New("wait for the pending goal command"))
		}
		m.goalRequestCancel()
	}
	m.goalRequestGeneration++
	generation := m.goalRequestGeneration
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	m.goalRequestCancel = cancel
	var sess session.Session
	if m.session != nil {
		sess = *m.session
	}
	origin := sess.ID
	workspace := m.com.Workspace
	if req.Op == "set" || req.Op == "create" || req.Op == "guided" {
		attachments = slices.Clone(attachments)
		m.attachments.Reset()
	} else {
		attachments = nil
	}
	return func() tea.Msg {
		result := goalCommandMsg{generation: generation, origin: origin, session: sess, request: req, draft: draft, attachments: attachments}
		if req.Op == "set" || req.Op == "create" || req.Op == "resume" || req.Op == "guided" {
			if err := workspace.AgentReadyErr(); err != nil {
				result.err = err
				return result
			}
		}
		if sess.ID == "" {
			var err error
			sess, err = workspace.CreateSession(ctx, "Goal")
			if err != nil {
				result.err = err
				return result
			}
			result.session = sess
		}
		control := req
		if req.Op == "guided" {
			control = goals.Request{Op: "guided"}
		}
		var beforeBudget *goals.Goal
		if req.Op == "budget" {
			var err error
			beforeBudget, err = ws.ControlGoal(ctx, sess.ID, goals.Request{Op: "get"})
			if err != nil {
				result.err = err
				return result
			}
		}
		g, err := ws.ControlGoal(ctx, sess.ID, control)
		result.goal, result.err = g, err
		if err != nil {
			return result
		}
		if req.Op == "guided" {
			if g != nil && g.Status != goals.Complete {
				result.err = errors.New("resume or drop the current goal before starting a guided interview")
				return result
			}
			result.prompt = goals.GuidedInterview(req.Objective)
		} else if g.Active() {
			switch req.Op {
			case "create", "set":
				result.prompt = "Work toward the active goal:\n\n" + g.Objective
			case "resume":
				if !workspace.AgentIsSessionBusy(sess.ID) {
					result.prompt = g.Continuation()
				}
			case "budget":
				if beforeBudget != nil && beforeBudget.Status == goals.BudgetLimited && !workspace.AgentIsSessionBusy(sess.ID) {
					result.prompt = g.Continuation()
				}
			}
		}
		return result
	}
}

func (m *UI) applyGoalCommand(msg goalCommandMsg) tea.Cmd {
	if msg.generation != m.goalRequestGeneration {
		if msg.goal.Active() && (msg.request.Op == "create" || msg.request.Op == "set" || msg.prompt != "") {
			return m.pauseAbandonedGoal(msg.session.ID, msg.goal.ID)
		}
		return nil
	}
	if m.goalRequestCancel != nil {
		m.goalRequestCancel()
		m.goalRequestCancel = nil
	}
	current := m.currentSessionID()
	if msg.err != nil {
		if current == msg.origin && m.textarea.Value() == "" {
			m.textarea.SetValue(msg.draft)
			for _, attachment := range msg.attachments {
				m.attachments.Update(attachment)
			}
			m.updateLayoutAndSize()
		}
		return util.ReportError(msg.err)
	}
	var cmds []tea.Cmd
	if current == msg.origin {
		if !m.hasSession() {
			m.session = &msg.session
			m.setState(uiChat, m.focus)
			cmds = append(cmds, m.loadSession(msg.session.ID))
		}
		m.currentGoal = msg.goal
		m.goalSnapshotGeneration++
		if msg.goal != nil && msg.goal.Status == goals.Dropped {
			m.currentGoal = nil
		}
		m.updateLayoutAndSize()
		if msg.request.Op == "get" || msg.request.Op == "show" {
			m.dialog.OpenDialog(dialog.NewGoal(m.com, m.currentGoal, msg.session.ID))
		} else if msg.prompt == "" {
			status := "Goal dropped"
			if msg.goal != nil && msg.goal.Status != goals.Dropped {
				status = "Goal " + string(msg.goal.Status)
			}
			cmds = append(cmds, util.ReportInfo(status))
		}
	}
	if msg.prompt != "" {
		m.goalRunGeneration++
		generation := m.goalRunGeneration
		ctx, cancel := context.WithCancel(context.Background())
		m.goalRunCancel, m.goalRunSessionID = cancel, msg.session.ID
		if m.currentSessionID() == msg.session.ID {
			common.StartTurn()
			m.agentBusyCache.set(true)
			m.busyFetchGen++
			m.invalidatePromptQueue()
		}
		ws := m.com.Workspace
		cmds = append(cmds, func() tea.Msg {
			err := ws.AgentRun(ctx, msg.session.ID, msg.prompt, msg.attachments...)
			goalID := ""
			if msg.goal != nil {
				goalID = msg.goal.ID
			}
			return goalRunSubmittedMsg{generation, msg.session.ID, goalID, err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *UI) pauseAbandonedGoal(sessionID, goalID string) tea.Cmd {
	ws := m.com.Workspace.(goalWorkspace)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = ws.ControlGoal(ctx, sessionID, goals.Request{Op: "pause", GoalID: goalID})
		return nil
	}
}

func (m *UI) goalLine(width int) string {
	g := m.currentGoal
	if g == nil || g.SessionID != m.currentSessionID() {
		return ""
	}
	if m.goalLineFor != g {
		usage := fmt.Sprintf("%d tokens", g.TokensUsed)
		if g.TokenBudget != nil {
			usage = fmt.Sprintf("%d/%d tokens", g.TokensUsed, *g.TokenBudget)
		}
		objective := strings.Join(strings.Fields(ansi.Strip(g.Objective)), " ")
		m.goalLineText = fmt.Sprintf("Goal %s | %s | %s", g.Status, usage, objective)
		m.goalLineFor = g
	}
	return m.com.Styles.Header.KeystrokeTip.Render(ansi.Truncate(m.goalLineText, max(0, width), "…"))
}
