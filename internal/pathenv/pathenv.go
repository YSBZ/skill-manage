// Package pathenv hardens the process PATH so the daemon can find user-installed
// tools (npx, git, node) regardless of how it was launched. A daemon started
// from a GUI (Finder / Dock / launchd / a desktop wrapper) inherits only a
// minimal PATH on macOS (/usr/bin:/bin:/usr/sbin:/sbin) — the shell-managed tool
// dirs (Homebrew, nvm, fnm, volta, asdf) are absent, so exec.LookPath("npx")
// and friends fail even though they work fine from a terminal. Ensure merges the
// user's real login-shell PATH plus common tool locations into the live PATH.
package pathenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Ensure augments the process $PATH in place: current PATH first, then the
// user's login-shell PATH (best-effort), then a static set of common tool dirs —
// de-duplicated, order preserved. Best-effort and side-effecting; call once at
// startup before any exec.LookPath / tool invocation. No-op-safe to call again.
func Ensure() {
	merged := mergePaths(
		splitList(os.Getenv("PATH")),
		loginShellPath(),
		commonDirs(),
	)
	if len(merged) > 0 {
		_ = os.Setenv("PATH", strings.Join(merged, string(os.PathListSeparator)))
	}
}

// loginShellPath asks the user's login shell for its PATH (so nvm/fnm/volta/asdf
// shims set up in shell rc files are included). Returns nil on Windows, on
// timeout, or on any error. Markers fence the value off from rc-file banner noise.
func loginShellPath() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// -l (login) sources profile/rc where tool managers extend PATH; -i is needed
	// by some setups (nvm) to run. Markers isolate the value from any rc output.
	out, err := exec.CommandContext(ctx, shell, "-lic", `printf '__SMPATH__%s__SMEND__' "$PATH"`).Output()
	if err != nil {
		return nil
	}
	s := string(out)
	i := strings.Index(s, "__SMPATH__")
	j := strings.Index(s, "__SMEND__")
	if i < 0 || j < 0 || j < i {
		return nil
	}
	return splitList(s[i+len("__SMPATH__") : j])
}

// commonDirs is a static fallback of well-known tool locations, used even if the
// login shell can't be queried. Homebrew (both arches), and per-user bin dirs.
func commonDirs() []string {
	dirs := []string{
		"/opt/homebrew/bin", "/opt/homebrew/sbin", // Apple-silicon Homebrew
		"/usr/local/bin", "/usr/local/sbin", // Intel Homebrew / common
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, ".volta", "bin"),
		)
	}
	return dirs
}

// splitList splits a PATH-list string on the OS separator, dropping empties.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, string(os.PathListSeparator)) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mergePaths concatenates the lists, keeping first occurrence order and dropping
// duplicates.
func mergePaths(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range lists {
		for _, p := range list {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
