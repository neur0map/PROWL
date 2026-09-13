package gitpanel

import (
	"context"
	"errors"
	"strings"
)

// errGhMissing marks the gh binary as unavailable on PATH.
var errGhMissing = errors.New("gh not installed")

// cmdError wraps an exec failure with the command's stderr so callers can
// surface a short, human-readable reason.
type cmdError struct {
	err    error
	stderr string
}

func (e *cmdError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		return e.err.Error()
	}
	return msg
}

func (e *cmdError) Unwrap() error { return e.err }

// shortReason condenses an error into a single trimmed line suitable for a
// dim status note.
func shortReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > 60 {
		msg = msg[:57] + "…"
	}
	if msg == "" {
		return "error"
	}
	return msg
}

// ghReason maps a gh failure to a compact, user-facing note. It distinguishes
// a missing binary, an auth problem, and a generic failure.
func ghReason(err error) string {
	if errors.Is(err, errGhMissing) {
		return "gh not available"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "gh timed out"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not logged") ||
		strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "gh auth login"):
		return "gh not authenticated"
	case strings.Contains(msg, "no git remotes") ||
		strings.Contains(msg, "not a git repository") ||
		strings.Contains(msg, "could not determine"):
		return "no GitHub remote"
	default:
		return "gh unavailable"
	}
}
