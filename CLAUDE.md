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

**No shell.** Every external command goes through `exec.CommandContext` with an
argv slice. Quotes written in issues and comments are for reading; an argument
never contains them.

**Cancellation is a feature.** Every exec and every HTTP request takes a
`context.Context`. `exec.CommandContext` alone is not enough when the child
re-execs and a descendant inherits the pipes — set `cmd.WaitDelay`, and then
remember that `WaitDelay` returns `exec.ErrWaitDelay` on an otherwise
successful exit.

**Dependencies.** `bubbletea`, `bubbles`, `lipgloss`, and nothing else. The
standard library covers the rest. `internal/ytdlp` imports no third-party
package at all.

## Verification

A check that was not run is not a check that passed. Paste real command output
when reporting; say plainly when something was skipped or failed.
