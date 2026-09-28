package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/anim"
)

// spinner is the progress indicator of a non-interactive run.
type spinner struct {
	done chan struct{}
	prog *tea.Program
}

type spinnerModel struct {
	cancel context.CancelFunc
	anim   *anim.Anim
}

type spinnerTickMsg struct{}

func spinnerTick() tea.Cmd {
	return tea.Tick(anim.FrameInterval(), func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

func (m spinnerModel) Init() tea.Cmd  { return spinnerTick() }
func (m spinnerModel) View() tea.View { return tea.NewView(m.anim.Render()) }

// Update implements tea.Model.
func (m spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}
	case spinnerTickMsg:
		m.anim.Advance()
		return m, spinnerTick()
	}
	return m, nil
}

// newSpinner creates a spinner that calls cancel on ctrl+c or esc.
func newSpinner(ctx context.Context, cancel context.CancelFunc, animSettings anim.Settings) *spinner {
	m := spinnerModel{
		anim:   anim.New(animSettings),
		cancel: cancel,
	}

	p := tea.NewProgram(m, tea.WithOutput(os.Stderr), tea.WithContext(ctx))

	return &spinner{
		prog: p,
		done: make(chan struct{}, 1),
	}
}

// Start begins the spinner animation
func (s *spinner) Start() {
	go func() {
		defer close(s.done)
		_, err := s.prog.Run()
		// ensures line is cleared
		fmt.Fprint(os.Stderr, ansi.EraseEntireLine)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, tea.ErrInterrupted) {
			fmt.Fprintf(os.Stderr, "Error running spinner: %v\n", err)
		}
	}()
}

// Stop ends the spinner animation
func (s *spinner) Stop() {
	s.prog.Quit()
	<-s.done
}
