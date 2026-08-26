package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlistRetriesAfterAFailedStart(t *testing.T) {
	got := Plist("com.example.widget", "/usr/local/bin/widget")

	// KeepAlive has to be the dictionary form: plain true would fight the user's
	// Quit, and false gave up after one failed start at login.
	if !strings.Contains(got, "<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>") {
		t.Errorf("KeepAlive is not the SuccessfulExit form:\n%s", got)
	}
	for _, want := range []string{
		"<string>com.example.widget</string>",
		"<string>/usr/local/bin/widget</string>",
		"<string>--foreground</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>ThrottleInterval</key>",
		"<string>Interactive</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func readJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEnsureStatusLineCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	c := ClaudeSettings{Path: path}

	changed, err := c.EnsureStatusLine("/bin/widget statusline")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	sl := readJSON(t, path)["statusLine"].(map[string]interface{})
	if sl["command"] != "/bin/widget statusline" || sl["type"] != "command" {
		t.Errorf("statusLine = %+v", sl)
	}
}

// The file belongs to Claude Code: everything this app does not own has to come
// back out untouched.
func TestEnsureStatusLinePreservesOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"model":"opus","hooks":{"Stop":[{"x":1}]},
		"statusLine":{"type":"command","command":"/old/path statusline"}}`), 0644)

	c := ClaudeSettings{Path: path}
	if changed, err := c.EnsureStatusLine("/new/path statusline"); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}

	got := readJSON(t, path)
	if got["model"] != "opus" {
		t.Error("model setting lost")
	}
	if _, ok := got["hooks"]; !ok {
		t.Error("hooks lost")
	}
	if sl := got["statusLine"].(map[string]interface{}); sl["command"] != "/new/path statusline" {
		t.Errorf("statusLine not updated: %+v", sl)
	}
}

// Setup runs on every launch, so an already-correct file must not be rewritten.
func TestEnsureStatusLineIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	c := ClaudeSettings{Path: path}
	if _, err := c.EnsureStatusLine("/bin/widget statusline"); err != nil {
		t.Fatal(err)
	}
	changed, err := c.EnsureStatusLine("/bin/widget statusline")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second call should report no change")
	}
}

func TestEnsureStatusLineRefusesBrokenSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"model":`), 0644)

	// Overwriting a file we cannot parse would throw away the user's settings.
	if _, err := (ClaudeSettings{Path: path}).EnsureStatusLine("/bin/widget statusline"); err == nil {
		t.Error("want an error rather than a clobbered file")
	}
	if raw, _ := os.ReadFile(path); string(raw) != `{"model":` {
		t.Errorf("file was modified: %s", raw)
	}
}

func TestRemoveStatusLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"model":"opus",
		"statusLine":{"type":"command","command":"/bin/claude-usage-bar statusline"}}`), 0644)

	removed, err := (ClaudeSettings{Path: path}).RemoveStatusLine("claude-usage-bar")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	got := readJSON(t, path)
	if _, ok := got["statusLine"]; ok {
		t.Error("statusLine should be gone")
	}
	if got["model"] != "opus" {
		t.Error("other settings should survive")
	}
}

// Uninstalling must not take away a status line the user set up for something
// else.
func TestRemoveStatusLineLeavesForeignCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := `{"statusLine":{"type":"command","command":"/usr/local/bin/other-tool"}}`
	os.WriteFile(path, []byte(body), 0644)

	removed, err := (ClaudeSettings{Path: path}).RemoveStatusLine("claude-usage-bar")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("a foreign status line should be left alone")
	}
	if _, ok := readJSON(t, path)["statusLine"]; !ok {
		t.Error("foreign status line was removed")
	}
}

func TestRemoveStatusLineWhenAbsent(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "no-statusline.json")
	os.WriteFile(path, []byte(`{"model":"opus"}`), 0644)
	if removed, err := (ClaudeSettings{Path: path}).RemoveStatusLine("x"); err != nil || removed {
		t.Errorf("removed=%v err=%v", removed, err)
	}

	if _, err := (ClaudeSettings{Path: filepath.Join(dir, "absent.json")}).RemoveStatusLine("x"); err == nil {
		t.Error("a missing settings file should be reported")
	}
}

func TestLaunchAgentDetection(t *testing.T) {
	dir := t.TempDir()
	a := LaunchAgent{
		Label:        "com.example.widget",
		Path:         filepath.Join(dir, "ours.plist"),
		HomebrewPath: filepath.Join(dir, "brew.plist"),
	}

	if a.IsInstalled() || a.IsHomebrewManaged() {
		t.Fatal("nothing should be detected in an empty directory")
	}
	os.WriteFile(a.Path, []byte(Plist(a.Label, "/bin/widget")), 0644)
	if !a.IsInstalled() {
		t.Error("our plist should be detected")
	}
	os.WriteFile(a.HomebrewPath, []byte("x"), 0644)
	if !a.IsHomebrewManaged() {
		t.Error("the Homebrew plist should be detected")
	}
}

// A plist written by an older version is refreshed in place, and the recorded
// binary path is reused rather than re-resolved.
func TestRefreshPlistUpdatesKeysAndKeepsThePath(t *testing.T) {
	dir := t.TempDir()
	a := LaunchAgent{
		Label:        "com.example.widget",
		Path:         filepath.Join(dir, "ours.plist"),
		HomebrewPath: filepath.Join(dir, "brew.plist"),
	}
	const recorded = "/Users/me/.local/bin/claude-usage-bar"
	old := strings.Replace(Plist(a.Label, recorded),
		"<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>",
		"<key>KeepAlive</key>\n\t<false/>", 1)
	if old == Plist(a.Label, recorded) {
		t.Fatal("test setup failed to produce an outdated plist")
	}
	os.WriteFile(a.Path, []byte(old), 0644)

	changed, err := a.RefreshPlist()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("an outdated plist should be rewritten")
	}
	got, _ := os.ReadFile(a.Path)
	if string(got) != Plist(a.Label, recorded) {
		t.Errorf("refreshed plist does not match this build:\n%s", got)
	}
	if !strings.Contains(string(got), recorded) {
		t.Error("the recorded binary path must be reused verbatim")
	}

	if changed, err := a.RefreshPlist(); err != nil || changed {
		t.Errorf("an up-to-date plist should be left alone: changed=%v err=%v", changed, err)
	}
}

func TestRefreshPlistStandsAside(t *testing.T) {
	dir := t.TempDir()
	a := LaunchAgent{
		Label:        "com.example.widget",
		Path:         filepath.Join(dir, "ours.plist"),
		HomebrewPath: filepath.Join(dir, "brew.plist"),
	}

	// Nothing installed: nothing to refresh.
	if changed, err := a.RefreshPlist(); err != nil || changed {
		t.Errorf("changed=%v err=%v", changed, err)
	}

	// Homebrew owns launch-at-login: leave its job alone.
	os.WriteFile(a.Path, []byte("outdated"), 0644)
	os.WriteFile(a.HomebrewPath, []byte("x"), 0644)
	if changed, err := a.RefreshPlist(); err != nil || changed {
		t.Errorf("homebrew-managed: changed=%v err=%v", changed, err)
	}

	// An unparseable plist has no path to reuse, so it is left as it is.
	os.Remove(a.HomebrewPath)
	if changed, err := a.RefreshPlist(); err != nil || changed {
		t.Errorf("unreadable plist: changed=%v err=%v", changed, err)
	}
}
