package embed

import (
	_ "embed" // for the //go:embed directives below
	"sync"
)

// DocsModelName identifies the documentation/knowledge embedder in stored vector
// metadata. It is distinct from the code embedder (ModelName) so the docs corpus
// is embedded and searched with a general-text model rather than the code-tuned
// one; switching it triggers a clean re-embed of the docs store.
const DocsModelName = "static:potion-base-8M"

// The docs embedder ships inside the binary alongside the code embedder:
// potion-base-8M is a general-text static Model2Vec model (distilled from
// bge-base-en-v1.5), a better fit for prose documentation and knowledge than the
// code-tuned potion-code model. Like the code model it is frozen and pinned, so
// documentation semantic search works offline with no download or daemon.
//
//go:embed models/potion-base-8M/model.safetensors
var docsMatrix []byte

//go:embed models/potion-base-8M/tokenizer.json
var docsTokenizer []byte

var (
	docsLoadOnce sync.Once
	docsLoaded   *Model
	docsLoadErr  error
)

// LoadDocs returns the process-wide documentation/knowledge embedder, parsing
// the bundled potion-base-8M model on first use. It is safe for concurrent
// callers; the model parses at most once.
func LoadDocs() (*Model, error) {
	docsLoadOnce.Do(func() {
		docsLoaded, docsLoadErr = loadModel(docsMatrix, docsTokenizer, DocsModelName)
	})
	return docsLoaded, docsLoadErr
}
