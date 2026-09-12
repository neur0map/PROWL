package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/neur0map/prowl/internal/home"
)

// ManagedSkillsSubdir is the directory (under the prowl config dir) where the
// agent writes its own managed skills via the manage_skill and learn tools.
// Managed skills always have the lowest discovery precedence, so any same-named
// builtin or user skill overrides them.
const ManagedSkillsSubdir = "managed-skills"

// ManagedSkillsDirEnv overrides the managed-skills root, primarily for tests so
// discovery and writes stay hermetic.
const ManagedSkillsDirEnv = "PROWL_MANAGED_SKILLS_DIR"

// maxManagedSkillBytes caps a managed SKILL.md (frontmatter + body) so a
// runaway write cannot balloon the skill store.
const maxManagedSkillBytes = 64 * 1024

// managedNamePattern matches the same shape as builtin/user skill names:
// lowercase alphanumeric segments joined by single hyphens, with no leading,
// trailing, or consecutive hyphens. Length is bounded separately.
var managedNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// managedWriteMu serializes same-process writes so two concurrent create/update
// calls for the same name cannot interleave.
var managedWriteMu sync.Mutex

// ManagedSkillsDir returns the absolute managed-skills root, honoring
// ManagedSkillsDirEnv for tests. It returns "" only when the home config
// directory cannot be determined.
func ManagedSkillsDir() string {
	if override := os.Getenv(ManagedSkillsDirEnv); override != "" {
		return override
	}
	cfg := home.Config()
	if cfg == "" {
		return ""
	}
	return filepath.Join(cfg, "prowl", ManagedSkillsSubdir)
}

// NormalizeManagedName lowercases and trims a proposed managed skill name and
// validates it, returning an error for names that would be unsafe as a
// directory.
func NormalizeManagedName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if len(name) > MaxNameLength || !managedNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q: use lowercase letters and digits with single hyphens (no leading/trailing/consecutive hyphens, max %d chars)", raw, MaxNameLength)
	}
	return name, nil
}

// sanitizeManagedDescription collapses a description to a single safe line,
// dropping control characters and markup that would break the generated YAML
// frontmatter or the prompt XML.
func sanitizeManagedDescription(desc string) string {
	desc = strings.Map(func(r rune) rune {
		switch {
		case r == '<' || r == '>' || r == '`':
			return -1
		case r < 0x20:
			return ' '
		default:
			return r
		}
	}, desc)
	return strings.Join(strings.Fields(desc), " ")
}

// WriteManagedSkill creates or updates a managed skill's SKILL.md with
// generated frontmatter. It normalizes the name, sanitizes the description,
// enforces the size cap, and refuses to write through a symlink. When create is
// true it fails if the skill already exists; when false (update) it fails if it
// does not. It returns the written file path.
func WriteManagedSkill(name, description, body string, create bool) (string, error) {
	name, err := NormalizeManagedName(name)
	if err != nil {
		return "", err
	}
	description = sanitizeManagedDescription(description)
	if description == "" {
		return "", fmt.Errorf("managed skill %q needs a non-empty description", name)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("managed skill %q needs a non-empty body", name)
	}

	root := ManagedSkillsDir()
	if root == "" {
		return "", errors.New("cannot resolve managed-skills directory")
	}

	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, body)
	if len(content) > maxManagedSkillBytes {
		return "", fmt.Errorf("managed skill is %d bytes; the limit is %d", len(content), maxManagedSkillBytes)
	}

	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()

	dir := filepath.Join(root, name)
	file := filepath.Join(dir, SkillFileName)

	fi, statErr := os.Lstat(file)
	exists := statErr == nil
	switch {
	case create && exists:
		return "", fmt.Errorf("managed skill %q already exists", name)
	case !create && !exists:
		return "", fmt.Errorf("managed skill %q does not exist", name)
	case exists && fi.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("managed skill %q is a symlink; refusing to write", name)
	case exists && !fi.Mode().IsRegular():
		return "", fmt.Errorf("managed skill %q is not a regular file; refusing to write", name)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create managed skill dir: %w", err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write managed skill: %w", err)
	}
	return file, nil
}

// DeleteManagedSkill removes a managed skill directory, erroring if it does not
// exist.
func DeleteManagedSkill(name string) error {
	name, err := NormalizeManagedName(name)
	if err != nil {
		return err
	}
	root := ManagedSkillsDir()
	if root == "" {
		return errors.New("cannot resolve managed-skills directory")
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("managed skill %q does not exist", name)
		}
		return err
	}
	return os.RemoveAll(dir)
}
