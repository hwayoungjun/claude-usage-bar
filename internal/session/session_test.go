package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hwayoungjun/claude-usage-bar/internal/textwidth"
)

func TestLabelWithoutProject(t *testing.T) {
	s := Session{Project: "/Users/me/work", FirstPrompt: "짧은 프롬프트"}
	if got := s.Label(false); got != "짧은 프롬프트" {
		t.Errorf("Label(false) = %q, want the bare prompt", got)
	}
	long := Session{FirstPrompt: strings.Repeat("가", 100)}
	if w := textwidth.Width(long.Label(false)); w > RowWidth {
		t.Errorf("row is %d columns, over the %d budget", w, RowWidth)
	}
}

func TestLabelWithProject(t *testing.T) {
	s := Session{Project: "/Users/me/keepgrow-claude", FirstPrompt: "미커밋 확인"}
	got := s.Label(true)
	if !strings.HasPrefix(got, "[keepgrow-claude] ") {
		t.Errorf("Label(true) = %q, want the project prefixed", got)
	}
}

// Both halves have to be trimmed. An unbounded project name used to push the
// row wide while the prompt was cut on its own budget.
func TestLabelStaysInsideBudget(t *testing.T) {
	cases := []Session{
		{Project: "/x/" + strings.Repeat("long-project-", 5), FirstPrompt: strings.Repeat("가", 60)},
		{Project: "/x/" + strings.Repeat("긴프로젝트", 6), FirstPrompt: "short"},
		{Project: "/x/a", FirstPrompt: strings.Repeat("word ", 40)},
	}
	for _, s := range cases {
		if w := textwidth.Width(s.Label(true)); w > RowWidth {
			t.Errorf("row is %d columns, over the %d budget: %q", w, RowWidth, s.Label(true))
		}
	}
}

// Even with a project name that eats the whole budget, the prompt keeps a floor
// so a row never degenerates to a lone ellipsis.
func TestLabelKeepsPromptFloor(t *testing.T) {
	s := Session{Project: "/x/" + strings.Repeat("가", 40), FirstPrompt: strings.Repeat("나", 40)}
	got := s.Label(true)
	prompt := got[strings.Index(got, "] ")+len("] "):]
	if w := textwidth.Width(prompt); w < PromptMin-1 {
		t.Errorf("prompt got %d columns, want at least %d: %q", w, PromptMin, got)
	}
}

func TestSharedProject(t *testing.T) {
	tests := []struct {
		name string
		in   []Session
		want string
	}{
		{"none", nil, ""},
		{"single", []Session{{Project: "/a/one"}}, "one"},
		{"all the same", []Session{{Project: "/a/one"}, {Project: "/a/one"}}, "one"},
		{"different", []Session{{Project: "/a/one"}, {Project: "/a/two"}}, ""},
		// Same leaf, different projects: the rows must keep their prefixes.
		{"same folder name under different parents",
			[]Session{{Project: "/work/api"}, {Project: "/side/api"}}, ""},
	}
	for _, tt := range tests {
		if got := SharedProject(tt.in); got != tt.want {
			t.Errorf("%s: SharedProject = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestIsTempPath(t *testing.T) {
	temp := []string{
		"/tmp", "/tmp/x", "/private/tmp/claude-501/scratch",
		"/var/tmp/y", "/private/var/tmp/y",
		"/var/folders/zz/T/probe", "/private/var/folders/zz/T/probe",
		"/tmp/../tmp/z",
	}
	for _, p := range temp {
		if !IsTempPath(p) {
			t.Errorf("IsTempPath(%q) = false, want true", p)
		}
	}
	// A real project that merely has "tmp" in the name is not temporary.
	notTemp := []string{"", "/Users/me/tmp/real-project", "/Users/me/work", "/tmpfoo", "/var/folder"}
	for _, p := range notTemp {
		if IsTempPath(p) {
			t.Errorf("IsTempPath(%q) = true, want false", p)
		}
	}
}

func TestPromptText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain string", `"확인해줘"`, "확인해줘"},
		{"blocks", `[{"type":"text","text":"블록 프롬프트"}]`, "블록 프롬프트"},
		{"blocks skip non-text", `[{"type":"image"},{"type":"text","text":"after"}]`, "after"},
		{"first line only", `"첫 줄\n둘째 줄"`, "첫 줄"},
		{"leading blank lines", `"\n\n실제 내용"`, "실제 내용"},
		{"slash command is not a prompt", `"/insights"`, ""},
		{"exit is not a prompt", `"exit"`, ""},
		{"injected envelope is not a prompt", `"<command-name>/foo</command-name>"`, ""},
		{"empty", `""`, ""},
		{"whitespace only", `"   \n  "`, ""},
		{"unexpected shape", `{"unexpected":true}`, ""},
		{"null", `null`, ""},
	}
	for _, tt := range tests {
		if got := PromptText(json.RawMessage(tt.raw)); got != tt.want {
			t.Errorf("%s: PromptText(%s) = %q, want %q", tt.name, tt.raw, got, tt.want)
		}
	}
}
