package fsio

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestWriteFileAtomicConcurrentReadersNeverSeeTornFile hammers a single path
// with many writers of distinct valid payloads while readers poll it; every
// successful read must be byte-identical to one of the payloads, proving the
// temp-file-plus-rename write is never observed half-written or empty.
func TestWriteFileAtomicConcurrentReadersNeverSeeTornFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")

	const writers = 8
	const iterations = 200
	payloads := make([][]byte, writers)
	valid := make(map[string]bool, writers)
	for i := range payloads {
		// Vary length substantially so a torn write cannot coincidentally
		// match a different full payload.
		payloads[i] = []byte(fmt.Sprintf("writer: %d\nvalue: %s\n", i, strings.Repeat("x", i*17)))
		valid[string(payloads[i])] = true
	}

	// Seed the file so readers observe a full payload from the very first read.
	if err := WriteFileAtomic(path, payloads[0], 0o600); err != nil {
		t.Fatal(err)
	}

	var readErr atomic.Value // error
	stop := make(chan struct{})

	var writersWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writersWG.Add(1)
		go func(idx int) {
			defer writersWG.Done()
			for i := 0; i < iterations; i++ {
				if err := WriteFileAtomic(path, payloads[idx], 0o600); err != nil {
					readErr.Store(fmt.Errorf("writer %d: %w", idx, err))
					return
				}
			}
		}(w)
	}

	var readersWG sync.WaitGroup
	const readers = 6
	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(path)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					readErr.Store(err)
					return
				}
				if len(data) == 0 {
					readErr.Store(fmt.Errorf("read observed an empty file"))
					return
				}
				if !valid[string(data)] {
					readErr.Store(fmt.Errorf("read observed a torn payload: %q", data))
					return
				}
			}
		}()
	}

	writersWG.Wait()
	close(stop)
	readersWG.Wait()

	if err, ok := readErr.Load().(error); ok && err != nil {
		t.Fatal(err)
	}

	// Permission must be applied to the final file.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}

	// No temp files may be left behind in the directory.
	assertNoTempFiles(t, dir, "state.yaml")
}

func assertNoTempFiles(t *testing.T, dir, base string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "."+base+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestWriteFileAtomicAppliesPermAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := WriteFileAtomic(path, []byte("a: 1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("perm = %v, want 0640", info.Mode().Perm())
	}
	// Overwrite with a different perm and payload; the rename must replace the
	// file in place without leaving temp artifacts.
	if err := WriteFileAtomic(path, []byte("a: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a: 2\n" {
		t.Fatalf("content = %q", data)
	}
	assertNoTempFiles(t, dir, "config.yaml")
}

func TestWriteFileAtomicErrorsWhenDirMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "state.yaml")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected error writing into a missing directory")
	}
}

// TestWriteFileAtomicErrorsWhenDirUnwritable exercises the CreateTemp failure
// path with a directory that exists but denies writes (0555), and proves the
// call neither creates the target nor leaves a temp file behind.
func TestWriteFileAtomicErrorsWhenDirUnwritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore write bits so t.TempDir cleanup can remove the directory.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	path := filepath.Join(dir, "state.yaml")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected error writing into a read-only directory")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("target must not exist after a failed write, stat err = %v", err)
	}
	assertNoTempFiles(t, dir, "state.yaml")
}

// TestWriteFileAtomicRenameFailureCleansTempAndLeavesTargetIntact drives the
// final Rename to fail by making the target path an existing non-empty
// directory. The write must report the error, remove its temp file, and leave
// the pre-existing target contents untouched (no partial/torn state).
func TestWriteFileAtomicRenameFailureCleansTempAndLeavesTargetIntact(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.yaml")

	// Make the target an existing directory containing a child, so rename of a
	// file over it fails on every platform.
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "child"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(target, []byte("new"), 0o600); err == nil {
		t.Fatal("expected error renaming a file over an existing directory")
	}

	// The pre-existing directory and its contents must be untouched.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("target directory was clobbered by a failed atomic write")
	}
	child, err := os.ReadFile(filepath.Join(target, "child"))
	if err != nil {
		t.Fatal(err)
	}
	if string(child) != "keep" {
		t.Fatalf("target contents mutated on failure: %q", child)
	}
	// The temp file must be cleaned up on the failure path.
	assertNoTempFiles(t, dir, "state.yaml")
}

// TestWithLockSerializesLoadModifySave runs concurrent read-increment-write
// cycles on a counter file under WithLock; the exclusive advisory lock must
// prevent lost updates so the final value equals the total number of
// increments.
func TestWithLockSerializesLoadModifySave(t *testing.T) {
	dir := t.TempDir()
	counterPath := filepath.Join(dir, "counter")
	lockPath := filepath.Join(dir, "counter.lock")

	if err := WriteFileAtomic(counterPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}

	const goroutines = 12
	const perGoroutine = 50
	var wg sync.WaitGroup
	var failure atomic.Value // error
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if err := WithLock(lockPath, func() error {
					data, err := os.ReadFile(counterPath)
					if err != nil {
						return err
					}
					n, err := strconv.Atoi(strings.TrimSpace(string(data)))
					if err != nil {
						return err
					}
					return WriteFileAtomic(counterPath, []byte(strconv.Itoa(n+1)), 0o600)
				}); err != nil {
					failure.Store(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err, ok := failure.Load().(error); ok && err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatal(err)
	}
	final, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if want := goroutines * perGoroutine; final != want {
		t.Fatalf("final counter = %d, want %d (lost update under WithLock)", final, want)
	}
}

func TestWithLockCreatesLockFileAndRunsFn(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "manifest.lock")
	ran := false
	if err := WithLock(lockPath, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn was not invoked under WithLock")
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file was not created: %v", err)
	}
}

func TestWithLockErrorsOnUnopenablePath(t *testing.T) {
	// A lock path whose parent directory does not exist cannot be opened.
	lockPath := filepath.Join(t.TempDir(), "missing-dir", "x.lock")
	called := false
	err := WithLock(lockPath, func() error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "open lock") {
		t.Fatalf("expected open lock error, got %v", err)
	}
	if called {
		t.Fatal("fn must not run when the lock cannot be opened")
	}
}
