//go:build unix

package butterfish

import (
	"os"

	"golang.org/x/sys/unix"
)

func foregroundProcessGroupID(ptyFile *os.File) (int, error) {
	return unix.IoctlGetInt(int(ptyFile.Fd()), unix.TIOCGPGRP)
}

func processGroupID(pid int) (int, error) {
	return unix.Getpgid(pid)
}
