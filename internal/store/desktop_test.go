package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var parseNow = time.Unix(1_800_000_000, 0)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan-usage-history.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseDesktopHistoryTakesTheNewestSample(t *testing.T) {
	path := writeFile(t, `{"version":2,"samples":[
		{"t":1799999000000,"org":"o","u":{"fh":10,"sd":40}},
		{"t":1799999900000,"org":"o","u":{"fh":22,"sd":47}}]}`)

	got := parseDesktopHistory(path, parseNow)
	if got == nil {
		t.Fatal("want a reading")
	}
	if *got.FiveHour.UsedPercentage != 22 || *got.SevenDay.UsedPercentage != 47 {
		t.Errorf("wrong sample: %+v", got)
	}
	if got.UpdatedAt != 1799999900 {
		t.Errorf("UpdatedAt = %d, want the sample's millis converted to seconds", got.UpdatedAt)
	}
	// The desktop history carries no reset times, and must not invent any.
	if got.FiveHour.ResetsAt != nil || got.SevenDay.ResetsAt != nil {
		t.Error("reset times should stay unknown")
	}
}

// This is another app's file, so anything unexpected has to degrade to "no
// data" rather than a wrong number.
func TestParseDesktopHistoryRejectsBadInput(t *testing.T) {
	bad := []struct {
		name string
		body string
	}{
		{"truncated json", `{`},
		{"no samples", `{"version":2,"samples":[]}`},
		{"timestamp older than the feature", `{"version":2,"samples":[{"t":1,"u":{"fh":5,"sd":5}}]}`},
		{"timestamp in the future", `{"version":2,"samples":[{"t":9999999999000,"u":{"fh":5,"sd":5}}]}`},
		{"percentage over 100", `{"version":2,"samples":[{"t":1799999900000,"u":{"fh":900,"sd":5}}]}`},
		{"negative percentage", `{"version":2,"samples":[{"t":1799999900000,"u":{"fh":-1,"sd":5}}]}`},
		{"no windows at all", `{"version":2,"samples":[{"t":1799999900000,"u":{}}]}`},
		{"wrong shape", `{"version":2,"samples":{"t":1}}`},
	}
	for _, tt := range bad {
		if got := parseDesktopHistory(writeFile(t, tt.body), parseNow); got != nil {
			t.Errorf("%s: want nil, got %+v", tt.name, got)
		}
	}
	if got := parseDesktopHistory(filepath.Join(t.TempDir(), "absent.json"), parseNow); got != nil {
		t.Errorf("missing file: want nil, got %+v", got)
	}
}

// A malformed trailing sample costs freshness, not the whole file.
func TestParseDesktopHistoryWalksBackPastBadSamples(t *testing.T) {
	path := writeFile(t, `{"version":2,"samples":[
		{"t":1799999000000,"u":{"fh":10,"sd":40}},
		{"t":1799999900000,"u":{"fh":500,"sd":47}}]}`)

	got := parseDesktopHistory(path, parseNow)
	if got == nil || *got.FiveHour.UsedPercentage != 10 {
		t.Errorf("want the earlier good sample, got %+v", got)
	}
}

// A version bump that only adds fields must keep working; the shape checks are
// what actually guard us.
func TestParseDesktopHistoryToleratesNewerVersions(t *testing.T) {
	path := writeFile(t, `{"version":9,"extra":true,
		"samples":[{"t":1799999900000,"org":"o","note":"x","u":{"fh":12,"sd":34,"future":1}}]}`)

	got := parseDesktopHistory(path, parseNow)
	if got == nil || *got.FiveHour.UsedPercentage != 12 || *got.SevenDay.UsedPercentage != 34 {
		t.Errorf("want the sample parsed, got %+v", got)
	}
}

// One window present and the other missing is still a usable reading.
func TestParseDesktopHistoryAcceptsPartialSamples(t *testing.T) {
	path := writeFile(t, `{"version":2,"samples":[{"t":1799999900000,"u":{"sd":47}}]}`)
	got := parseDesktopHistory(path, parseNow)
	if got == nil || got.FiveHour.UsedPercentage != nil || *got.SevenDay.UsedPercentage != 47 {
		t.Errorf("got %+v", got)
	}
}

func TestDesktopHistoryCachesUntilTheFileChanges(t *testing.T) {
	path := writeFile(t, `{"version":2,"samples":[{"t":1799999900000,"u":{"fh":22,"sd":47}}]}`)
	h := &DesktopHistory{Path: path, Now: func() time.Time { return parseNow }}

	first := h.Read()
	if first == nil {
		t.Fatal("want a reading")
	}
	if second := h.Read(); second != first {
		t.Error("an unchanged file should hand back the cached reading")
	}

	// Rewriting with a new size and mtime must invalidate.
	if err := os.WriteFile(path, []byte(`{"version":2,"samples":[{"t":1799999900000,"u":{"fh":23,"sd":48}},{"t":1799999950000,"u":{"fh":24,"sd":49}}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if got := h.Read(); got == nil || *got.FiveHour.UsedPercentage != 24 {
		t.Errorf("want the rewritten file re-read, got %+v", got)
	}
}

func TestDesktopHistoryMissingFile(t *testing.T) {
	h := &DesktopHistory{Path: filepath.Join(t.TempDir(), "absent.json"), Now: func() time.Time { return parseNow }}
	if got := h.Read(); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}
