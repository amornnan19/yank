package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ui"
	"github.com/amornnan19/yank/internal/ytdlp"
)

// call is what the interface was started with, or the fact that it was never
// started at all. Every test that is about the command line and not about the
// UI asserts on one of these.
type call struct {
	started bool
	url     string
	deps    ui.Deps

	// The context is inspected inside the stub, not afterwards: run defers the
	// stop that signal.NotifyContext hands back, so by the time it returns the
	// context is cancelled — as it should be.
	ctxNil         bool
	ctxCancellable bool
	ctxErr         error
}

// harness swaps the two package variables run reaches the outside world
// through: the UI entry point and the terminal check. tty says which case is
// being exercised, and err is what the stubbed interface returns.
//
// The stub never blocks, so no test needs a timeout, and the real ui.Run is
// never called: these tests have no terminal and must not fetch yt-dlp.
func harness(t *testing.T, tty bool, err error) *call {
	t.Helper()

	got := &call{}
	oldStart, oldTTY := startUI, stdoutIsTTY
	t.Cleanup(func() { startUI, stdoutIsTTY = oldStart, oldTTY })

	stdoutIsTTY = func() bool { return tty }
	startUI = func(ctx context.Context, url string, deps ui.Deps) error {
		got.started, got.url, got.deps = true, url, deps
		got.ctxNil = ctx == nil
		if ctx != nil {
			got.ctxCancellable, got.ctxErr = ctx.Done() != nil, ctx.Err()
		}
		return err
	}
	return got
}

// exec runs the command with fresh buffers and returns everything it produced.
func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestHelpPrintsUsageToStdoutAndExitsZero(t *testing.T) {
	for _, flag := range []string{"--help", "-h", "-help"} {
		t.Run(flag, func(t *testing.T) {
			started := harness(t, true, nil)

			code, stdout, stderr := exec(t, flag)

			if code != exitOK {
				t.Fatalf("%s exited %d, want %d", flag, code, exitOK)
			}
			if stderr != "" {
				t.Fatalf("%s wrote to stderr: %q", flag, stderr)
			}
			// Useful means it answers the questions a first-time user has:
			// what it is, both ways to start it, both flags, where the files
			// land, and that there is a debug log.
			for _, want := range []string{
				"yt-dlp",
				"yank <url>",
				"--help",
				"--version",
				"~/Downloads",
				"YANK_DEBUG=1",
				"--update",
				"YANK_NO_UPDATE",
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("usage does not mention %q:\n%s", want, stdout)
				}
			}
			if started.started {
				t.Fatal("--help started the interface")
			}
		})
	}
}

func TestVersionPrintsTheVersionAndExitsZero(t *testing.T) {
	started := harness(t, true, nil)

	code, stdout, stderr := exec(t, "--version")

	if code != exitOK {
		t.Fatalf("--version exited %d, want %d", code, exitOK)
	}
	if stdout != Version+"\n" {
		t.Fatalf("--version printed %q, want %q", stdout, Version+"\n")
	}
	if stderr != "" {
		t.Fatalf("--version wrote to stderr: %q", stderr)
	}
	if started.started {
		t.Fatal("--version started the interface")
	}
}

// Version has to stay a plain package-scope string for -ldflags -X to reach it.
// A test cannot observe the linker, but it can observe the two things that
// would break it: a different type, and a name that is not addressable from
// outside the file.
func TestVersionIsANonEmptyString(t *testing.T) {
	var v string = Version
	if v == "" {
		t.Fatal("Version is empty")
	}
}

