// Package usage is the rate limit domain: the shape of a reading, which of
// several readings to trust, and how old a reading may get before the widget
// stops believing it. It performs no I/O — adapters hand it data and it hands
// back decisions — which is what makes the rules testable.
package usage

import "time"

// Rate is one limit window. Both fields are pointers because Claude Code omits
// them until the first API response of a session, and "unknown" has to stay
// distinguishable from zero.
type Rate struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"`
}

// Data is one reading of the subscription limits.
type Data struct {
	UpdatedAt int64  `json:"updated_at"`
	FiveHour  Rate   `json:"five_hour"`
	SevenDay  Rate   `json:"seven_day"`
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
}

// Source names where a reading came from. The two differ in freshness and in
// what they carry, so the UI has to be able to tell them apart.
type Source int

const (
	SourceNone Source = iota
	SourceStatusLine
	SourceDesktopApp
)

const (
	// StatusLineStaleAfter: statusLine fires on every assistant message, so a
	// reading older than this means nobody is working in a terminal session.
	StatusLineStaleAfter = 10 * time.Minute

	// DesktopStaleAfter: the desktop app samples every ~15 minutes, so its data
	// is stale by design. A tighter bound would flip the widget to idle between
	// two perfectly good samples.
	DesktopStaleAfter = 20 * time.Minute
)

// Snapshot is what the UI renders: a reading, where it came from, how old it
// is, and whether that age has crossed the source's limit.
type Snapshot struct {
	Data   *Data
	Source Source
	Age    time.Duration
	Stale  bool
}

// Resolve picks between the readings the app can get. statusLine covers
// terminal sessions; the desktop app's own history covers the sessions where
// statusLine never fires. Whichever saw usage last wins, and the staleness
// budget follows the winner's cadence. Either argument may be nil.
func Resolve(now time.Time, statusLine, desktop *Data) Snapshot {
	winner, source, staleAfter := statusLine, SourceStatusLine, StatusLineStaleAfter
	if desktop != nil && (statusLine == nil || desktop.UpdatedAt > statusLine.UpdatedAt) {
		winner, source, staleAfter = merge(desktop, statusLine, now), SourceDesktopApp, DesktopStaleAfter
	}
	if winner == nil {
		return Snapshot{Source: SourceNone}
	}
	age := now.Sub(time.Unix(winner.UpdatedAt, 0))
	return Snapshot{Data: winner, Source: source, Age: age, Stale: age > staleAfter}
}

// merge fills in what the desktop history doesn't carry — the reset times —
// from the last statusLine report. A reset time that has already passed is
// dropped rather than shown: the window rolled over, and this source gives us
// no way to learn the new boundary.
func merge(desktop, statusLine *Data, now time.Time) *Data {
	out := *desktop
	if statusLine == nil {
		return &out
	}
	cutoff := now.Unix()
	if r := statusLine.FiveHour.ResetsAt; r != nil && *r > cutoff {
		out.FiveHour.ResetsAt = r
	}
	if r := statusLine.SevenDay.ResetsAt; r != nil && *r > cutoff {
		out.SevenDay.ResetsAt = r
	}
	return &out
}

// Label names the source for the status row. Desktop readings deliberately do
// not borrow the model name from an unrelated terminal session.
func (s Snapshot) Label() string {
	switch {
	case s.Source == SourceDesktopApp:
		return "Claude app"
	case s.Data == nil || s.Data.Model == "":
		return "Claude Code"
	default:
		return s.Data.Model
	}
}
