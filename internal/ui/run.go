package ui

import (
	"context"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
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
	bg := newBackground(ctx)
	deps.Update = bg.wrap(deps.Update)
	defer bg.stop(quitGrace)

	m := New(ctx, deps, startURL)
	// Motion is switched on here rather than in New, so every model a test
	// builds is the static screen unless it asks for motion. The seed is the
	// only thing that differs run to run.
	m.motion = newMotion(os.LookupEnv, uint64(time.Now().UnixNano()))
	return runProgram(tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)))
}

// background owns the yt-dlp update check for the life of Run. The check is
// not an attempt: quit does not wait on it and no state waits for it, so
// nothing in the model can cancel it at the right moment. Run does instead,
// once the loop has ended by whatever route — a key, a signal, a crash — and
// then waits, bounded, for it to return, which is what removes a partial
// download's temp file before the process exits.
type background struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	stopped bool
	running sync.WaitGroup
}

func newBackground(ctx context.Context) *background {
	b := &background{}
	b.ctx, b.cancel = context.WithCancel(ctx)
	return b
}

// wrap returns update running under the background's context and counted,
// or nil for a nil update. The context the model passes is the program's,
// which the background's is derived from, so replacing it only narrows it. A
// call after stop does not run at all.
func (b *background) wrap(update func(context.Context, ytdlp.Result) (ytdlp.UpdateResult, error)) func(context.Context, ytdlp.Result) (ytdlp.UpdateResult, error) {
	if update == nil {
		return nil
	}
	return func(_ context.Context, res ytdlp.Result) (ytdlp.UpdateResult, error) {
		b.mu.Lock()
		if b.stopped {
			b.mu.Unlock()
			return ytdlp.UpdateResult{}, context.Canceled
		}
		b.running.Add(1)
		b.mu.Unlock()
		defer b.running.Done()
		return update(b.ctx, res)
	}
}

// stop cancels whatever is running and waits for it to return, for at most
// grace: a user who quit is owed an exit, and what an abandoned check leaves
// is a temp file the next download's sweep removes.
func (b *background) stop(grace time.Duration) {
	b.mu.Lock()
	b.stopped = true
	b.mu.Unlock()
	b.cancel()

	done := make(chan struct{})
	go func() {
		b.running.Wait()
		close(done)
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
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
