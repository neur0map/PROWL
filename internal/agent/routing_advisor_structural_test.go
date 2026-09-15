package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAStructuralPatternIsAMisrouteAtAnyScope covers what the path-width rule
// missed: the reported search was an alternation of six spellings of one
// command name, scoped to a subdirectory. Nothing about its path was broad,
// and it was still a `find`/`def` question written as text search.
func TestAStructuralPatternIsAMisrouteAtAnyScope(t *testing.T) {
	t.Parallel()

	reported := `{"pattern":"\"track\"\\|track --source\\|cmdTrack\\|func.*[Tr]rack","path":"ryoku/cli/"}`
	require.True(t, isMisroutedSearch("grep", reported, "/home/me/project"),
		"an alternation bounded to a subtree is still a symbol question")

	for _, input := range []string{
		`{"pattern":"func NewShell","path":"internal"}`,
		`{"pattern":"type .*Advisor","path":"internal/agent"}`,
		`{"pattern":"handleFoo|handleBar","path":"internal/gateway/api"}`,
	} {
		require.True(t, isMisroutedSearch("grep", input, "/home/me/project"),
			"structural pattern should be flagged: %s", input)
	}
}

// TestALiteralSearchIsLeftAlone is the other half of the rule: exact text in a
// bounded path is what grep is for, and flagging it would train the model to
// ignore the advisory.
func TestALiteralSearchIsLeftAlone(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{"pattern":"PROWL_WEBUI_DIR","path":"internal"}`,
		`{"pattern":"no such module: fts5","path":"internal/paengine"}`,
		`{"pattern":"freellmapi_dashboard_token","path":"internal/gateway/webui"}`,
	} {
		require.False(t, isMisroutedSearch("grep", input, "/home/me/project"),
			"literal search in a bounded path is legitimate: %s", input)
	}
}

// TestTheAdvisorSharpensAfterTheIndexWasUsed is the reported sequence: the
// model queried prowl_agent and searched manually anyway. A generic "consider
// the index" reminder is wrong there — it already did, so the advice has to be
// about following citations instead.
func TestTheAdvisorSharpensAfterTheIndexWasUsed(t *testing.T) {
	t.Parallel()

	advisor := newRoutingAdvisor("/home/me/project")
	require.Equal(t, routingReminder, advisor.reminder(),
		"before any index call, point at the index")

	advisor.indexCalls.Add(1)
	require.Equal(t, alreadyAsked, advisor.reminder(),
		"after an index call, a manual scan of the same ground needs different advice")
	require.Contains(t, advisor.reminder(), "citations")
}
