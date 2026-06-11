//go:build windows

package linker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"skillmanage/internal/config"
)

// createPrimitive creates a directory junction on the same volume, or falls
// back to a copied tree when source and target are on different volumes
// (junctions cannot span volumes reliably) (KTD2/KTD12).
//
// NOTE: junction creation shells out to `cmd /c mklink /J` (unprivileged,
// directory-scoped). Precise junction target-reading via reparse tags
// (Microsoft/go-winio) is deferred until a Windows host is available to
// validate it; dangling detection meanwhile keys off the manifest source and
// os.ModeIrregular (see isLinkMode), which does not require reading the target.
func createPrimitive(source, target string) (config.LinkType, error) {
	if filepath.VolumeName(source) != filepath.VolumeName(target) {
		if err := copyTree(source, target); err != nil {
			return "", fmt.Errorf("copy fallback (cross-volume): %w", err)
		}
		return config.LinkCopy, nil
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", target, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("mklink /J failed: %w: %s", err, out)
	}
	return config.LinkJunction, nil
}

// isLinkMode reports whether a mode is one of our link kinds on Windows. Under
// the Go 1.23 winsymlink default, junctions report ModeIrregular (not
// ModeSymlink), so both are checked (KTD10).
func isLinkMode(mode os.FileMode) bool {
	return mode&(os.ModeSymlink|os.ModeIrregular) != 0
}

// readLinkTarget best-effort reads a link target. os.Readlink does not reliably
// resolve junctions on Windows; on failure we return an error and callers treat
// the target as unknown (conservative — e.g. signature adoption declines).
func readLinkTarget(path string) (string, error) {
	return os.Readlink(path)
}
