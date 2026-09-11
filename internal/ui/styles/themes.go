package styles

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// Prowl brand colors for the logo wordmark and header art: a slightly
// orange ember blending into a space bone white.
var (
	// Exported brand palette for CLI output and any package outside the
	// styles tree.
	BrandEmber    = lipgloss.Color("#E0863C")
	BrandBone     = lipgloss.Color("#EDE6D6")
	BrandBoneMute = lipgloss.Color("#C7B79C")
	BrandEmberDim = lipgloss.Color("#A86A3A")
	BrandInk      = lipgloss.Color("#241A12")
	BrandGold     = lipgloss.Color("#D9A05B")
)

// ThemeKeyForProvider returns a stable identifier for the theme
// associated with the given provider ID. Providers that share a theme
// yield the same key, so callers can cheaply detect when switching
// providers would not actually change the active theme and skip the
// expensive style rebuild. This is the single source of truth for the
// provider-to-theme mapping; [ThemeForProvider] builds on it.
//
// Hyper is intentionally treated as the default theme: we ship the Hyper
// provider so power users can opt in (`prowl auth hyper`), but it is no
// longer a distinct visual identity.
func ThemeKeyForProvider(providerID string) string {
	return "default"
}

// ThemeForProvider returns the Styles associated with the given provider
// ID. Unknown or empty provider IDs yield the default Ryokutone Pantera
// theme.
func ThemeForProvider(providerID string) Styles {
	return RyokutonePantera()
}

// RyokutonePantera returns the Ryokutone dark theme. It's the default style
// for the UI.
func RyokutonePantera() Styles {
	s := quickStyle(quickStyleOpts{
		primary:   BrandEmber,
		secondary: BrandBone,
		accent:    BrandGold,
		keyword:   BrandEmber,

		fgBase:       charmtone.Sash,
		fgMoreSubtle: charmtone.Squid,
		fgSubtle:     charmtone.Smoke,
		fgMostSubtle: charmtone.Oyster,

		onPrimary: BrandInk,

		bgBase:         charmtone.Pepper,
		bgLeastVisible: charmtone.BBQ,
		bgLessVisible:  charmtone.Char,
		bgMostVisible:  charmtone.Iron,

		separator: charmtone.Char,

		destructive:       charmtone.Coral,
		error:             charmtone.Sriracha,
		warningSubtle:     charmtone.Zest,
		warning:           charmtone.Mustard,
		attention:         charmtone.Tang,
		busy:              charmtone.Citron,
		info:              charmtone.Malibu,
		infoMoreSubtle:    charmtone.Sardine,
		infoMostSubtle:    charmtone.Damson,
		success:           charmtone.Julep,
		successMoreSubtle: charmtone.Bok,
		successMostSubtle: charmtone.Guac,

		// ANSI 16-color palette for remapping raw terminal output
		// (e.g. bang-mode shell commands) onto legible Ryokutone colors.
		ansiBlack:   charmtone.BBQ,
		ansiRed:     charmtone.Coral,
		ansiGreen:   charmtone.Guac,
		ansiYellow:  charmtone.Mustard,
		ansiBlue:    charmtone.Malibu,
		ansiMagenta: BrandEmber,
		ansiCyan:    charmtone.Malibu,
		ansiWhite:   charmtone.Smoke,

		ansiBrightBlack:   charmtone.Iron,
		ansiBrightRed:     charmtone.Tuna,
		ansiBrightGreen:   charmtone.Julep,
		ansiBrightYellow:  charmtone.Zest,
		ansiBrightBlue:    charmtone.Guppy,
		ansiBrightMagenta: BrandGold,
		ansiBrightCyan:    charmtone.Sardine,
		ansiBrightWhite:   charmtone.Salt,
	})

	// Logo/brand overrides: an ember-to-bone gradient for the wordmark,
	// header barcode art, and kanji decor.
	s.Logo.FieldColor = BrandEmber
	s.Logo.TitleColorA = BrandEmber
	s.Logo.TitleColorB = BrandBone
	s.Logo.RyokuColor = BrandBone
	s.Logo.VersionColor = BrandBoneMute
	s.Logo.SmallRyoku = lipgloss.NewStyle().Foreground(BrandBone)
	s.Logo.SmallDiagonals = lipgloss.NewStyle().Foreground(BrandEmberDim)
	s.Logo.SmallGradFromColor = BrandEmber
	s.Logo.SmallGradToColor = BrandBone

	// Compact header wordmark and its barcode fill.
	s.Header.Ryoku = lipgloss.NewStyle().Foreground(BrandBone)
	s.Header.Diagonals = lipgloss.NewStyle().Foreground(BrandEmberDim)
	s.Header.LogoGradFromColor = BrandEmber
	s.Header.LogoGradToColor = BrandBone

	// Bang ! prompt overrides - warm ember/bone instead of purple.
	s.Editor.PromptBangIconFocused = s.Editor.PromptBangIconFocused.
		Foreground(BrandInk).
		Background(BrandEmber)
	s.Editor.PromptBangDotsFocused = s.Editor.PromptBangDotsFocused.
		Foreground(BrandEmber)
	s.Editor.PromptBangDotsBlurred = s.Editor.PromptBangDotsBlurred.
		Foreground(BrandBoneMute)

	// Shell bar/prompt overrides - warm ember/bone instead of purple.
	s.Messages.ShellBarFocused = s.Messages.ShellBarFocused.
		BorderForeground(BrandEmber)
	s.Messages.ShellBarBlurred = s.Messages.ShellBarBlurred.
		BorderForeground(charmtone.Iron)
	s.Messages.ShellPrompt = s.Messages.ShellPrompt.
		Foreground(BrandEmber)
	s.Messages.ShellPromptBlurred = s.Messages.ShellPromptBlurred.
		Foreground(BrandBoneMute)

	// The ◆ hypercredit symbol inside subdued text (e.g. savings
	// suffixes) uses gold so it stays visible against its surroundings.
	s.Messages.SubduedHypercreditIcon = s.Messages.SubduedHypercreditIcon.
		Foreground(BrandGold)

	return s
}
