// Package notify defines domain notification types for agent events.
// These types are decoupled from UI concerns so the agent can publish
// events without importing UI packages.
package notify

// Type identifies the kind of agent notification.
type Type string

const (
	// TypeAgentFinished indicates the agent has completed its turn.
	TypeAgentFinished Type = "agent_finished"
	// TypeReAuthenticate indicates the agent encountered an
	// authentication error and the user needs to re-authenticate.
	TypeReAuthenticate Type = "re_authenticate"
	// TypeAgentError indicates the agent's turn terminated with an
	// error. The error text is carried in Notification.Message.
	TypeAgentError Type = "error"
	// TypeAWSSSOAuth indicates AWS SSO credentials have expired and the
	// coordinator is running the configured refresh command. It opens the
	// AWS SSO dialog; a follow-up with the same type carries the SSO URL
	// once it appears in the command output. AWSSOCommand carries the
	// command being run; AWSSOURL carries the verification URL when known.
	TypeAWSSSOAuth Type = "aws_sso_auth"
	// TypeAWSSSOAuthResult indicates the AWS SSO refresh command has
	// finished. Message carries the error text when it failed, empty on
	// success.
	TypeAWSSSOAuthResult Type = "aws_sso_auth_result"
	// TypeReasoningChanged reports the reasoning mode/effort resolved for
	// the active run (auto classification, an ultrathink override, or the
	// persisted manual preference). It is scoped to a single Run instance
	// via Notification.ReasoningTurnID: one start event carries the
	// resolved effort, and a matching end event (empty ReasoningEffort)
	// fires on every exit. It is never persisted to model config.
	TypeReasoningChanged Type = "reasoning_changed"
)

// Notification represents a domain event published by the agent.
type Notification struct {
	SessionID    string
	SessionTitle string
	Type         Type
	ProviderID   string
	// RunID, when non-empty, is the caller-supplied correlator
	// (proto.AgentMessage.RunID) for the run that produced this
	// notification. It lets observers attribute a TypeAgentError to a
	// specific request rather than to any in-flight run on the
	// session. Empty when no caller set one.
	RunID string
	// Message carries the error text for TypeAgentError. Other
	// notification types ignore it.
	Message string
	// AWSSOCommand carries the shell command for TypeAWSSSOAuth.
	AWSSOCommand string
	// AWSSOURL carries the SSO verification URL for TypeAWSSSOAuth once it
	// appears in the refresh command's output.
	AWSSOURL string
	// Reasoning fields carry the per-run reasoning decision for
	// TypeReasoningChanged. They are transient UI state and are never
	// written back to model config.
	//
	// ReasoningTurnID uniquely identifies the active Run instance that
	// produced the event (not the caller-supplied RunID): a folded
	// follow-up prompt updates the same turn id, while the UI ignores an
	// end event whose turn id is stale. ReasoningMode is one of
	// "auto", "ultrathink", or "manual". ReasoningEffort is the resolved
	// concrete effort applied to the request; an empty ReasoningEffort on
	// a TypeReasoningChanged event marks the end of the turn's reasoning.
	// ModelID names the selected model the decision applies to so the UI
	// can validate the event against its current model/provider.
	ReasoningTurnID string
	ReasoningMode   string
	ReasoningEffort string
	ModelID         string
}

// RunComplete is the authoritative end-of-run signal for a session.
// It is published exactly once per top-level agent run (per
// [sessionAgent.Run] invocation that actually executed) after all
// message updates for the turn have been flushed via
// message.Service.FlushAll. Carries the final assistant text and
// message ID so non-interactive clients can reconcile stdout even if
// SSE events arrive out of order or are dropped by the broker. Error
// is non-empty when the run terminated with an error; Cancelled is
// true when the run terminated due to context cancellation. The two
// are mutually exclusive in the success case but may overlap when a
// cancel triggers a downstream error.
//
// RunID identifies the specific request that produced this event.
// It is the value the caller set on `proto.AgentMessage.RunID` (or
// equivalently propagated via agent.WithRunID on the context that
// reaches the coordinator); empty when no caller set one. Filtering
// by RunID lets a client correlate a SendMessage call with its
// terminal event even when the session is busy and other turns are
// finishing on the same session.
type RunComplete struct {
	SessionID string
	RunID     string
	MessageID string
	Text      string
	Error     string
	Cancelled bool
}
