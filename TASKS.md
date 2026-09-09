# yank — task list

A terminal UI wrapper around the `yt-dlp` CLI, written in Go with Bubble Tea.
Paste a video URL, pick a format, watch a progress bar, get the file path.

Reference for the idea (not the code): https://github.com/pablostanley/yoinks —
same concept in TypeScript/Ink. We are not porting it; the design below is ours.

The name `yank` is a placeholder. Renaming means changing the module path,
the `cmd/` directory name, and the `~/.yank/bin` cache path.

## Environment (already done)

- [x] Go 1.27.1 — `brew install go`
- [x] ffmpeg 9.0.1 — `brew install ffmpeg` (needed to merge video+audio streams)
- [ ] yt-dlp — deliberately NOT installed; `yank` fetches it itself on first run,
      and that path needs to be exercised

## 0. Project setup

- [ ] `git init`, default branch `main`
- [ ] `go mod init github.com/amornnan/yank`
- [ ] `.gitignore` — built binary, `.DS_Store`
- [ ] Add deps: `bubbletea`, `bubbles`, `lipgloss` (latest stable)
      No other third-party packages — standard library for everything else.

## 1. Package layout

```
cmd/yank/main.go            flag parsing + entry point
internal/ytdlp/binary.go    binary resolution
internal/ytdlp/probe.go     `yt-dlp -J` -> structs
internal/ytdlp/formats.go   format ranking  ← the part with real logic
internal/ytdlp/download.go  exec + progress parsing
internal/ui/model.go        bubbletea Model / Init / Update
internal/ui/view.go         rendering
internal/ui/styles.go       lipgloss styles
```

## 2. internal/ytdlp/binary.go

- [ ] Resolve yt-dlp in order: `yt-dlp` on PATH → `~/.yank/bin/yt-dlp` →
      download from GitHub releases. Verify each with `--version` exiting 0.
- [ ] Release URL: `https://github.com/yt-dlp/yt-dlp/releases/latest/download/<asset>`
      - darwin → `yt-dlp_macos`
      - windows → `yt-dlp.exe`
      - linux/arm64 → `yt-dlp_linux_aarch64`
      - otherwise → `yt-dlp_linux`
- [ ] Download to `<path>.download`, chmod 0755, then `os.Rename` into place.
      An interrupted download must not leave a half-written binary that passes
      the existence check next time.
- [ ] ffmpeg: if on PATH, return empty string (yt-dlp finds it itself).
      Do not bundle it. Missing ffmpeg is not fatal — single-file formats still work.
- [ ] Every exec takes a `context.Context` and uses `exec.CommandContext`.

## 3. internal/ytdlp/probe.go

- [ ] Run `yt-dlp -J --no-playlist --no-warnings <url>`.
- [ ] `VideoInfo`: Title, Uploader, Duration, WebpageURL, ExtractorKey, Formats.
- [ ] `RawFormat`: FormatID, Ext, VCodec, ACodec, Height, Width, ABR, TBR,
      Filesize, FilesizeApprox.
- [ ] Save raw `-J` stdout to a temp file, return the path — the download step
      passes `--load-info-json <path>` so extraction does not run twice.
      Clean the temp file up on exit.
- [ ] On non-zero exit, surface yt-dlp's stderr but strip the `ERROR: ` and
      `[youtube] <id>: ` prefixes so the user sees one readable line.

## 4. internal/ytdlp/formats.go ← the important one

`yt-dlp -J` returns 100+ formats. Reduce to at most 7 rows a human can choose
between. This is what makes the wrapper good or useless.

- [ ] Video rows: only formats with a real `Height`.
- [ ] Group by height, one representative each. Prefer a codec that plays
      everywhere (`avc1`/`h264`) over `vp9`/`av01` unless the size difference is
      large; among equals prefer higher `TBR`.
- [ ] Keep at most 6 heights, favouring the common ladder
      (2160, 1440, 1080, 720, 480, 360) and always including the highest available.
- [ ] Sort descending by height.
- [ ] Row label: `1080p  mp4  ~142 MB`.
      Size = `Filesize` → else `FilesizeApprox` → else estimate from `TBR × Duration`
      → else `~?`. Write a `formatBytes` helper.
