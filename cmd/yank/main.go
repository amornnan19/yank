package main

import (
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"

	// bubbles and lipgloss are not used yet; the next slice builds the TUI
	// on top of them. Blank-imported here so go.mod/go.sum already pin them.
	_ "github.com/charmbracelet/bubbles"
	_ "github.com/charmbracelet/lipgloss"
)

// Version is the current yank version. Set via -ldflags at build time.
var Version = "0.1.0"

func main() {
	if os.Getenv("YANK_DEBUG") == "1" {
		if f, err := setupDebugLog(); err == nil {
			defer f.Close()
		}
	}

	// TODO: next slice wires up the TUI (tea.NewProgram) and CLI entry flow.
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
