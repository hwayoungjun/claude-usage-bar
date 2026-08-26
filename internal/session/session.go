// Package session models the recent-sessions list: what a session row is, how
// it is laid out against the menu's column budget, and which sessions are worth
// listing. The rules here are pure; reading them off disk lives in this
// package's transcript adapter.
package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hwayoungjun/claude-usage-bar/internal/textwidth"
)

// Session is one row: enough to label it and to resume it.
type Session struct {
	ID          string
	Project     string // absolute working directory
	FirstPrompt string
	LastActive  int64 // unix seconds
}

const (
	// Session rows get a far bigger budget than the fixed rows above them: the
	// prompt is the only thing that tells two sessions apart, so it earns a wide
	// dropdown. Widths are display columns (see textwidth).
	//
	// These are caps, not padding — macOS sizes the menu to the widest row it
	// actually draws. Wider budgets were tried and walked back: a long prompt
	// stretched the dropdown far past every other row, which read worse than the
	// truncation it avoided. 44 keeps session rows just under the widest fixed
	// row above them, so they never decide how wide the menu is drawn.
	RowWidth     = 44
	ProjectWidth = 16
	PromptMin    = 20
)

// ProjectName is the leaf of a working directory, which is what a row shows.
func ProjectName(path string) string {
	return filepath.Base(path)
}

// Label lays out one row against the row budget. Both halves are trimmed: an
// unbounded project name would push the row wide and still leave the prompt cut
// early, which reads as the right side clipping before the left. When every
// visible session shares a project the caller passes withProject=false and the
// whole budget goes to the prompt.
func (s Session) Label(withProject bool) string {
	if !withProject {
		return textwidth.Truncate(s.FirstPrompt, RowWidth)
	}
	project := textwidth.Truncate(ProjectName(s.Project), ProjectWidth)
	room := RowWidth - textwidth.Width(project) - textwidth.Width("[] ")
	if room < PromptMin {
		room = PromptMin
	}
	return fmt.Sprintf("[%s] %s", project, textwidth.Truncate(s.FirstPrompt, room))
}

// Tooltip carries what a row cannot: the full project path and whole prompt.
func (s Session) Tooltip() string {
	return s.Project + " · " + s.FirstPrompt
}

// SharedProject returns the project every session belongs to, or "" when they
// differ. Repeating one identical "[project]" on every row spends columns the
// prompt can use instead, so a shared project moves up to the section header.
// Full paths are compared, so two different projects that happen to share a
// folder name stay distinct.
func SharedProject(sessions []Session) string {
	if len(sessions) == 0 {
		return ""
	}
	first := sessions[0].Project
	for _, s := range sessions[1:] {
		if s.Project != first {
			return ""
		}
	}
	return ProjectName(first)
}

// Sessions run out of a temp directory — scratch work, one-off probes, harness
// scratchpads — are not projects anyone comes back to.
var tempRoots = []string{
	"/tmp", "/private/tmp",
	"/var/tmp", "/private/var/tmp",
	"/var/folders", "/private/var/folders",
}

// IsTempPath reports whether path sits under a temp root. A real project that
// merely has "tmp" in its name (~/tmp/thing) is not one.
func IsTempPath(path string) bool {
	if path == "" {
		return false
	}
	path = filepath.Clean(path)
	for _, root := range tempRoots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// PromptText reduces a transcript message's content — a plain string, or a list
// of blocks — to the one line worth putting in a menu. Slash commands and the
// XML-tagged envelopes Claude Code injects (command output, reminders) are not
// what the user typed, so they yield "" and the caller keeps looking.
func PromptText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return ""
		}
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				text = b.Text
				break
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "<") || strings.HasPrefix(line, "/") || line == "exit" {
			return ""
		}
		return line
	}
	return ""
}
