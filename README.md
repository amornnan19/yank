# yank

A terminal UI for downloading video with [yt-dlp](https://github.com/yt-dlp/yt-dlp).
Paste a URL, pick a format from a short list, watch a progress bar, get the path
of the file that landed on disk.

yank is a wrapper. It does not fetch a single byte of video itself: yt-dlp does
the downloading, and yank is the interface in front of it.

## Install

Go 1.27.1 or newer is required (the version in `go.mod`). Build from a clone:

```
git clone https://github.com/amornnan19/yank
cd yank
go build -o yank ./cmd/yank
```

Then put the `yank` binary somewhere on your `PATH`. There is no
`go install ...@latest` line here because the repository is private, and a
command that cannot work for the person reading it does not belong in a README.

The version string can be set at build time:

```
go build -ldflags "-X main.Version=1.2.3" ./cmd/yank
```

The `Makefile` wraps the same commands: `make build` (with `VERSION=1.2.3` to
set the version), `make check` for gofmt, vet and tests, `make help` for the
rest.

## Usage

```
yank                start on the input screen, ready for a pasted URL
yank <url>          start with that URL
yank -- <url>       the same, for a URL that begins with a dash
```

More than one argument is refused rather than guessed at, because a shell glob
or an unquoted URL with an ampersand in it can produce a second one by accident.

Flags:

```
-h, --help          show this help and exit
    --version       print the version and exit
```

Keys, by screen: `enter` fetches the URL; `esc` cancels a fetch or a download
and goes back to the input screen; in the picker `↑↓` and `jk` move the cursor,
`enter` downloads the row under it, and `1`-`9` jump to a row without starting
it; on the done screen `enter` starts another and `q` quits. `ctrl+c` quits
from anywhere, waiting for the run it cancelled to finish dying.

**Where files go.** `~/Downloads`, created if it is missing. When no home
directory can be resolved at all, yank writes to the working directory instead
of refusing, and the done screen says so rather than pointing you at a
Downloads folder the file is not in.

**Debug log.** Set `YANK_DEBUG=1` to append a debug log to `yank.log` in yank's
cache directory — `$XDG_CACHE_HOME/yank/yank.log`, or `~/.cache/yank/yank.log`
when `XDG_CACHE_HOME` is unset. A log file that cannot be opened does not stop
the run.

**A terminal is required.** The interface is full-screen, so starting it with
stdout redirected into a pipe or a file is refused rather than writing escape
codes somewhere nobody can read them. `--help` and `--version` work anywhere.
The check is an ioctl, not a file-mode test, so `yank <url> > /dev/null` is
refused too.

Exit codes: `0` when the job was done or you asked yank to stop (`q`, `ctrl+c`,
a signal, the terminal closing); `1` when yank failed to do the job; `2` for a
bad flag or too many arguments.

## How it works

**yt-dlp does the downloading.** Everything yank fetches, it fetches by running
[yt-dlp](https://github.com/yt-dlp/yt-dlp) with an argv it built — no shell is
involved anywhere. The credit for the hard part, extraction, belongs there.

yt-dlp does not need to be installed separately. yank uses a copy already on
`PATH` if there is one and it answers `--version`; otherwise it looks in its own
cache, and failing that downloads the release asset for this platform, checks it
against the SHA-256 listed in the release's `SHA2-256SUMS`, and installs it into
`$XDG_CACHE_HOME/yank/bin` (or `~/.cache/yank/bin`). A copy whose checksum does
not match is never installed. Delete that directory and the next run fetches a
fresh copy.

A `yt-dlp -J` extraction of a popular site lists a hundred-odd formats, most of
which differ in ways nobody wants to read about. yank reduces them to at most
seven rows: up to six video heights plus one audio-only mp3 row. Within a
height it prefers avc1/h264, which plays everywhere, unless the vp9/av01 stream
is at least 25% smaller. The row you pick is downloaded by its concrete format
id, so the codec and the size on the row describe the file you actually get.

**ffmpeg is optional.** yank neither bundles nor downloads it. With ffmpeg on
`PATH`, video-only and audio-only streams can be merged and the mp3 row can
transcode. Without it, rows that would need a merge are hidden along with the
mp3 row, only formats that already carry both video and audio are offered, and
the picker says why. Missing ffmpeg is a smaller menu, not a failure — though
on a site that serves video and audio separately for everything, that menu can
come out empty, and yank says that plainly too.

## What this is for

yank is a personal-archiving tool: keeping a copy of something you want to
watch later, offline, or after it goes away. Whether you may keep a copy of a
particular video is between you, the site's terms, and the law where you live —
yank does not know and cannot check. Please do not use it to redistribute other
people's work.

## Platform support

macOS (darwin) is the platform yank is developed and tested on.

The Windows and Linux release-asset paths are written and unit-tested but have
never been exercised end to end: no one has run a real download on either. They
may well work; treat them as untested rather than supported.
