package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func settingsFilePath() string {
	return filepath.Join(configDir(), "settings.json")
}

// ── User settings ──

type DisplayMode string

const (
	DisplayShort DisplayMode = "short" // only 5h session
	DisplayFull  DisplayMode = "full"  // 5h session + 7d week
)

type Settings struct {
	DisplayMode DisplayMode `json:"display_mode"`
}

func loadSettings() Settings {
	s := Settings{DisplayMode: DisplayFull}
	raw, err := os.ReadFile(settingsFilePath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	if s.DisplayMode != DisplayShort && s.DisplayMode != DisplayFull {
		s.DisplayMode = DisplayFull
	}
	return s
}

func saveSettings(s Settings) {
	os.MkdirAll(configDir(), 0755)
	out, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(settingsFilePath(), out, 0644)
}

// ── Single-instance lock ──
//
// We hold an exclusive flock on this file for the lifetime of the widget
// process. A flock is released by the kernel when the holding process exits
// (or its fd is closed), which makes it robust against PID reuse — a problem
// the previous pid-file approach had after reboots, when an unrelated process
// could inherit our old PID and trick us into thinking we were still running.

func lockFilePath() string {
	return filepath.Join(configDir(), "lock")
}

func openLockFile() (*os.File, error) {
	os.MkdirAll(configDir(), 0755)
	return os.OpenFile(lockFilePath(), os.O_CREATE|os.O_RDWR, 0644)
}

// isAlreadyRunning briefly probes the lock to report whether another instance
// holds it. The probe acquires and immediately releases, so it does NOT
// reserve the slot for the caller.
func isAlreadyRunning() bool {
	f, err := openLockFile()
	if err != nil {
		return false
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil
}

// acquireSingleInstanceLock takes an exclusive non-blocking lock and holds it
// for the rest of the process lifetime. Returns false if another instance
// already holds the lock.
func acquireSingleInstanceLock() bool {
	f, err := openLockFile()
	if err != nil {
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	// Intentionally leak f; the kernel releases the lock on process exit.
	return true
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
	if !acquireSingleInstanceLock() {
		fmt.Println("claude-usage-bar is already running.")
		return
	}
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

// runStatusLine captures the rate limit data Claude Code passes on stdin and
// writes nothing back. The statusLine slot in ~/.claude/settings.json holds
// only one command, and this app claims it to read the data — not to draw in
// the terminal. Printing nothing leaves the row under the input box to Claude
// Code; the numbers show up in the menu bar instead.
func runStatusLine() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var sl StatusLineInput
	if err := json.Unmarshal(input, &sl); err != nil {
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
	refreshLaunchAgentPlist()
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

// displayWidth approximates how wide a string renders in the menu: CJK and
// emoji runes take about two columns, everything else one. Session rows are
// budgeted in these columns rather than in runes, so a Korean prompt and an
// English one get cut at roughly the same visual width.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals, Kangxi, CJK punctuation
		r >= 0x3041 && r <= 0x33FF, // kana, Hangul compatibility, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK extension A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji
		r >= 0x1F900 && r <= 0x1F9FF:
		return 2
	}
	return 1
}

// truncateWidth cuts s down to maxWidth display columns, marking the cut with
// an ellipsis (itself one column wide).
func truncateWidth(s string, maxWidth int) string {
	if maxWidth <= 1 {
		return "…"
	}
	if displayWidth(s) <= maxWidth {
		return s
	}
	budget := maxWidth - 1
	w := 0
	var out []rune
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > budget {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}

// sessionLabel lays "[project] prompt" out against a fixed column budget. Both
// halves are trimmed now: the project name used to be unbounded while the
// prompt was capped on its own, so a long project name pushed the row wide and
// still cut the prompt early — which reads as the right side of the row
// clipping well before the left.
func sessionLabel(s RecentSession) string {
	proj := truncateWidth(projectName(s.Project), sessionProjectWidth)
	room := sessionLabelWidth - displayWidth(proj) - len("[] ")
	if room < sessionPromptMin {
		room = sessionPromptMin
	}
	return fmt.Sprintf("[%s] %s", proj, truncateWidth(s.FirstDisplay, room))
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

	settings Settings
)

const (
	barWidth    = 20
	maxSessions = 5

	// Session rows are budgeted in display columns (see displayWidth). The widest
	// fixed row in the dropdown is 46 columns, so they stay just under it and
	// never become the row that decides how wide the menu gets drawn.
	sessionLabelWidth   = 44
	sessionProjectWidth = 16
	sessionPromptMin    = 10
)

func onReady() {
	settings = loadSettings()
	systray.SetTitle(initialTitle())
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

	mDisplay := systray.AddMenuItem("Display", "Tray display mode")
	mDisplayShort := mDisplay.AddSubMenuItem("5h only", "Show only 5h session")
	mDisplayFull := mDisplay.AddSubMenuItem("5h + 7d", "Show 5h session and 7d week")
	applyDisplayCheck(mDisplayShort, mDisplayFull)

	mLaunch := systray.AddMenuItem("Launch at Login", "Toggle launch at login")
	if isHomebrewManaged() {
		mLaunch.Check()
		mLaunch.SetTooltip("Managed by Homebrew — use `brew services` to change")
		mLaunch.Disable()
	} else if isLaunchAgentInstalled() {
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
			case <-mDisplayShort.ClickedCh:
				settings.DisplayMode = DisplayShort
				saveSettings(settings)
				applyDisplayCheck(mDisplayShort, mDisplayFull)
				refreshUI()
			case <-mDisplayFull.ClickedCh:
				settings.DisplayMode = DisplayFull
				saveSettings(settings)
				applyDisplayCheck(mDisplayShort, mDisplayFull)
				refreshUI()
			case <-mLaunch.ClickedCh:
				if isHomebrewManaged() {
					// Disabled — handler shouldn't fire, but guard just in case.
					continue
				}
				if isLaunchAgentInstalled() {
					if err := removeLaunchAgent(); err != nil {
						fmt.Fprintln(os.Stderr, "Launch at Login: remove failed:", err)
						continue
					}
					mLaunch.Uncheck()
				} else {
					if err := installLaunchAgent(); err != nil {
						fmt.Fprintln(os.Stderr, "Launch at Login: install failed:", err)
						continue
					}
					mLaunch.Check()
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func applyDisplayCheck(short, full *systray.MenuItem) {
	if settings.DisplayMode == DisplayShort {
		short.Check()
		full.Uncheck()
	} else {
		short.Uncheck()
		full.Check()
	}
}

func initialTitle() string {
	if settings.DisplayMode == DisplayShort {
		return "[ 5h -- ]"
	}
	return "[ 5h --  ·  7d -- ]"
}

func formatTitle(fiveHour, sevenDay string) string {
	if settings.DisplayMode == DisplayShort {
		return fmt.Sprintf("[ 5h %s ]", fiveHour)
	}
	return fmt.Sprintf("[ 5h %s  ·  7d %s ]", fiveHour, sevenDay)
}

var currentSessions []RecentSession

func refreshSessions() {
	currentSessions = loadRecentSessions(maxSessions)
	for i := 0; i < maxSessions; i++ {
		if i < len(currentSessions) {
			s := currentSessions[i]
			mSessionItems[i].SetTitle(sessionLabel(s))
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
	// Lock is released automatically when the process exits.
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

// usageSource records where the numbers on screen came from. statusLine only
// fires for terminal sessions, so the status row has to be honest about which
// of the two sources the widget is showing.
type usageSource int

const (
	sourceStatusLine usageSource = iota
	sourceDesktopApp
)

func statusLabel(d *UsageData, src usageSource) string {
	if src == sourceDesktopApp {
		return "Claude app"
	}
	if d.Model == "" {
		return "Claude Code"
	}
	return d.Model
}

func refreshUI() {
	// statusLine covers terminal sessions; the desktop app's own history covers
	// the sessions where statusLine never fires. Whichever saw usage last wins.
	d, err := loadUsage()
	if err != nil {
		d = nil
	}

	src := sourceStatusLine
	staleAfter := 10 * time.Minute
	if desktop := loadDesktopUsage(); desktop != nil && (d == nil || desktop.UpdatedAt > d.UpdatedAt) {
		d = mergeDesktop(desktop, d)
		src = sourceDesktopApp
		staleAfter = desktopStaleAfter
	}

	if d == nil {
		setInactive()
		return
	}

	staleness := time.Since(time.Unix(d.UpdatedAt, 0))
	if staleness > staleAfter {
		setStale(d, staleness, src)
		return
	}

	setActive(d, src)
}

func setActive(d *UsageData, src usageSource) {
	s := pct(d.FiveHour.UsedPercentage)
	w := pct(d.SevenDay.UsedPercentage)

	systray.SetTitle(formatTitle(s, w))

	m5hLabel.SetTitle(fmt.Sprintf("5h Session                           %s used", s))
	m5hBar.SetTitle(bar(d.FiveHour.UsedPercentage))
	m5hReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.FiveHour.ResetsAt)))

	m7dLabel.SetTitle(fmt.Sprintf("7d All Models                        %s used", w))
	m7dBar.SetTitle(bar(d.SevenDay.UsedPercentage))
	m7dReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.SevenDay.ResetsAt)))

	ago := fmtAgo(time.Since(time.Unix(d.UpdatedAt, 0)))
	mStatus.SetTitle(fmt.Sprintf("%s · %s", statusLabel(d, src), ago))
}

func setStale(d *UsageData, staleness time.Duration, src usageSource) {
	s := pct(d.FiveHour.UsedPercentage)
	w := pct(d.SevenDay.UsedPercentage)

	systray.SetTitle("[ ⏸ ]")

	m5hLabel.SetTitle(fmt.Sprintf("5h Session                           %s used", s))
	m5hBar.SetTitle(bar(d.FiveHour.UsedPercentage))
	m5hReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.FiveHour.ResetsAt)))

	m7dLabel.SetTitle(fmt.Sprintf("7d All Models                        %s used", w))
	m7dBar.SetTitle(bar(d.SevenDay.UsedPercentage))
	m7dReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.SevenDay.ResetsAt)))

	mStatus.SetTitle(fmt.Sprintf("%s · inactive %s", statusLabel(d, src), fmtAgo(staleness)))
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
const homebrewLaunchAgentLabel = "homebrew.mxcl.claude-usage-bar"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func homebrewLaunchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", homebrewLaunchAgentLabel+".plist")
}

