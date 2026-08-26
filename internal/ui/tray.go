// Package ui is the menu bar itself: it turns snapshots and session lists into
// menu item titles and routes clicks back out. It holds no rules of its own —
// what to show comes from the domain packages, what to read comes from store.
package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/getlantern/systray"

	"github.com/hwayoungjun/claude-usage-bar/internal/install"
	"github.com/hwayoungjun/claude-usage-bar/internal/session"
	"github.com/hwayoungjun/claude-usage-bar/internal/shellquote"
	"github.com/hwayoungjun/claude-usage-bar/internal/store"
	"github.com/hwayoungjun/claude-usage-bar/internal/textwidth"
	"github.com/hwayoungjun/claude-usage-bar/internal/usage"
)

const (
	maxSessions = 5

	// The widget re-reads its sources on this cadence as a floor; file watching
	// handles the statusLine case instantly, and the desktop history only moves
	// every ~15 minutes.
	refreshInterval = 30 * time.Second
)

// Widget owns the tray. Its dependencies are injected so the composition root
// decides where data comes from.
type Widget struct {
	Usage    store.UsageFile
	Desktop  *store.DesktopHistory
	Settings store.SettingsFile
	Sessions session.Transcripts
	Agent    install.LaunchAgent
	BinPath  string           // recorded in the LaunchAgent plist on install
	Now      func() time.Time // injectable for tests and for a fixed clock

	settings store.Settings

	fiveHourLabel, fiveHourBar, fiveHourReset *systray.MenuItem
	sevenDayLabel, sevenDayBar, sevenDayReset *systray.MenuItem
	sessionsHeader                            *systray.MenuItem
	sessionItems                              []*systray.MenuItem

	current []session.Session
}

// Run mounts the tray and blocks until the user quits.
func (w *Widget) Run() {
	if w.Now == nil {
		w.Now = time.Now
	}
	systray.Run(w.onReady, func() {
		// The single-instance lock is released by the kernel on exit.
	})
}

