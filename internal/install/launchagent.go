package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LaunchAgent is the launchd job behind Launch at Login.
type LaunchAgent struct {
	Label        string
	Path         string // our plist
	HomebrewPath string // the plist `brew services` installs, if any
	UID          int
}

// DefaultLaunchAgent points at the current user's LaunchAgents directory.
func DefaultLaunchAgent(label, homebrewLabel string) LaunchAgent {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Library", "LaunchAgents")
	return LaunchAgent{
		Label:        label,
		Path:         filepath.Join(dir, label+".plist"),
		HomebrewPath: filepath.Join(dir, homebrewLabel+".plist"),
		UID:          os.Getuid(),
	}
}

// Plist renders the job definition.
//
// KeepAlive restarts only after a crash: a clean exit means the user picked
// Quit, while a start that fails at login — the window server can still be
// coming up that early — gets another attempt instead of leaving the menu bar
// empty until the next reboot.
func Plist(label, binPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--foreground</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, label, binPath)
}

// IsInstalled reports whether our own plist exists.
func (a LaunchAgent) IsInstalled() bool {
	_, err := os.Stat(a.Path)
	return err == nil
}

// IsHomebrewManaged reports whether `brew services` owns launch-at-login. When
// it does, the in-app toggle stands aside rather than registering a second job
// for the same binary.
func (a LaunchAgent) IsHomebrewManaged() bool {
	_, err := os.Stat(a.HomebrewPath)
	return err == nil
}

// Install writes the plist and loads it, so the toggle takes effect without a
// reboot.
func (a LaunchAgent) Install(binPath string) error {
	if err := os.MkdirAll(filepath.Dir(a.Path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(a.Path, []byte(Plist(a.Label, binPath)), 0644); err != nil {
		return err
	}
	return a.Bootstrap()
}

// Remove unloads the job and deletes the plist. Unloading comes first;
// otherwise the file goes away while the running job lingers until reboot.
func (a LaunchAgent) Remove() error {
	if err := a.Bootout(); err != nil {
		return err
	}
	if err := os.Remove(a.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ProgramPath reads the binary path back out of the installed plist.
func (a LaunchAgent) ProgramPath() string {
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :ProgramArguments:0", a.Path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RefreshPlist brings an already-installed plist up to date with the keys this
// build writes, so plist fixes reach machines that turned Launch at Login on
// with an older version. Only the keys are refreshed — the recorded binary path
// is reused verbatim, because a path resolved under launchd (whose PATH has no
// Homebrew prefix) can be a version-specific Cellar path that would break at
// the next upgrade. Rewriting the file is enough: launchd re-reads it at the
// next login, and re-bootstrapping here would tear down the process doing the
// rewrite. It reports whether the file changed.
func (a LaunchAgent) RefreshPlist() (bool, error) {
	if a.IsHomebrewManaged() || !a.IsInstalled() {
		return false, nil
	}
	binPath := a.ProgramPath()
	if binPath == "" {
		return false, nil
	}
	want := Plist(a.Label, binPath)
	if current, err := os.ReadFile(a.Path); err == nil && string(current) == want {
		return false, nil
	}
	if err := os.WriteFile(a.Path, []byte(want), 0644); err != nil {
		return false, err
	}
	return true, nil
}

func (a LaunchAgent) domain() string { return fmt.Sprintf("gui/%d", a.UID) }

// Bootstrap loads the plist into the user's launchd domain. An already-loaded
// job counts as success.
func (a LaunchAgent) Bootstrap() error {
	out, err := exec.Command("launchctl", "bootstrap", a.domain(), a.Path).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := string(out)
	if strings.Contains(msg, "already") || strings.Contains(msg, "Bootstrap failed: 5") {
		return nil
	}
	return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(msg))
}

// Bootout unloads the plist. A job that was not loaded counts as success.
func (a LaunchAgent) Bootout() error {
	target := fmt.Sprintf("%s/%s", a.domain(), a.Label)
	out, err := exec.Command("launchctl", "bootout", target).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := string(out)
	if strings.Contains(msg, "No such process") || strings.Contains(msg, "Could not find") {
		return nil
	}
	return fmt.Errorf("launchctl bootout: %v: %s", err, strings.TrimSpace(msg))
}
