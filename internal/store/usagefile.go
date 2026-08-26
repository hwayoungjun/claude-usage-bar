package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hwayoungjun/claude-usage-bar/internal/usage"
)

// UsageFile is the reading captured from the statusLine hook, shared between
// the hook subcommand that writes it and the widget that watches it.
type UsageFile struct {
	Path string
}

// DefaultUsageFile points at the path under the user's config directory.
func DefaultUsageFile() UsageFile { return UsageFile{Path: UsagePath()} }

// Load reads the last statusLine reading. A missing, unparseable, or
// never-written file is an error, not an empty reading.
func (f UsageFile) Load() (*usage.Data, error) {
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	var d usage.Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.UpdatedAt == 0 {
		return nil, fmt.Errorf("no data")
	}
	return &d, nil
}

// Save replaces the file. The widget watches it, so writes are what wake it up.
func (f UsageFile) Save(d *usage.Data) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0755); err != nil {
		return err
	}
	out, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(f.Path, out, 0644)
}
