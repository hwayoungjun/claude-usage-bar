package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The uninstaller reports each step independently, and only removes the status
// line it installed itself.
func TestUninstallerRemovesWhatWeInstalled(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	os.WriteFile(settingsPath, []byte(`{"model":"opus",
		"statusLine":{"type":"command","command":"/bin/claude-usage-bar statusline"}}`), 0644)
	configDir := filepath.Join(dir, "config")
	os.MkdirAll(configDir, 0755)

	var out strings.Builder
	Uninstaller{
		// No plist on disk, so nothing calls launchctl.
		Agent:     LaunchAgent{Label: "com.example.widget", Path: filepath.Join(dir, "absent.plist")},
		Settings:  ClaudeSettings{Path: settingsPath},
		ConfigDir: configDir,
		Marker:    "claude-usage-bar",
	}.Run(&out)

	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Error("config directory should be gone")
	}
	raw, _ := os.ReadFile(settingsPath)
	if strings.Contains(string(raw), "statusLine") {
		t.Errorf("statusLine should be gone: %s", raw)
	}
	if !strings.Contains(string(raw), "opus") {
		t.Error("unrelated settings should survive")
	}

	report := out.String()
	for _, want := range []string{"LaunchAgent not found", "Removed statusLine", "Removed " + configDir} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

// Nothing installed: every step says so and none of them fail.
func TestUninstallerOnACleanSystem(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	Uninstaller{
		Agent:     LaunchAgent{Label: "com.example.widget", Path: filepath.Join(dir, "absent.plist")},
		Settings:  ClaudeSettings{Path: filepath.Join(dir, "absent.json")},
		ConfigDir: filepath.Join(dir, "absent"),
		Marker:    "claude-usage-bar",
	}.Run(&out)

	if strings.Contains(out.String(), "✗") {
		t.Errorf("nothing should be reported as failed:\n%s", out.String())
	}
}
