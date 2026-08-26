package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hwayoungjun/claude-usage-bar/internal/usage"
)

func TestUsageFileRoundTrip(t *testing.T) {
	five, seven := 21.0, 48.0
	resetFive, resetSeven := int64(1_800_003_600), int64(1_800_090_000)
	want := &usage.Data{
		UpdatedAt: 1_800_000_000,
		FiveHour:  usage.Rate{UsedPercentage: &five, ResetsAt: &resetFive},
		SevenDay:  usage.Rate{UsedPercentage: &seven, ResetsAt: &resetSeven},
		Model:     "Opus 5 (1M context)",
		SessionID: "abc-123",
	}

	// Save creates the directory too: the hook can run before the widget ever has.
	f := UsageFile{Path: filepath.Join(t.TempDir(), "nested", "usage.json")}
	if err := f.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt != want.UpdatedAt || got.Model != want.Model || got.SessionID != want.SessionID {
		t.Errorf("metadata lost: %+v", got)
	}
	if *got.FiveHour.UsedPercentage != five || *got.FiveHour.ResetsAt != resetFive {
		t.Errorf("5h window lost: %+v", got.FiveHour)
	}
	if *got.SevenDay.UsedPercentage != seven || *got.SevenDay.ResetsAt != resetSeven {
		t.Errorf("7d window lost: %+v", got.SevenDay)
	}
}

// An unknown window has to survive as unknown, not as zero.
func TestUsageFileKeepsUnknownWindows(t *testing.T) {
	f := UsageFile{Path: filepath.Join(t.TempDir(), "usage.json")}
	if err := f.Save(&usage.Data{UpdatedAt: 1_800_000_000, Model: "Opus 5"}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.FiveHour.UsedPercentage != nil || got.SevenDay.ResetsAt != nil {
		t.Errorf("want unknown windows preserved, got %+v", got)
	}
}

func TestUsageFileLoadErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := (UsageFile{Path: filepath.Join(dir, "absent.json")}).Load(); err == nil {
		t.Error("missing file should be an error")
	}

	broken := filepath.Join(dir, "broken.json")
	os.WriteFile(broken, []byte("{not json"), 0644)
	if _, err := (UsageFile{Path: broken}).Load(); err == nil {
		t.Error("unparseable file should be an error")
	}

	// A file that exists but was never stamped is not a reading.
	empty := filepath.Join(dir, "empty.json")
	os.WriteFile(empty, []byte(`{"five_hour":{"used_percentage":50}}`), 0644)
	if _, err := (UsageFile{Path: empty}).Load(); err == nil {
		t.Error("a reading without updated_at should be an error")
	}
}

// The file records session ids, so it should not be world-readable — including
// when it was created by a version that wrote it 0644.
func TestUsageFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte(`{"updated_at":1}`), 0644); err != nil {
		t.Fatal(err)
	}

	f := UsageFile{Path: path}
	if err := f.Save(&usage.Data{UpdatedAt: 1_800_000_000}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("mode = %04o, want 0600", mode)
	}
}