func TestAnUnknownFlagIsAUsageError(t *testing.T) {
	started := harness(t, true, nil)

	code, stdout, stderr := exec(t, "--bogus")

	if code != exitUsage {
		t.Fatalf("--bogus exited %d, want %d", code, exitUsage)
	}
	if code == exitOK {
		t.Fatal("--bogus exited successfully")
	}
	if stdout != "" {
		t.Fatalf("a usage error wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Errorf("the error does not name the flag that was wrong:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("a usage error did not print the usage:\n%s", stderr)
	}
	if started.started {
		t.Fatal("a bad flag still started the interface")
	}
}

func TestTwoURLsAreRejected(t *testing.T) {
	started := harness(t, true, nil)

	code, stdout, stderr := exec(t, "https://example.com/one", "https://example.com/two")

	if code != exitUsage {
		t.Fatalf("two arguments exited %d, want %d", code, exitUsage)
	}
	if stdout != "" {
		t.Fatalf("a usage error wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "too many arguments") {
		t.Errorf("the error does not say what was wrong:\n%s", stderr)
	}
	// The point of rejecting is that the first one is not quietly downloaded.
	if started.started {
		t.Fatal("two arguments started the interface anyway")
	}
}

func TestNotATerminalRefusesToStartTheUI(t *testing.T) {
	started := harness(t, false, nil)

	code, stdout, stderr := exec(t)

	if code == exitOK {
		t.Fatal("refusing to start scored as success")
	}
	if code != exitFailure {
		t.Fatalf("a piped stdout exited %d, want %d", code, exitFailure)
	}
	if started.started {
		t.Fatal("the alt-screen interface was started with stdout not a terminal")
	}
	if stdout != "" {
		t.Fatalf("the refusal wrote to stdout, which is the pipe: %q", stdout)
	}
	if !strings.Contains(stderr, "not a terminal") {
		t.Errorf("the refusal does not say why:\n%s", stderr)
	}
	// Plain means plain: no escape codes in the message that exists precisely
	// so escape codes do not end up in a pipe.
	if strings.ContainsAny(stderr, "\x1b\a") {
		t.Errorf("the refusal contains an escape sequence: %q", stderr)
	}
}

// --help and --version are the two answers that need no terminal, so a pipe
// must not turn them into a refusal.
func TestHelpAndVersionWorkWithoutATerminal(t *testing.T) {
	harness(t, false, nil)

	if code, stdout, _ := exec(t, "--help"); code != exitOK || !strings.Contains(stdout, "Usage:") {
		t.Errorf("--help through a pipe exited %d with stdout %q", code, stdout)
	}
	if code, stdout, _ := exec(t, "--version"); code != exitOK || stdout != Version+"\n" {
		t.Errorf("--version through a pipe exited %d with stdout %q", code, stdout)
	}
}

func TestAURLArgumentReachesTheInterface(t *testing.T) {
	started := harness(t, true, nil)

	const url = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	code, _, _ := exec(t, url)

	if code != exitOK {
		t.Fatalf("exited %d, want %d", code, exitOK)
	}
	if !started.started {
		t.Fatal("the interface was never started")
	}
	if started.url != url {
		t.Fatalf("the interface was started with url %q, want %q", started.url, url)
	}
}

// Without an argument the interface starts on the input screen: no URL, and
// still the real dependencies and a live context.
func TestNoArgumentsStartsWithNoURL(t *testing.T) {
	started := harness(t, true, nil)

	code, _, _ := exec(t)

	if code != exitOK {
		t.Fatalf("exited %d, want %d", code, exitOK)
	}
	if !started.started {
		t.Fatal("the interface was never started")
	}
	if started.url != "" {
		t.Fatalf("the interface was started with url %q, want empty", started.url)
	}
}

// The interface is handed the production wiring, not a zero Deps: a nil field
// there is a nil-pointer dereference on the first key press.
func TestTheInterfaceGetsProductionDepsAndALiveContext(t *testing.T) {
	started := harness(t, true, nil)

	if code, _, _ := exec(t); code != exitOK {
		t.Fatalf("exited %d, want %d", code, exitOK)
	}

	d := started.deps
	if d.Resolve == nil || d.Probe == nil || d.Rank == nil || d.Download == nil || d.Cleanup == nil || d.Update == nil {
		t.Fatalf("the interface was started with an incomplete Deps: %+v", d)
	}
	if started.ctxNil {
		t.Fatal("the interface was started with a nil context")
	}
	// signal.NotifyContext gives a cancellable context, and it must still be
	// live when the UI gets it — a context that is already done would kill the
	// program on its first tick.
	if !started.ctxCancellable {
		t.Fatal("the context handed to the interface cannot be cancelled: no signal is wired")
	}
	if started.ctxErr != nil {
		t.Fatalf("the context handed to the interface was already done: %v", started.ctxErr)
	}
}

// "--" ends the flags. Without it a URL that begins with a dash is read as one.
func TestDoubleDashEndsTheFlags(t *testing.T) {
	started := harness(t, true, nil)

	const url = "-https://example.com/watch?v=x"
	code, _, stderr := exec(t, "--", url)

	if code != exitOK {
		t.Fatalf("exited %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	if started.url != url {
		t.Fatalf("the interface was started with url %q, want %q", started.url, url)
	}
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		code    int
		onErrIO bool // the failure is reported to the user
	}{
		{
			name: "a clean quit",
			err:  nil,
			code: exitOK,
		},
		{
			// A signal or a closed terminal: the context ended the program and
			// ui.Run released the info-json on the way out. That is the
			// shutdown working, not a failure.
			name: "killed by the context",
			err:  tea.ErrProgramKilled,
			code: exitOK,
		},
		{
			// The shape Program.Run really produces when the external context
			// ends it: tea.go:727, fmt.Errorf("%w: %w", ErrProgramKilled, err).
			name: "killed wrapping the context error",
			err:  fmt.Errorf("%w: %w", tea.ErrProgramKilled, context.Canceled),
			code: exitOK,
		},
		{
			// tea.go:406 returns ErrInterrupted when Bubble Tea's own handler
			// turns a SIGINT into an InterruptMsg; tea.go:734 then wraps it.
			// Both spellings mean the user asked to stop.
			name: "interrupted",
			err:  tea.ErrInterrupted,
			code: exitOK,
		},
		{
			name: "killed wrapping an interrupt",
			err:  fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrInterrupted),
			code: exitOK,
		},
		{
			// tea.go:637. A crash arrives inside the same sentinel a clean
			// shutdown does, so it has to be classified first.
			name:    "a recovered panic",
			err:     fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrProgramPanic),
			code:    exitFailure,
			onErrIO: true,
		},
		{
			name:    "a text lookalike is not the sentinel",
			err:     errors.New("wrapped: " + tea.ErrProgramKilled.Error()),
			code:    exitFailure,
			onErrIO: true,
		},
		{
			name:    "a real failure",
			err:     errors.New("the terminal could not be put into raw mode"),
			code:    exitFailure,
			onErrIO: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			harness(t, true, tc.err)

			code, stdout, stderr := exec(t)

			if code != tc.code {
				t.Fatalf("ui.Run returning %v exited %d, want %d", tc.err, code, tc.code)
			}
			if stdout != "" {
				t.Errorf("wrote to stdout: %q", stdout)
			}
			if tc.onErrIO && !strings.Contains(stderr, tc.err.Error()) {
				t.Errorf("the failure was not reported: stderr is %q", stderr)
			}
			if !tc.onErrIO && strings.Contains(stderr, "yank: the") {
				t.Errorf("a clean quit was reported as an error: %q", stderr)
			}
		})
	}
}

