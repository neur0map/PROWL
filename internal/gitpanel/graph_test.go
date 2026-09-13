package gitpanel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func rails(commits []Commit) []string {
	rows := buildGraph(commits)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Rail
	}
	return out
}

func TestBuildGraphLinear(t *testing.T) {
	commits := []Commit{
		{Hash: "c3", Parents: []string{"c2"}},
		{Hash: "c2", Parents: []string{"c1"}},
		{Hash: "c1"},
	}
	require.Equal(t, []string{"●", "●", "●"}, rails(commits))
}

func TestBuildGraphSingleMerge(t *testing.T) {
	// M merges A and B; both descend from C.
	//   M
	//   ├╮
	//   A│
	//   │B
	//   ●╯  C
	commits := []Commit{
		{Hash: "M", Parents: []string{"A", "B"}},
		{Hash: "A", Parents: []string{"C"}},
		{Hash: "B", Parents: []string{"C"}},
		{Hash: "C"},
	}
	require.Equal(t, []string{"●╮", "●│", "│●", "●╯"}, rails(commits))
}

func TestBuildGraphMergeThenLinear(t *testing.T) {
	// After the merge collapses back to one lane, the graph is single-column
	// again.
	commits := []Commit{
		{Hash: "M", Parents: []string{"A", "B"}},
		{Hash: "A", Parents: []string{"C"}},
		{Hash: "B", Parents: []string{"C"}},
		{Hash: "C", Parents: []string{"D"}},
		{Hash: "D"},
	}
	got := rails(commits)
	require.Equal(t, []string{"●╮", "●│", "│●", "●╯", "●"}, got)
}

func TestBuildGraphEmpty(t *testing.T) {
	require.Empty(t, rails(nil))
}
