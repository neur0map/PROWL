package prompt

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/filepathext"
	"github.com/neur0map/prowl/internal/home"
	"github.com/neur0map/prowl/internal/rules"
	"github.com/neur0map/prowl/internal/skills"
)

// Prompt represents a template-based prompt generator.
type Prompt struct {
	template   *template.Template
	platform   string
	workingDir string
}

type PromptDat struct {
	Config             config.Config
	WorkingDir         string
	Platform           string
	ContextFiles       []ContextFile
	GlobalContextFiles []ContextFile
	Rules              []rules.Rule
	AvailSkillXML      string
}

type ContextFile struct {
	Path    string
	Content string
}

type Option func(*Prompt)

func WithPlatform(platform string) Option {
	return func(p *Prompt) {
		p.platform = platform
	}
}

func WithWorkingDir(workingDir string) Option {
	return func(p *Prompt) {
		p.workingDir = workingDir
	}
}

func NewPrompt(name, promptTemplate string, opts ...Option) (*Prompt, error) {
	parsed, err := template.New(name).Parse(promptTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}
	p := &Prompt{template: parsed}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

func (p *Prompt) Build(ctx context.Context, store *config.ConfigStore) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var sb strings.Builder
	d := p.promptData(store)
	if err := p.template.Execute(&sb, d); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}

	return sb.String(), nil
}

func processFile(filePath string) *ContextFile {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return &ContextFile{
		Path:    filePath,
		Content: string(content),
	}
}

// expandPath expands ~ and environment variables in file paths
func expandPath(path string, store *config.ConfigStore) string {
	path = home.Long(path)
	// Handle environment variable expansion using the same pattern as config
	if strings.HasPrefix(path, "$") {
		if expanded, err := store.Resolver().ResolveValue(path); err == nil {
			path = expanded
		}
	}

	return path
}

// loadContextFiles preserves configured precedence while deduplicating files,
// including files reached through overlapping directories or symbolic links.
func loadContextFiles(paths []string, store *config.ConfigStore) []ContextFile {
	if len(paths) == 0 {
		return nil
	}
	var files []ContextFile
	seenRoots := make(map[string]struct{}, len(paths))
	seenFiles := make(map[string]struct{}, len(paths))
	addFile := func(path string) {
		path = canonicalContextPath(path)
		if _, ok := seenFiles[path]; ok {
			return
		}
		seenFiles[path] = struct{}{}
		if file := processFile(path); file != nil {
			files = append(files, *file)
		}
	}
	for _, path := range paths {
		path = canonicalContextPath(filepathext.SmartJoin(store.WorkingDir(), expandPath(path, store)))
		if _, ok := seenRoots[path]; ok {
			continue
		}
		seenRoots[path] = struct{}{}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			addFile(path)
			continue
		}
		// WalkDir visits entries in lexical order without following directory
		// symlinks, so a recursive context path is deterministic and bounded.
		_ = filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				addFile(path)
			}
			return nil
		})
	}
	return files
}

func canonicalContextPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func (p *Prompt) promptData(store *config.ConfigStore) PromptDat {
	workingDir := cmp.Or(p.workingDir, store.WorkingDir())
	platform := cmp.Or(p.platform, runtime.GOOS)

	cfg := store.Config()
	contextFiles := loadContextFiles(cfg.Options.ContextPaths, store)
	globalContextFiles := loadContextFiles(cfg.Options.GlobalContextPaths, store)

	// Rules are the highest-priority instruction files, injected above project
	// context and skills. An empty rule set leaves the prompt unchanged.
	ruleFiles := rules.List(cfg.Options.ResolveRulesPaths(store.Resolver()))

	// Discover skills (builtin + managed + user) through the shared pipeline
	// so the prompt's advertised set matches the coordinator's active set,
	// including agent-authored managed skills and hide / always-apply
	// handling.
	var availSkillXML string
	discoveryCfg := skills.DiscoveryConfig{
		SkillsPaths:      cfg.Options.SkillsPaths,
		DisabledSkills:   cfg.Options.DisabledSkills,
		WorkingDir:       store.WorkingDir(),
		ManagedSkillsDir: skills.ManagedSkillsDir(),
	}
	if r := store.Resolver(); r != nil {
		discoveryCfg.Resolver = r.ResolveValue
	}
	_, activeSkills, _ := skills.DiscoverFromConfig(discoveryCfg)
	if len(activeSkills) > 0 {
		availSkillXML = skills.ToPromptXML(activeSkills)
		if applied := skills.ToAppliedInstructions(activeSkills); applied != "" {
			availSkillXML += "\n" + applied
		}
	}

	data := PromptDat{
		Config:             *cfg,
		WorkingDir:         filepath.ToSlash(workingDir),
		Platform:           platform,
		AvailSkillXML:      availSkillXML,
		ContextFiles:       contextFiles,
		GlobalContextFiles: globalContextFiles,
		Rules:              ruleFiles,
	}

	return data
}

func (p *Prompt) Name() string {
	return p.template.Name()
}
