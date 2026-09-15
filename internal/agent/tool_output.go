package agent

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Tool output is billed again on every later request in a session, so bytes

// exactOutputTools return bytes the model feeds back into a later edit or
var exactOutputTools = map[string]struct{}{
	"view":        {},
	"edit":        {},
	"multiedit":   {},
	"write":       {},
	"patch":       {},
	"prowl_agent": {},
}

const (
	// maxDuplicateLines is the point at which repeated identical lines stop
	maxDuplicateLines = 3

	// maxBlankRun collapses vertical padding, which carries no information
	// and is common in command output.
	maxBlankRun = 1
)

// compactToolOutput returns a cheaper rendering of a tool result. The second
// return value reports whether anything changed, so callers can leave an
// untouched result strictly alone.
func compactToolOutput(toolName, content string) (string, bool) {
	if _, exact := exactOutputTools[toolName]; exact {
		return content, false
	}
	if content == "" {
		return content, false
	}

	compacted := scrub(content)
	compacted = collapseBlankRuns(compacted)
	compacted = dedupeLines(compacted)

	if compacted == content {
		return content, false
	}
	return compacted, true
}

// scrub removes terminal control sequences and trailing whitespace. ANSI
// styling costs tokens and carries nothing a model can act on.
func scrub(content string) string {
	if !strings.ContainsRune(content, 0x1b) && !strings.Contains(content, " \n") {
		return strings.TrimRight(content, " \t\n")
	}
	stripped := ansi.Strip(content)
	lines := strings.Split(stripped, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// collapseBlankRuns caps consecutive blank lines.
func collapseBlankRuns(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blanks++
			if blanks > maxBlankRun {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// dedupeLines keeps the first maxDuplicateLines occurrences of any repeated
// line and replaces the rest with one count, so recurrence stays visible
// while the repetition stops being paid for.
func dedupeLines(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) < maxDuplicateLines*2 {
		return content
	}
	seen := make(map[string]int, len(lines))
	suppressed := make(map[string]int)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		key := strings.TrimSpace(line)
		// Short lines are structure (brackets, separators), not content.
		if len(key) < 12 {
			out = append(out, line)
			continue
		}
		seen[key]++
		if seen[key] <= maxDuplicateLines {
			out = append(out, line)
			continue
		}
		suppressed[key]++
	}
	if len(suppressed) == 0 {
		return content
	}
	total := 0
	for _, n := range suppressed {
		total += n
	}
	return strings.Join(out, "\n") +
		fmt.Sprintf("\n\n[%d further duplicate line(s) across %d repeated value(s) omitted]", total, len(suppressed))
}
