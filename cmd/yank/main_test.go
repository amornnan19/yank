package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	// Aliased because this file already declares exec: the helper below that
	// runs the command the way a shell would.
	osexec "os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ui"
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
	if d.Resolve == nil || d.Probe == nil || d.Rank == nil || d.Download == nil || d.Cleanup == nil {
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

// The child of the second-signal test announces itself on stdout so the parent
// knows the handler is installed, and again once the first signal has landed,
// so that the parent is signalling into the shutdown grace and not before it.
const (
	secondSignalChildEnv = "YANK_TEST_SECOND_SIGNAL_CHILD"
	childReady           = "yank-test: the interface is running"
	childCancelled       = "yank-test: the context was cancelled"

	// Longer than the parent is willing to wait, so a child that outlives the
	// signals is a swallowed signal and not a race the parent lost.
	childGrace = 30 * time.Second
)

// secondSignalChild is the process under test: run, with the interface stubbed
// for something that sits in the shutdown grace the way updateQuitting does
// while it waits for the download to die and its leftovers to go. It does not
// return.
func secondSignalChild() {
	stdoutIsTTY = func() bool { return true }
	startUI = func(ctx context.Context, _ string, _ ui.Deps) error {
		fmt.Println(childReady)
		<-ctx.Done()
		fmt.Println(childCancelled)
		time.Sleep(childGrace)
		return nil
	}
	os.Exit(run(nil, io.Discard, io.Discard))
}

// A second signal arriving from outside during the shutdown grace has to end
// the process, not vanish into it.
//
// signal.NotifyContext keeps its handler registered until stop is called, so
// the process no longer has the default disposition for SIGINT, SIGTERM and
// SIGHUP — but it stops acting on them after the first, because the channel it
// reads is buffered at one and already full. Without context.AfterFunc(ctx,
// stop) the grace window is a hole: a second kill -TERM leaves the process
// running until the grace expires, which reads as a hang.
//
// This has to be a real process, because what is being tested is the kernel's
// disposition for a signal. In-process the second SIGTERM would either be
// dropped by our own handler (the bug) or end the test binary (the fix), and
// only a child can be signalled and have its wait status read.
func TestASecondSignalDuringTheShutdownGraceEndsTheProcess(t *testing.T) {
	if os.Getenv(secondSignalChildEnv) == "1" {
		secondSignalChild()
		return
	}

	cmd := osexec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), secondSignalChildEnv+"=1")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("piping the child's stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}

	// The pipe has to be drained to EOF before Wait, so one goroutine owns it
	// and reports the two markers and the end of the output on channels.
	ready, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			switch strings.TrimSpace(sc.Text()) {
			case childReady:
				ready <- struct{}{}
			case childCancelled:
				cancelled <- struct{}{}
			}
		}
	}()

	// Nothing is left running behind a failure: SIGKILL is not what is under
	// test, it is the cleanup no handler can refuse.
	stopChild := func() {
		if err := cmd.Process.Signal(syscall.SIGKILL); err == nil {
			<-ended
			_ = cmd.Wait()
		}
	}
	await := func(what string, c <-chan struct{}) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(30 * time.Second):
			stopChild()
			t.Fatalf("the child never reported %s", what)
		}
	}

	await("that it was running", ready)

	// The first signal. Cancelling the context is all it is asked to do here;
	// TestSignalsCancelTheContextTheInterfaceRunsUnder covers that on its own.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		stopChild()
		t.Skipf("this platform cannot send a process SIGTERM: %v", err)
	}
	await("that the context was cancelled", cancelled)

	// Now the grace window, and the signal that used to disappear into it.
	// Sending it more than once is not because more than one is needed: every
	// signal after the first is swallowed for as long as the handler stays
	// registered, so the repeats only close the gap between the context being
	// cancelled — which is what the child has just announced — and the
	// AfterFunc that unregisters the handler having run. Whichever one lands
	// after that is the second signal the user typed, and it must be fatal.
	deadline := time.After(20 * time.Second)
	for alive := true; alive; {
		select {
		case <-ended:
			alive = false
		case <-deadline:
			stopChild()
			t.Fatal("the child outlived every signal after the first: they were swallowed by the handler NotifyContext leaves registered")
		case <-time.After(25 * time.Millisecond):
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				// Already gone, or the platform refuses to send it. Either way
				// the wait status below is the answer.
				alive = false
				<-ended
			}
		}
	}

	err = cmd.Wait()
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the child exited cleanly (%v) instead of being signalled: the second signal was swallowed and it sat out the whole grace", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Skipf("this platform does not report a wait status: %T", exitErr.Sys())
	}
	if !status.Signaled() {
		t.Fatalf("the child exited with code %d rather than dying of a signal: %v", status.ExitStatus(), err)
	}
	if got := status.Signal(); got != syscall.SIGTERM {
		t.Fatalf("the child died of %v, want %v", got, syscall.SIGTERM)
	}
}
