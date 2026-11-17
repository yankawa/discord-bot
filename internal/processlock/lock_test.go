package processlock

import (
	"path/filepath"
	"testing"
)

func TestExclusiveAndReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	release, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire(path); err == nil {
		_ = second()
		t.Fatal("second process lock unexpectedly succeeded")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	again, err := Acquire(path)
	if err != nil {
		t.Fatalf("released lock cannot be reacquired: %v", err)
	}
	if err := again(); err != nil {
		t.Fatal(err)
	}
}
