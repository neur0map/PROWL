// Package exitbanner renders what Prowl prints after the TUI exits.
package exitbanner

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/neur0map/prowl/internal/config"
	"github.com/neur0map/prowl/internal/session"
	"github.com/neur0map/prowl/internal/ui/logo"
	"github.com/neur0map/prowl/internal/ui/styles"
	"github.com/neur0map/prowl/internal/version"
)

// FallbackWidth is used when stdout is not a terminal, so the banner still
// wraps to something sane when output is piped or captured.
const FallbackWidth = 80

// Savings is prowl-agent's token-savings summary for the exit banner: the
// project's cumulative total and the portion attributable to this run.
type Savings struct {
	Total     int
	Session   int
	Available bool
}

// Render returns the exit banner for the given style, or an empty string when
// there is nothing to print. A nil or untitled session means the resume hint is
// omitted, which for the compact banner leaves nothing at all.
func Render(banner config.ExitBanner, sess *session.Session, width int, savings Savings) string {
	if width <= 0 {
		width = FallbackWidth
	}
	hasSession := sess != nil && sess.ID != ""

	switch banner {
	case config.ExitBannerNone:
		return ""

	case config.ExitBannerCompact:
		if !hasSession {
			return ""
		}
		return sessionResumeLines(sess, width)

	default:
		// Unrecognized values render the full banner rather than nothing.
		style := lipgloss.NewStyle().Padding(1, 3)
		contentWidth := width - style.GetHorizontalFrameSize()

		sections := []string{logoSection(contentWidth, savings)}
		if hasSession {
			sections = append(sections, sessionResumeLines(sess, contentWidth))
		}
		return style.Render(strings.Join(sections, "\n\n"))
	}
}

// logoSection returns the ASCII art logo followed by the parting message.
func logoSection(contentWidth int, savings Savings) string {
	t := styles.ThemeForProvider("")
	prowlLogo := logo.Render(t.Logo.GradCanvas, version.Version, true, logo.Opts{
		FieldColor:   t.Logo.FieldColor,
		TitleColorA:  t.Logo.TitleColorA,
		TitleColorB:  t.Logo.TitleColorB,
		RyokuColor:   t.Logo.RyokuColor,
		VersionColor: t.Logo.VersionColor,
	})
	// Wrap the greeting and the message together: wrapping only the message
	// leaves the greeting's own width unaccounted for and overflows the frame.
	return prowlLogo + "\n" +
		lipgloss.NewStyle().Width(contentWidth).Render(partingMessage(savings))
}

// partingMessage is the line under the logo. It reports prowl-agent's token
// savings when there are any, otherwise a plain thank-you.
func partingMessage(s Savings) string {
	if !s.Available || s.Total <= 0 {
		return "Thanks for using Prowl!"
	}
	if s.Session > 0 {
		return fmt.Sprintf("Thanks for using Prowl! prowl-agent saved %s tokens this session · %s total.",
			commaInt(s.Session), commaInt(s.Total))
	}
	return fmt.Sprintf("Thanks for using Prowl! prowl-agent saved %s tokens for this project.", commaInt(s.Total))
}

// sessionResumeLines returns the "Session  <title>\nContinue prowl -s <hash>"
// pair used by the exit banner.
func sessionResumeLines(sess *session.Session, contentWidth int) string {
	title := strings.ReplaceAll(sess.Title, "\n", " ")

	labelWidth := lipgloss.Width("Session  ")
	titleWidth := contentWidth - labelWidth
	if titleWidth > 0 {
		title = ansi.Truncate(title, titleWidth, "…")
	}

	hash := session.HashID(sess.ID)[:7]
	label := lipgloss.NewStyle().Foreground(styles.ThemeForProvider("").Logo.FieldColor)
	sessionLine := label.Render("Session  ") + title
	continueLine := label.Render("Continue ") + "prowl -s " + hash
	return sessionLine + "\n" + continueLine
}

// commaInt formats a non-negative integer with comma thousands separators
// (e.g. 15231 -> "15,231").
func commaInt(n int) string {
	s := strconv.Itoa(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b []byte
	lead := len(s) % 3
	if lead > 0 {
		b = append(b, s[:lead]...)
	}
	for i := lead; i < len(s); i += 3 {
		if len(b) > 0 {
			b = append(b, ',')
		}
		b = append(b, s[i:i+3]...)
	}
	return string(b)
}
