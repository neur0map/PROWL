package main

// This is an example for testing logo treatments. Do not remove.

import (
	"fmt"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/neur0map/prowl/internal/ui/logo"
	"github.com/neur0map/prowl/internal/ui/styles"
)

func main() {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not get terminal size: %s", err)
	}

	s := styles.RyokutonePantera()
	opts := logo.Opts{
		FieldColor:   s.Logo.FieldColor,
		TitleColorA:  s.Logo.TitleColorA,
		TitleColorB:  s.Logo.TitleColorB,
		RyokuColor:   s.Logo.RyokuColor,
		VersionColor: s.Logo.VersionColor,
		Width:        w,
		Unstable:     true,
	}

	renderCompact := func() string {
		return logo.Render(s.Logo.GradCanvas, "v1.0.0", true, opts)
	}

	renderWide := func() string {
		return logo.Render(s.Logo.GradCanvas, "v1.0.0", false, opts)
	}

	lipgloss.Println(
		lipgloss.JoinHorizontal(lipgloss.Top, renderCompact(), "  ", renderCompact()),
	)

	for range 6 {
		lipgloss.Println(renderWide())
	}
}
