package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// ── Paths ──

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName)
}

func usageFilePath() string {
	return filepath.Join(configDir(), "usage.json")
}

// ── Entry point ──

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "statusline":
			runStatusLine()
			return
		case "setup":
			runSetup()
			return
		case "-h", "--help", "help":
			printHelp()
			return
		}
	}

	systray.Run(onReady, onExit)
}

func printHelp() {
	fmt.Printf(`%s — Claude Code usage monitor for macOS menu bar

Usage:
  %s              Launch the menu bar widget
  %s statusline   StatusLine handler (used by Claude Code)
  %s setup        Auto-configure ~/.claude/settings.json
`, appName, appName, appName, appName)
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

// ── Setup subcommand ──

func runSetup() {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Read existing settings
	var settings map[string]interface{}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		// No settings file yet — create one
		settings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(raw, &settings); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing %s: %v\n", settingsPath, err)
			os.Exit(1)
		}
	}

	// Check if already configured
	if sl, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := sl["command"].(string); ok && cmd == appName+" statusline" {
			fmt.Println("✓ Already configured in", settingsPath)
			return
		}
	}

	// Set statusLine
	settings["statusLine"] = map[string]string{
		"type":    "command",
		"command": appName + " statusline",
	}

	// Write back
	os.MkdirAll(filepath.Dir(settingsPath), 0755)
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", settingsPath, err)
		os.Exit(1)
	}

	fmt.Println("✓ Added statusLine to", settingsPath)
	fmt.Println("  Restart Claude Code to activate.")
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
)

const barWidth = 20

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

	mLaunch := systray.AddMenuItem("Launch at Login", "Toggle launch at login")
	if isLaunchAgentInstalled() {
		mLaunch.Check()
	}

	mQuit := systray.AddMenuItem("Quit", "")

	setInactive()
	refreshUI()

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

func onExit() {}

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

func installLaunchAgent() error {
	binPath, _ := os.Executable()
	binPath, _ = filepath.Abs(binPath)

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
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
