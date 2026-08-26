package shellquote

import (
	"os/exec"
	"strings"
	"testing"
)

func TestQuoteLeavesOrdinaryWordsAlone(t *testing.T) {
	for _, s := range []string{
		"abc", "/Users/me/work", "session-id-123",
		"/opt/homebrew/bin/claude-usage-bar", "a.b/c-d_e:f=g@h%i+j",
	} {
		if got := Quote(s); got != s {
			t.Errorf("Quote(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestQuoteWrapsAnythingElse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "''"},
		{"space", "/Users/me/My Project", `'/Users/me/My Project'`},
		{"command chaining", "/tmp/x; echo pwned", `'/tmp/x; echo pwned'`},
		{"command substitution", "$(id)", `'$(id)'`},
		{"backticks", "`id`", "'`id`'"},
		{"pipe", "a|b", `'a|b'`},
		{"newline", "a\nb", "'a\nb'"},
		{"single quote", "it's", `'it'\''s'`},
		{"only a single quote", "'", `''\'''`},
		{"non-ascii", "/Users/me/한글", `'/Users/me/한글'`},
	}
	for _, tt := range tests {
		if got := Quote(tt.in); got != tt.want {
			t.Errorf("%s: Quote(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

// The real requirement is behavioural: whatever goes in comes back out as one
// argument, with nothing executed along the way.
func TestQuotedStringSurvivesTheShellIntact(t *testing.T) {
	inputs := []string{
		"plain",
		"/Users/me/My Project",
		"/tmp/x; echo INJECTED",
		"$(echo INJECTED)",
		"`echo INJECTED`",
		"it's got a quote",
		"a|b&c>d<e*f?g[h]i{j}k",
		"trailing backslash \\",
		"/Users/me/한글 경로",
	}
	for _, in := range inputs {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+Quote(in)).Output()
		if err != nil {
			t.Errorf("Quote(%q) produced something the shell rejected: %v", in, err)
			continue
		}
		if string(out) != in {
			t.Errorf("Quote(%q) came back as %q", in, out)
		}
		if strings.Contains(string(out), "INJECTED") && !strings.Contains(in, "INJECTED") {
			t.Errorf("Quote(%q) executed something", in)
		}
	}
}
