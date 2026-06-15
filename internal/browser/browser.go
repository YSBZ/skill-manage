// Package browser opens the OS default web browser at a URL. It is best-effort:
// the daemon also prints (and logs) the URL, so a failure here is never fatal.
// Keeping it dependency-free and runtime-dispatched (no build tags) keeps the
// single-binary cross-compile simple.
package browser

import (
	"os/exec"
	"runtime"
)

// Open launches the default browser at url without blocking. On Windows it uses
// the URL protocol handler (works from a windowless GUI build); macOS uses
// `open`; everything else uses `xdg-open`.
func Open(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
