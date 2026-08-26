// Command claude-usage-bar shows Claude Code rate limit usage in the macOS
// menu bar.
//
// This file is the composition root: it dispatches subcommands and wires the
// adapters (internal/store, internal/install, internal/ui) to the domain
// packages (internal/usage, internal/session, internal/textwidth). Behaviour
// belongs in those packages, not here.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/hwayoungjun/claude-usage-bar/internal/app"
	"github.com/hwayoungjun/claude-usage-bar/internal/install"
	"github.com/hwayoungjun/claude-usage-bar/internal/session"
	"github.com/hwayoungjun/claude-usage-bar/internal/statusline"
	"github.com/hwayoungjun/claude-usage-bar/internal/store"
	"github.com/hwayoungjun/claude-usage-bar/internal/ui"
)

// envDaemon marks the backgrounded child, so it starts the widget instead of
// forking again.
const envDaemon = "CLAUDE_USAGE_BAR_DAEMON"

// version is stamped at build time (see the Makefile\'s -X flag). A plain
// `go build` leaves it empty and the revision Go embeds from git is used
// instead, so a binary always knows what it is.
var version string

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "statusline":
			runStatusLine()
			return
		case "setup":
			runSetup()
			return
		case "uninstall":
			runUninstall()
			return
		case "--foreground":
			startWidget()
			return
		case "-h", "--help", "help":
			printHelp()
			return
		case "-v", "--version", "version":
			printVersion()
			return
		}
	}

	if os.Getenv(envDaemon) == "1" {
		startWidget()
		return
	}

	if store.DefaultLock().HeldElsewhere() {
		fmt.Println(app.Name, "is already running.")
		return
	}

	daemonize()
}

// daemonize re-executes this binary in the background and returns, so launching
// from a shell does not tie up the terminal.
func daemonize() {
	cmd := exec.Command(stableBinPath())
	cmd.Env = append(os.Environ(), envDaemon+"=1")
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to start in background:", err)
		os.Exit(1)
	}
	fmt.Println(app.Name, "started (pid", cmd.Process.Pid, ")")
}

func startWidget() {
	if !store.DefaultLock().Acquire() {
		fmt.Println(app.Name, "is already running.")
		return
	}
	ensureSetup()

	widget := &ui.Widget{
		Usage:    store.DefaultUsageFile(),
		Desktop:  store.DefaultDesktopHistory(),
		Settings: store.DefaultSettingsFile(),
		Sessions: session.DefaultTranscripts(),
		Agent:    defaultLaunchAgent(),
		BinPath:  stableBinPath(),
		Now:      time.Now,
	}
	widget.Run()
}

// runStatusLine is the hook Claude Code invokes on every assistant message. It
// captures the reading and prints nothing: the terminal row belongs to Claude
// Code.
func runStatusLine() {
	data, err := statusline.Parse(os.Stdin, time.Now())
	if err != nil {
		return
	}
	if err := store.DefaultUsageFile().Save(data); err != nil {
		fmt.Fprintln(os.Stderr, "statusline: write failed:", err)
	}
}

// ensureSetup runs on every launch so a fresh install, or an older install
// whose plist predates a fix, converges without the user doing anything.
func ensureSetup() {
	if _, err := claudeSettings().EnsureStatusLine(statusLineCommand()); err != nil {
		fmt.Fprintln(os.Stderr, "Auto-setup failed:", err)
	}
	if _, err := defaultLaunchAgent().RefreshPlist(); err != nil {
		fmt.Fprintln(os.Stderr, "Launch at Login: plist refresh failed:", err)
	}
}

func runSetup() {
	if _, err := claudeSettings().EnsureStatusLine(statusLineCommand()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Please add the following to", store.ClaudeSettingsPath(), "manually:")
		fmt.Fprintf(os.Stderr, "  \"statusLine\": { \"type\": \"command\", \"command\": \"%s\" }\n", statusLineCommand())
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "If you see 'operation not permitted', check macOS Privacy & Security > Full Disk Access.")
		os.Exit(1)
	}
	fmt.Println("✓ Configured statusLine in", store.ClaudeSettingsPath())
}

func runUninstall() {
	fmt.Println("Uninstalling", app.Name+"...")
	install.Uninstaller{
		Agent:     defaultLaunchAgent(),
		Settings:  claudeSettings(),
		ConfigDir: store.ConfigDir(),
		Marker:    app.Name,
	}.Run(os.Stdout)
	fmt.Println("Done.")
}

func claudeSettings() install.ClaudeSettings {
	return install.ClaudeSettings{Path: store.ClaudeSettingsPath()}
}

func defaultLaunchAgent() install.LaunchAgent {
	return install.DefaultLaunchAgent(app.LaunchAgentLabel, app.HomebrewLaunchAgentLabel)
}

// statusLineCommand is what gets written into Claude Code's settings.
func statusLineCommand() string {
	binPath, err := exec.LookPath(os.Args[0])
	if err != nil {
		binPath = os.Args[0]
	}
	binPath, _ = filepath.Abs(binPath)
	return binPath + " statusline"
}

// stableBinPath prefers the PATH entry (for example
// /opt/homebrew/bin/claude-usage-bar), which is a symlink that survives brew
// upgrades. os.Executable resolves symlinks on macOS and would hand back the
// version-specific Cellar path, which breaks at the next upgrade.
func stableBinPath() string {
	if p, err := exec.LookPath(app.Name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	binPath, _ := os.Executable()
	binPath, _ = filepath.Abs(binPath)
	return binPath
}

// printVersion reports the build and the binary behind it. The path matters as
// much as the version: with an install in ~/.local/bin and another under a
// Homebrew prefix, "which one is running" is the actual question.
func printVersion() {
	fmt.Printf("%s %s\n", app.Name, buildVersion())
	if exe, err := os.Executable(); err == nil {
		fmt.Println(exe)
	}
}

func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := revisionFrom(info.Settings); v != "" {
		return v
	}
	return "unknown"
}

// revisionFrom renders the git revision Go stamps into a binary built from a
// checkout, marking a working tree that had uncommitted changes.
func revisionFrom(settings []debug.BuildSetting) string {
	var revision, suffix string
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
			if len(revision) > 12 {
				revision = revision[:12]
			}
		case "vcs.modified":
			if s.Value == "true" {
				suffix = "-dirty"
			}
		}
	}
	if revision == "" {
		return ""
	}
	return revision + suffix
}

func printHelp() {
	fmt.Printf(`%s — Claude Code usage monitor for macOS menu bar

Usage:
  %s              Launch the menu bar widget (backgrounds automatically)
  %s --foreground Launch in foreground (for debugging)
  %s statusline   StatusLine handler (used by Claude Code)
  %s setup        Auto-configure ~/.claude/settings.json
  %s uninstall    Remove all config, LaunchAgent, and statusLine settings
  %s --version    Print the build and the path of this binary
`, app.Name, app.Name, app.Name, app.Name, app.Name, app.Name, app.Name)
}
