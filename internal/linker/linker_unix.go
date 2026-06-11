//go:build !windows

package linker

import (
	"os"

	"skillmanage/internal/config"
)

// createPrimitive creates a symlink on macOS/Linux/WSL.
func createPrimitive(source, target string) (config.LinkType, error) {
	if err := os.Symlink(source, target); err != nil {
		return "", err
	}
	return config.LinkSymlink, nil
}

// isLinkMode reports whether a mode is one of our link kinds on Unix.
func isLinkMode(mode os.FileMode) bool {
	return mode&os.ModeSymlink != 0
}

// readLinkTarget returns a symlink's target.
func readLinkTarget(path string) (string, error) {
	return os.Readlink(path)
}
