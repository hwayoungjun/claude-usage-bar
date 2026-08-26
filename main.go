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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hwayoungjun/claude-usage-bar/internal/app"
	"github.com/hwayoungjun/claude-usage-bar/internal/install"
	"github.com/hwayoungjun/claude-usage-bar/internal/session"
	"github.com/hwayoungjun/claude-usage-bar/internal/shellquote"
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
		case "restart":
			runRestart()
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

// restartStopTimeout bounds how long a restart waits for the outgoing instance
// to let go of the single-instance lock.
const restartStopTimeout = 10 * time.Second

// runRestart stops whatever is running and starts it again. Doing this by hand
// has three traps — a pkill pattern that misses the backgrounded instance
// because only launchd passes --foreground, the lock the outgoing process holds
// for a moment after being signalled, and a launchd-managed job that has to be
// reloaded rather than killed — so it lives here instead of in a README.
func runRestart() {
	agent := defaultLaunchAgent()
	mode := agent.RestartMode()

	if mode == install.RestartViaHomebrew {
		fmt.Println("Launch at Login is managed by Homebrew; restart it there:")
		fmt.Printf("  brew services restart %s\n", app.Name)
		return
	}

	// Unloading first stops the job launchd is running, and is what makes it
	// re-read a plist this build has since changed.
	if mode == install.RestartViaLaunchd {
		if err := agent.Bootout(); err != nil {
			fmt.Fprintln(os.Stderr, "restart:", err)
			os.Exit(1)
		}
	}

	// An instance started by hand is not launchd\'s to stop.
	stopRunningInstances()

	if !store.DefaultLock().WaitUntilFree(restartStopTimeout) {
		fmt.Fprintln(os.Stderr, "restart: an instance is still holding the lock; nothing was started")
		os.Exit(1)
	}

	if mode == install.RestartViaLaunchd {
		if err := agent.Bootstrap(); err != nil {
			fmt.Fprintln(os.Stderr, "restart:", err)
			os.Exit(1)
		}
		fmt.Println("Restarted via launchd.")
		return
	}
	daemonize()
}

// stopRunningInstances signals every other copy of this binary. The lock
// records no pid, so instances have to be found by name — and this process
// answers to that name too, so it is skipped.
func stopRunningInstances() {
	out, err := exec.Command("pgrep", "-x", app.Name).Output()
	if err != nil {
		// pgrep exits non-zero when nothing matches.
		return
	}
	self := os.Getpid()
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid == self {
			continue
		}
		syscall.Kill(pid, syscall.SIGTERM)
	}
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

// statusLineCommand is what gets written into Claude Code's settings. Claude
// Code runs it through a shell, so the install path is quoted: a directory name
// with a space in it would otherwise produce a broken hook, and a stranger
// character would produce a hook that does more than it says.
func statusLineCommand() string {
	binPath, err := exec.LookPath(os.Args[0])
	if err != nil {
		binPath = os.Args[0]
	}
	binPath, _ = filepath.Abs(binPath)
	return shellquote.Quote(binPath) + " statusline"
}

// stableBinPath prefers the PATH entry (for example
// /opt/homebrew/bin/claude-usage-bar), which is a symlink that survives brew
// upgrades. os.Executable resolves symlinks on macOS and would hand back the
// version-specific Cellar path, which breaks at the next upgrade.
//
// The PATH entry is only trusted when it is this very binary. This path is
// written into the LaunchAgent plist and re-executed when backgrounding, so a
// writable directory earlier in PATH would otherwise be a way to get a
// different binary launched at every login. Following the symlink and comparing
// files accepts the legitimate case — the Homebrew symlink and its Cellar
// target are one file — and rejects a planted impostor.
func stableBinPath() string {
	self, err := os.Executable()
	if err == nil {
		self, _ = filepath.Abs(self)
	}
	if p, lookErr := exec.LookPath(app.Name); lookErr == nil {
		if abs, absErr := filepath.Abs(p); absErr == nil && sameFile(abs, self) {
			return abs
		}
	}
	return self
}

// sameFile reports whether two paths resolve to one file on disk.
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	infoA, err := os.Stat(a)
	if err != nil {
		return false
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
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
  %s restart      Stop the running widget and start it again
  %s --version    Print the build and the path of this binary
`, app.Name, app.Name, app.Name, app.Name, app.Name, app.Name, app.Name, app.Name)
}
