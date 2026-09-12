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
	"time"

	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/filepathext"
	"github.com/neur0map/prowl/internal/home"
	"github.com/neur0map/prowl/internal/shell"
	"github.com/neur0map/prowl/internal/skills"
)

// Prompt represents a template-based prompt generator.
type Prompt struct {
	name       string
	template   string
	now        func() time.Time
	platform   string
	workingDir string
}

type PromptDat struct {
	Provider           string
	Model              string
	Config             config.Config
	WorkingDir         string
	IsGitRepo          bool
	Platform           string
	Date               string
	GitStatus          string
	ContextFiles       []ContextFile
	GlobalContextFiles []ContextFile
	AvailSkillXML      string
}

type ContextFile struct {
	Path    string
	Content string
}

type Option func(*Prompt)

func WithTimeFunc(fn func() time.Time) Option {
	return func(p *Prompt) {
		p.now = fn
	}
}

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
	p := &Prompt{
		name:     name,
		template: promptTemplate,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

func (p *Prompt) Build(ctx context.Context, provider, model string, store *config.ConfigStore) (string, error) {
	t, err := template.New(p.name).Parse(p.template)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var sb strings.Builder
	d, err := p.promptData(ctx, provider, model, store)
	if err != nil {
		return "", err
	}
	if err := t.Execute(&sb, d); err != nil {
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

func (p *Prompt) promptData(ctx context.Context, provider, model string, store *config.ConfigStore) (PromptDat, error) {
	workingDir := cmp.Or(p.workingDir, store.WorkingDir())
	platform := cmp.Or(p.platform, runtime.GOOS)

	cfg := store.Config()
	contextFiles := loadContextFiles(cfg.Options.ContextPaths, store)
	globalContextFiles := loadContextFiles(cfg.Options.GlobalContextPaths, store)

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

	isGit := isGitRepo(store.WorkingDir())
	data := PromptDat{
		Provider:      provider,
		Model:         model,
		Config:        *cfg,
		WorkingDir:    filepath.ToSlash(workingDir),
		IsGitRepo:     isGit,
		Platform:      platform,
		Date:          p.now().Format("1/2/2006"),
		AvailSkillXML:     availSkillXML,
		ContextFiles:     contextFiles,
		GlobalContextFiles: globalContextFiles,
	}
	if isGit {
		var err error
		data.GitStatus, err = getGitStatus(ctx, store.WorkingDir())
		if err != nil {
			return PromptDat{}, err
		}
	}

	return data, nil
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func getGitStatus(ctx context.Context, dir string) (string, error) {
	sh := shell.NewShell(&shell.Options{
		WorkingDir: dir,
	})
	branch, err := getGitBranch(ctx, sh)
	if err != nil {
		return "", err
	}
	status, err := getGitStatusSummary(ctx, sh)
	if err != nil {
		return "", err
	}
	commits, err := getGitRecentCommits(ctx, sh)
	if err != nil {
		return "", err
	}
	return branch + status + commits, nil
}

func getGitBranch(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git branch --show-current 2>/dev/null")
	if err != nil {
		return "", nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", nil
	}
	return fmt.Sprintf("Current branch: %s\n", out), nil
}

func getGitStatusSummary(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git status --short 2>/dev/null | head -20")
	if err != nil {
		return "", nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "Status: clean\n", nil
	}
	return fmt.Sprintf("Status:\n%s\n", out), nil
}

func getGitRecentCommits(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git log --oneline -n 3 2>/dev/null")
	if err != nil || out == "" {
		return "", nil
	}
	out = strings.TrimSpace(out)
	return fmt.Sprintf("Recent commits:\n%s\n", out), nil
}

func (p *Prompt) Name() string {
	return p.name
}
