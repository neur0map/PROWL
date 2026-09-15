package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/prowlagent"
)

func resetMemoryStore(t *testing.T) {
	t.Helper()
	memoryStore.mu.Lock()
	memoryStore.entries = map[string]*memoryEntry{}
	memoryStore.mu.Unlock()
}

// TestRecallDoesNotWaitForTheEngine is the property that keeps memory off the
// interactive path. Reading knowledge goes through the engine, which
// serializes against indexing, so a turn that waited for it would stall
// behind an unrelated reindex.
func TestRecallDoesNotWaitForTheEngine(t *testing.T) {
	resetMemoryStore(t)

	dir := t.TempDir()
	memoryStore.mu.Lock()
	// Pretend a fetch is already in flight and no snapshot exists yet: the
	// worst case for a caller.
	memoryStore.entries[dir] = &memoryEntry{loading: true}
	memoryStore.mu.Unlock()

	done := make(chan []prowlagent.KnowledgeDoc, 1)
	go func() { done <- recallMemory(nil, dir) }()

	select {
	case docs := <-done:
		require.Empty(t, docs, "a cold cache recalls nothing rather than blocking")
	case <-time.After(time.Second):
		t.Fatal("recall blocked while a fetch was in flight")
	}
}

// TestRecallSeesKnowledgeAcceptedMidSession is the live half: a document
// accepted after the session started must be recalled without a restart.
func TestRecallSeesKnowledgeAcceptedMidSession(t *testing.T) {
	resetMemoryStore(t)

	dir := t.TempDir()
	memoryStore.mu.Lock()
	memoryStore.entries[dir] = &memoryEntry{fetchedAt: time.Now()}
	memoryStore.mu.Unlock()

	require.Empty(t, recallMemory(nil, dir))

	// A background refresh lands, as it would after a human accepted a lesson.
	memoryStore.mu.Lock()
	memoryStore.entries[dir].docs = []prowlagent.KnowledgeDoc{
		{Type: "Decision", Title: "Index identity is a constant", Path: "lessons/a.md"},
	}
	memoryStore.mu.Unlock()

	docs := recallMemory(nil, dir)
	require.Len(t, docs, 1, "the next turn must recall newly accepted knowledge")
	require.Equal(t, "Index identity is a constant", docs[0].Title)
}

// TestMemoryBlockCarriesOnlyTheIndex pins the cost model: the prompt gets
// titles and paths, not document bodies, so memory cannot crowd out the task.
func TestMemoryBlockCarriesOnlyTheIndex(t *testing.T) {
	t.Parallel()

	block := memoryBlock([]prowlagent.KnowledgeDoc{
		{Type: "Decision", Title: "Gateway binds loopback only", Path: "lessons/net.md"},
	})

	require.Contains(t, block, "<project_memory>")
	require.Contains(t, block, "Gateway binds loopback only")
	require.Contains(t, block, "lessons/net.md")
	require.Empty(t, memoryBlock(nil), "no memory must add nothing to the prompt")
}

// TestMemoryCannotOutrankTheRepository keeps the guardrails attached to the
// index. A confidently stated stale fact is harder to catch than an absent
// one, so the block must say what memory is worth and how to verify it.
func TestMemoryCannotOutrankTheRepository(t *testing.T) {
	t.Parallel()

	block := memoryBlock([]prowlagent.KnowledgeDoc{
		{Type: "Decision", Title: "Index identity is a constant", Path: "decisions/id.md"},
	})

	require.Contains(t, block, "heuristic", "memory is context, not instruction")
	require.Contains(t, block, "Prefer current source")
	require.Contains(t, block, "stale")
	require.Contains(t, block, "knowledge show",
		"the agent must be told how to read a document instead of guessing from the title")
}

// TestMemoryIndexIsBounded proves the block cannot grow without limit and
// start competing with the task for context.
func TestMemoryIndexIsBounded(t *testing.T) {
	t.Parallel()

	require.LessOrEqual(t, maxMemoryEntries, 32, "a prompt cannot carry an unbounded memory index")

	many := make([]prowlagent.KnowledgeDoc, 0, maxMemoryEntries*3)
	for range maxMemoryEntries * 3 {
		many = append(many, prowlagent.KnowledgeDoc{Type: "Claim", Title: "entry", Path: "lessons/e.md"})
	}
	require.Len(t, trimMemory(many), maxMemoryEntries)
}

// TestWithProjectMemoryLeavesPromptAloneWhenEmpty keeps an unindexed project
// from paying for a feature it is not using.
func TestWithProjectMemoryLeavesPromptAloneWhenEmpty(t *testing.T) {
	t.Parallel()

	const base = "system prompt"
	require.Equal(t, base, withProjectMemory(base, nil))
	require.Equal(t, base, withProjectMemory(base, func() []prowlagent.KnowledgeDoc { return nil }))
}
