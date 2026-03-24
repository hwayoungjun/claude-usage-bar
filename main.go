package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/getlantern/systray"
)

const appName = "claude-usage-bar"

// ── Data types ──

type UsageData struct {
	UpdatedAt int64    `json:"updated_at"`
	FiveHour  RateInfo `json:"five_hour"`
	SevenDay  RateInfo `json:"seven_day"`
	Model     string   `json:"model"`
	SessionID string   `json:"session_id"`
}

type RateInfo struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"`
}

type StatusLineInput struct {
	RateLimits *struct {
		FiveHour *RateInfo `json:"five_hour"`
		SevenDay *RateInfo `json:"seven_day"`
	} `json:"rate_limits"`
	Model *struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	SessionID string `json:"session_id"`
}

type HistoryEntry struct {
	Display   string `json:"display"`
	Timestamp int64  `json:"timestamp"`
	Project   string `json:"project"`
	SessionID string `json:"sessionId"`
}

type RecentSession struct {
	SessionID    string
	Project      string
	FirstDisplay string
	LastActive   int64
}

// ── Paths ──

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName)
}

func usageFilePath() string {
	return filepath.Join(configDir(), "usage.json")
}

// ── PID file ──

func pidFilePath() string {
	return filepath.Join(configDir(), "pid")
}

func isAlreadyRunning() bool {
	raw, err := os.ReadFile(pidFilePath())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if the process exists without killing it
	return proc.Signal(syscall.Signal(0)) == nil
}

func writePidFile() {
	os.MkdirAll(configDir(), 0755)
	os.WriteFile(pidFilePath(), []byte(strconv.Itoa(os.Getpid())), 0644)
}

func removePidFile() {
	os.Remove(pidFilePath())
}

// ── Entry point ──

const envDaemon = "CLAUDE_USAGE_BAR_DAEMON"

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
		}
	}

	// Already running as daemon child — start the widget
	if os.Getenv(envDaemon) == "1" {
		startWidget()
		return
	}

	// Check if already running
	if isAlreadyRunning() {
		fmt.Println("claude-usage-bar is already running.")
		return
	}

	// Fork to background and exit the parent
	bin := stableBinPath()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), envDaemon+"=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to start in background:", err)
		os.Exit(1)
	}
	fmt.Println("claude-usage-bar started (pid", cmd.Process.Pid, ")")
}

func startWidget() {
	if isAlreadyRunning() {
		fmt.Println("claude-usage-bar is already running.")
		return
	}
	writePidFile()
	ensureSetup()
	systray.Run(onReady, onExit)
}

func printHelp() {
	fmt.Printf(`%s — Claude Code usage monitor for macOS menu bar

Usage:
  %s              Launch the menu bar widget (backgrounds automatically)
  %s --foreground Launch in foreground (for debugging)
  %s statusline   StatusLine handler (used by Claude Code)
  %s setup        Auto-configure ~/.claude/settings.json
  %s uninstall    Remove all config, LaunchAgent, and statusLine settings
`, appName, appName, appName, appName, appName, appName)
}

// ── StatusLine subcommand ──

func runStatusLine() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Println("")
		return
	}

	var sl StatusLineInput
	if err := json.Unmarshal(input, &sl); err != nil {
		fmt.Println("")
		return
	}

	data := UsageData{
		UpdatedAt: time.Now().Unix(),
		SessionID: sl.SessionID,
	}

	if sl.Model != nil {
		data.Model = sl.Model.DisplayName
	}
	if sl.RateLimits != nil {
		if sl.RateLimits.FiveHour != nil {
			data.FiveHour = *sl.RateLimits.FiveHour
		}
		if sl.RateLimits.SevenDay != nil {
			data.SevenDay = *sl.RateLimits.SevenDay
		}
	}

	dir := configDir()
	os.MkdirAll(dir, 0755)

	out, _ := json.Marshal(data)
	os.WriteFile(usageFilePath(), out, 0644)

	fmt.Println("")
}

// ── Setup ──

// setupStatusLine configures ~/.claude/settings.json and returns any error.
func setupStatusLine() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Resolve full path to the binary
	binPath, err := exec.LookPath(os.Args[0])
	if err != nil {
		binPath = os.Args[0]
	}
	binPath, _ = filepath.Abs(binPath)
	command := binPath + " statusline"

	// Read existing settings
	var settings map[string]interface{}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		settings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("error parsing %s: %v", settingsPath, err)
		}
	}

	// Check if already configured
	if sl, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := sl["command"].(string); ok && cmd == command {
			return nil
		}
	}

	// Set statusLine
	settings["statusLine"] = map[string]string{
		"type":    "command",
		"command": command,
	}

	// Write back
	dir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("error creating directory %s: %v", dir, err)
	}
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, out, 0644); err != nil {
		return fmt.Errorf("error writing %s: %v", settingsPath, err)
	}

	return nil
}

