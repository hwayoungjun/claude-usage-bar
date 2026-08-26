package store

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/hwayoungjun/claude-usage-bar/internal/usage"
)

// Settings is this app's own preferences, as stored on disk.
type Settings struct {
	DisplayMode usage.DisplayMode `json:"display_mode"`
}

// SettingsFile persists Settings.
type SettingsFile struct {
	Path string
}

// DefaultSettingsFile points at the path under the user's config directory.
func DefaultSettingsFile() SettingsFile { return SettingsFile{Path: SettingsPath()} }

// Load returns the stored preferences, falling back to the defaults for a
// missing, unparseable, or unrecognised value.
func (f SettingsFile) Load() Settings {
	s := Settings{DisplayMode: usage.DisplayFull}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	if !s.DisplayMode.Valid() {
		s.DisplayMode = usage.DisplayFull
	}
	return s
}

// Save writes the preferences back.
func (f SettingsFile) Save(s Settings) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.Path, out, 0644)
}