// The blocking case, spelled out: Program.Run wraps a recovered panic in the
// same ErrProgramKilled a clean shutdown returns, so matching "killed" first
// would hand a crash to the shell as a success. Bubble Tea prints the panic to
// stdout — which a script has redirected — so the exit code is all there is.
func TestAPanicIsAFailureEvenThoughItIsAlsoErrProgramKilled(t *testing.T) {
	// Built the way bubbletea v1.3.10 builds it (tea.go:637), not from text
	// that resembles it.
	err := fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrProgramPanic)
	if !errors.Is(err, tea.ErrProgramKilled) {
		t.Fatal("the test error is not the shape production produces: it does not match ErrProgramKilled")
	}
	if !errors.Is(err, tea.ErrProgramPanic) {
		t.Fatal("the test error does not carry ErrProgramPanic")
	}

	harness(t, true, err)

	code, stdout, stderr := exec(t)

	if code == exitOK {
		t.Fatal("a crash exited 0: `yank <url> && rm <src>` would treat it as a download")
	}
	if code != exitFailure {
		t.Fatalf("a crash exited %d, want %d", code, exitFailure)
	}
	if stdout != "" {
		t.Errorf("wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, tea.ErrProgramPanic.Error()) {
		t.Errorf("the crash was not reported on stderr: %q", stderr)
	}
}

