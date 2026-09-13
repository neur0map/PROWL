package model

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/neur0map/prowl/internal/skills"
	"github.com/neur0map/prowl/internal/ui/common"
	"github.com/neur0map/prowl/internal/ui/dialog"
	"github.com/neur0map/prowl/internal/ui/styles"
)

type skillStatusItem struct {
	icon  string
	name  string
	title string
	// description is reserved for future use (e.g. showing error details).
	description string
}

var builtinSkillsCache struct {
	once   sync.Once
	skills []*skills.Skill
}

func cachedBuiltinSkills() []*skills.Skill {
	builtinSkillsCache.once.Do(func() {
		builtinSkillsCache.skills = skills.DiscoverBuiltin()
	})
	return builtinSkillsCache.skills
}

// sectionCount renders a small right-aligned count for a landing/sidebar
// Section header (e.g. "Skills ──────── 17").
func sectionCount(t *styles.Styles, n int) string {
	return t.Resource.CapabilityCount.Render(strconv.Itoa(n))
}

// skillCounts summarizes the active skills as total, builtin, and added (user)
// counts, mirroring the dedupe and disable rules of skillEntries.
func (m *UI) skillCounts() (total, builtin, added int) {
	for _, e := range m.skillEntries() {
		total++
		if e.Builtin {
			builtin++
		} else {
			added++
		}
	}
	return total, builtin, added
}

// skillsInfo renders the compact sidebar Skills section: a tight header with
// the total split into builtin and added counts, then only the added (user)
// skills, plus a hint to open the full browser. The full builtin list lives in
// the Ctrl+K modal, not the sidebar.
func (m *UI) skillsInfo(width, maxItems int, isSection bool) string {
	t := m.com.Styles

	entries := m.skillEntries()
	total := len(entries)
	builtin := 0
	added := make([]dialog.SkillEntry, 0, total)
	for _, e := range entries {
		if e.Builtin {
			builtin++
			continue
		}
		added = append(added, e)
	}

	title := t.Resource.Heading.Render(fmt.Sprintf("Skills — %d", total))
	if isSection {
		title = common.Section(t, "Skills", width, sectionCount(t, total))
	}
	counts := t.Resource.AdditionalText.Render(
		fmt.Sprintf("%d builtin · %d added", builtin, len(added)),
	)
	hint := t.Resource.AdditionalText.Render("ctrl+k to browse")
	header := fmt.Sprintf("%s\n%s", title, counts)

	if len(added) == 0 {
		return lipgloss.NewStyle().Width(width).Render(
			fmt.Sprintf("%s\n\n%s", header, hint),
		)
	}

	items := make([]skillStatusItem, 0, len(added))
	for _, e := range added {
		icon := t.Resource.OnlineIcon.String()
		if e.Errored {
			icon = t.Resource.ErrorIcon.String()
		}
		items = append(items, skillStatusItem{
			icon:  icon,
			name:  e.Name,
			title: t.Resource.Name.Render(e.Name),
		})
	}
	body := lipgloss.JoinVertical(lipgloss.Left, skillsList(t, items, width, maxItems), hint)
	return lipgloss.NewStyle().Width(width).Render(
		fmt.Sprintf("%s\n\n%s", header, body),
	)
}

// skillsSummary renders the landing card's compact skills section: the total
// with its builtin/added split plus a hint to open the full, sorted skills
// modal (Ctrl+K). The card no longer inlines the whole list.
func (m *UI) skillsSummary(width int) string {
	t := m.com.Styles
	total, builtin, added := m.skillCounts()
	title := common.Section(t, "Skills", width, sectionCount(t, total))
	counts := t.Resource.AdditionalText.Render(
		fmt.Sprintf("%d builtin · %d added", builtin, added),
	)
	hint := t.Resource.AdditionalText.Render("press ^K to browse")
	return lipgloss.NewStyle().Width(width).Render(
		fmt.Sprintf("%s\n%s\n\n%s", title, counts, hint),
	)
}

