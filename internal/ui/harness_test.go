package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// --- fakes ------------------------------------------------------------------

// probeOutcome and downloadOutcome are what a fake returns for one call. A
// queue of them is consumed in order and the last entry repeats, so a test that
// cares about only the first call does not have to describe the rest.
type probeOutcome struct {
	probe *ytdlp.ProbeResult
	err   error
}

type downloadOutcome struct {
	res *ytdlp.DownloadResult
	err error
}

// fakes stands in for internal/ytdlp. Every field a test asserts on is guarded
// by mu: the model calls these from tea.Cmd goroutines in the few tests that
// run commands for real.
type fakes struct {
	mu sync.Mutex

	resolveCalls int
	resolveRes   ytdlp.Result
	resolveErr   error

	probeCalls int
	probeURLs  []string
	probes     []probeOutcome

	rankCalls  int
	rankFFmpeg []bool
	rows       []ytdlp.Row

	downloadCalls int
	downloadRows  []ytdlp.Row
	downloads     []downloadOutcome

	cleanupCalls int
}

func (f *fakes) deps() Deps {
	return Deps{
		Resolve: func(ctx context.Context) (ytdlp.Result, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.resolveCalls++
			return f.resolveRes, f.resolveErr
		},
		Probe: func(ctx context.Context, ytdlpPath, url string) (*ytdlp.ProbeResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.probeCalls++
			f.probeURLs = append(f.probeURLs, url)
			out := next(f.probes, f.probeCalls-1)
			return out.probe, out.err
		},
		Rank: func(info *ytdlp.VideoInfo, hasFFmpeg bool) []ytdlp.Row {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.rankCalls++
			f.rankFFmpeg = append(f.rankFFmpeg, hasFFmpeg)
			return f.rows
		},
		Download: func(ctx context.Context, ytdlpPath string, probe *ytdlp.ProbeResult, row ytdlp.Row, updates chan<- ytdlp.Progress) (*ytdlp.DownloadResult, error) {
			f.mu.Lock()
			f.downloadCalls++
			f.downloadRows = append(f.downloadRows, row)
			out := next(f.downloads, f.downloadCalls-1)
			f.mu.Unlock()
			// The real Download streams progress and closes the channel before
			// returning. A fake that skipped the close would leave the drain
			// command blocked forever, which is exactly the bug the model has
			// to survive, so the fake keeps the contract.
			if updates != nil {
				close(updates)
			}
			return out.res, out.err
		},
		Cleanup: func(probe *ytdlp.ProbeResult) error {
			f.mu.Lock()
			f.cleanupCalls++
			f.mu.Unlock()
			// The real method is still called, so a test can assert the file
			// itself is gone and not merely that the model meant to remove it.
			return probe.Cleanup()
		},
	}
}

// next picks the i-th outcome, repeating the last one past the end.
func next[T any](queue []T, i int) T {
	var zero T
	switch {
	case len(queue) == 0:
		return zero
	case i >= len(queue):
		return queue[len(queue)-1]
	default:
		return queue[i]
	}
}

func (f *fakes) counts() (probe, download, cleanup int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probeCalls, f.downloadCalls, f.cleanupCalls
}

func (f *fakes) cleanups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cleanupCalls
}

// --- fixtures ---------------------------------------------------------------

// newProbe builds a ProbeResult backed by a real temp file, so Cleanup has
// something to remove and a test can check that it went.
func newProbe(t *testing.T, title, uploader string) *ytdlp.ProbeResult {
	t.Helper()
	path := filepath.Join(t.TempDir(), "info.json")
	if err := os.WriteFile(path, []byte(`{"title":"x"}`), 0o600); err != nil {
		t.Fatalf("writing the fake info-json: %v", err)
	}
	return &ytdlp.ProbeResult{
		Info:         ytdlp.VideoInfo{Title: title, Uploader: uploader},
		InfoJSONPath: path,
	}
}

// staleErr is what yt-dlp refusing an expired info-json looks like: an
// ExtractError, because that is the only shape IsStaleInfo can classify.
func staleErr() error {
	return &ytdlp.ExtractError{
		Message:  "unable to download video data: HTTP Error 403: Forbidden",
		Stderr:   "ERROR: unable to download video data: HTTP Error 403: Forbidden",
		ExitCode: 1,
	}
}

// hardErr is a positive failure with nothing retryable about it.
func hardErr() error {
	return &ytdlp.ExtractError{
		Message:  "Video unavailable. This video is private.",
		Stderr:   "ERROR: [youtube] abc: Video unavailable. This video is private.",
		ExitCode: 1,
	}
}

func cancelledErr() error { return fmt.Errorf("downloading: %w", context.Canceled) }

// --- driving the model ------------------------------------------------------

// testModel is a model with a known terminal size, so View assertions do not
// depend on the default. It starts on the input screen; startModel is the one
// that starts from a command-line URL.
func testModel(t *testing.T, f *fakes, width int) Model {
	t.Helper()
	m := New(context.Background(), f.deps(), "")
	m.width, m.height = width, 24
	m.layout()
	return m
}

// step feeds one message and returns the model and whatever command came back.
func step(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// send feeds one message and drops the command.
func send(m Model, msg tea.Msg) Model {
	next, _ := step(m, msg)
	return next
}

// runes builds the key message for typed characters, which is how j, k, q and
// the digits arrive.
func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func keyOf(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// typeURL puts a URL in the box the way the user would, without going through
// every keystroke.
func typeURL(m Model, url string) Model {
	m.input.SetValue(url)
	return m
}

// collectWindow is how long collect waits for a command to produce something.
// Every fake here answers immediately; the window is for the scheduler, not for
// the work.
const collectWindow = 2 * time.Second

// collect runs cmd, expanding tea.Batch, and returns the messages that arrived
// within collectWindow.
//
// Commands that are meant to block — the first-run notice timer, a drain
// waiting on a channel nothing has written to — simply do not appear in the
// result. Each test says which messages it expects rather than asserting on the
// whole set.
func collect(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}

	out := make(chan tea.Msg, 32)
	var wg sync.WaitGroup
	var run func(c tea.Cmd)
	run = func(c tea.Cmd) {
		defer wg.Done()
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, leaf := range batch {
				wg.Add(1)
				go run(leaf)
			}
			return
		}
		if msg != nil {
			out <- msg
		}
	}
	wg.Add(1)
	go run(cmd)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(collectWindow):
	}

	var got []tea.Msg
	for {
		select {
		case msg := <-out:
			got = append(got, msg)
		default:
			return got
		}
	}
}

// advance feeds every message cmd produced back into the model, the way the
// Bubble Tea loop would, and hands back the commands that came out of it. It
// does not loop: a spinner tick answers with another tick, so a fixed point is
// something this would never reach.
func advance(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	var out []tea.Cmd
	for _, msg := range collect(t, cmd) {
		var c tea.Cmd
		m, c = step(m, msg)
		if c != nil {
			out = append(out, c)
		}
	}
	return m, tea.Batch(out...)
}

// findMsg returns the first message of type T that collect produced.
func findMsg[T tea.Msg](msgs []tea.Msg) (T, bool) {
	for _, msg := range msgs {
		if typed, ok := msg.(T); ok {
			return typed, true
		}
	}
	var zero T
	return zero, false
}

// gone reports whether a path no longer exists, which is what a real Cleanup
// leaves behind.
func gone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// errString is an error with no structure to it: the shape a wrapped stdlib
// failure reaches the UI in.
func errString(s string) error { return errors.New(s) }

// fmtErrorf wraps, so a test can build the sentinel chains ytdlp returns.
func fmtErrorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
