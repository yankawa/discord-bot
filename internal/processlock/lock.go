// Package processlock prevents concurrent processes from sharing a state file.
package processlock

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

func Acquire(stateFile string) (func() error, error) {
	abs, err := filepath.Abs(stateFile)
	if err != nil {
		return nil, fmt.Errorf("resolve state path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	// Resolve directory symlinks so aliases cannot create independent lock files.
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	abs = filepath.Join(dir, filepath.Base(abs))
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("STATE_FILE must be a regular path, not a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect state path: %w", err)
	}
	lock := flock.New(abs+".lock", flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock state file: %w", err)
	}
	if !ok {
		_ = lock.Close()
		return nil, fmt.Errorf("another process is using STATE_FILE %s", abs)
	}
	// Leave the lock inode in place. The operating system releases the lock on
	// exit/crash; deleting a live lock file would let a second process bypass it.
	return lock.Close, nil
}
