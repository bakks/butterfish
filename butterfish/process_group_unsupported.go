//go:build !unix

package butterfish

import (
	"fmt"
	"os"
)

func foregroundProcessGroupID(_ *os.File) (int, error) {
	return 0, fmt.Errorf("PTY foreground process group lookup is unsupported")
}

func processGroupID(_ int) (int, error) {
	return 0, fmt.Errorf("process group lookup is unsupported")
}
