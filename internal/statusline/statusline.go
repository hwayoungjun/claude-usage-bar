// Package statusline implements the hook Claude Code runs on every assistant
// message. It exists to capture the rate limit data the hook carries, not to
// draw anything: the statusLine slot in Claude Code's settings holds one
// command, and this app claims it to read. Printing nothing leaves the row
// under the input box to Claude Code.
package statusline

import (
	"encoding/json"
	"io"
	"time"

	"github.com/hwayoungjun/claude-usage-bar/internal/usage"
)

// Input is the part of the hook payload this app uses. Rate limits are absent
// for non-subscribers and until the first API response of a session.
type Input struct {
	// The payload also declares seven_day_opus, seven_day_sonnet,
	// seven_day_oauth_apps, a model_scoped list the server labels itself, and an
	// extra_usage credit budget. None are read here. On the one account this
	// could be checked against — a Team seat on the Max 5x limit tier, against
	// Claude Code 2.1.247 — every one of them arrived null or absent, so there
	// was nothing to draw and no live value to learn the unfamiliar shapes from
	// (model_scoped reports a utilization in unstated units and a reset time
	// typed as a string rather than the epoch seconds used below). Should a row
	// ever turn out to be missing, they take the same shape as these two.
	RateLimits *struct {
		FiveHour *usage.Rate `json:"five_hour"`
		SevenDay *usage.Rate `json:"seven_day"`
	} `json:"rate_limits"`
	Model *struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	SessionID string `json:"session_id"`
}

// Parse turns one hook payload into a reading stamped with now. A payload
// without rate limits still yields a reading: the model and session id are
// worth recording, and the windows stay unknown.
func Parse(r io.Reader, now time.Time) (*usage.Data, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}

	d := &usage.Data{UpdatedAt: now.Unix(), SessionID: in.SessionID}
	if in.Model != nil {
		d.Model = in.Model.DisplayName
	}
	if in.RateLimits != nil {
		if in.RateLimits.FiveHour != nil {
			d.FiveHour = *in.RateLimits.FiveHour
		}
		if in.RateLimits.SevenDay != nil {
			d.SevenDay = *in.RateLimits.SevenDay
		}
	}
	return d, nil
}
