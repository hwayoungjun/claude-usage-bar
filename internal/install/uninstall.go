package install

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Uninstaller removes everything this app put on the system. Steps are
// independent: a failure in one is reported and the rest still run.
type Uninstaller struct {
	Agent     LaunchAgent
	Settings  ClaudeSettings
	ConfigDir string

	// Marker identifies our own statusLine command, so a status line the user
	// configured for something else is left alone.
	Marker string
}

// Run performs the uninstall, narrating each step to out.
func (u Uninstaller) Run(out io.Writer) {
	if u.Agent.IsInstalled() {
		if err := u.Agent.Remove(); err != nil {
			fmt.Fprintf(out, "  ✗ Failed to remove LaunchAgent: %v\n", err)
		} else {
			fmt.Fprintln(out, "  ✓ Removed LaunchAgent")
		}
	} else {
		fmt.Fprintln(out, "  - LaunchAgent not found (skipped)")
	}

	switch removed, err := u.Settings.RemoveStatusLine(u.Marker); {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(out, "  - Settings file not found (skipped)")
	case err != nil:
		fmt.Fprintf(out, "  ✗ Failed to update %s: %v\n", u.Settings.Path, err)
	case removed:
		fmt.Fprintln(out, "  ✓ Removed statusLine from", u.Settings.Path)
	default:
		fmt.Fprintln(out, "  - statusLine not ours or not set (skipped)")
	}

	if _, err := os.Stat(u.ConfigDir); err == nil {
		if err := os.RemoveAll(u.ConfigDir); err != nil {
			fmt.Fprintf(out, "  ✗ Failed to remove %s: %v\n", u.ConfigDir, err)
		} else {
			fmt.Fprintln(out, "  ✓ Removed", u.ConfigDir)
		}
	} else {
		fmt.Fprintln(out, "  - Config directory not found (skipped)")
	}
}
