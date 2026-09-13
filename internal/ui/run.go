package ui

import (
	"context"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the interface and blocks until the user quits.
//
// ctx bounds the program: cancelling it cancels whatever yt-dlp run is in
// flight and ends the loop. The alt screen is entered so the terminal the user
// came from is untouched when yank exits.
//
// startURL is the URL yank was started with, or "" to start on the input
// screen; see New for what a URL that does not validate does.
func Run(ctx context.Context, deps Deps, startURL string) error {
	m := New(ctx, deps, startURL)
	// Motion is switched on here rather than in New, so every model a test
	// builds is the static screen unless it asks for motion. The seed is the
	// only thing that differs run to run.
	m.motion = newMotion(os.LookupEnv, uint64(time.Now().UnixNano()))
	return runProgram(tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)))
}

// runProgram is the loop plus the cleanup that has to happen whichever way it
// ended. It is separate from Run so a test can drive it with a program of its
// own — one whose model already owns an info-json, and whose context is already
// cancelled — which is the only way to reach the shutdown route Run exists to
// cover.
func runProgram(p *tea.Program) error {
	final, err := p.Run()
	releaseFinal(final)
	return err
}

// releaseFinal cleans up after the model the program ended on.
//
// Every ordinary exit has already released the info-json inside Update, and
// Cleanup is idempotent, so this is usually a no-op. It is not optional: with
// tea.WithContext, a cancelled context shuts the program down without Update
// ever running — the CLI hands Run a signal.NotifyContext, so this is what a
// terminal hang-up, a SIGTERM or a ctrl+z-then-kill takes — and Bubble Tea
// returns the live model beside ErrProgramKilled. That is the one route out of
// the program the key handlers cannot cover, and what it would leave behind is
// the multi-megabyte `-J` document Probe saved, in the temp directory, for good.
//
// It takes a tea.Model rather than a Model so the Run above can hand over
// whatever came back, including a nil interface after an early failure.
func releaseFinal(final tea.Model) {
	m, ok := final.(Model)
	if !ok {
		return
	}
	m.releaseProbe()
}
