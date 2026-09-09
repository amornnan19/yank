package main

import (
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

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
// logging via tea.LogToFile, where <cache> is $XDG_CACHE_HOME if set and
// non-empty, otherwise ~/.cache.
func setupDebugLog() (*os.File, error) {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		cacheDir = filepath.Join(home, ".cache")
	}

	logDir := filepath.Join(cacheDir, "yank")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}

	return tea.LogToFile(filepath.Join(logDir, "yank.log"), "debug")
}
