package catalog

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDirectoryListsEveryRoutableProvider is the gap users hit: the merged
// upstream lists omit providers Prowl has adapters for, so the Providers tab
// hid two dozen entries while the Add-key dialog offered them from its own
// hardcoded list. The two vocabularies disagreed about what the product
// supports.
func TestDirectoryListsEveryRoutableProvider(t *testing.T) {
	t.Parallel()

	entries, err := Directory()
	require.NoError(t, err)

	byID := make(map[string]DirectoryEntry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	for _, extra := range adapterOnlyProviders {
		got, ok := byID[extra.ID]
		require.True(t, ok, "%s has an adapter but is missing from the directory", extra.ID)
		require.NotEmpty(t, got.Name, "%s must be presentable", extra.ID)
		require.NotEmpty(t, got.Class, "%s must declare a class so it sorts", extra.ID)
		require.NotEmpty(t, got.Friction, "%s must say what signing up costs", extra.ID)
		require.True(t, got.Routable, "%s is routable by definition here", extra.ID)
		// A count we do not know must stay zero rather than be invented.
		require.Zero(t, got.FreeModels, "%s claims a model count it cannot support", extra.ID)
	}
}

// TestDirectoryPutsFreeFirst pins the ordering the list is read in: someone
// looking for capacity should meet what costs nothing before what only bills.
func TestDirectoryPutsFreeFirst(t *testing.T) {
	t.Parallel()

	entries, err := Directory()
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	lastRank := -1
	for _, e := range entries {
		rank := directoryClassRank(e.Class)
		require.GreaterOrEqual(t, rank, lastRank,
			"%s (%s) breaks the class ordering", e.ID, e.Class)
		lastRank = rank
	}
	require.Equal(t, ClassFree, entries[0].Class, "the list must open on free capacity")

	// Within the free block, the provider offering the most free models leads.
	var free []DirectoryEntry
	for _, e := range entries {
		if e.Class == ClassFree {
			free = append(free, e)
		}
	}
	require.Greater(t, len(free), 1)
	for i := 1; i < len(free); i++ {
		require.GreaterOrEqual(t, free[i-1].FreeModels, free[i].FreeModels,
			"free providers must be ordered by what they actually offer")
	}
}

// TestDirectoryIsStableAcrossCalls is the regression for a cache the merge
// corrupted. The merge appended to, and the sort reordered, the slice held by
// the cached loader: the first caller saw every provider and every caller
// after it saw the original length, with two dozen entries pushed past it and
// silently lost. A dashboard therefore showed the full catalogue once and a
// short one for the rest of the process's life.
func TestDirectoryIsStableAcrossCalls(t *testing.T) {
	first, err := Directory()
	require.NoError(t, err)
	require.NotEmpty(t, first)

	for i := 2; i <= 4; i++ {
		again, err := Directory()
		require.NoError(t, err)
		require.Len(t, again, len(first), "call %d returned a different catalogue size", i)
		for j := range first {
			require.Equal(t, first[j].ID, again[j].ID,
				"call %d differs from the first at position %d", i, j)
		}
	}
}

// TestDirectoryCallerCannotCorruptTheCache covers the other half: a consumer
// that sorts or truncates its copy must not change what the next reader sees.
func TestDirectoryCallerCannotCorruptTheCache(t *testing.T) {
	before, err := Directory()
	require.NoError(t, err)
	require.Greater(t, len(before), 2)
	// Copied by VALUE: holding the slice would alias the cache the assertion
	// is meant to protect, and the comparison would pass against itself.
	firstID, count := before[0].ID, len(before)

	mine, err := Directory()
	require.NoError(t, err)
	sort.Slice(mine, func(i, j int) bool { return mine[i].ID > mine[j].ID })
	mine[0] = DirectoryEntry{ID: "scribbled-over"}

	after, err := Directory()
	require.NoError(t, err)
	require.Len(t, after, count)
	require.Equal(t, firstID, after[0].ID, "a caller reordered the shared catalogue")
}