func (w *Widget) onReady() {
	w.settings = w.Settings.Load()
	systray.SetTitle(usage.TrayTitle(w.settings.DisplayMode, "--", "--"))
	systray.SetTooltip("Claude Usage Bar")

	w.fiveHourLabel = systray.AddMenuItem("", "")
	w.fiveHourBar = systray.AddMenuItem("", "")
	w.fiveHourReset = systray.AddMenuItem("", "")

	systray.AddSeparator()

	w.sevenDayLabel = systray.AddMenuItem("", "")
	w.sevenDayBar = systray.AddMenuItem("", "")
	w.sevenDayReset = systray.AddMenuItem("", "")

	systray.AddSeparator()

	w.sessionsHeader = systray.AddMenuItem("Recent Sessions", "")
	w.sessionsHeader.Disable()
	for i := 0; i < maxSessions; i++ {
		item := systray.AddMenuItem("", "")
		item.Hide()
		w.sessionItems = append(w.sessionItems, item)
	}

	systray.AddSeparator()

	display := systray.AddMenuItem("Display", "Tray display mode")
	displayShort := display.AddSubMenuItem("5h only", "Show only 5h session")
	displayFull := display.AddSubMenuItem("5h + 7d", "Show 5h session and 7d week")
	w.applyDisplayCheck(displayShort, displayFull)

	launch := systray.AddMenuItem("Launch at Login", "Toggle launch at login")
	switch {
	case w.Agent.IsHomebrewManaged():
		launch.Check()
		launch.SetTooltip("Managed by Homebrew — use `brew services` to change")
		launch.Disable()
	case w.Agent.IsInstalled():
		launch.Check()
	}

	quit := systray.AddMenuItem("Quit", "")

	w.showIdle()
	w.refresh()
	w.refreshSessions()

	go w.watchUsageFile()
	go w.refreshLoop()

	go func() {
		for {
			select {
			case <-displayShort.ClickedCh:
				w.setDisplayMode(usage.DisplayShort, displayShort, displayFull)
			case <-displayFull.ClickedCh:
				w.setDisplayMode(usage.DisplayFull, displayShort, displayFull)
			case <-launch.ClickedCh:
				w.toggleLaunchAtLogin(launch)
			case <-quit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func (w *Widget) setDisplayMode(mode usage.DisplayMode, short, full *systray.MenuItem) {
	w.settings.DisplayMode = mode
	if err := w.Settings.Save(w.settings); err != nil {
		fmt.Fprintln(os.Stderr, "Display mode: save failed:", err)
	}
	w.applyDisplayCheck(short, full)
	w.refresh()
}

func (w *Widget) applyDisplayCheck(short, full *systray.MenuItem) {
	if w.settings.DisplayMode == usage.DisplayShort {
		short.Check()
		full.Uncheck()
		return
	}
	short.Uncheck()
	full.Check()
}

func (w *Widget) toggleLaunchAtLogin(item *systray.MenuItem) {
	if w.Agent.IsHomebrewManaged() {
		// The item is disabled, but a stray click must not register a second job.
		return
	}
	if w.Agent.IsInstalled() {
		if err := w.Agent.Remove(); err != nil {
			fmt.Fprintln(os.Stderr, "Launch at Login: remove failed:", err)
			return
		}
		item.Uncheck()
		return
	}
	if err := w.Agent.Install(w.BinPath); err != nil {
		fmt.Fprintln(os.Stderr, "Launch at Login: install failed:", err)
		return
	}
	item.Check()
}

// watchUsageFile reacts to the statusLine hook the moment it writes.
func (w *Widget) watchUsageFile() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer watcher.Close()

	dir := store.ConfigDir()
	os.MkdirAll(dir, 0755)
	watcher.Add(dir)

	for {
		select {
		case event := <-watcher.Events:
			if event.Name == w.Usage.Path && event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				// The hook writes the whole file; give it a moment to land.
				time.Sleep(50 * time.Millisecond)
				w.refresh()
			}
		case <-watcher.Errors:
		}
	}
}

func (w *Widget) refreshLoop() {
	for {
		time.Sleep(refreshInterval)
		w.refresh()
		w.refreshSessions()
	}
}

func (w *Widget) refresh() {
	statusLine, err := w.Usage.Load()
	if err != nil {
		statusLine = nil
	}
	snapshot := usage.Resolve(w.Now(), statusLine, w.Desktop.Read())

	if snapshot.Data == nil {
		w.showIdle()
		return
	}
	w.showWindows(snapshot)

	// A reading too old to trust shows as the idle title in the menu bar; the
	// dropdown carries no status row of its own.
	if snapshot.Stale {
		systray.SetTitle(usage.IdleTrayTitle)
		return
	}
	systray.SetTitle(usage.TrayTitle(w.settings.DisplayMode,
		usage.Percent(snapshot.Data.FiveHour.UsedPercentage),
		usage.Percent(snapshot.Data.SevenDay.UsedPercentage)))
}

func (w *Widget) showWindows(s usage.Snapshot) {
	w.fiveHourLabel.SetTitle(fmt.Sprintf("5h Session                           %s used", usage.Percent(s.Data.FiveHour.UsedPercentage)))
	w.fiveHourBar.SetTitle(usage.Bar(s.Data.FiveHour.UsedPercentage))
	w.fiveHourReset.SetTitle(fmt.Sprintf("Resets %s", usage.ResetDate(s.Data.FiveHour.ResetsAt)))

	w.sevenDayLabel.SetTitle(fmt.Sprintf("7d All Models                        %s used", usage.Percent(s.Data.SevenDay.UsedPercentage)))
	w.sevenDayBar.SetTitle(usage.Bar(s.Data.SevenDay.UsedPercentage))
	w.sevenDayReset.SetTitle(fmt.Sprintf("Resets %s", usage.ResetDate(s.Data.SevenDay.ResetsAt)))
}

func (w *Widget) showIdle() {
	systray.SetTitle(usage.IdleTrayTitle)

	w.fiveHourLabel.SetTitle("5h Session                            --")
	w.fiveHourBar.SetTitle(strings.Repeat("░", usage.BarWidth))
	w.fiveHourReset.SetTitle(" ")

	w.sevenDayLabel.SetTitle("7d All Models                         --")
	w.sevenDayBar.SetTitle(strings.Repeat("░", usage.BarWidth))
	w.sevenDayReset.SetTitle(" ")
}

func (w *Widget) refreshSessions() {
	w.current = w.Sessions.Recent(maxSessions)
	shared := session.SharedProject(w.current)
	w.setSessionsHeader(shared)

	for i := 0; i < maxSessions; i++ {
		if i >= len(w.current) {
			w.sessionItems[i].Hide()
			continue
		}
		s := w.current[i]
		w.sessionItems[i].SetTitle(s.Label(shared == ""))
		w.sessionItems[i].SetTooltip(s.Tooltip())
		w.sessionItems[i].Show()
		go w.handleSessionClick(i)
	}
}

// setSessionsHeader names the shared project on the section header when the
// rows no longer carry it themselves.
func (w *Widget) setSessionsHeader(shared string) {
	if shared == "" {
		w.sessionsHeader.SetTitle("Recent Sessions")
		return
	}
	const prefix = "Recent Sessions — "
	w.sessionsHeader.SetTitle(prefix + textwidth.Truncate(shared, session.RowWidth-textwidth.Width(prefix)))
}

func (w *Widget) handleSessionClick(index int) {
	<-w.sessionItems[index].ClickedCh
	if index < len(w.current) {
		s := w.current[index]
		// Both halves are quoted: the project is a transcript's cwd field and the
		// id is a transcript's filename, so neither is ours to trust in a command
		// the user is about to paste into a shell.
		copyToClipboard(fmt.Sprintf("cd %s && claude --resume %s",
			shellquote.Quote(s.Project), shellquote.Quote(s.ID)))
	}
	go w.handleSessionClick(index)
}

// copyToClipboard hands text to pbcopy. A menu row cannot resume a session by
// itself, so the click leaves the command ready to paste.
func copyToClipboard(text string) {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Copy failed:", err)
	}
}
