//go:build windows

package cli

import "errors"

// restartSelf is never reached on Windows: a running vsfleet.exe cannot be
// replaced there, so update.Detect never offers to run the upgrade.
func restartSelf() error {
	return errors.New("restarting is not supported on Windows")
}