func isLaunchAgentInstalled() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

// isHomebrewManaged reports whether brew services is managing launch-at-login.
// When true, the in-app toggle is disabled to avoid registering a duplicate
// LaunchAgent for the same binary.
func isHomebrewManaged() bool {
	_, err := os.Stat(homebrewLaunchAgentPath())
	return err == nil
}

func userDomainTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// bootstrapLaunchAgent loads the plist into the user's launchd domain so the
// service starts immediately (and on every subsequent login). Returns nil if
// the agent is already loaded.
func bootstrapLaunchAgent() error {
	out, err := exec.Command("launchctl", "bootstrap", userDomainTarget(), launchAgentPath()).CombinedOutput()
	if err == nil {
		return nil
	}
	// "service already loaded" / "already bootstrapped" — treat as success.
	msg := string(out)
	if strings.Contains(msg, "already") || strings.Contains(msg, "Bootstrap failed: 5") {
		return nil
	}
	return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(msg))
}

// bootoutLaunchAgent unloads the plist from launchd. Returns nil if not loaded.
func bootoutLaunchAgent() error {
	target := fmt.Sprintf("%s/%s", userDomainTarget(), launchAgentLabel)
	out, err := exec.Command("launchctl", "bootout", target).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := string(out)
	// "No such process" / not loaded — treat as success.
	if strings.Contains(msg, "No such process") || strings.Contains(msg, "Could not find") {
		return nil
	}
	return fmt.Errorf("launchctl bootout: %v: %s", err, strings.TrimSpace(msg))
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

func launchAgentPlist(binPath string) string {
	// KeepAlive restarts only after a crash: a clean exit means the user picked
	// Quit, while a start that fails at login — the window server can still be
	// coming up that early — gets another attempt instead of leaving the menu bar
	// empty until the next reboot.
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
`, launchAgentLabel, binPath)
}

func installLaunchAgent() error {
	dir := filepath.Dir(launchAgentPath())
	os.MkdirAll(dir, 0755)
	if err := os.WriteFile(launchAgentPath(), []byte(launchAgentPlist(stableBinPath())), 0644); err != nil {
		return err
	}
	return bootstrapLaunchAgent()
}

// plistProgramPath reads the binary path back out of the installed plist.
func plistProgramPath() string {
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :ProgramArguments:0", launchAgentPath()).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refreshLaunchAgentPlist brings an already-installed plist up to date with the
// keys this build writes, so plist fixes reach machines that turned Launch at
// Login on with an older version. Only the keys are refreshed — the recorded
// binary path is reused verbatim, because stableBinPath() falls back to a
// version-specific Cellar path when it runs under launchd (whose PATH has no
// Homebrew prefix) and would break at the next upgrade. Rewriting the file is
// enough: launchd re-reads it at the next login, and re-bootstrapping here
// would tear down the very process doing the rewrite.
func refreshLaunchAgentPlist() {
	if isHomebrewManaged() || !isLaunchAgentInstalled() {
		return
	}
	binPath := plistProgramPath()
	if binPath == "" {
		return
	}
	want := launchAgentPlist(binPath)
	if cur, err := os.ReadFile(launchAgentPath()); err == nil && string(cur) == want {
		return
	}
	if err := os.WriteFile(launchAgentPath(), []byte(want), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "Launch at Login: plist refresh failed:", err)
	}
}

func removeLaunchAgent() error {
	// Unload first so the service stops; otherwise the file goes away but the
	// running launchd job lingers until reboot.
	if err := bootoutLaunchAgent(); err != nil {
		return err
	}
	if err := os.Remove(launchAgentPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
