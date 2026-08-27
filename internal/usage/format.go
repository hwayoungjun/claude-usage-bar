package usage

import (
	"fmt"
	"strings"
	"time"
)

// BarWidth is the length of the progress bars in the dropdown, in characters.
const BarWidth = 20

// Percent renders a utilization value, or "--" when the window is unknown.
func Percent(p *float64) string {
	if p == nil {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

// WarnAbove is the utilization at which a window is worth flagging. The
// dropdown draws a bar, which shows a nearly-full window without being told;
// the menu bar title has room for a number and nothing else, so there a window
// this high gets a mark instead.
const WarnAbove = 80

// warnMark flags a window that has reached WarnAbove. The variation selector is
// not decoration: macOS draws U+26A0 on its own as a colour emoji, and U+FE0E
// asks for the text glyph instead, which takes the menu bar's own colour in
// both light and dark and sits with the numbers rather than shouting over them.
// Written as escapes because the selector is invisible, and an editor that
// dropped it would quietly bring the emoji back.
const warnMark = " \u26a0\ufe0e"

// PercentMarked renders a utilization for the menu bar title, flagging a window
// that has reached WarnAbove. The mark is the whole point of the title at that
// moment: the number is already on screen, and what it has crossed is not.
func PercentMarked(p *float64) string {
	if p != nil && *p >= WarnAbove {
		return Percent(p) + warnMark
	}
	return Percent(p)
}

// Bar draws a fixed-width progress bar. Out-of-range values are clamped rather
// than trusted: the numbers come from outside this program.
func Bar(p *float64) string {
	if p == nil {
		return strings.Repeat("░", BarWidth)
	}
	filled := int(*p / 100 * float64(BarWidth))
	if filled > BarWidth {
		filled = BarWidth
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", BarWidth-filled)
}

// ResetDate renders a window boundary, or "--" when it is unknown.
func ResetDate(ts *int64) string {
	if ts == nil {
		return "--"
	}
	return time.Unix(*ts, 0).Format("01/02 15:04")
}

// Ago renders an age the way the status row wants it.
func Ago(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm ago", int(d.Hours()), int(d.Minutes())%60)
}

// DisplayMode is the user's choice of how much to show in the menu bar itself.
type DisplayMode string

const (
	DisplayShort DisplayMode = "short" // 5h session only
	DisplayFull  DisplayMode = "full"  // 5h session and 7d week
)

// Valid reports whether m is a mode this build knows, so a hand-edited or
// future config falls back instead of rendering an empty title.
func (m DisplayMode) Valid() bool {
	return m == DisplayShort || m == DisplayFull
}

// TrayTitle renders the menu bar title itself.
func TrayTitle(mode DisplayMode, fiveHour, sevenDay string) string {
	if mode == DisplayShort {
		return fmt.Sprintf("[ 5h %s ]", fiveHour)
	}
	return fmt.Sprintf("[ 5h %s  ·  7d %s ]", fiveHour, sevenDay)
}

// IdleTrayTitle is what the menu bar shows with nothing to report.
const IdleTrayTitle = "[ ⏸ ]"
