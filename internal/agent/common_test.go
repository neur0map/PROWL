package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/stretchr/testify/require"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/neur0map/prowl/internal/agent/prompt"
	"github.com/neur0map/prowl/internal/agent/tools"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/csync"
	"github.com/neur0map/prowl/internal/db"
	"github.com/neur0map/prowl/internal/filetracker"
	"github.com/neur0map/prowl/internal/history"
	"github.com/neur0map/prowl/internal/lsp"
	"github.com/neur0map/prowl/internal/message"
	"github.com/neur0map/prowl/internal/permission"
	"github.com/neur0map/prowl/internal/session"

	_ "github.com/joho/godotenv/autoload"
)

// fakeEnv is an environment for testing.
type fakeEnv struct {
	workingDir  string
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	history     history.Service
	filetracker *filetracker.Service
	lspClients  *csync.Map[string, *lsp.Client]
}

type builderFunc func(t *testing.T, r *recorder.Recorder) (fantasy.LanguageModel, error)

type modelPair struct {
	name       string
	largeModel builderFunc
	smallModel builderFunc
}

func hyperBuilder(model string) builderFunc {
	return func(t *testing.T, r *recorder.Recorder) (fantasy.LanguageModel, error) {
		provider, err := openaicompat.New(
			openaicompat.WithBaseURL("https://hyper.charm.land/v1"),
			openaicompat.WithAPIKey(os.Getenv("PROWL_HYPER_API_KEY")),
			openaicompat.WithHTTPClient(&http.Client{Transport: r}),
		)
		if err != nil {
			return nil, err
		}
		return provider.LanguageModel(t.Context(), model)
	}
}

func newAgentCassetteRecorder(t *testing.T) *recorder.Recorder {
	t.Helper()

	r, err := recorder.New(
		filepath.Join("testdata", t.Name()),
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(agentCassetteMatcher(t)),
		recorder.WithSkipRequestLatency(true),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, r.Stop())
	})
	return r
}

func agentCassetteMatcher(t *testing.T) recorder.MatcherFunc {
	t.Helper()

	return func(request *http.Request, recorded cassette.Request) bool {
		if request.Method != recorded.Method || request.URL.String() != recorded.URL {
			return false
		}
		if request.Body == nil || request.Body == http.NoBody {
			return recorded.Body == ""
		}

		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("Read cassette request body: %v", err)
			return false
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(body))

		current, ok := stableAgentRequest(body)
		if !ok {
			return false
		}
		saved, ok := stableAgentRequest([]byte(recorded.Body))
		return ok && reflect.DeepEqual(current, saved)
	}
}

func stableAgentRequest(body []byte) (map[string]any, bool) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, false
	}

	delete(request, "tools")
	if messages, ok := request["messages"].([]any); ok {
		for _, raw := range messages {
			message, ok := raw.(map[string]any)
			if ok && message["role"] == "system" {
				delete(message, "content")
			}
		}
	}
	return request, true
}

func testEnv(t *testing.T) fakeEnv {
	workingDir := filepath.Join("/tmp/prowl-test/", t.Name())
	os.RemoveAll(workingDir)

	err := os.MkdirAll(workingDir, 0o755)
	require.NoError(t, err)

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)

	permissions := permission.NewPermissionService(workingDir, true, []string{})
	history := history.NewService(q, conn)
	filetrackerService := filetracker.NewService(q)
	lspClients := csync.NewMap[string, *lsp.Client]()

	t.Cleanup(func() {
		conn.Close()
		os.RemoveAll(workingDir)
	})

	return fakeEnv{
		workingDir,
		sessions,
		messages,
		permissions,
		history,
		&filetrackerService,
		lspClients,
	}
}

func testSessionAgent(env fakeEnv, large, small fantasy.LanguageModel, systemPrompt string, tools ...fantasy.AgentTool) SessionAgent {
	largeModel := Model{
		Model: large,
		CatwalkCfg: catwalk.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	smallModel := Model{
		Model: small,
		CatwalkCfg: catwalk.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	agent := NewSessionAgent(SessionAgentOptions{
		LargeModel:   largeModel,
		SmallModel:   smallModel,
		SystemPrompt: systemPrompt,
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Tools:        tools,
	})
	return agent
}

func coderAgent(r *recorder.Recorder, env fakeEnv, large, small fantasy.LanguageModel) (SessionAgent, error) {
	prompt, err := coderPrompt(
		prompt.WithPlatform("linux"),
		prompt.WithWorkingDir(filepath.ToSlash(env.workingDir)),
	)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Init(env.workingDir, "", false)
	if err != nil {
		return nil, err
	}

	// NOTE(@andreynering): Set a fixed config to ensure cassettes match
	// independently of user config on `$HOME/.config/prowl/prowl.json`.
	cfg.Config().Options.Attribution = &config.Attribution{
		TrailerStyle:  "co-authored-by",
		GeneratedWith: true,
	}

	// Clear some fields to avoid issues with VCR cassette matching.
	cfg.Config().Options.SkillsPaths = nil
	cfg.Config().Options.DisabledSkills = []string{"prowl-config"}
	cfg.Config().Options.ContextPaths = nil
	cfg.Config().Options.GlobalContextPaths = nil
	cfg.Config().LSP = nil

	systemPrompt, err := prompt.Build(context.TODO(), cfg)
	if err != nil {
		return nil, err
	}

	// Get the model name for the bash tool
	modelName := large.Model() // fallback to ID if Name not available
	if model := cfg.Config().GetModel(large.Provider(), large.Model()); model != nil {
		modelName = model.Name
	}

	allTools := []fantasy.AgentTool{
		tools.NewBashTool(env.permissions, env.workingDir, cfg.Config().Options.Attribution, modelName),
		tools.NewDownloadTool(env.permissions, env.workingDir, r.GetDefaultClient()),
		tools.NewEditTool(nil, env.permissions, env.history, *env.filetracker, env.workingDir),
		tools.NewMultiEditTool(nil, env.permissions, env.history, *env.filetracker, env.workingDir),
		tools.NewFetchTool(env.permissions, env.workingDir, r.GetDefaultClient()),
		tools.NewGlobTool(env.workingDir, cfg.Config().Tools.Glob),
		tools.NewGrepTool(env.workingDir, cfg.Config().Tools.Grep),
		tools.NewLsTool(env.permissions, env.workingDir, cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(r.GetDefaultClient()),
		tools.NewViewTool(nil, env.permissions, *env.filetracker, nil, env.workingDir),
		tools.NewWriteTool(nil, env.permissions, env.history, *env.filetracker, env.workingDir),
	}

	return testSessionAgent(env, large, small, systemPrompt, allTools...), nil
}

// createSimpleGoProject creates a simple Go project structure in the given directory.
// It creates a go.mod file and a main.go file with a basic hello world program.
func createSimpleGoProject(t *testing.T, dir string) {
	goMod := `module example.com/testproject

go 1.23
`
	err := os.WriteFile(dir+"/go.mod", []byte(goMod), 0o644)
	require.NoError(t, err)

	mainGo := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err = os.WriteFile(dir+"/main.go", []byte(mainGo), 0o644)
	require.NoError(t, err)
}
