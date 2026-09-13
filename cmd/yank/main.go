// Command yank is a terminal UI wrapper around yt-dlp: paste a video URL, pick
// a format, watch a progress bar, get the file path.
//
// This file is the command line and nothing else. Everything the program does
// lives in internal/ui and internal/ytdlp; what happens here is argument
// parsing, the two flags that answer without starting anything, the refusal to
// paint a full-screen UI into a pipe, the signal context the UI shuts down on,
// and turning however the interface ended into an exit code.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"

	"github.com/amornnan19/yank/internal/ui"
	"github.com/amornnan19/yank/internal/ytdlp"
)

// Version is the current yank version. Set at build time with:
//
//	go build -ldflags "-X main.Version=1.2.3" ./cmd/yank
var Version = "0.1.0"

// Exit codes are part of the interface, not an afterthought: the scriptable
// flags in the backlog are specified against them. A quit the user asked for
// scores 0 — pressing q, and equally a signal that shut the program down — and
// only yank failing to do the job scores a failure.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `yank — a terminal UI for downloading video with yt-dlp.

Paste a URL, pick a format, watch a progress bar, get the file path.

Usage:
  yank                start on the input screen, ready for a pasted URL
  yank <url>          start with that URL
  yank -- <url>       the same, for a URL that begins with a dash

Flags:
  -h, --help          show this help and exit
      --version       print the version and exit
      --update        update the cached yt-dlp to the latest release now,
                      print what happened, and exit

Downloads are saved to ~/Downloads, created if it is missing. When no home
directory can be resolved, yank writes to the working directory and says so on
screen rather than claiming the file is in Downloads.

yt-dlp itself does not need to be installed: yank downloads a checksum-verified
copy on first run and caches it under $XDG_CACHE_HOME/yank/bin, or
~/.cache/yank/bin when that is unset. ffmpeg is optional — without it, the
merged and mp3 rows are hidden and the picker says so.

The cached yt-dlp is kept up to date: at most once a day, in the background,
yank checks for a newer release and downloads it for the next launch. A yt-dlp
found on your PATH is never touched. Set YANK_NO_UPDATE=1 to switch the
background check off; yank --update still works.

Set YANK_DEBUG=1 to append a debug log to yank.log in that same cache
directory, next to the cached binary.

The interface needs a terminal. --help, --version and --update work anywhere;
starting the UI with stdout redirected is refused rather than writing escape
codes into a pipe.
`

const notATerminal = `yank: stdout is not a terminal.

