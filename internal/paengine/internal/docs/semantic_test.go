package docs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/neur0map/prowl/internal/paengine/internal/embed"
	"github.com/neur0map/prowl/internal/paengine/internal/store"
	"github.com/stretchr/testify/require"
)

// TestDocsEmbedderLoads verifies the dedicated documentation model is bundled and
// parses, and is a distinct model from the code embedder.
func TestDocsEmbedderLoads(t *testing.T) {
	m, err := embed.LoadDocs()
	require.NoError(t, err)
	require.Equal(t, embed.DocsModelName, m.EmbedModelID())
	require.NotEqual(t, embed.ModelName, embed.DocsModelName)
	require.Positive(t, m.Dim())
}

// TestDocsSemanticSearch proves the end-to-end docs pipeline: ingesting a local
// Markdown tree embeds the corpus with the dedicated docs model and semantic
// search returns the relevant document.
func TestDocsSemanticSearch(t *testing.T) {
	home := t.TempDir()

	srcDir := t.TempDir()
	writeDoc(t, srcDir, "networking.md", "# Cluster Networking\n\nPods communicate across nodes through an overlay network. "+
		"Each service gets a stable virtual IP, and the proxy load-balances traffic to the healthy endpoints behind it.\n")
	writeDoc(t, srcDir, "storage.md", "# Persistent Storage\n\nVolumes outlive a pod restart. A claim binds to a volume so the "+
		"application keeps its data when the container is rescheduled onto another node.\n")

	if _, err := AddLocal(context.Background(), home, "handbook", srcDir); err != nil {
		t.Fatalf("AddLocal: %v", err)
	}

	// The corpus must be embedded with the dedicated docs model.
	s, err := store.Open(storePath(home))
	require.NoError(t, err)
	defer s.Close()
	model, _ := s.GetMeta("embed_model")
	require.Equal(t, embed.DocsModelName, model, "docs corpus must be embedded with the docs model")
	require.True(t, s.VectorsReady(), "docs vectors must be ready after ingest")

	// Semantic search returns the relevant document.
	packet, err := Search(home, "how do containers talk to each other over the network", 1800)
	require.NoError(t, err)
	require.NotEmpty(t, packet.Items, "expected at least one result")

	var cited string
	for _, it := range packet.Items {
		for _, c := range it.Citations {
			cited += c.Path + " "
		}
	}
	require.Contains(t, cited, "networking.md", "networking doc should be retrieved for a networking question")
}

func writeDoc(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}
