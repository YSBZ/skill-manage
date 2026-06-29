package lock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSingleInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	l1, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// second acquire on the same path must fail while the first is held
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire = %v, want ErrLocked", err)
	}

	// after release, acquiring again succeeds
	if err := l1.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("re-Acquire after release: %v", err)
	}
	_ = l2.Release()
}
