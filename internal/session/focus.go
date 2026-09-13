package session

import (
	"context"
	"fmt"

	"github.com/neur0map/prowl/internal/db"
	"github.com/neur0map/prowl/internal/pubsub"
)

// FocusMode is a persistent, user-selected response style. The empty value
// means the session has never opted in; explicit off survives compaction.
type FocusMode string

const (
	FocusModeOn  FocusMode = "on"
	FocusModeOff FocusMode = "off"
)

// Valid reports whether the mode is an explicit user-selectable setting.
func (mode FocusMode) Valid() bool {
	return mode == FocusModeOn || mode == FocusModeOff
}

// SetFocusMode updates only the preference, so stale usage or title saves
// cannot restore an older mode.
func (s *service) SetFocusMode(ctx context.Context, id string, mode FocusMode) (Session, error) {
	if !mode.Valid() {
		return Session{}, fmt.Errorf("invalid focus mode %q: use on or off", mode)
	}
	item, err := s.q.SetSessionFocusMode(ctx, db.SetSessionFocusModeParams{ID: id, FocusMode: string(mode)})
	if err != nil {
		return Session{}, err
	}
	saved := s.fromDBItem(item)
	s.applyEstimatedUsageState(&saved)
	s.Publish(pubsub.UpdatedEvent, saved)
	return saved, nil
}
