// Package store is the file-backed side of the app: where things live on disk
// and how they are read and written. Nothing here decides anything — the rules
// live in the domain packages — so every reader degrades to "no data" rather
// than guessing.
package store

import (
	"os"
	"path/filepath"

	"github.com/hwayoungjun/claude-usage-bar/internal/app"
)

// ConfigDir is where this app keeps its own state.
func ConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", app.Name)
}

// UsagePath is the reading captured from Claude Code's statusLine hook.
func UsagePath() string { return filepath.Join(ConfigDir(), "usage.json") }

// SettingsPath is this app's own preferences.
func SettingsPath() string { return filepath.Join(ConfigDir(), "settings.json") }

// LockPath backs the single-instance lock.
func LockPath() string { return filepath.Join(ConfigDir(), "lock") }

// ClaudeSettingsPath is Claude Code's settings file, which holds the statusLine
// hook this app installs.
func ClaudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}
