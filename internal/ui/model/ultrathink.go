// Textarea wrapping is adapted from Bubbles. See NOTICE.md for its MIT notice.

package model

import (
	"image/color"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/reasoning"
	"github.com/rivo/uniseg"
)

// editorPromptWidth is the fixed cell width of the editor prompt gutter
// (see setEditorPrompt, which calls SetPromptFunc with this width). The
// ultrathink highlighter uses it to translate a prompt-line column into the
// on-screen column of the rendered content.
const editorPromptWidth = 4

// ultrathinkRect is a run of already-rendered cells, in the textarea's
// visible coordinate space, that spells a live ultrathink trigger. Rows are
// relative to the first visible textarea row; columns are display columns
// including the prompt gutter.
type ultrathinkRect struct {
	row      int
	startCol int
	endCol   int
}

// editorTextareaView renders the prompt textarea and, when the active model
// can reason and the draft contains a real ultrathink trigger, repaints just
// those keyword cells with the reasoning gradient. The textarea's own bytes,
// cursor, selection, wrapping and widths are never touched: highlighting
// happens entirely on the rendered cell grid, so a code span, path, or markup
// occurrence (which reasoning.UltrathinkSpans excludes) stays inert.
func (m *UI) editorTextareaView() string {
	view := m.textarea.View()
	value := m.textarea.Value()
	// Cheap gate first: skip the mask/scan unless the literal is present and
	// the model would actually honor it. Nonreason models never advertise a
	// boosted capability.
	if value == "" || !strings.Contains(value, reasoning.Ultrathink) || !m.modelCanReason() {
		return view
	}
	rects := ultrathinkHighlightRects(
		value,
		m.textarea.Width(),
		editorPromptWidth,
		m.textarea.ScrollYOffset(),
		m.textarea.Height(),
	)
	if len(rects) == 0 {
		return view
	}
	t := m.com.Styles
	return highlightUltrathinkView(view, rects, t.ModelInfo.ReasoningHighFrom, t.ModelInfo.ReasoningHighTo)
}

// modelCanReason reports whether the memoized active model supports reasoning.
// It never probes the workspace, so it is safe on the draw path.
func (m *UI) modelCanReason() bool {
	model := m.selectedLargeModel()
	return model != nil && model.CatwalkCfg.CanReason
}

// draftHasUltrathink reports whether the current draft carries a live
// ultrathink trigger that the active model would honor.
func (m *UI) draftHasUltrathink() bool {
	if !m.modelCanReason() {
		return false
	}
	value := m.textarea.Value()
	return strings.Contains(value, reasoning.Ultrathink) && reasoning.HasUltrathink(value)
}

// ultrathinkHighlightRects maps every ultrathink trigger span in value onto
// the textarea's visible cell grid. It reuses reasoning.UltrathinkSpans for
// detection (so only prose triggers match) and reproduces the textarea's
// word-wrap so a highlighted run lands exactly where the widget drew it.
// Spans scrolled out of the viewport are dropped.
func ultrathinkHighlightRects(value string, contentWidth, promptWidth, yOffset, height int) []ultrathinkRect {
	spans := reasoning.UltrathinkSpans(value)
	if len(spans) == 0 || contentWidth <= 0 || height <= 0 {
		return nil
	}

	lines := strings.Split(value, "\n")
	lineByteStart := make([]int, len(lines))
	byteAcc := 0
	for i, ln := range lines {
		lineByteStart[i] = byteAcc
		byteAcc += len(ln) + 1 // + newline
	}

	// Per logical line: its wrapped segments and the visual row it starts on.
	segsByLine := make([][][]rune, len(lines))
	rowBase := make([]int, len(lines))
	rowAcc := 0
	for i, ln := range lines {
		segs := wrapEditorLine([]rune(ln), contentWidth)
		segsByLine[i] = segs
		rowBase[i] = rowAcc
		rowAcc += len(segs)
	}

	var out []ultrathinkRect
	for _, sp := range spans {
		li := 0
		for li+1 < len(lines) && lineByteStart[li+1] <= sp.Start {
			li++
		}
		lineText := lines[li]
		startInLine := sp.Start - lineByteStart[li]
		endInLine := sp.End - lineByteStart[li]
		if startInLine < 0 || endInLine > len(lineText) {
			continue
		}
		runeStart := utf8.RuneCountInString(lineText[:startInLine])
		runeEnd := utf8.RuneCountInString(lineText[:endInLine])

		// A word never spans two wrapped segments (word-wrap keeps it whole),
		// but degenerate widths can split it; emit a rect per touched segment.
		base := 0
		for si, seg := range segsByLine[li] {
			segStart := base
			segEnd := base + len(seg)
			base = segEnd
			lo := max(runeStart, segStart)
			hi := min(runeEnd, segEnd)
			if lo >= hi {
				continue
			}
			visualRow := rowBase[li] + si - yOffset
			if visualRow < 0 || visualRow >= height {
				continue
			}
			dispStart := promptWidth + uniseg.StringWidth(string(seg[:lo-segStart]))
			dispEnd := promptWidth + uniseg.StringWidth(string(seg[:hi-segStart]))
			out = append(out, ultrathinkRect{row: visualRow, startCol: dispStart, endCol: dispEnd})
		}
	}
	return out
}

// highlightUltrathinkView repaints the given trigger cells in a rendered
// textarea view with a bold gradient from -> to. It draws the view into an
// off-screen buffer, restyles only the trigger cells, and flattens it back:
// every other cell keeps its exact style, width and content.
func highlightUltrathinkView(view string, rects []ultrathinkRect, from, to color.Color) string {
	lines := strings.Split(view, "\n")
	height := len(lines)
	width := 0
	for _, ln := range lines {
		width = max(width, ansi.StringWidth(ln))
	}
	if width == 0 || height == 0 {
		return view
	}

	buf := uv.NewScreenBuffer(width, height)
	uv.NewStyledString(view).Draw(&buf, buf.Bounds())

	for _, r := range rects {
		n := r.endCol - r.startCol
		if n <= 0 || r.row < 0 || r.row >= height {
			continue
		}
		line := buf.Line(r.row)
		if line == nil {
			continue
		}
		ramp := lipgloss.Blend1D(n, from, to)
		idx := 0
		for x := r.startCol; x < r.endCol; x++ {
			cell := line.At(x)
			idx++
			if cell == nil {
				continue
			}
			cell.Style.Fg = ramp[min(idx-1, len(ramp)-1)]
			cell.Style.Attrs |= uv.AttrBold
		}
	}
	return buf.Render()
}

// wrapEditorLine reproduces charm.land/bubbles/v2 textarea word-wrapping so
// the highlighter can locate a rune offset on the exact visual row the widget
// drew it. It mirrors the widget's unexported wrap: greedy word wrap, breaking
// a single word only when it alone exceeds the width, with trailing spaces
// preserved per segment (that is what the widget's wrappedBase counts).
func wrapEditorLine(runes []rune, width int) [][]rune {
	if width <= 0 {
		return [][]rune{append([]rune(nil), runes...)}
	}
	var (
		lines  = [][]rune{{}}
		word   []rune
		row    int
		spaces int
	)
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
			}
			lines[row] = append(lines[row], word...)
			lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			spaces = 0
			word = nil
		} else if len(word) > 0 {
			lastCharLen := uniseg.StringWidth(string(word[len(word)-1]))
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], []rune(strings.Repeat(" ", spaces))...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
	}
	return lines
}
