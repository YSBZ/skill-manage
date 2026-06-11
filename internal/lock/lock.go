// Package lock provides the single-instance guard: a flock on a lockfile in the
// central folder (KTD8). Port binding cannot be the guard because the free-port
// fallback (R21) lets two processes bind different ports and both run; the lock
// is acquired before any sync/reconcile so a second daemon cannot race the
// manifest and git trees.
package lock

import (
	"errors"
	"fmt"

	"github.com/gofrs/flock"
)

// ErrLocked means another instance already holds the lock.
var ErrLocked = errors.New("another skillmanage instance is already running")

// Lock is a held single-instance lock.
type Lock struct {
	fl *flock.Flock
}

// Acquire tries to take an exclusive lock on path. It returns ErrLocked if
// another process holds it. The OS releases the lock automatically if this
// process dies, so there is no stale-PID problem.
func Acquire(path string) (*Lock, error) {
	fl := flock.New(path)
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	if !ok {
		return nil, ErrLocked
	}
	return &Lock{fl: fl}, nil
}

// Release frees the lock.
func (l *Lock) Release() error {
	if l == nil || l.fl == nil {
		return nil
	}
	return l.fl.Unlock()
}