The interface is a full-screen terminal UI, so redirecting it into a pipe or a
file would produce escape codes and nothing you could read. Run yank in a
terminal instead. --help, --version and --update work anywhere.
`

// startUI enters the interface. It is a variable so the tests can drive run
// without a terminal and without yt-dlp, and it is the one place a URL from the
// command line is handed over.
var startUI = func(ctx context.Context, url string, deps ui.Deps) error {
	return ui.Run(ctx, deps, url)
}

// resolveForUpdate and updateCached are the two ytdlp calls --update makes.
// Variables so a test can drive every outcome without the network.
var (
	resolveForUpdate = ytdlp.Resolve
	updateCached     = ytdlp.Update
)

// stdoutIsTTY reports whether the interface has a terminal to draw on. A
// variable so a test can state which case it is exercising instead of depending
// on how the test binary was run; the check itself is fileIsTTY.
var stdoutIsTTY = func() bool { return fileIsTTY(os.Stdout) }

// fileIsTTY asks the terminal driver, not the file mode.
//
// os.Stdout.Stat plus os.ModeCharDevice answers "is this a character device",
// which /dev/null and /dev/zero both are — so `yank <url> > /dev/null`, the
// ordinary way to run a command quietly, would pass the guard and Bubble Tea
// would put the keyboard in raw mode and paint the alt screen into the void.
//
// term.IsTerminal is an ioctl, which is the actual question. The package is
// already in the build: Bubble Tea uses it for the same job.
func fileIsTTY(f *os.File) bool { return term.IsTerminal(f.Fd()) }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command with its inputs and outputs passed in, so a test can
// call it the way a shell would. It returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("yank", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// The flag package prints its own usage on -h, to stderr, and there is no
	// way to tell it that asking for help is not an error. Silenced here and
	// answered below: help goes to stdout and exits 0, a bad flag goes to
	// stderr and exits non-zero.
	fs.Usage = func() {}
	version := fs.Bool("version", false, "print the version and exit")
	update := fs.Bool("update", false, "update the cached yt-dlp and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		// Parse has already named the offending flag on stderr.
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	if *version {
		fmt.Fprintln(stdout, Version)
		return exitOK
	}

	// One URL, or none. Ignoring the rest would quietly download something
	// other than what was asked for, which a shell glob or an unquoted URL with
	// an ampersand in it can produce by accident.
	var url string
	switch rest := fs.Args(); len(rest) {
	case 0:
	case 1:
		url = rest[0]
	default:
		fmt.Fprintf(stderr, "yank: too many arguments: expected at most one URL, got %d\n\n", len(rest))
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	if *update {
		if url != "" {
			fmt.Fprintf(stderr, "yank: --update takes no URL\n\n")
			fmt.Fprint(stderr, usage)
			return exitUsage
		}
		return runUpdate(stdout, stderr)
	}

	if !stdoutIsTTY() {
		fmt.Fprint(stderr, notATerminal)
		return exitFailure
	}

	if os.Getenv("YANK_DEBUG") == "1" {
		// A debug log that could not be opened is not a reason to refuse to
		// download anything; the run continues without one.
		if f, err := setupDebugLog(); err == nil {
			defer f.Close()
		}
	}

	// The UI is bounded by a context that ends on a signal, and ui.Run releases
	// the info-json from whatever model the loop ended on — the one route out
	// of the program the key handlers cannot cover.
	//
	// SIGHUP is in the list because it is what a terminal window closing sends,
	// which is one of the cases this wiring exists for; without it that route
	// kills the process outright and leaves the info-json behind. Bubble Tea
	// installs its own handler for SIGINT and SIGTERM but not for SIGHUP.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	// Deliberately no context.AfterFunc(ctx, stop) here, though NotifyContext
	// does swallow every signal after the first — its channel is buffered at
	// one and already full. Handing the signals back to the kernel on the first
	// one buys nothing and costs the terminal.
	//
	// It buys nothing because there is no long window to interrupt. An external
	// signal never reaches Model.Update: tea.WithContext returns from the event
	// loop on ctx.Done(), and Bubble Tea's own handler turns SIGINT into
	// InterruptMsg and SIGTERM into QuitMsg, both of which the loop consumes
	// itself. The model's shutdown grace runs for a ctrl+c key press and for
	// nothing else, so what a second signal would cut short is Bubble Tea's
	// teardown — milliseconds, not the two seconds of quitGrace.
	//
	// It costs the terminal because that teardown is what leaves the alt screen
	// and restores termios. Bubble Tea unregisters its own handler on the same
	// cancellation, so restoring the default disposition here would leave the
	// process killable by a second signal precisely while it is putting the
	// terminal back — and a user who typed kill twice would be left in raw mode
	// with no echo, typing `reset` blind.

	err := startUI(ctx, url, ui.Production())
	switch {
	case err == nil:
		return exitOK

	case errors.Is(err, tea.ErrProgramPanic):
		// A failure is classified positively, and before the sentinel that
		// merely says the loop ended: Program.Run wraps everything the event
		// loop produced in ErrProgramKilled, a recovered panic included
		// (fmt.Errorf("%w: %w", ErrProgramKilled, ErrProgramPanic)), so
		// checking "killed" first would score a crash as a clean quit. Bubble
		// Tea prints the panic and its stack to stdout, which a script has
		// redirected; the exit code is the only signal it gets.
		fmt.Fprintf(stderr, "yank: %v\n", err)
		return exitFailure

	case errors.Is(err, tea.ErrProgramKilled), errors.Is(err, tea.ErrInterrupted):
		// The program was asked to stop: the context ended it — a signal, or
		// the terminal going away — or Bubble Tea's own handler turned a SIGINT
		// into an interrupt. Either way it scores the same as pressing q: the
		// user asked yank to stop and it stopped, releasing what it owned.
		return exitOK

	default:
		fmt.Fprintf(stderr, "yank: %v\n", err)
		return exitFailure
	}
}

// runUpdate is yank --update: the check and the install, synchronously, with
// no interface and no throttle, reported in one line.
//
// Its exit codes follow the ones above. Up to date, updated, downloaded and
// staged for a later launch — whatever kept it from being switched in now —
// and a yt-dlp on PATH that yank leaves alone all score 0; a failure scores
// exitFailure with the reason on stderr. A cancel — ctrl+c, or a signal — scores what a signal
// that shut the interface down scores, which is 0: the user asked yank to
// stop and it stopped, leaving any cached copy exactly as it was. It is still
// said on stderr, so a person at the terminal is not left guessing.
func runUpdate(stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	res, err := resolveForUpdate(ctx)
	if err == nil && res.Source == ytdlp.SourcePATH {
		fmt.Fprintf(stdout, "yt-dlp is on your PATH (%s); update it with your package manager\n", res.Path)
		return exitOK
	}
	var out ytdlp.UpdateResult
	if err == nil {
		out, err = updateCached(ctx, res)
	}
	switch {
	case err != nil && ctx.Err() != nil:
		// Classified by our own context, not by the error chain: an HTTP client
		// timeout also satisfies errors.Is(context.DeadlineExceeded), and that
		// is a failure, not the user stopping yank.
		fmt.Fprintln(stderr, "yank: yt-dlp update cancelled")
		return exitOK
	case err != nil:
		fmt.Fprintf(stderr, "yank: %v\n", err)
		return exitFailure
	case out.Status == ytdlp.UpdateInstalled:
		fmt.Fprintf(stdout, "yt-dlp updated %s → %s\n", out.Previous, out.Version)
	case out.Status == ytdlp.UpdateStaged && errors.Is(out.Kept, ytdlp.ErrInUse):
		// Another yank is running the cached copy, and replacing it under a
		// running yt-dlp can break it (#26). The staged release waits for the
		// first launch that finds no other yank running.
		fmt.Fprintf(stdout, "yt-dlp %s is downloaded and will be used once other yank windows close\n", out.Staged)
	case out.Status == ytdlp.UpdateStaged && out.Kept != nil:
		// Kept for any other reason — the lock cannot be held, the rename was
		// refused, the cached copy's record does not describe it. Closing
		// windows would not help, so the line does not say it would.
		fmt.Fprintf(stdout, "yt-dlp %s is downloaded but could not be switched in: %v; yank will try again next launch\n", out.Staged, out.Kept)
	case out.Status == ytdlp.UpdateStaged:
		fmt.Fprintf(stdout, "yt-dlp %s is downloaded and will be used next launch\n", out.Staged)
	case res.Updated:
		// Resolve itself put an earlier staged release in place.
		fmt.Fprintf(stdout, "yt-dlp updated to %s\n", out.Version)
	default:
		fmt.Fprintf(stdout, "yt-dlp is up to date (%s)\n", out.Version)
	}
	return exitOK
}

// setupDebugLog opens (creating as needed) <cache>/yank/yank.log for debug
// logging via tea.LogToFile. The <cache>/yank root comes from ytdlp.CacheRoot
// so that the log file and the cached yt-dlp binary cannot drift apart.
func setupDebugLog() (*os.File, error) {
	logDir, err := ytdlp.CacheRoot()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}

	return tea.LogToFile(filepath.Join(logDir, "yank.log"), "debug")
}
