package statusline

import (
	"strings"
	"testing"
	"time"
)

var now = time.Unix(1_800_000_000, 0)

func TestParseFullPayload(t *testing.T) {
	in := `{"session_id":"abc-123","model":{"display_name":"Opus 5 (1M context)"},
		"rate_limits":{"five_hour":{"used_percentage":21,"resets_at":1800003600},
		"seven_day":{"used_percentage":48,"resets_at":1800090000}}}`

	got, err := Parse(strings.NewReader(in), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt != now.Unix() {
		t.Errorf("UpdatedAt = %d, want the passed clock", got.UpdatedAt)
	}
	if got.SessionID != "abc-123" || got.Model != "Opus 5 (1M context)" {
		t.Errorf("metadata = %+v", got)
	}
	if *got.FiveHour.UsedPercentage != 21 || *got.FiveHour.ResetsAt != 1800003600 {
		t.Errorf("5h = %+v", got.FiveHour)
	}
	if *got.SevenDay.UsedPercentage != 48 || *got.SevenDay.ResetsAt != 1800090000 {
		t.Errorf("7d = %+v", got.SevenDay)
	}
}

// Claude Code omits rate_limits for non-subscribers, and until the first API
// response of a session. The reading is still worth keeping.
func TestParseWithoutRateLimits(t *testing.T) {
	got, err := Parse(strings.NewReader(`{"session_id":"abc","model":{"display_name":"Opus 5"}}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.FiveHour.UsedPercentage != nil || got.SevenDay.UsedPercentage != nil {
		t.Errorf("windows should stay unknown: %+v", got)
	}
	if got.Model != "Opus 5" || got.UpdatedAt != now.Unix() {
		t.Errorf("got %+v", got)
	}
}

func TestParsePartialPayloads(t *testing.T) {
	cases := map[string]string{
		"only one window": `{"rate_limits":{"five_hour":{"used_percentage":21}}}`,
		"no model":        `{"session_id":"abc"}`,
		"empty object":    `{}`,
		"unknown fields":  `{"future_field":1,"session_id":"abc"}`,
	}
	for name, in := range cases {
		if _, err := Parse(strings.NewReader(in), now); err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for name, in := range map[string]string{
		"truncated": `{"session_id":`,
		"not json":  `hello`,
		"empty":     ``,
	} {
		if _, err := Parse(strings.NewReader(in), now); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
