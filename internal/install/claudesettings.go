// Package install owns everything that touches the system outside this app's
// own config: the statusLine hook in Claude Code's settings, and the launchd
// job behind Launch at Login.
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeSettings edits Claude Code's settings file in place, preserving every
// key it does not own.
type ClaudeSettings struct {
	Path string
}

// EnsureStatusLine points the statusLine hook at command, and reports whether
// the file had to change.
func (c ClaudeSettings) EnsureStatusLine(command string) (bool, error) {
	settings, err := c.read()
	if err != nil {
		return false, err
	}

	if sl, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := sl["command"].(string); ok && cmd == command {
			return false, nil
		}
	}
	settings["statusLine"] = map[string]string{"type": "command", "command": command}

	if err := c.write(settings); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveStatusLine drops the hook when it points at a command containing
// marker, so an unrelated status line configured by the user survives. It
// reports whether anything was removed.
func (c ClaudeSettings) RemoveStatusLine(marker string) (bool, error) {
	raw, err := os.ReadFile(c.Path)
	if err != nil {
		return false, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("error parsing %s: %v", c.Path, err)
	}

	sl, ok := settings["statusLine"].(map[string]interface{})
	if !ok {
		return false, nil
	}
	cmd, ok := sl["command"].(string)
	if !ok || !strings.Contains(cmd, marker) {
		return false, nil
	}

	delete(settings, "statusLine")
	if err := c.write(settings); err != nil {
		return false, err
	}
	return true, nil
}

func (c ClaudeSettings) read() (map[string]interface{}, error) {
	raw, err := os.ReadFile(c.Path)
	if err != nil {
		return map[string]interface{}{}, nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("error parsing %s: %v", c.Path, err)
	}
	if settings == nil {
		settings = map[string]interface{}{}
	}
	return settings, nil
}

func (c ClaudeSettings) write(settings map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0755); err != nil {
		return fmt.Errorf("error creating directory %s: %v", filepath.Dir(c.Path), err)
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.Path, out, 0644); err != nil {
		return fmt.Errorf("error writing %s: %v", c.Path, err)
	}
	return nil
}
