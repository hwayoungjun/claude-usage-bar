// Package shellquote makes strings safe to put inside a shell command.
//
// Two places in this app hand a shell something it did not choose: the resume
// command copied to the clipboard for the user to paste, and the statusLine
// command written into Claude Code's settings, which Claude Code runs through a
// shell. Both interpolate paths that come from outside the program — a
// transcript's cwd field, a transcript's filename, the install location — so
// both need quoting rather than trust.
package shellquote

import "strings"

// safe holds the characters that need no quoting in any POSIX shell.
const safe = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" + "_./:=@%+-"

// Quote returns s as a single shell word. A string made only of unambiguous
// characters is returned unchanged, so a normal path stays readable — the point
// is that anything else, from a space to a command substitution, comes back
// inert.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsFunc(s, func(r rune) bool {
		return !strings.ContainsRune(safe, r)
	}) {
		return s
	}
	// Single quotes suppress every shell expansion; the only thing they cannot
	// contain is a single quote, which is closed, escaped, and reopened.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
