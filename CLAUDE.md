# yank

A terminal UI wrapper around the `yt-dlp` CLI, in Go with Bubble Tea. Paste a
video URL, pick a format, watch a progress bar, get the file path.

## Where the plan lives

GitHub issues on `amornnan19/yank`, not a file in the repo. Issue #1 is the
tracker: it holds the settled design decisions D1-D5 and the package layout,
and every other issue refers back to it. Read #1 before changing behaviour.

## Rules that outlive any one issue

**Cached artifacts.** yank caches an expensive, checksum-verified `yt-dlp`
binary under `<cache>/yank/bin`. Never delete or replace it on the default
`if err != nil` branch. A cached artifact may only be removed after the failure
has been positively classified as corruption — a probe that timed out, was
cancelled, returned `exec.ErrWaitDelay` after a successful exit, or was refused
by the OS is not evidence that the file is bad. The same distinction applies to
the cached `-J` info-json and to a download the user cancelled: cancelled is not
failed.

**Classifying a failed run.** Every helper that turns an `exec` outcome into an
error must separate a *positive* failure — the process ran and refused — from an
*inconclusive* one: the caller cancelled, our own timeout fired, a signal we did
not send killed it (`ExitCode() < 0`), or `exec.ErrWaitDelay` cut the pipes. Only
a positive failure may be reported as the URL's or the file's fault.
`classifyProbe` in `binary.go` is the reference.

**Exit codes are classified positively too.** `tea.ErrProgramKilled` is not proof
of a clean shutdown: `Program.Run` wraps *every* error the event loop produced in
it, including a recovered panic
(`fmt.Errorf("%w: %w", ErrProgramKilled, ErrProgramPanic)`). Test the failure
sentinels first — `tea.ErrProgramPanic` before `tea.ErrProgramKilled` — and build
test errors with the wrapping the library actually produces, never a lookalike
from `errors.New`. Bubble Tea prints a recovered panic to *stdout*, so the exit
code is the only signal a script gets.

**Never classify by substring of `err.Error()`.** The wrapped chain contains
stdlib text and any URL we interpolated. `exec.ErrWaitDelay` stringifies to
"exec: WaitDelay **expired** before I/O complete". Match on typed errors, or on
a field that holds only yt-dlp's own words.

**No shell.** Every external command goes through `exec.CommandContext` with an
argv slice. Quotes written in issues and comments are for reading; an argument
never contains them.

**Absent codec fields.** yt-dlp spells "this stream is missing" as the string
`"none"`. An empty `VCodec`/`ACodec` — the field was null or absent, which some
extractors do — means *unknown*, and unknown reads as **present**, matching
yt-dlp's own `best` selector. Never treat `""` as `"none"`. Where a function
instead needs positive evidence that a stream exists — picking which audio track
to merge, say — that is a different question and gets its own named predicate
with the reason on it, not the same expression written inline a second way.

**Cancellation is a feature.** Every exec and every HTTP request takes a
`context.Context`. `exec.CommandContext` alone is not enough when the child
re-execs and a descendant inherits the pipes — set `cmd.WaitDelay`, and then
remember that `WaitDelay` returns `exec.ErrWaitDelay` on an otherwise
successful exit.

**Remote text is not safe to print.** Titles, uploaders and yt-dlp's own error
messages come from a page we did not write. Strip control characters and escape
sequences before measuring or rendering them. `lipgloss.Width` is a display-width
helper, not a sanitiser: it reports the widest *line* of a multi-line string, so a
title with a newline in it measures small, survives truncation intact, and takes
an extra row on screen. Truncate before styling, never after — cutting a rendered
string can drop its reset sequence and let the style bleed down the page.

**Dependencies.** `bubbletea`, `bubbles`, `lipgloss`, and what they already
compile in. The standard library covers the rest, and `internal/ytdlp` imports no
third-party package at all. `x/term` is a direct require because the TTY guard
needs the same ioctl Bubble Tea already uses for it: the rule is about what ships
in the binary, and hand-rolling a per-GOOS ioctl to avoid *declaring* a package
we already ship would add untestable platform code to keep a list short.

## Verification

A check that was not run is not a check that passed. Paste real command output
when reporting; say plainly when something was skipped or failed.
