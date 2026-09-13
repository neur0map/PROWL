// Package goals manages one persistent autonomous objective per session.
package goals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/neur0map/prowl/internal/db"
	"github.com/neur0map/prowl/internal/pubsub"
)

type Status string

const (
	Active        Status = "active"
	Paused        Status = "paused"
	BudgetLimited Status = "budget-limited"
	Complete      Status = "complete"
	Dropped       Status = "dropped"
)

// Goal is independent of conversation summaries and session usage snapshots.
type Goal struct {
	SessionID string `json:"session_id"`
	// ID identifies an activation; resuming invalidates stale run controls.
	ID              string  `json:"id"`
	Objective       string  `json:"objective"`
	Status          Status  `json:"status"`
	TokenBudget     *int64  `json:"token_budget,omitempty"`
	TokensUsed      int64   `json:"tokens_used"`
	TimeUsedSeconds float64 `json:"time_used_seconds"`
	CreatedAt       int64   `json:"created_at"`
	UpdatedAt       int64   `json:"updated_at"`
}

func (g *Goal) Active() bool { return g != nil && g.Status == Active }

func (g *Goal) RemainingTokens() *int64 {
	if g == nil || g.TokenBudget == nil {
		return nil
	}
	n := max(0, *g.TokenBudget-g.TokensUsed)
	return &n
}

// Request carries user controls or the narrower goal tool operations. A nil
// TokenBudget on a budget request removes the cap.
type Request struct {
	Op          string `json:"op"`
	Objective   string `json:"objective,omitempty"`
	TokenBudget *int64 `json:"token_budget,omitempty"`
	GoalID      string `json:"goal_id,omitempty"`
}

// Service serializes transitions and usage updates before publishing snapshots.
type Service struct {
	*pubsub.Broker[Goal]
	mu       sync.Mutex
	q        *db.Queries
	restored map[string]bool
}

func NewService(q *db.Queries) *Service {
	return &Service{Broker: pubsub.NewBroker[Goal](), q: q, restored: make(map[string]bool)}
}

func (s *Service) Get(ctx context.Context, sessionID string) (*Goal, error) {
	row, err := s.q.GetSessionGoal(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g := &Goal{
		SessionID: row.SessionID, ID: row.ID, Objective: row.Objective,
		Status: Status(row.Status), TokensUsed: row.TokensUsed,
		TimeUsedSeconds: row.TimeUsedSeconds, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.TokenBudget.Valid {
		g.TokenBudget = &row.TokenBudget.Int64
	}
	return g, nil
}

// Restore pauses active records inherited from a previous process. Goals
// explicitly activated in this service stay active, including background
// sessions and internal continuations.
func (s *Service) Restore(ctx context.Context, sessionID string) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !s.restored[sessionID] && g.Active() {
		g.Status = Paused
		return s.save(ctx, g)
	}
	s.restored[sessionID] = true
	return g, nil
}

func (s *Service) Apply(ctx context.Context, sessionID string, req Request) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	if req.TokenBudget != nil && *req.TokenBudget <= 0 {
		return nil, errors.New("goal budget must be a positive integer")
	}
	g, err := s.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if req.Op == "get" || req.Op == "show" {
		return g, nil
	}
	if req.GoalID != "" && (g == nil || g.ID != req.GoalID) {
		return nil, errors.New("goal changed; inspect the current goal before acting")
	}
	if req.Op == "create" || req.Op == "set" {
		objective := strings.TrimSpace(req.Objective)
		if objective == "" {
			return nil, errors.New("goal objective is required")
		}
		if req.Op == "create" && g != nil && g.Status != Complete {
			return nil, errors.New("a goal already exists; resume it or drop it before creating another")
		}
		g = &Goal{
			SessionID: sessionID, ID: uuid.NewString(), Objective: objective,
			Status: Active, TokenBudget: req.TokenBudget, CreatedAt: time.Now().UnixMilli(),
		}
		return s.save(ctx, g)
	}
	if g == nil {
		return nil, errors.New("no goal is set")
	}
	switch req.Op {
	case "pause":
		if g.Status == Complete {
			return nil, errors.New("goal is already complete")
		}
		g.Status = Paused
	case "resume":
		if g.Status == Complete {
			return nil, errors.New("a completed goal cannot be resumed; set a new objective")
		}
		if left := g.RemainingTokens(); left != nil && *left == 0 {
			return nil, errors.New("goal budget is exhausted; raise it or use /goal budget off before resuming")
		}
		if g.Status != Active {
			g.ID = uuid.NewString()
		}
		g.Status = Active
	case "budget":
		if g.Status == Complete {
			return nil, errors.New("goal is already complete")
		}
		g.TokenBudget = req.TokenBudget
		if left := g.RemainingTokens(); left != nil && *left == 0 {
			if g.Status == Active {
				g.Status = BudgetLimited
			}
		} else if g.Status == BudgetLimited {
			g.ID = uuid.NewString()
			g.Status = Active
		}
	case "complete":
		if g.Status == Complete {
			return nil, errors.New("goal is already complete")
		}
		if g.Status == Paused {
			return nil, errors.New("resume the paused goal before completing it")
		}
		g.Status = Complete
	case "drop":
		if err := s.q.DeleteSessionGoal(ctx, sessionID); err != nil {
			return nil, err
		}
		g.Status = Dropped
		g.UpdatedAt = time.Now().UnixMilli()
		s.Publish(pubsub.DeletedEvent, *g)
		return g, nil
	default:
		return nil, fmt.Errorf("unknown goal operation %q", req.Op)
	}
	return s.save(ctx, g)
}

// Account charges the goal that owned a model step, even if that step paused or
// completed it. Replacing/dropping a goal never charges its successor. Cache
// reads are excluded by the caller; input, cache writes and output are charged.
func (s *Service) Account(ctx context.Context, sessionID, goalID string, tokens int64, elapsed time.Duration) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.Get(ctx, sessionID)
	if err != nil || g == nil || goalID == "" || g.ID != goalID {
		return g, err
	}
	g.TokensUsed += max(0, tokens)
	g.TimeUsedSeconds += max(0, elapsed.Seconds())
	if left := g.RemainingTokens(); g.Status == Active && left != nil && *left == 0 {
		g.Status = BudgetLimited
	}
	return s.save(ctx, g)
}

// PauseActive leaves terminal and already-paused records alone. It is used on
// interruption and when restoring a saved session, never on an internal
// continuation or context compaction.
func (s *Service) PauseActive(ctx context.Context, sessionID, expectedID string) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.Get(ctx, sessionID)
	if err != nil || !g.Active() || (expectedID != "" && g.ID != expectedID) {
		return g, err
	}
	g.Status = Paused
	return s.save(ctx, g)
}

func (s *Service) save(ctx context.Context, g *Goal) (*Goal, error) {
	g.UpdatedAt = time.Now().UnixMilli()
	budget := sql.NullInt64{}
	if g.TokenBudget != nil {
		budget = sql.NullInt64{Int64: *g.TokenBudget, Valid: true}
	}
	_, err := s.q.SaveSessionGoal(ctx, db.SaveSessionGoalParams{
		SessionID: g.SessionID, ID: g.ID, Objective: g.Objective, Status: string(g.Status),
		TokenBudget: budget, TokensUsed: g.TokensUsed, TimeUsedSeconds: g.TimeUsedSeconds,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	})
	if err != nil {
		return nil, err
	}
	s.restored[g.SessionID] = true
	s.Publish(pubsub.UpdatedEvent, *g)
	return g, nil
}
