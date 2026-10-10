//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// restartSelf replaces this process with whatever "vsfleet" now resolves to,
// with the same arguments and environment. It goes through the name the
// operator ran rather than os.Executable: that is the resolved file, and
// Homebrew deletes the old version's directory as part of the upgrade.
func restartSelf() error {
	path := os.Args[0]
	if !strings.ContainsRune(path, os.PathSeparator) {
		p, err := exec.LookPath(path)
		if err != nil {
			return err
		}
		path = p
	}
	return syscall.Exec(path, os.Args, os.Environ())
}
