package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/agent/tools"
	"github.com/neur0map/prowl/internal/message"
)

type namedTool struct{ name string }

func (n namedTool) Info() fantasy.ToolInfo                       { return fantasy.ToolInfo{Name: n.name} }
func (n namedTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (n namedTool) SetProviderOptions(_ fantasy.ProviderOptions) {}
func (n namedTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{}, nil
}

func userTurns(n int) []message.Message {
	msgs := make([]message.Message, 0, n*2)
	for range n {
		msgs = append(msgs,
			message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do it"}}},
			message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "done"}}},
		)
	}
	return msgs
}

var learnTools = []fantasy.AgentTool{namedTool{name: tools.LearnToolName}, namedTool{name: "bash"}}

// TestReviewIsNotEveryTurn is the cost property: a nudge on every turn is a
// tax on every request and trains the model to ignore it.
func TestReviewIsNotEveryTurn(t *testing.T) {
	t.Parallel()

	nudged := 0
	for turns := 1; turns <= learnReviewInterval*3; turns++ {
		if learnReviewDue(userTurns(turns), learnReviewInterval) {
			nudged++
		}
	}
	require.Equal(t, 3, nudged, "exactly one nudge per interval of user turns")
}

// TestReviewSkipsAFreshSession keeps a brand-new session from being asked to
// record a lesson before it has done any work.
func TestReviewSkipsAFreshSession(t *testing.T) {
	t.Parallel()

	require.False(t, learnReviewDue(nil, learnReviewInterval))
	require.False(t, learnReviewDue(userTurns(1), learnReviewInterval))
	require.True(t, learnReviewDue(userTurns(learnReviewInterval), learnReviewInterval))
}

// TestReviewCadenceSurvivesRestart is why the count comes from the transcript
// instead of an in-memory counter: a counter that resets on restart would
// either nudge constantly or never in a long-lived session.
func TestReviewCadenceSurvivesRestart(t *testing.T) {
	t.Parallel()

	// A resumed session arrives with its history intact and no counter.
	resumed := userTurns(learnReviewInterval)
	require.True(t, learnReviewDue(resumed, learnReviewInterval),
		"the cadence must be recoverable from history alone")
}

// TestNudgeRequiresTheTool stops the prompt asking for something the agent
// cannot do: with learn disabled the instruction would be unfollowable.
func TestNudgeRequiresTheTool(t *testing.T) {
	t.Parallel()

	msgs := userTurns(learnReviewInterval)
	withTool := withLearnReview("BASE", msgs, learnTools)
	require.Contains(t, withTool, "durable_knowledge_review")

	withoutTool := withLearnReview("BASE", msgs, []fantasy.AgentTool{namedTool{name: "bash"}})
	require.Equal(t, "BASE", withoutTool, "no learn tool means no nudge")
}

// TestNudgeIsNotDuplicated guards the prompt against accumulating copies.
func TestNudgeIsNotDuplicated(t *testing.T) {
	t.Parallel()

	msgs := userTurns(learnReviewInterval)
	once := withLearnReview("BASE", msgs, learnTools)
	twice := withLearnReview(once, msgs, learnTools)
	require.Equal(t, once, twice)
}

// TestNudgeNamesTheRefusals is the substance of the review, not its wording:
// each refusal below is a lesson that would actively mislead a later session,
// so the instruction has to rule them out explicitly.
func TestNudgeNamesTheRefusals(t *testing.T) {
	t.Parallel()

	nudge := withLearnReview("BASE", userTurns(learnReviewInterval), learnTools)

	for _, refusal := range []string{
		"did not verify", // unverified claims
		"unresolved",     // a failed attempt written up as a workflow
		"transient",      // an error that resolved on its own
		"this machine",   // environment-dependent failure
		"negative claim", // refusals the agent would later cite at itself
	} {
		require.Contains(t, nudge, refusal, "the review must rule out %q", refusal)
	}
	require.Contains(t, nudge, "review inbox",
		"the model must know a lesson is staged for a human, not applied")
}

// TestBaseSessionPromptUnchanged proves the common path pays nothing.
func TestBaseSessionPromptUnchanged(t *testing.T) {
	t.Parallel()

	for _, turns := range []int{0, 1, 3, 7, 9} {
		require.Equal(t, "BASE", withLearnReview("BASE", userTurns(turns), learnTools),
			"turn %d must not carry the nudge", turns)
	}
}
