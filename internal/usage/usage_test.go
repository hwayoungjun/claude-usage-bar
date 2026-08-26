package usage

import (
	"testing"
	"time"
)

func pct(v float64) *float64 { return &v }
func ts(v int64) *int64      { return &v }

var now = time.Unix(1_800_000_000, 0)

func TestResolveNoReadings(t *testing.T) {
	got := Resolve(now, nil, nil)
	if got.Data != nil || got.Source != SourceNone {
		t.Errorf("want an empty snapshot, got %+v", got)
	}
}

func TestResolvePicksFresherReading(t *testing.T) {
	older := &Data{UpdatedAt: now.Unix() - 600, FiveHour: Rate{UsedPercentage: pct(10)}, Model: "Opus 5"}
	newer := &Data{UpdatedAt: now.Unix() - 60, FiveHour: Rate{UsedPercentage: pct(20)}}

	if got := Resolve(now, older, newer); got.Source != SourceDesktopApp || *got.Data.FiveHour.UsedPercentage != 20 {
		t.Errorf("desktop is fresher, want it to win: %+v", got)
	}
	if got := Resolve(now, newer, older); got.Source != SourceStatusLine || *got.Data.FiveHour.UsedPercentage != 20 {
		t.Errorf("statusLine is fresher, want it to win: %+v", got)
	}
}

// A tie goes to statusLine: it carries reset times and a model name, so there
// is nothing to gain from preferring the coarser source.
func TestResolveTieGoesToStatusLine(t *testing.T) {
	at := now.Unix() - 60
	got := Resolve(now, &Data{UpdatedAt: at}, &Data{UpdatedAt: at})
	if got.Source != SourceStatusLine {
		t.Errorf("Source = %v, want SourceStatusLine", got.Source)
	}
}

func TestResolveFallsBackToTheOnlyReading(t *testing.T) {
	desktop := &Data{UpdatedAt: now.Unix() - 60, SevenDay: Rate{UsedPercentage: pct(47)}}
	if got := Resolve(now, nil, desktop); got.Source != SourceDesktopApp || got.Data == nil {
		t.Errorf("want the desktop reading, got %+v", got)
	}

	statusLine := &Data{UpdatedAt: now.Unix() - 60, Model: "Opus 5"}
	if got := Resolve(now, statusLine, nil); got.Source != SourceStatusLine || got.Data == nil {
		t.Errorf("want the statusLine reading, got %+v", got)
	}
}

func TestResolveStaleness(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		desktop   bool
		wantStale bool
	}{
		{"fresh statusLine", 30 * time.Second, false, false},
		{"statusLine just inside", StatusLineStaleAfter - time.Second, false, false},
		{"statusLine past its limit", StatusLineStaleAfter + time.Second, false, true},
		// A 15 minute sample cadence must not read as idle.
		{"desktop sample age", 16 * time.Minute, true, false},
		{"desktop past its limit", DesktopStaleAfter + time.Second, true, true},
	}
	for _, tt := range tests {
		d := &Data{UpdatedAt: now.Add(-tt.age).Unix()}
		var got Snapshot
		if tt.desktop {
			got = Resolve(now, nil, d)
		} else {
			got = Resolve(now, d, nil)
		}
		if got.Stale != tt.wantStale {
			t.Errorf("%s: Stale = %v, want %v (age %v)", tt.name, got.Stale, tt.wantStale, got.Age)
		}
	}
}

func TestResolveCarriesResetTimesOntoDesktopReadings(t *testing.T) {
	future, past := now.Unix()+3600, now.Unix()-3600
	statusLine := &Data{
		UpdatedAt: now.Unix() - 600,
		FiveHour:  Rate{UsedPercentage: pct(10), ResetsAt: ts(past)},
		SevenDay:  Rate{UsedPercentage: pct(40), ResetsAt: ts(future)},
		Model:     "Opus 5",
	}
	desktop := &Data{
		UpdatedAt: now.Unix() - 60,
		FiveHour:  Rate{UsedPercentage: pct(0)},
		SevenDay:  Rate{UsedPercentage: pct(41)},
	}

	got := Resolve(now, statusLine, desktop)
	if got.Data.FiveHour.ResetsAt != nil {
		t.Error("a reset time that has already passed must be dropped, not shown")
	}
	if got.Data.SevenDay.ResetsAt == nil || *got.Data.SevenDay.ResetsAt != future {
		t.Error("a reset time still in the future should carry over")
	}
	if got.Source != SourceDesktopApp {
		t.Errorf("Source = %v, want SourceDesktopApp", got.Source)
	}
	if statusLine.SevenDay.UsedPercentage == nil || *statusLine.SevenDay.UsedPercentage != 40 {
		t.Error("merging must not mutate the inputs")
	}
}
