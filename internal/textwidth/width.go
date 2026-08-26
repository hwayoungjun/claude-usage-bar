// Package textwidth measures strings the way a menu renders them. macOS gives
// us no control over menu item width, padding, or truncation — the only lever
// is the string itself — so every label is budgeted in display columns here.
package textwidth

// Rune returns how many columns a rune occupies: two for CJK and emoji, one
// for everything else. It is an approximation of East Asian width, kept as an
// explicit range list so no Unicode table is needed.
func Rune(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals, Kangxi, CJK punctuation
		r >= 0x3041 && r <= 0x33FF, // kana, Hangul compatibility, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK extension A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji
		r >= 0x1F900 && r <= 0x1F9FF:
		return 2
	}
	return 1
}

// Width is the total column count of s.
func Width(s string) int {
	w := 0
	for _, r := range s {
		w += Rune(r)
	}
	return w
}

// Truncate cuts s down to max columns, marking the cut with an ellipsis that
// takes one of them.
func Truncate(s string, max int) string {
	if max <= 1 {
		return "…"
	}
	if Width(s) <= max {
		return s
	}
	budget := max - 1
	w := 0
	var out []rune
	for _, r := range s {
		rw := Rune(r)
		if w+rw > budget {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}
