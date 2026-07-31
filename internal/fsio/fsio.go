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
	// Best-effort cleanup: after a successful rename the temp file is gone
	// and this remove fails harmlessly.
	defer func() { _ = os.Remove(tmpName) }()
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

// WriteFileExclusive writes data to path but fails with os.ErrExist when path
// already exists, so concurrent creators of the same path get exactly one
// winner. The fast path keeps WriteFileAtomic's torn-file guarantees: the
// bytes land in a temp file which is hard-linked into place (link, unlike
// rename, refuses to replace an existing target). Filesystems without hard
// links (exFAT, SMB, some container mounts) reject the link, in which case
// the write degrades to writeFileExcl: same one-winner contract, minus the
// never-observe-a-partial-file guarantee during the write itself.
func WriteFileExclusive(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// The temp file must always be removed: on success the link leaves it as a
	// second name for the same inode, on failure it is an orphan either way.
	defer func() { _ = os.Remove(tmpName) }()
	_, writeErr := tmp.Write(data)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	linkErr := os.Link(tmpName, path)
	if linkErr == nil || errors.Is(linkErr, os.ErrExist) {
		return linkErr
	}
	// Any other link failure is treated as "hard links unsupported here"
	// (EPERM/ENOTSUP and friends vary by platform and mount); the O_EXCL
	// fallback re-surfaces genuine problems such as a vanished directory.
	return writeFileExcl(path, data, perm)
}

// writeFileExcl is the portable degrade for WriteFileExclusive: O_EXCL create
// keeps the one-winner contract without hard links, at the cost of writing
// the target in place.
func writeFileExcl(path string, data []byte, perm os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	// Chmod through the handle: the open above is subject to the umask.
	chmodErr := file.Chmod(perm)
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, chmodErr, closeErr); err != nil {
		// Remove the partial file so a failed create does not permanently
		// block retries behind os.ErrExist.
		_ = os.Remove(path)
		return err
	}
	return nil
}

// WithLock runs fn while holding an exclusive advisory lock on lockPath,
// serializing load-modify-save cycles across concurrent stave processes.
// The lock file is created if missing and left in place afterwards.
func WithLock(lockPath string, fn func() error) error {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	// Closing also releases the flock, so both deferred failures are moot.
	defer func() { _ = file.Close() }()
	if err := lockFile(file); err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() { _ = unlockFile(file) }()
	return fn()
}
