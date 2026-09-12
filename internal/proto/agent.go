package proto

import (
	"encoding/json"
	"errors"
)

// AgentEventType represents the type of agent event.
type AgentEventType string

const (
	AgentEventTypeError     AgentEventType = "error"
	AgentEventTypeResponse  AgentEventType = "response"
	AgentEventTypeSummarize AgentEventType = "summarize"
	// AgentEventTypeReasoningChanged mirrors notify.TypeReasoningChanged
	// over the wire so remote clients receive per-run reasoning state.
	AgentEventTypeReasoningChanged AgentEventType = "reasoning_changed"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (t AgentEventType) MarshalText() ([]byte, error) {
	return []byte(t), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (t *AgentEventType) UnmarshalText(text []byte) error {
	*t = AgentEventType(text)
	return nil
}

// AgentEvent represents an event emitted by the agent.
type AgentEvent struct {
	Type    AgentEventType `json:"type"`
	Message Message        `json:"message"`
	Error   error          `json:"error,omitempty"`

	// RunID echoes the caller-supplied AgentMessage.RunID for the run
	// that produced this event. It lets observers (notably
	// `prowl run`) attribute an error event to a specific request
	// instead of to any in-flight run on the session. Empty when no
	// caller set one.
	RunID string `json:"run_id,omitempty"`

	// When summarizing.
	SessionID    string `json:"session_id,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
	Progress     string `json:"progress,omitempty"`
	Done         bool   `json:"done,omitempty"`

	// AWS SSO progress fields, carried for TypeAWSSSOAuth and
	// TypeAWSSSOAuthResult so the refresh dialog works in client/server
	// mode. The command runs on the server; these ferry its progress to
	// the client. AWSSOCommand is the refresh command being run; AWSSOURL
	// is the verification URL once it appears in the command output. The
	// result's failure text travels through Error, like TypeAgentError.
	AWSSOCommand string `json:"aws_sso_command,omitempty"`
	AWSSOURL     string `json:"aws_sso_url,omitempty"`

	// ProviderID names the provider the event applies to. It mirrors
	// notify.Notification.ProviderID (previously dropped on the wire) and
	// is required so remote clients can validate a reasoning event against
	// their selected model.
	ProviderID string `json:"provider_id,omitempty"`

	// Reasoning fields mirror notify.Notification for
	// AgentEventTypeReasoningChanged. ReasoningTurnID scopes the event to
	// one active Run instance; ReasoningMode is auto|ultrathink|manual;
	// ReasoningEffort is the resolved concrete effort ("" marks the end of
	// the turn's reasoning); ModelID is the selected model the decision
	// applies to.
	ReasoningTurnID string `json:"reasoning_turn_id,omitempty"`
	ReasoningMode   string `json:"reasoning_mode,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ModelID         string `json:"model_id,omitempty"`
}

// MarshalJSON implements the [json.Marshaler] interface.
func (e AgentEvent) MarshalJSON() ([]byte, error) {
	type Alias AgentEvent
	return json.Marshal(&struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Error: func() string {
			if e.Error != nil {
				return e.Error.Error()
			}
			return ""
		}(),
		Alias: Alias(e),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (e *AgentEvent) UnmarshalJSON(data []byte) error {
	type Alias AgentEvent
	aux := &struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Alias: Alias(*e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*e = AgentEvent(aux.Alias)
	if aux.Error != "" {
		e.Error = errors.New(aux.Error)
	}
	return nil
}
