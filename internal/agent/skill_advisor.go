package agent

import (
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/gjson"

	"github.com/neur0map/prowl/internal/skills"
)

// A skill's `globs` field was declared, parsed and printed into the prompt,
// but nothing read it — so activation rested on the model judging a prose
// description, which it regularly got wrong. When a tool touches a file a
// skill claims and that skill is unloaded, its result carries one line naming
// it. Advice, never a gate.

// skillHint is one advertised skill that claims particular files.
type skillHint struct {
	name     string
	location string
	globs    []string
}

// skillAdvisor reminds the model about a claimed-but-unloaded skill.
type skillAdvisor struct {
	hints []skillHint

	mu       sync.Mutex
	loaded   map[string]bool // skill bodies read this session
	reminded map[string]bool // one reminder per skill, not per file
}

// newSkillAdvisor keeps only the skills that claim files and can be
// model-invoked. A skill with no globs makes no claim, so it is left to the
// description-based match in the prompt.
func newSkillAdvisor(active []*skills.Skill) *skillAdvisor {
	var hints []skillHint
	for _, s := range active {
		if s == nil || len(s.Globs) == 0 || s.DisableModelInvocation || s.AlwaysApply {
			continue
		}
		location := s.SkillFilePath
		if location == "" {
			location = s.Path
		}
		if location == "" {
			continue
		}
		hints = append(hints, skillHint{name: s.Name, location: location, globs: s.Globs})
	}
	if len(hints) == 0 {
		return nil
	}
	return &skillAdvisor{
		hints:    hints,
		loaded:   map[string]bool{},
		reminded: map[string]bool{},
	}
}

// skillTargetPaths returns the files a completed call touched, for the tools
// where "the model is working on this file" is unambiguous. A search or a
// shell command is deliberately excluded: matching those would fire on
// incidental mentions.
func skillTargetPaths(toolName, input string) []string {
	switch toolName {
	case "edit", "write", "view", "multiedit":
		var out []string
		if p := gjson.Get(input, "file_path").String(); p != "" {
			out = append(out, p)
		}
		if p := gjson.Get(input, "path").String(); p != "" {
			out = append(out, p)
		}
		return out
	default:
		return nil
	}
}

// noteLoaded records that a skill body was read, which retires its reminder.
func (a *skillAdvisor) noteLoaded(paths []string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range paths {
		for _, hint := range a.hints {
			if sameSkillFile(p, hint) {
				a.loaded[hint.name] = true
			}
		}
	}
}

// sameSkillFile reports whether a viewed path is this skill's own body. A
// builtin is addressed by its virtual prowl://skills/<name>/ location, a user
// skill by its file path, so both forms are accepted.
func sameSkillFile(viewed string, hint skillHint) bool {
	if viewed == "" {
		return false
	}
	if strings.EqualFold(filepath.ToSlash(viewed), filepath.ToSlash(hint.location)) {
		return true
	}
	slashed := strings.ToLower(filepath.ToSlash(viewed))
	return strings.Contains(slashed, "/"+strings.ToLower(hint.name)+"/") &&
		strings.HasSuffix(slashed, "skill.md")
}

// advise returns the reminder for a claimed file, or an empty string.
func (a *skillAdvisor) advise(toolName, input string) string {
	if a == nil {
		return ""
	}
	targets := skillTargetPaths(toolName, input)
	if len(targets) == 0 {
		return ""
	}
	// A view of a skill body is how the model loads one, so record that
	// before deciding whether to advise.
	a.noteLoaded(targets)

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, hint := range a.hints {
		if a.loaded[hint.name] || a.reminded[hint.name] {
			continue
		}
		if !claimsAny(hint.globs, targets) {
			continue
		}
		a.reminded[hint.name] = true
		return "Prowl skill reminder: the " + hint.name + " skill covers the file you just " +
			"touched. Read it with the view tool at " + hint.location +
			" before continuing; it exists because this surface has traps that " +
			"a plausible-looking edit does not survive."
	}
	return ""
}

// claimsAny reports whether any glob matches any touched path. A pattern is
// tried against the full path and against the base name, so both "**/*.qml"
// and "*.qml" behave as an author would expect.
func claimsAny(globs, targets []string) bool {
	for _, target := range targets {
		slashed := filepath.ToSlash(target)
		base := path.Base(slashed)
		for _, glob := range globs {
			pattern := filepath.ToSlash(strings.TrimSpace(glob))
			if pattern == "" {
				continue
			}
			if matchGlob(pattern, slashed) || matchGlob(pattern, base) {
				return true
			}
			// "**/" is a doublestar idiom path.Match does not know; drop the
			// prefix and match the tail against the base name.
			if trimmed, ok := strings.CutPrefix(pattern, "**/"); ok && matchGlob(trimmed, base) {
				return true
			}
		}
	}
	return false
}

func matchGlob(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}
