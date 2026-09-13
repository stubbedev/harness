package main

// This is an example for testing logo treatments. Do not remove.

import (
	"fmt"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/stubbedev/harness/internal/ui/logo"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func main() {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not get terminal size: %s\n", err)
		w = 0
	}

	s := styles.CharmtonePantera()
	opts := logo.Opts{
		TitleColorA:  s.Logo.TitleColorA,
		TitleColorB:  s.Logo.TitleColorB,
		CharmColor:   s.Logo.CharmColor,
		VersionColor: s.Logo.VersionColor,
		Width:        w,
	}

	lipgloss.Println(
		logo.Render(s.Logo.GradCanvas, "v1.0.0", opts),
		logo.Render(s.Logo.GradCanvas, "v1.0.0", logo.Opts{
			TitleColorA:  opts.TitleColorA,
			TitleColorB:  opts.TitleColorB,
			CharmColor:   opts.CharmColor,
			VersionColor: opts.VersionColor,
			Width:        40,
		}),
	)
}
