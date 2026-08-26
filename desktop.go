package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ── Claude desktop app usage history ──
//
// statusLine never fires for sessions started from the Claude desktop app: the
// app spawns Claude Code headless (`--output-format stream-json
// --input-format stream-json`) and draws its own UI, so there is no terminal
// status line to render and the command is never invoked. Hooks do run there,
// but no hook payload carries rate_limits, so the hook route is a dead end.
//
// The desktop app does keep its own history for the usage meter in its UI —
// the same five-hour / seven-day utilization numbers, appended roughly every
// 15 minutes while the app is running. Reading it costs no API call, no OAuth
// token, and no keychain access, and it only has data while the app is open,
// which is exactly when statusLine leaves us blind.
//
// It is another app's internal file: every field is treated as untrusted and
// anything unexpected degrades to "no data" rather than a wrong number.

const (
	// Samples land every ~15 min, so desktop-sourced data is stale by design.
	// Allow a full interval plus slack before the widget calls itself inactive.
	desktopStaleAfter = 20 * time.Minute

	// Sanity floor for sample timestamps: older than the feature itself.
	desktopEpochFloor = 1704067200 // 2024-01-01
)

func desktopHistoryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Claude", "plan-usage-history.json")
}

type desktopHistory struct {
	Version int `json:"version"`
	Samples []struct {
		T int64 `json:"t"` // unix millis
		U struct {
			FH *float64 `json:"fh"` // five-hour utilization, percent
			SD *float64 `json:"sd"` // seven-day utilization, percent
		} `json:"u"`
	} `json:"samples"`
}

// The history file grows without bound (one sample per 15 min), so re-parsing
// it on every refresh is wasted work. Cache the result until the file changes.
var desktopCache struct {
	sync.Mutex
	valid   bool
	modTime time.Time
	size    int64
	data    *UsageData
}

// loadDesktopUsage returns the newest usable sample, or nil when the file is
// missing, unreadable, or shaped in a way we don't recognise.
func loadDesktopUsage() *UsageData {
	info, err := os.Stat(desktopHistoryPath())
	if err != nil {
		return nil
	}

	desktopCache.Lock()
	defer desktopCache.Unlock()

	if desktopCache.valid && info.ModTime().Equal(desktopCache.modTime) && info.Size() == desktopCache.size {
		return desktopCache.data
	}

	desktopCache.valid = true
	desktopCache.modTime = info.ModTime()
	desktopCache.size = info.Size()
	desktopCache.data = parseDesktopHistory(desktopHistoryPath())
	return desktopCache.data
}

// parseDesktopHistory walks samples newest-first and returns the first one that
// passes every sanity check, so a single malformed trailing entry costs us
// freshness rather than the whole file.
func parseDesktopHistory(path string) *UsageData {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	// Version is recorded but not enforced: a bump that only adds fields should
	// keep working, and a bump that changes the shape fails the checks below.
	var h desktopHistory
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil
	}

	cutoff := time.Now().Unix() + 60 // tolerate minor clock skew
	for i := len(h.Samples) - 1; i >= 0; i-- {
		s := h.Samples[i]
		ts := s.T / 1000
		if ts < desktopEpochFloor || ts > cutoff {
			continue
		}
		if !validPercentage(s.U.FH) || !validPercentage(s.U.SD) {
			continue
		}
		if s.U.FH == nil && s.U.SD == nil {
			continue
		}
		return &UsageData{
			UpdatedAt: ts,
			FiveHour:  RateInfo{UsedPercentage: s.U.FH},
			SevenDay:  RateInfo{UsedPercentage: s.U.SD},
		}
	}
	return nil
}

func validPercentage(p *float64) bool {
	return p == nil || (*p >= 0 && *p <= 100)
}

// mergeDesktop fills in what the desktop history doesn't carry — the reset
// times — from the last statusLine report. A reset time that has already
// passed is dropped rather than shown: the window rolled over and this source
// gives us no way to learn the new boundary.
func mergeDesktop(desktop, statusLine *UsageData) *UsageData {
	out := *desktop
	if statusLine == nil {
		return &out
	}
	now := time.Now().Unix()
	if r := statusLine.FiveHour.ResetsAt; r != nil && *r > now {
		out.FiveHour.ResetsAt = r
	}
	if r := statusLine.SevenDay.ResetsAt; r != nil && *r > now {
		out.SevenDay.ResetsAt = r
	}
	return &out
}
