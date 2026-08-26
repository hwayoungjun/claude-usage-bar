package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscript lays down one transcript file and stamps its mtime, which is
// what Recent ranks on.
func writeTranscript(t *testing.T, root, project, id string, age time.Duration, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, "slug-"+id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return path
}

func userLine(project, text string) string {
	return `{"type":"user","cwd":"` + project + `","message":{"content":"` + text + `"}}`
}

func TestRecentOrdersByLastActivity(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "/w/one", "aaa", 3*time.Hour, userLine("/w/one", "oldest"))
	writeTranscript(t, root, "/w/two", "bbb", time.Hour, userLine("/w/two", "middle"))
	writeTranscript(t, root, "/w/three", "ccc", time.Minute, userLine("/w/three", "newest"))

	got := Transcripts{Root: root}.Recent(5)
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	for i, want := range []string{"newest", "middle", "oldest"} {
		if got[i].FirstPrompt != want {
			t.Errorf("position %d = %q, want %q", i, got[i].FirstPrompt, want)
		}
	}
	if got[0].ID != "ccc" || got[0].Project != "/w/three" {
		t.Errorf("session metadata not picked up: %+v", got[0])
	}
	if got[0].LastActive == 0 {
		t.Error("LastActive not stamped")
	}
}

func TestRecentHonoursLimit(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"a", "b", "c", "d"} {
		writeTranscript(t, root, "/w/"+id, id, time.Minute, userLine("/w/"+id, "prompt "+id))
	}
	if got := (Transcripts{Root: root}).Recent(2); len(got) != 2 {
		t.Errorf("got %d sessions, want 2", len(got))
	}
}

// A transcript is skipped when nothing in it is worth showing, and the next
// candidate takes the slot rather than the list coming back short.
func TestRecentSkipsUnusableTranscripts(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "", "empty", time.Minute)
	writeTranscript(t, root, "/w/slash", "slashonly", 2*time.Minute,
		`{"type":"user","cwd":"/w/slash","message":{"content":"/insights"}}`)
	writeTranscript(t, root, "/w/sidechain", "sidechain", 3*time.Minute,
		`{"type":"user","cwd":"/w/sidechain","isSidechain":true,"message":{"content":"subagent work"}}`)
	writeTranscript(t, root, "/w/meta", "meta", 4*time.Minute,
		`{"type":"user","cwd":"/w/meta","isMeta":true,"message":{"content":"meta note"}}`)
	writeTranscript(t, root, "/w/broken", "broken", 5*time.Minute, `not json at all`)
	writeTranscript(t, root, "/w/real", "real", 6*time.Minute, userLine("/w/real", "actual prompt"))

	got := Transcripts{Root: root}.Recent(5)
	if len(got) != 1 || got[0].FirstPrompt != "actual prompt" {
		t.Fatalf("want only the usable transcript, got %+v", got)
	}
}

// A broken line must not stop the scan: the real prompt below it still counts.
func TestRecentSkipsBrokenLinesWithinATranscript(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "/w/x", "mixed", time.Minute,
		`{ not json`,
		`{"type":"system","cwd":"/w/x"}`,
		userLine("/w/x", "real prompt"))

	got := Transcripts{Root: root}.Recent(5)
	if len(got) != 1 || got[0].FirstPrompt != "real prompt" {
		t.Fatalf("got %+v", got)
	}
}

func TestRecentLeavesOutTempDirectories(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "/private/tmp/scratch", "probe", time.Second,
		userLine("/private/tmp/scratch", "reply with just: ok"))
	writeTranscript(t, root, "/w/real", "real", time.Minute, userLine("/w/real", "actual work"))

	got := Transcripts{Root: root}.Recent(5)
	if len(got) != 1 || got[0].FirstPrompt != "actual work" {
		t.Fatalf("temp-directory session should be left out, got %+v", got)
	}
}

func TestRecentOnMissingRoot(t *testing.T) {
	if got := (Transcripts{Root: filepath.Join(t.TempDir(), "absent")}).Recent(5); got != nil {
		t.Errorf("want nil for a missing store, got %+v", got)
	}
}

// The opening prompt is looked for in the head of the file only; a transcript
// that buries it deeper is skipped rather than scanned in full.
func TestRecentBoundsHowFarItReads(t *testing.T) {
	root := t.TempDir()
	lines := make([]string, 0, headLines+2)
	for i := 0; i < headLines+1; i++ {
		lines = append(lines, `{"type":"system","cwd":"/w/deep"}`)
	}
	lines = append(lines, userLine("/w/deep", "buried prompt"))
	writeTranscript(t, root, "/w/deep", "deep", time.Minute, lines...)

	if got := (Transcripts{Root: root}).Recent(5); len(got) != 0 {
		t.Errorf("want the transcript skipped, got %+v", got)
	}
}