- [ ] A merged row's size must account for the audio track, not just the video
      stream. Approximate is fine; silently understating it is not.
- [ ] One audio row appended: `audio only  mp3`.
- [ ] Args carried per row:
      - video: `-f "bv[height<=N]+ba/b[height<=N]"`
      - audio: `-x --audio-format mp3`

- [ ] **`formats_test.go`** — table-driven, covering:
      empty format list · no video formats · duplicate heights collapsing to one
      row · the 7-row cap · `formatBytes` boundaries.
      This is the one file where tests earn their keep.

## 5. internal/ytdlp/download.go

- [ ] Args: `--load-info-json <path>`, `--no-playlist`,
      `-o "<downloads>/%(title)s.%(ext)s"`, the row's format args, `--newline`, and

      ```
      --progress-template "download:%(progress.downloaded_bytes)s/%(progress.total_bytes)s/%(progress.total_bytes_estimate)s/%(progress.speed)s"
      ```

- [ ] Parse progress by splitting on `/`. Do NOT regex yt-dlp's human-readable
      output — the template exists so the format is ours, not theirs.
- [ ] Fields can be the literal `NA`. Treat as unknown, do not fail the parse.
- [ ] Emit progress on a channel the UI drains.
- [ ] Output dir: `~/Downloads`.
- [ ] Capture the final saved path from `[download] Destination: ` and
      `[Merger] Merging formats into "..."`; prefer the merger path when present.
- [ ] On cancel: kill the process, remove `.part` / `.ytdl` leftovers.

## 6. internal/ui

States: `input` → `probing` → `picker` → `downloading` → `done`,
plus `error` reachable from any of them.

- [ ] `input` — `textinput` in a framed box, app name above. Enter submits;
      empty or obviously-not-a-URL shows an inline hint instead of advancing.
- [ ] `probing` — spinner + status line ("fetching video info…",
      "first run: fetching yt-dlp…" while the binary downloads).
- [ ] `picker` — title and uploader on top, then the format rows.
      `↑`/`↓` and `j`/`k` move · `1`-`9` jump · enter selects · `esc` back to input.
- [ ] `downloading` — title, chosen format label, progress bar,
      downloaded/total and speed. `esc` cancels.
- [ ] `done` — saved file path; enter starts another, `q` quits.
- [ ] `error` — the message; `esc`/enter back to input.
- [ ] Global `ctrl+c` quits. Run with `tea.WithAltScreen()`.
- [ ] All `exec` work goes through `tea.Cmd`, never inside `View` or inline in `Update`.

Not in this pass: theme system, mouse support, clipboard detection.
Plain lipgloss styles readable on both light and dark terminals; prefer the
terminal's default foreground over hardcoded greys.

## 7. CLI surface

- [ ] `yank <url>` — straight to probing
- [ ] `yank` — start on the input screen
- [ ] `yank --help` / `-h`
- [ ] `yank --version` — `var Version = "0.1.0"`, settable via ldflags later

## 8. Done means

- [ ] `go build ./...` clean
- [ ] `go vet ./...` clean
- [ ] `go test ./...` passing
- [ ] `gofmt -l .` reports nothing
- [ ] `--help` and `--version` print sensible text
- [ ] `README.md` — install, usage, and a "how it works" section crediting
      yt-dlp with a link
- [ ] One real end-to-end download run by hand, saved file played back

## Later (not now)

- `--best` / `--mp3` flags to skip the picker (scriptable mode)
- `-o <dir>` for the output folder
- Theme system (auto / light / dark)
- Mouse support — `tea.WithMouseCellMotion()` makes this cheap
- Clipboard detection: launch bare, suggest the URL already copied
- Playlist support
- Self-update for the cached yt-dlp binary

## Notes

- Bubble Tea gives us `tea.WithAltScreen()` and `tea.WithMouseCellMotion()` for
  free. The Ink version had to hand-roll both.
- Fair use: this is a personal-archiving tool. Say so in the README.