// ensureSetup runs setup silently on every app launch.
func ensureSetup() {
	if err := setupStatusLine(); err != nil {
		fmt.Fprintln(os.Stderr, "Auto-setup failed:", err)
	}
}

// runSetup is the CLI entrypoint for `claude-usage-bar setup`.
func runSetup() {
	if err := setupStatusLine(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		home, _ := os.UserHomeDir()
		settingsPath := filepath.Join(home, ".claude", "settings.json")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Please add the following to", settingsPath, "manually:")
		fmt.Fprintf(os.Stderr, "  \"statusLine\": { \"type\": \"command\", \"command\": \"%s statusline\" }\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "If you see 'operation not permitted', check macOS Privacy & Security > Full Disk Access.")
		os.Exit(1)
	}
	home, _ := os.UserHomeDir()
	fmt.Println("✓ Configured statusLine in", filepath.Join(home, ".claude", "settings.json"))
}

// ── Uninstall subcommand ──

func runUninstall() {
	fmt.Println("Uninstalling", appName+"...")

	// 1. Remove LaunchAgent
	if isLaunchAgentInstalled() {
		// Unload the agent first
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), launchAgentPath()).Run()
		if err := removeLaunchAgent(); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Failed to remove LaunchAgent: %v\n", err)
		} else {
			fmt.Println("  ✓ Removed LaunchAgent")
		}
	} else {
		fmt.Println("  - LaunchAgent not found (skipped)")
	}

	// 2. Remove statusLine from ~/.claude/settings.json
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if raw, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if err := json.Unmarshal(raw, &settings); err == nil {
			if sl, ok := settings["statusLine"].(map[string]interface{}); ok {
				if cmd, ok := sl["command"].(string); ok && strings.Contains(cmd, appName) {
					delete(settings, "statusLine")
					out, _ := json.MarshalIndent(settings, "", "  ")
					if err := os.WriteFile(settingsPath, out, 0644); err != nil {
						fmt.Fprintf(os.Stderr, "  ✗ Failed to update %s: %v\n", settingsPath, err)
					} else {
						fmt.Println("  ✓ Removed statusLine from", settingsPath)
					}
				}
			}
		}
	} else {
		fmt.Println("  - Settings file not found (skipped)")
	}

	// 3. Remove config directory
	cfgDir := configDir()
	if _, err := os.Stat(cfgDir); err == nil {
		if err := os.RemoveAll(cfgDir); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Failed to remove %s: %v\n", cfgDir, err)
		} else {
			fmt.Println("  ✓ Removed", cfgDir)
		}
	} else {
		fmt.Println("  - Config directory not found (skipped)")
	}

	fmt.Println("Done.")
}

// ── Recent sessions ──

func historyFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "history.jsonl")
}

func loadRecentSessions(limit int) []RecentSession {
	f, err := os.Open(historyFilePath())
	if err != nil {
		return nil
	}
	defer f.Close()

	// Track per-session: first display and last timestamp
	type sessionAcc struct {
		project      string
		firstDisplay string
		firstTS      int64
		lastTS       int64
	}
	sessions := make(map[string]*sessionAcc)
	var order []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var e HistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.SessionID == "" || e.Display == "" {
			continue
		}
		// Skip slash commands and short noise
		if strings.HasPrefix(e.Display, "/") || e.Display == "exit" {
			// Still update lastTS
			if s, ok := sessions[e.SessionID]; ok {
				if e.Timestamp > s.lastTS {
					s.lastTS = e.Timestamp
				}
			}
			continue
		}

		if s, ok := sessions[e.SessionID]; ok {
			if e.Timestamp > s.lastTS {
				s.lastTS = e.Timestamp
			}
		} else {
			sessions[e.SessionID] = &sessionAcc{
				project:      e.Project,
				firstDisplay: e.Display,
				firstTS:      e.Timestamp,
				lastTS:       e.Timestamp,
			}
			order = append(order, e.SessionID)
		}
	}

	// Sort by lastTS descending (most recent first)
	// Simple selection sort for small N
	for i := 0; i < len(order); i++ {
		maxIdx := i
		for j := i + 1; j < len(order); j++ {
			if sessions[order[j]].lastTS > sessions[order[maxIdx]].lastTS {
				maxIdx = j
			}
		}
		order[i], order[maxIdx] = order[maxIdx], order[i]
	}

	var result []RecentSession
	for _, sid := range order {
		if len(result) >= limit {
			break
		}
		s := sessions[sid]
		result = append(result, RecentSession{
			SessionID:    sid,
			Project:      s.project,
			FirstDisplay: s.firstDisplay,
			LastActive:   s.lastTS,
		})
	}
	return result
}