// The terminal check has to be an ioctl and not a file mode: /dev/null is a
// character device, so `yank <url> > /dev/null` — the ordinary quiet run —
// passes a ModeCharDevice test and lands in the alt screen with the keyboard
// in raw mode and nothing on screen.
//
// This exercises the real check, not the variable the other tests replace.
func TestTheTerminalCheckRejectsAFileAndDevNull(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("creating the temp file: %v", err)
	}
	defer f.Close()
	if fileIsTTY(f) {
		t.Error("a regular file was taken for a terminal")
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer devNull.Close()
	if fileIsTTY(devNull) {
		t.Errorf("%s was taken for a terminal", os.DevNull)
	}

	// And the mode bit that used to be the whole check says the opposite, which
	// is the reason this test exists.
	info, err := devNull.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", os.DevNull, err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Fatalf("%s is not a character device here, so this test proves nothing", os.DevNull)
	}
}

// A signal must reach the context the interface is running under. That is the
// route out of the program the key handlers cannot cover: ui.Run releases the
// info-json from whatever model the loop ended on, and it only gets the chance
// if the context is cancelled rather than the process being killed outright.
//
// The signal is sent from inside the stubbed interface, which is where the real
// UI would be sitting when a terminal closes or a kill -TERM lands. If the
// handler were not installed, SIGTERM would kill the test binary — which is the
// failure being tested for, and it fails loudly.
func TestSignalsCancelTheContextTheInterfaceRunsUnder(t *testing.T) {
	// SIGHUP is here and not only SIGTERM because a terminal window closing
	// sends SIGHUP, and Bubble Tea's own handler covers SIGINT and SIGTERM
	// only: without this wiring that route kills the process outright, with the
	// info-json still on disk.
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			oldStart, oldTTY := startUI, stdoutIsTTY
			t.Cleanup(func() { startUI, stdoutIsTTY = oldStart, oldTTY })

			stdoutIsTTY = func() bool { return true }

			var cancelled bool
			startUI = func(ctx context.Context, _ string, _ ui.Deps) error {
				self, err := os.FindProcess(os.Getpid())
				if err != nil {
					t.Skipf("cannot address this process to signal it: %v", err)
				}
				if err := self.Signal(sig); err != nil {
					// Windows has no such signal to send a running process.
					t.Skipf("this platform cannot send itself %v: %v", sig, err)
				}
				select {
				case <-ctx.Done():
					cancelled = true
				case <-time.After(5 * time.Second):
				}
				return nil
			}

			if code := run(nil, io.Discard, io.Discard); code != exitOK {
				t.Fatalf("exited %d, want %d", code, exitOK)
			}
			if !cancelled {
				t.Fatalf("%v did not cancel the context the interface was given", sig)
			}
		})
	}
}

// --- yank --update ------------------------------------------------------------

// updateHarness replaces the two ytdlp calls --update makes. The interface is
// stubbed too, with a terminal that is not one, so a test that reached it
// would fail on the refusal rather than pass by accident.
func updateHarness(t *testing.T, resolve func(context.Context) (ytdlp.Result, error), update func(context.Context, ytdlp.Result) (ytdlp.UpdateResult, error)) *call {
	t.Helper()
	started := harness(t, false, nil)
	oldResolve, oldUpdate := resolveForUpdate, updateCached
	t.Cleanup(func() { resolveForUpdate, updateCached = oldResolve, oldUpdate })
	resolveForUpdate, updateCached = resolve, update
	return started
}