func (m *UI) skillStatusItems() []skillStatusItem {
	t := m.com.Styles
	var items []skillStatusItem
	stateNames := make(map[string]struct{}, len(m.skillStates))

	disabledSet := make(map[string]bool)
	if m.com != nil && m.com.Workspace != nil {
		if cfg := m.com.Config(); cfg != nil {
			for _, name := range cfg.Options.DisabledSkills {
				disabledSet[name] = true
			}
		}
	}

	states := slices.Clone(m.skillStates)
	slices.SortStableFunc(states, func(a, b *skills.SkillState) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, state := range states {
		name := state.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(state.Path))
		}
		if disabledSet[name] {
			continue
		}
		if _, exists := stateNames[name]; exists {
			continue
		}
		stateNames[name] = struct{}{}
		icon := t.Resource.OnlineIcon.String()
		if state.State == skills.StateError {
			icon = t.Resource.ErrorIcon.String()
		}
		items = append(items, skillStatusItem{
			icon:  icon,
			name:  name,
			title: t.Resource.Name.Render(name),
		})
	}

	builtin := cachedBuiltinSkills()
	slices.SortStableFunc(builtin, func(a, b *skills.Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, skill := range builtin {
		if _, ok := stateNames[skill.Name]; ok {
			continue
		}
		if disabledSet[skill.Name] {
			continue
		}
		items = append(items, skillStatusItem{
			icon:  t.Resource.OnlineIcon.String(),
			name:  skill.Name,
			title: t.Resource.Name.Render(skill.Name),
		})
	}

	slices.SortStableFunc(items, func(a, b skillStatusItem) int {
		return strings.Compare(a.name, b.name)
	})

	return items
}

func skillsList(t *styles.Styles, items []skillStatusItem, width, maxItems int) string {
	if maxItems <= 0 {
		return ""
	}

	if len(items) > maxItems {
		visibleItems := items[:maxItems-1]
		remaining := len(items) - (maxItems - 1)
		items = append(visibleItems, skillStatusItem{
			name:  "more",
			title: t.Resource.AdditionalText.Render(fmt.Sprintf("…and %d more", remaining)),
		})
	}

	renderedItems := make([]string, 0, len(items))
	for _, item := range items {
		renderedItems = append(renderedItems, common.Status(t, common.StatusOpts{
			Icon:        item.icon,
			Title:       item.title,
			Description: item.description,
		}, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderedItems...)
}

// skillEntries builds the sorted rows for the Skills modal (Ctrl+K): every
// active skill with its on-disk location, name-sorted. Disabled skills are
// omitted, mirroring skillStatusItems.
func (m *UI) skillEntries() []dialog.SkillEntry {
	disabledSet := make(map[string]bool)
	if m.com != nil && m.com.Workspace != nil {
		if cfg := m.com.Config(); cfg != nil {
			for _, name := range cfg.Options.DisabledSkills {
				disabledSet[name] = true
			}
		}
	}

	seen := make(map[string]struct{})
	var entries []dialog.SkillEntry

	states := slices.Clone(m.skillStates)
	slices.SortStableFunc(states, func(a, b *skills.SkillState) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, state := range states {
		name := state.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(state.Path))
		}
		if disabledSet[name] {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, dialog.SkillEntry{
			Name:    name,
			Path:    state.Path,
			Errored: state.State == skills.StateError,
			Builtin: strings.HasPrefix(state.Path, skills.BuiltinPrefix),
		})
	}

	for _, skill := range cachedBuiltinSkills() {
		if _, ok := seen[skill.Name]; ok {
			continue
		}
		if disabledSet[skill.Name] {
			continue
		}
		seen[skill.Name] = struct{}{}
		entries = append(entries, dialog.SkillEntry{
			Name:    skill.Name,
			Path:    skill.SkillFilePath,
			Builtin: true,
		})
	}

	slices.SortStableFunc(entries, func(a, b dialog.SkillEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
	return entries
}