func projectName(fullPath string) string {
	return filepath.Base(fullPath)
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

func copyResumeCommand(sessionID, project string) {
	cmd := fmt.Sprintf("cd %s && claude --resume %s", project, sessionID)
	p := exec.Command("pbcopy")
	p.Stdin = strings.NewReader(cmd)
	p.Run()
}

// ── Menu bar widget ──

var (
	m5hLabel *systray.MenuItem
	m5hBar   *systray.MenuItem
	m5hReset *systray.MenuItem

	m7dLabel *systray.MenuItem
	m7dBar   *systray.MenuItem
	m7dReset *systray.MenuItem

	mStatus *systray.MenuItem

	mSessionItems []*systray.MenuItem
)

const (
	barWidth        = 20
	maxSessions     = 5
	sessionLabelLen = 20
)

func onReady() {
	systray.SetTitle("[ 5h --  ·  7d -- ]")
	systray.SetTooltip("Claude Usage Bar")

	m5hLabel = systray.AddMenuItem("", "")
	m5hBar = systray.AddMenuItem("", "")
	m5hReset = systray.AddMenuItem("", "")

	systray.AddSeparator()

	m7dLabel = systray.AddMenuItem("", "")
	m7dBar = systray.AddMenuItem("", "")
	m7dReset = systray.AddMenuItem("", "")

	systray.AddSeparator()

	mStatus = systray.AddMenuItem("", "")

	systray.AddSeparator()

	// Recent sessions
	systray.AddMenuItem("Recent Sessions", "").Disable()
	for i := 0; i < maxSessions; i++ {
		item := systray.AddMenuItem("", "")
		item.Hide()
		mSessionItems = append(mSessionItems, item)
	}

	systray.AddSeparator()

	mLaunch := systray.AddMenuItem("Launch at Login", "Toggle launch at login")
	if isLaunchAgentInstalled() {
		mLaunch.Check()
	}

	mQuit := systray.AddMenuItem("Quit", "")

	setInactive()
	refreshUI()
	refreshSessions()

	go watchFile()
	go periodicRefresh()

	go func() {
		for {
			select {
			case <-mLaunch.ClickedCh:
				if isLaunchAgentInstalled() {
					removeLaunchAgent()
					mLaunch.Uncheck()
				} else {
					installLaunchAgent()
					mLaunch.Check()
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

var currentSessions []RecentSession

func refreshSessions() {
	currentSessions = loadRecentSessions(maxSessions)
	for i := 0; i < maxSessions; i++ {
		if i < len(currentSessions) {
			s := currentSessions[i]
			proj := projectName(s.Project)
			label := fmt.Sprintf("[%s] %s", proj, truncate(s.FirstDisplay, sessionLabelLen))
			mSessionItems[i].SetTitle(label)
			mSessionItems[i].SetTooltip(s.FirstDisplay)
			mSessionItems[i].Show()
			// Start click handler for this item
			go handleSessionClick(i)
		} else {
			mSessionItems[i].Hide()
		}
	}
}

func handleSessionClick(idx int) {
	<-mSessionItems[idx].ClickedCh
	if idx < len(currentSessions) {
		s := currentSessions[idx]
		copyResumeCommand(s.SessionID, s.Project)
	}
	go handleSessionClick(idx)
}

func onExit() {
	removePidFile()
}

func watchFile() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer watcher.Close()

	dir := configDir()
	os.MkdirAll(dir, 0755)
	watcher.Add(dir)

	for {
		select {
		case ev := <-watcher.Events:
			if ev.Name == usageFilePath() && (ev.Op&(fsnotify.Write|fsnotify.Create)) != 0 {
				time.Sleep(50 * time.Millisecond)
				refreshUI()
			}
		case <-watcher.Errors:
		}
	}
}

func periodicRefresh() {
	for {
		time.Sleep(30 * time.Second)
		refreshUI()
		refreshSessions()
	}
}

func refreshUI() {
	d, err := loadUsage()
	if err != nil {
		setInactive()
		return
	}

	staleness := time.Since(time.Unix(d.UpdatedAt, 0))
	if staleness > 10*time.Minute {
		setStale(d, staleness)
		return
	}

	setActive(d)
}

func setActive(d *UsageData) {
	s := pct(d.FiveHour.UsedPercentage)
	w := pct(d.SevenDay.UsedPercentage)

	systray.SetTitle(fmt.Sprintf("[ 5h %s  ·  7d %s ]", s, w))

	m5hLabel.SetTitle(fmt.Sprintf("5h Session                           %s used", s))
	m5hBar.SetTitle(bar(d.FiveHour.UsedPercentage))
	m5hReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.FiveHour.ResetsAt)))

	m7dLabel.SetTitle(fmt.Sprintf("7d All Models                        %s used", w))
	m7dBar.SetTitle(bar(d.SevenDay.UsedPercentage))
	m7dReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.SevenDay.ResetsAt)))

	ago := fmtAgo(time.Since(time.Unix(d.UpdatedAt, 0)))
	mStatus.SetTitle(fmt.Sprintf("%s · %s", d.Model, ago))
}