func cachedAt(version string) func(context.Context) (ytdlp.Result, error) {
	return func(context.Context) (ytdlp.Result, error) {
		return ytdlp.Result{Path: "/cache/yank/bin/yt-dlp", Version: version, Source: ytdlp.SourceCache}, nil
	}
}

func TestUpdatePrintsOneLineAndExitsZero(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(context.Context) (ytdlp.Result, error)
		result  ytdlp.UpdateResult
		want    string
		updates bool
	}{
		{
			name:    "up to date",
			resolve: cachedAt("2026.08.19"),
			result:  ytdlp.UpdateResult{Status: ytdlp.UpdateUpToDate, Previous: "2026.08.19", Version: "2026.08.19", Latest: "2026.08.19"},
			want:    "yt-dlp is up to date (2026.08.19)\n",
			updates: true,
		},
		{
			name:    "updated",
			resolve: cachedAt("2026.07.01"),
			result:  ytdlp.UpdateResult{Status: ytdlp.UpdateInstalled, Previous: "2026.07.01", Version: "2026.08.19", Latest: "2026.08.19"},
			want:    "yt-dlp updated 2026.07.01 → 2026.08.19\n",
			updates: true,
		},
		{
			// Another yank is running the cached copy: the release waits.
			name:    "staged while another yank runs",
			resolve: cachedAt("2026.07.01"),
			result:  ytdlp.UpdateResult{Status: ytdlp.UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19", Kept: fmt.Errorf("wrapped: %w", ytdlp.ErrInUse)},
			want:    "yt-dlp 2026.08.19 is downloaded and will be used once other yank windows close\n",
			updates: true,
		},
		{
			// Kept for a reason that is not another yank: closing windows
			// would not help, so it is not what the line says.
			name:    "staged, the switch refused",
			resolve: cachedAt("2026.07.01"),
			result:  ytdlp.UpdateResult{Status: ytdlp.UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19", Kept: errors.New("could not replace /cache/yank/bin/yt-dlp: permission denied")},
			want:    "yt-dlp 2026.08.19 is downloaded but could not be switched in: could not replace /cache/yank/bin/yt-dlp: permission denied; yank will try again next launch\n",
			updates: true,
		},
		{
			// Staged and nothing tried to switch it in.
			name:    "staged, not tried",
			resolve: cachedAt("2026.07.01"),
			result:  ytdlp.UpdateResult{Status: ytdlp.UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19"},
			want:    "yt-dlp 2026.08.19 is downloaded and will be used next launch\n",
			updates: true,
		},
		{
			// Resolve put a release an earlier launch staged in place.
			name: "promoted by Resolve",
			resolve: func(context.Context) (ytdlp.Result, error) {
				return ytdlp.Result{Path: "/cache/yank/bin/yt-dlp", Version: "2026.08.19", Source: ytdlp.SourceCache, Updated: true}, nil
			},
			result:  ytdlp.UpdateResult{Status: ytdlp.UpdateUpToDate, Previous: "2026.08.19", Version: "2026.08.19", Latest: "2026.08.19"},
			want:    "yt-dlp updated to 2026.08.19\n",
			updates: true,
		},
		{
			name: "on PATH",
			resolve: func(context.Context) (ytdlp.Result, error) {
				return ytdlp.Result{Path: "/opt/homebrew/bin/yt-dlp", Version: "2026.08.19", Source: ytdlp.SourcePATH}, nil
			},
			want: "yt-dlp is on your PATH (/opt/homebrew/bin/yt-dlp); update it with your package manager\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated := false
			started := updateHarness(t, tt.resolve, func(ctx context.Context, res ytdlp.Result) (ytdlp.UpdateResult, error) {
				updated = true
				if res.Source == ytdlp.SourcePATH {
					t.Errorf("--update asked to update a PATH copy: %+v", res)
				}
				return tt.result, nil
			})

			code, stdout, stderr := exec(t, "--update")

			if code != exitOK {
				t.Fatalf("exited %d, want %d; stderr %q", code, exitOK, stderr)
			}
			if stdout != tt.want {
				t.Errorf("stdout = %q, want %q", stdout, tt.want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing", stderr)
			}
			if updated != tt.updates {
				t.Errorf("update called = %t, want %t", updated, tt.updates)
			}
			if started.started {
				t.Error("--update started the interface")
			}
		})
	}
}

func TestUpdateFailureIsReportedOnStderr(t *testing.T) {
	for name, calls := range map[string]struct {
		resolve func(context.Context) (ytdlp.Result, error)
		update  func(context.Context, ytdlp.Result) (ytdlp.UpdateResult, error)
	}{
		"resolve fails": {
			resolve: func(context.Context) (ytdlp.Result, error) {
				return ytdlp.Result{}, errors.New("could not fetch yt-dlp: GET x: unexpected status 404 Not Found")
			},
			update: func(context.Context, ytdlp.Result) (ytdlp.UpdateResult, error) {
				t.Error("update ran after Resolve failed")
				return ytdlp.UpdateResult{}, nil
			},
		},
		"update fails": {
			resolve: cachedAt("2026.07.01"),
			update: func(context.Context, ytdlp.Result) (ytdlp.UpdateResult, error) {
				return ytdlp.UpdateResult{Latest: "2026.08.19"}, errors.New("could not update yt-dlp to 2026.08.19: checksum mismatch")
			},
		},
		// An HTTP client timeout satisfies errors.Is(context.DeadlineExceeded).
		// Nobody pressed ctrl+c, so it is a failure, not a cancel.
		"our own timeout": {
			resolve: cachedAt("2026.07.01"),
			update: func(context.Context, ytdlp.Result) (ytdlp.UpdateResult, error) {
				return ytdlp.UpdateResult{}, fmt.Errorf("could not look up the latest yt-dlp release: %w", context.DeadlineExceeded)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			updateHarness(t, calls.resolve, calls.update)

			code, stdout, stderr := exec(t, "--update")

			if code != exitFailure {
				t.Fatalf("exited %d, want %d", code, exitFailure)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if !strings.HasPrefix(stderr, "yank: could not") {
				t.Errorf("stderr = %q, want the reason", stderr)
			}
		})
	}
}

// ctrl+c during --update is the signal a terminal sends. It scores what a
// signal that ends the interface scores.
func TestACancelledUpdateExitsWithTheInterruptedCode(t *testing.T) {
	updateHarness(t, cachedAt("2026.07.01"), func(ctx context.Context, res ytdlp.Result) (ytdlp.UpdateResult, error) {
		self, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Skipf("cannot address this process to signal it: %v", err)
		}
		if err := self.Signal(os.Interrupt); err != nil {
			t.Skipf("this platform cannot send itself an interrupt: %v", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Error("the interrupt did not cancel the update's context")
		}
		return ytdlp.UpdateResult{}, fmt.Errorf("yt-dlp update cancelled: %w", ctx.Err())
	})

	code, stdout, stderr := exec(t, "--update")

	if code != exitOK {
		t.Fatalf("exited %d, want %d", code, exitOK)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "cancelled") {
		t.Errorf("stderr = %q, want the cancel said", stderr)
	}
}

func TestUpdateTakesNoURL(t *testing.T) {
	updateHarness(t, func(context.Context) (ytdlp.Result, error) {
		t.Error("--update with a URL resolved yt-dlp")
		return ytdlp.Result{}, nil
	}, nil)

	if code, _, stderr := exec(t, "--update", "https://example.com/v"); code != exitUsage {
		t.Fatalf("exited %d, want %d; stderr %q", code, exitUsage, stderr)
	}
}
