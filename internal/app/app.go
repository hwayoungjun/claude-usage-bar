// Package app holds the identifiers every layer shares. It depends on nothing
// so that domain and adapters can both name the app without depending on each
// other.
package app

const (
	// Name is the binary name, the config directory name, and the marker the
	// uninstaller looks for in Claude Code's settings.
	Name = "claude-usage-bar"

	// LaunchAgentLabel is our own launchd job. HomebrewLaunchAgentLabel is the
	// one `brew services` installs; when that exists, launch-at-login is out of
	// our hands.
	LaunchAgentLabel         = "com.github.hwayoungjun." + Name
	HomebrewLaunchAgentLabel = "homebrew.mxcl." + Name
)
