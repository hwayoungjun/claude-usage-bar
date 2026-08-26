package textwidth

import "testing"

func TestWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"hangul syllables", "한글", 4},
		{"hangul jamo", "ᄀ", 2},
		{"mixed", "5h 사용량", 9}, // "5h " is 3, three syllables are 6
		{"kana", "かな", 4},
		{"cjk ideograph", "漢字", 4},
		{"fullwidth", "Ａ", 2},
		{"emoji", "🙂", 2},
		{"ellipsis is one column", "…", 1},
		{"accented latin stays narrow", "café", 4},
	}
	for _, tt := range tests {
		if got := Width(tt.in); got != tt.want {
			t.Errorf("%s: Width(%q) = %d, want %d", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"fits exactly", "hello", 5, "hello"},
		{"fits with room", "hello", 20, "hello"},
		{"ascii cut keeps room for ellipsis", "abcdefgh", 4, "abc…"},
		{"korean cut lands on a whole rune", "가나다라마", 5, "가나…"},
		{"never splits a wide rune", "가나다", 4, "가…"},
		{"degenerate budget", "anything", 1, "…"},
		{"zero budget", "anything", 0, "…"},
		{"empty input", "", 10, ""},
	}
	for _, tt := range tests {
		got := Truncate(tt.in, tt.max)
		if got != tt.want {
			t.Errorf("%s: Truncate(%q, %d) = %q, want %q", tt.name, tt.in, tt.max, got, tt.want)
		}
		if w := Width(got); tt.max > 1 && w > tt.max {
			t.Errorf("%s: result %q is %d columns, over the %d budget", tt.name, got, w, tt.max)
		}
	}
}

// A Korean and an English label given the same budget must come out at
// comparable visual widths — the whole reason this package exists. Wide runes
// are indivisible, so the two can differ by at most the one column a wide rune
// would have overflowed by.
func TestTruncateEqualisesScripts(t *testing.T) {
	const budget = 20
	korean := Truncate("가나다라마바사아자차카타파하가나다라마바사", budget)
	english := Truncate("the quick brown fox jumps over the lazy dog", budget)
	diff := Width(korean) - Width(english)
	if diff < -1 || diff > 1 {
		t.Errorf("width mismatch: korean %d (%q) vs english %d (%q)",
			Width(korean), korean, Width(english), english)
	}
}
