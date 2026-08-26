package main

import (
	"runtime/debug"
	"testing"
)

func TestRevisionFrom(t *testing.T) {
	tests := []struct {
		name string
		in   []debug.BuildSetting
		want string
	}{
		{"clean checkout",
			[]debug.BuildSetting{
				{Key: "vcs.revision", Value: "bf15b38a1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f"},
				{Key: "vcs.modified", Value: "false"},
			},
			"bf15b38a1c2d"},
		{"uncommitted changes",
			[]debug.BuildSetting{
				{Key: "vcs.revision", Value: "bf15b38a1c2d3e4f"},
				{Key: "vcs.modified", Value: "true"},
			},
			"bf15b38a1c2d-dirty"},
		{"short revision is left alone",
			[]debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}},
			"abc123"},
		{"no vcs stamp", []debug.BuildSetting{{Key: "GOARCH", Value: "arm64"}}, ""},
		{"nothing at all", nil, ""},
	}
	for _, tt := range tests {
		if got := revisionFrom(tt.in); got != tt.want {
			t.Errorf("%s: revisionFrom = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A stamped build wins over the embedded revision; an unstamped one still
// reports something usable rather than an empty string.
func TestBuildVersion(t *testing.T) {
	original := version
	defer func() { version = original }()

	version = "v0.8.1"
	if got := buildVersion(); got != "v0.8.1" {
		t.Errorf("stamped build = %q, want the stamp", got)
	}

	version = ""
	if got := buildVersion(); got == "" {
		t.Error("unstamped build should still report something")
	}
}
