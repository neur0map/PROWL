package agent

import (
	"strings"

	"charm.land/fantasy"

	"github.com/neur0map/prowl/internal/agent/tools"
	"github.com/neur0map/prowl/internal/message"
)

// Recording a lesson is the agent's job; deciding whether it becomes durable

// learnReviewInterval is how many user turns pass between nudges. Hermes uses
// 10; the same number is right here for the same reason: often enough that a
// lesson is still fresh, rare enough that it is not a per-turn tax.
const learnReviewInterval = 10

// learnReviewNudge is appended to the system prompt on a turn where a review
const learnReviewNudge = `<durable_knowledge_review>
Before you finish this turn, consider whether the recent work produced a durable lesson, and record it with the learn tool if so. Recording is yours to decide; a recorded lesson enters a review inbox and only a human can accept it, so a proposal costs the user nothing but a glance.

Record: a root cause found after a wrong hypothesis, a convention this codebase follows but does not state, a trap that cost real time, or the reason behind a fix that the diff does not show.

Do not record: anything you did not verify, an unresolved attempt described as if it worked, a transient error that resolved on its own, a failure that depended on this machine's state, a negative claim about a tool or feature (those harden into refusals a later session will cite against itself), routine work, or a narrative of what you just did. If nothing meets the bar, record nothing and say nothing about it — an empty review is the common outcome, not a missed opportunity.
</durable_knowledge_review>`

// learnReviewDue reports whether this turn should carry the review nudge.
func learnReviewDue(msgs []message.Message, interval int) bool {
	if interval <= 0 {
		return false
	}
	turns := 0
	for _, m := range msgs {
		if m.Role == message.User {
			turns++
		}
	}
	// The nudge lands on the turn *after* a full interval of work, so a
	// brand-new session is never interrupted before it has done anything.
	return turns > 0 && turns%interval == 0
}

// hasLearnTool reports whether the lesson tool is available on this turn.
// Without it the nudge would ask for something the agent cannot do.
func hasLearnTool(agentTools []fantasy.AgentTool) bool {
	for _, tool := range agentTools {
		if tool.Info().Name == tools.LearnToolName {
			return true
		}
	}
	return false
}

// withLearnReview appends the nudge to a system prompt when one is due.
func withLearnReview(systemPrompt string, msgs []message.Message, agentTools []fantasy.AgentTool) string {
	if !hasLearnTool(agentTools) || !learnReviewDue(msgs, learnReviewInterval) {
		return systemPrompt
	}
	if strings.Contains(systemPrompt, "<durable_knowledge_review>") {
		return systemPrompt
	}
	return systemPrompt + "\n\n" + learnReviewNudge
}
