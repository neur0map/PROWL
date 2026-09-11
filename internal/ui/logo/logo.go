// Package logo renders a Prowl wordmark in a stylized way.
package logo

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/ui/styles"
)

// letterform represents a letterform. It can be stretched horizontally by
// a given amount via the boolean argument.
type letterform func(bool) string

// Opts are the options for rendering the Prowl title art.
type Opts struct {
	FieldColor   color.Color // barcode/kanji decor
	TitleColorA  color.Color // left gradient ramp point
	TitleColorB  color.Color // right gradient ramp point
	RyokuColor   color.Color // Ryoku™ text color
	VersionColor color.Color // version text color
	Width        int         // width of the rendered logo, used for truncation

	// When true, stretch a random letterform on each render. Has no effect in
	// compact mode. Mainly for testing. In production you will want to cache
	// the stretched letterform to keep the logo from jittering on resize.
	Unstable bool
}

// Render renders the Prowl logo. Set the argument to true to render the narrow
// version, intended for use in a sidebar.
//
// The compact argument determines whether it renders compact for the sidebar
// or wider for the main pane.
func Render(base lipgloss.Style, version string, compact bool, o Opts) string {
	ryoku := " " + "Ryoku™"

	fg := func(c color.Color, s string) string {
		return lipgloss.NewStyle().Foreground(c).Render(s)
	}

	// Title.
	const spacing = 1
	prowlLetterforms := []letterform{
		LetterP,
		LetterR,
		LetterO,
		LetterW,
		LetterL,
	}

	stretchIndex := -1 // -1 means no stretching.
	if !compact && !o.Unstable {
		// Always stretch the same letterform, which is picked once at random.
		stretchIndex = cachedRandN(len(prowlLetterforms))
	} else if !compact && o.Unstable {
		// Stretch a random letterform on every render.
		stretchIndex = rand.IntN(len(prowlLetterforms))
	}
	prowl := renderWord(spacing, stretchIndex, prowlLetterforms...)
	prowlWidth := lipgloss.Width(prowl)
	b := new(strings.Builder)
	for r := range strings.SplitSeq(prowl, "\n") {
		fmt.Fprintln(b, styles.ApplyForegroundGrad(base, r, o.TitleColorA, o.TitleColorB))
	}
	prowl = b.String()

	// Ryoku and version.
	metaRowGap := 1
	maxVersionWidth := prowlWidth - lipgloss.Width(ryoku) - metaRowGap
	version = ansi.Truncate(version, maxVersionWidth, "…") // truncate version if too long.
	gap := max(0, prowlWidth-lipgloss.Width(ryoku)-lipgloss.Width(version))
	metaRow := fg(o.RyokuColor, ryoku) + strings.Repeat(" ", gap) + fg(o.VersionColor, version)

	// Join the meta row and big Prowl title.
	prowl = strings.TrimSpace(metaRow + "\n" + prowl)

	// Narrow sidebar version.
	if compact {
		field := barcodeBlock(base, prowlWidth, 1, o.FieldColor, o.RyokuColor)
		return strings.Join([]string{field, field, prowl, field, ""}, "\n")
	}

	fieldHeight := lipgloss.Height(prowl)

	// Barcode and kanji decor flanking the wordmark, replacing the old
	// diagonal rules. Bars are shaded along the brand gradient.
	const decoWidth = 9
	leftBarcode := barcodeBlock(base, decoWidth, fieldHeight, o.FieldColor, o.RyokuColor)
	rightBarcode := barcodeBlock(base, decoWidth, fieldHeight, o.RyokuColor, o.FieldColor)
	leftKanji := kanjiColumn(base, fieldHeight, o.FieldColor, o.RyokuColor)
	rightKanji := kanjiColumn(base, fieldHeight, o.RyokuColor, o.FieldColor)

	leftDeco := lipgloss.JoinHorizontal(lipgloss.Top, leftKanji, " ", leftBarcode)
	rightDeco := lipgloss.JoinHorizontal(lipgloss.Right, rightBarcode, " ", rightKanji)

	// Return the wide version.
	const hGap = " "
	logo := lipgloss.JoinHorizontal(lipgloss.Top, leftDeco, hGap, prowl, hGap, rightDeco)
	if o.Width > 0 {
		// Truncate the logo to the specified width.
		lines := strings.Split(logo, "\n")
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "")
		}
		logo = strings.Join(lines, "\n")
	}
	return logo
}

// decoKanji are decorative Japanese characters used as header art. The
// sequence evokes stealth, a wolf, shadow, and patrol: a prowl.
var decoKanji = []string{"忍", "狼", "影", "巡", "闇", "疾"}

// barcodeUnit is the repeating motif for the decorative barcode. Bars are
// block glyphs and gaps are spaces.
const barcodeUnit = "█ ██ █ ▏█ ███ █▎ ██ █  █▍ █ ██ ▏▎"

// BarcodeFill returns a barcode motif of exactly width cells (rune-aware).
func BarcodeFill(width int) string {
	if width < 1 {
		return ""
	}
	unit := []rune(barcodeUnit)
	if len(unit) == 0 {
		return ""
	}
	out := make([]rune, 0, width)
	for len(out) < width {
		out = append(out, unit...)
	}
	return string(out[:width])
}

// barcodeBlock renders a barcode of the given width and height, each row
// shaded along the gradient from color1 to color2.
func barcodeBlock(base lipgloss.Style, width, height int, color1, color2 color.Color) string {
	if height < 1 {
		height = 1
	}
	row := styles.ApplyForegroundGrad(base, BarcodeFill(width), color1, color2)
	lines := make([]string, height)
	for i := range lines {
		lines[i] = row
	}
	return strings.Join(lines, "\n")
}

// kanjiColumn stacks decorative kanji vertically to the given height, each
// shaded along the gradient from color1 to color2.
func kanjiColumn(base lipgloss.Style, height int, color1, color2 color.Color) string {
	if height < 1 {
		height = 1
	}
	lines := make([]string, height)
	for i := range lines {
		k := decoKanji[i%len(decoKanji)]
		lines[i] = styles.ApplyForegroundGrad(base, k, color1, color2)
	}
	return strings.Join(lines, "\n")
}

// SmallRender renders a smaller version of the Prowl logo, suitable for
// smaller windows or sidebar usage.
func SmallRender(t *styles.Styles, width int, o Opts) string {
	name := "Prowl"
	ryoku := "Ryoku™"
	title := t.Logo.SmallRyoku.Render(ryoku)
	title = fmt.Sprintf("%s %s", title, styles.ApplyBoldForegroundGrad(t.Logo.GradCanvas, name, t.Logo.SmallGradFromColor, t.Logo.SmallGradToColor))
	remainingWidth := width - lipgloss.Width(title) - 1 // 1 for the space after the name
	if remainingWidth > 0 {
		title = fmt.Sprintf("%s %s", title, t.Logo.SmallDiagonals.Render(BarcodeFill(remainingWidth)))
	}
	return title
}
