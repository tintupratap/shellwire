package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// getPTYPid returns the foreground process group PID of the PTY.
func getPTYPid(ptmx *os.File) (int, error) {
	pid, err := unix.IoctlRetInt(int(ptmx.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return 0, fmt.Errorf("TIOCGPGRP: %w", err)
	}
	return pid, nil
}
