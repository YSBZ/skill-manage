package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultCentralDir is the default central folder: ~/.skillmanage (R22).
func DefaultCentralDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".skillmanage"), nil
}

// LockfilePath is the single-instance lockfile inside the central folder
// (KTD8). It always lives on native FS because the central folder does.
func LockfilePath(centralDir string) string {
	return filepath.Join(centralDir, "lock")
}

// AddressPath is where the daemon writes its resolved UI bind address on
// startup so an autostart-launched (terminal-less) instance is discoverable
// (KTD8).
func AddressPath(centralDir string) string {
	return filepath.Join(centralDir, "address")
}

// TokenPath is where the API bearer token is stored (0600), generated on first
// run (KTD11/R23).
func TokenPath(centralDir string) string {
	return filepath.Join(centralDir, "token")
}

// CredentialsPath is where per-host HTTPS credentials are stored (0600),
// machine-local and NEVER part of export/import (like the manifest and token).
func CredentialsPath(centralDir string) string {
	return filepath.Join(centralDir, "credentials.yaml")
}

// LastSyncPath records the timestamp of the last successful sync, so the
// scheduler's startup missed-run check has a real value to compare against
// (KTD7) instead of assuming "just ran".
func LastSyncPath(centralDir string) string {
	return filepath.Join(centralDir, "last-sync")
}
