package usage

import (
	"strings"
	"testing"
	"time"
)

func TestPercent(t *testing.T) {
	if got := Percent(nil); got != "--" {
		t.Errorf("unknown window should read --, got %q", got)
	}
	for _, tt := range []struct {
		in   float64
		want string
	}{{0, "0%"}, {21, "21%"}, {21.4, "21%"}, {21.6, "22%"}, {100, "100%"}} {
		if got := Percent(pct(tt.in)); got != tt.want {
			t.Errorf("Percent(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBar(t *testing.T) {
	tests := []struct {
		name   string
		in     *float64
		filled int
	}{
		{"unknown", nil, 0},
		{"empty", pct(0), 0},
		{"half", pct(50), 10},
		{"full", pct(100), 20},
		{"over 100 is clamped", pct(150), 20},
		{"negative is clamped", pct(-10), 0},
	}
	for _, tt := range tests {
		got := Bar(tt.in)
		if n := len([]rune(got)); n != BarWidth {
			t.Errorf("%s: bar is %d runes, want %d", tt.name, n, BarWidth)
		}
		if n := strings.Count(got, "█"); n != tt.filled {
			t.Errorf("%s: %d filled cells, want %d (%q)", tt.name, n, tt.filled, got)
		}
	}
}

func TestResetDate(t *testing.T) {
	if got := ResetDate(nil); got != "--" {
		t.Errorf("unknown reset should read --, got %q", got)
	}
	at := time.Date(2026, 3, 4, 5, 6, 0, 0, time.Local).Unix()
	if got := ResetDate(&at); got != "03/04 05:06" {
		t.Errorf("ResetDate = %q", got)
	}
}

func TestAgo(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0s ago"},
		{45 * time.Second, "45s ago"},
		{time.Minute, "1m ago"},
		{59 * time.Minute, "59m ago"},
		{time.Hour, "1h0m ago"},
		{90 * time.Minute, "1h30m ago"},
	} {
		if got := Ago(tt.in); got != tt.want {
			t.Errorf("Ago(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTrayTitle(t *testing.T) {
	if got := TrayTitle(DisplayShort, "21%", "48%"); got != "[ 5h 21% ]" {
		t.Errorf("short mode = %q", got)
	}
	if got := TrayTitle(DisplayFull, "21%", "48%"); got != "[ 5h 21%  ·  7d 48% ]" {
		t.Errorf("full mode = %q", got)
	}
	// An unrecognised mode must still render something.
	if got := TrayTitle(DisplayMode("bogus"), "--", "--"); got != "[ 5h --  ·  7d -- ]" {
		t.Errorf("unknown mode = %q", got)
	}
}

func TestDisplayModeValid(t *testing.T) {
	for _, m := range []DisplayMode{DisplayShort, DisplayFull} {
		if !m.Valid() {
			t.Errorf("%q should be valid", m)
		}
	}
	for _, m := range []DisplayMode{"", "SHORT", "bogus"} {
		if m.Valid() {
			t.Errorf("%q should not be valid", m)
		}
	}
}

func TestPercentMarked(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	tests := []struct {
		in   *float64
		want string
	}{
		{nil, "--"},
		{p(0), "0%"},
		{p(79), "79%"},
		// The threshold itself counts as crossed, so a window sitting exactly on
		// it is flagged rather than waiting for the next reading.
		{p(80), "80% \u26a0\ufe0e"},
		{p(100), "100% \u26a0\ufe0e"},
	}
	for _, tt := range tests {
		if got := PercentMarked(tt.in); got != tt.want {
			t.Errorf("PercentMarked(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