func setStale(d *UsageData, staleness time.Duration) {
	s := pct(d.FiveHour.UsedPercentage)
	w := pct(d.SevenDay.UsedPercentage)

	systray.SetTitle("[ ⏸ ]")

	m5hLabel.SetTitle(fmt.Sprintf("5h Session                           %s used", s))
	m5hBar.SetTitle(bar(d.FiveHour.UsedPercentage))
	m5hReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.FiveHour.ResetsAt)))

	m7dLabel.SetTitle(fmt.Sprintf("7d All Models                        %s used", w))
	m7dBar.SetTitle(bar(d.SevenDay.UsedPercentage))
	m7dReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.SevenDay.ResetsAt)))

	mStatus.SetTitle(fmt.Sprintf("%s · inactive %s", d.Model, fmtAgo(staleness)))
}

func setInactive() {
	systray.SetTitle("[ ⏸ ]")

	m5hLabel.SetTitle("5h Session                            --")
	m5hBar.SetTitle(strings.Repeat("░", barWidth))
	m5hReset.SetTitle(" ")

	m7dLabel.SetTitle("7d All Models                         --")
	m7dBar.SetTitle(strings.Repeat("░", barWidth))
	m7dReset.SetTitle(" ")

	mStatus.SetTitle("Waiting for Claude Code...")
}

// ── Helpers ──

func pct(p *float64) string {
	if p == nil {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

func bar(p *float64) string {
	if p == nil {
		return strings.Repeat("░", barWidth)
	}
	filled := int(*p / 100 * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}

func resetDate(ts *int64) string {
	if ts == nil {
		return "--"
	}
	t := time.Unix(*ts, 0)
	return t.Format("01/02 15:04")
}

func fmtAgo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm ago", int(d.Hours()), int(d.Minutes())%60)
}

// ── LaunchAgent ──

const launchAgentLabel = "com.github.hwayoungjun.claude-usage-bar"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func isLaunchAgentInstalled() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

func stableBinPath() string {
	// Prefer the PATH-based path (e.g. /opt/homebrew/bin/claude-usage-bar)
	// which is a stable symlink that survives brew upgrades.
	// os.Executable() resolves symlinks on macOS, returning the Cellar path
	// which breaks after brew upgrade.
	if p, err := exec.LookPath(appName); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	// Fallback to the resolved executable path
	binPath, _ := os.Executable()
	binPath, _ = filepath.Abs(binPath)
	return binPath
}

func installLaunchAgent() error {
	binPath := stableBinPath()

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
	<false/>
</dict>
</plist>
`, launchAgentLabel, binPath)

	dir := filepath.Dir(launchAgentPath())
	os.MkdirAll(dir, 0755)
	return os.WriteFile(launchAgentPath(), []byte(plist), 0644)
}

func removeLaunchAgent() error {
	return os.Remove(launchAgentPath())
}

func loadUsage() (*UsageData, error) {
	raw, err := os.ReadFile(usageFilePath())
	if err != nil {
		return nil, err
	}
	var d UsageData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.UpdatedAt == 0 {
		return nil, fmt.Errorf("no data")
	}
	return &d, nil
}
