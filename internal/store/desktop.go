package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hwayoungjun/claude-usage-bar/internal/usage"
)

// DesktopHistory reads the usage history the Claude desktop app keeps for the
// meter in its own UI.
//
// statusLine never fires for sessions started from the desktop app: it spawns
// Claude Code headless (--output-format stream-json --input-format stream-json)
// and draws its own UI, so there is no terminal status line to render and the
// command is never invoked. Hooks do run there, but no hook payload carries
// rate limits, so the hook route is a dead end for the numbers. This file is
// the local source that covers those sessions, and reading it costs no API
// call, no credentials, and no keychain access.
//
// It belongs to another app: every field is range-checked and anything
// unexpected degrades to "no data" instead of a wrong number.
type DesktopHistory struct {
	Path string

	// Now is injectable so the sanity check on sample timestamps can be tested
	// against a fixed clock. Nil means time.Now.
	Now func() time.Time

	// The file grows by one sample every ~15 minutes and is re-read on every
	// refresh, so parse results are cached until it changes on disk.
	mu      sync.Mutex
	cached  bool
	modTime time.Time
	size    int64
	data    *usage.Data
}

// DefaultDesktopHistory points at the desktop app's support directory.
func DefaultDesktopHistory() *DesktopHistory {
	home, _ := os.UserHomeDir()
	return &DesktopHistory{
		Path: filepath.Join(home, "Library", "Application Support", "Claude", "plan-usage-history.json"),
		Now:  time.Now,
	}
}

func (h *DesktopHistory) now() time.Time {
	if h.Now == nil {
		return time.Now()
	}
	return h.Now()
}

// Sanity floor for sample timestamps: older than the feature itself.
const desktopEpochFloor = 1704067200 // 2024-01-01

type desktopFile struct {
	Version int `json:"version"`
	Samples []struct {
		T int64 `json:"t"` // unix millis
		U struct {
			FH *float64 `json:"fh"` // five-hour utilization, percent
			SD *float64 `json:"sd"` // seven-day utilization, percent
		} `json:"u"`
	} `json:"samples"`
}

// Read returns the newest usable sample, or nil when the file is missing,
// unreadable, or shaped in a way this build does not recognise.
func (h *DesktopHistory) Read() *usage.Data {
	info, err := os.Stat(h.Path)
	if err != nil {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.cached && info.ModTime().Equal(h.modTime) && info.Size() == h.size {
		return h.data
	}

	h.cached = true
	h.modTime = info.ModTime()
	h.size = info.Size()
	h.data = parseDesktopHistory(h.Path, h.now())
	return h.data
}

// parseDesktopHistory walks samples newest-first and returns the first one that
// passes every check, so a single malformed trailing entry costs freshness
// rather than the whole file. The version is recorded but not enforced: a bump
// that only adds fields keeps working, and one that changes the shape fails the
// checks below anyway.
func parseDesktopHistory(path string, now time.Time) *usage.Data {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f desktopFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}

	cutoff := now.Unix() + 60 // tolerate minor clock skew
	for i := len(f.Samples) - 1; i >= 0; i-- {
		s := f.Samples[i]
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
		return &usage.Data{
			UpdatedAt: ts,
			FiveHour:  usage.Rate{UsedPercentage: s.U.FH},
			SevenDay:  usage.Rate{UsedPercentage: s.U.SD},
		}
	}
	return nil
}

func validPercentage(p *float64) bool {
	return p == nil || (*p >= 0 && *p <= 100)
}
