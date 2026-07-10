//go:build windows

package fsio

import "os"

// Windows has no flock; the atomic rename in WriteFileAtomic still prevents
// torn reads, so cross-process serialization is best-effort there.
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
