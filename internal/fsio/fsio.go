// Package fsio provides crash-safe file persistence primitives shared by the
// manifest and config writers.
package fsio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path so readers never observe a torn file:
// the bytes land in a temp file in the same directory which is then renamed
// over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, writeErr := tmp.Write(data)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// WithLock runs fn while holding an exclusive advisory lock on lockPath,
// serializing load-modify-save cycles across concurrent stave processes.
// The lock file is created if missing and left in place afterwards.
func WithLock(lockPath string, fn func() error) error {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	defer file.Close()
	if err := lockFile(file); err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer unlockFile(file)
	return fn()
}
