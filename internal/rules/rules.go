// Package rules manages Prowl's rule files: user-authored Markdown
// instructions that are injected into the system prompt at the highest
// priority, above project context and skills. Rules live in a project's
// .prowl/rules directory and in the global ~/.config/prowl/rules directory.
package rules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// maxRuleBytes caps a single rule file so a runaway write cannot balloon the
// rule store.
const maxRuleBytes = 64 * 1024

// maxRuleNameLength bounds a sanitized rule name so it stays a safe file name.
const maxRuleNameLength = 64

// ruleFileExt is the extension every rule file uses.
const ruleFileExt = ".md"

// writeMu serializes same-process writes so two concurrent writes for the same
// name cannot interleave.
var writeMu sync.Mutex

// Rule is a single rule file: its display name (file name without extension),
// its absolute on-disk path, and its Markdown content.
type Rule struct {
	Name    string
	Path    string
	Content string
}

// ProjectDir returns the project-level rules directory for workdir, where
// project rules are stored and discovered.
func ProjectDir(workdir string) string {
	return filepath.Join(workdir, ".prowl", "rules")
}

// List walks each configured path and returns every Markdown rule file found,
// sorted by name. Files reached through overlapping directories or symbolic
// links are read only once (dedup is by canonical path). A path that does not
// exist is skipped.
func List(paths []string) []Rule {
	if len(paths) == 0 {
		return nil
	}
	var out []Rule
	seenRoots := make(map[string]struct{}, len(paths))
	seenFiles := make(map[string]struct{}, len(paths))
	add := func(path string) {
		path = canonicalPath(path)
		if _, ok := seenFiles[path]; ok {
			return
		}
		seenFiles[path] = struct{}{}
		content, err := os.ReadFile(path)
		if err != nil {
			return
		}
		base := filepath.Base(path)
		out = append(out, Rule{
			Name:    strings.TrimSuffix(base, filepath.Ext(base)),
			Path:    path,
			Content: string(content),
		})
	}
	for _, path := range paths {
		path = canonicalPath(path)
		if _, ok := seenRoots[path]; ok {
			continue
		}
		seenRoots[path] = struct{}{}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if isMarkdown(path) {
				add(path)
			}
			continue
		}
		// WalkDir visits entries in lexical order without following directory
		// symlinks, so discovery is deterministic and bounded.
		_ = filepath.WalkDir(path, func(p string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && isMarkdown(p) {
				add(p)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Write creates or overwrites a rule file named after a sanitized name inside
// dir. It enforces the size cap and refuses to write through a symlink. It
// returns the written file path.
func Write(dir, name, body string) (string, error) {
	if dir == "" {
		return "", errors.New("rules directory is empty")
	}
	name, err := sanitizeName(name)
	if err != nil {
		return "", err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("rule %q needs a non-empty body", name)
	}
	content := body + "\n"
	if len(content) > maxRuleBytes {
		return "", fmt.Errorf("rule is %d bytes; the limit is %d", len(content), maxRuleBytes)
	}

	writeMu.Lock()
	defer writeMu.Unlock()

	// Write beneath an opened root so a symlink planted at the rule file cannot
	// redirect the write outside the rules directory.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create rules directory: %w", err)
	}
	rootDir, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer rootDir.Close()

	rel := name + ruleFileExt
	if fi, err := rootDir.Lstat(rel); err == nil {
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return "", fmt.Errorf("rule %q is a symlink; refusing to write", name)
		case !fi.Mode().IsRegular():
			return "", fmt.Errorf("rule %q is not a regular file; refusing to write", name)
		}
	}
	f, err := rootDir.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("write rule: %w", err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write rule: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write rule: %w", err)
	}
	return filepath.Join(dir, rel), nil
}

// sanitizeName lowercases a proposed rule name and reduces it to a kebab-case
// file stem: runs of non-alphanumeric characters collapse to single hyphens,
// with no leading, trailing, or consecutive hyphens. It errors when nothing
// usable remains.
func sanitizeName(raw string) (string, error) {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if b.Len() > 0 && !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "", fmt.Errorf("invalid rule name %q: needs at least one letter or digit", raw)
	}
	if len(name) > maxRuleNameLength {
		name = strings.Trim(name[:maxRuleNameLength], "-")
	}
	return name, nil
}

// isMarkdown reports whether path names a Markdown file, case-insensitively.
func isMarkdown(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ruleFileExt)
}

// canonicalPath resolves path to an absolute, symlink-free, cleaned form so
// two references to the same file compare equal.
func canonicalPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}
