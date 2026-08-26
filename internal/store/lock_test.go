package store

import (
	"path/filepath"
	"testing"
)

func TestLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "lock")

	first := &Lock{Path: path}
	if !first.Acquire() {
		t.Fatal("first Acquire should succeed")
	}
	// Held by this process through a different descriptor: flock is per-open-file,
	// so a second acquisition of the same path must fail.
	if (&Lock{Path: path}).Acquire() {
		t.Error("second Acquire should fail while the first is held")
	}
	if !(&Lock{Path: path}).HeldElsewhere() {
		t.Error("HeldElsewhere should report the held lock")
	}
}

func TestHeldElsewhereDoesNotReserve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	probe := &Lock{Path: path}
	if probe.HeldElsewhere() {
		t.Fatal("a fresh lock should be free")
	}
	// The probe must not have taken the slot it just checked.
	if !(&Lock{Path: path}).Acquire() {
		t.Error("Acquire after a probe should succeed")
	}
}
