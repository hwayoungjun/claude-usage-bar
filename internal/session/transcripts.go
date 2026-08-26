package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Transcripts lists sessions out of Claude Code's per-session transcript store.
//
// The obvious-looking source, ~/.claude/history.jsonl, is the terminal REPL's
// input buffer: sessions started from the Claude desktop app write nothing to
// it and never showed up. Transcripts cover every surface, and `claude
// --resume` works on any of them, so rows need no source distinction.
type Transcripts struct {
	Root string // usually ~/.claude/projects
}

// DefaultTranscripts points at the store in the user's home directory.
func DefaultTranscripts() Transcripts {
	home, _ := os.UserHomeDir()
	return Transcripts{Root: filepath.Join(home, ".claude", "projects")}
}

// headLines bounds how far into a transcript the opening prompt is looked for,
// so a long conversation costs no more than a short one.
const headLines = 200

type entry struct {
	Type        string `json:"type"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Cwd         string `json:"cwd"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Recent returns up to limit sessions, most recently active first. Transcripts
// are ranked by mtime and opened only until enough usable ones turn up, so the
// cost tracks limit rather than the size of the store.
func (t Transcripts) Recent(limit int) []Session {
	paths, err := filepath.Glob(filepath.Join(t.Root, "*", "*.jsonl"))
	if err != nil {
		return nil
	}

	type candidate struct {
		path     string
		modified int64
	}
	candidates := make([]candidate, 0, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{p, info.ModTime().Unix()})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modified > candidates[j].modified
	})

	var result []Session
	for _, c := range candidates {
		if len(result) >= limit {
			break
		}
		s, ok := readHead(c.path)
		if !ok || IsTempPath(s.Project) {
			continue
		}
		s.LastActive = c.modified
		result = append(result, s)
	}
	return result
}

// readHead pulls the session id, working directory and opening prompt out of a
// transcript. It reports false for a transcript with nothing worth showing —
// one whose only user records are subagent traffic or slash commands.
func readHead(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()

	s := Session{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for i := 0; i < headLines && scanner.Scan(); i++ {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if s.Project == "" {
			s.Project = e.Cwd
		}
		if s.FirstPrompt != "" || e.Type != "user" || e.IsMeta || e.IsSidechain {
			continue
		}
		s.FirstPrompt = PromptText(e.Message.Content)
	}
	if s.FirstPrompt == "" || s.Project == "" {
		return Session{}, false
	}
	return s, true
}
