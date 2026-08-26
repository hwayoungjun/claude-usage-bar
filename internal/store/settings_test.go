package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hwayoungjun/claude-usage-bar/internal/usage"
)

func TestSettingsFileDefaults(t *testing.T) {
	f := SettingsFile{Path: filepath.Join(t.TempDir(), "settings.json")}
	if got := f.Load().DisplayMode; got != usage.DisplayFull {
		t.Errorf("missing file should default to full, got %q", got)
	}
}

func TestSettingsFileRoundTrip(t *testing.T) {
	f := SettingsFile{Path: filepath.Join(t.TempDir(), "nested", "settings.json")}
	if err := f.Save(Settings{DisplayMode: usage.DisplayShort}); err != nil {
		t.Fatal(err)
	}
	if got := f.Load().DisplayMode; got != usage.DisplayShort {
		t.Errorf("DisplayMode = %q, want short", got)
	}
}

// A hand-edited or future value must fall back rather than render an empty
// menu bar title.
func TestSettingsFileRejectsUnknownValues(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"bogus mode": `{"display_mode":"enormous"}`,
		"empty mode": `{"display_mode":""}`,
		"broken":     `{not json`,
		"empty file": ``,
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if got := (SettingsFile{Path: path}).Load().DisplayMode; got != usage.DisplayFull {
			t.Errorf("%s: DisplayMode = %q, want the default", name, got)
		}
	}
}
