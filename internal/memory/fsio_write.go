package memory

import (
	"github.com/Nurozen/stave/internal/fsio"
)

func writeFileAtomic(path string, data []byte) error {
	return fsio.WriteFileAtomic(path, data, 0o644)
}
