// Package pathutil holds the single source of truth for turning a skill
// directory name into a filesystem-safe link name (KTD3). Both the scanner
// (which produces link names) and the linker (which uses them as the dedupe
// key) import this, so the sanitization can never drift between them.
package pathutil

import "strings"

// windowsReservedChars are illegal in Windows filenames. Colons appear in
// namespaced skill names (e.g. ce:plan) and must be sanitized at the FS
// boundary even though the source directory name is usually already valid.
const windowsReservedChars = `\/:*?"<>|`

// SanitizePathName maps a skill directory name to a link name safe on macOS,
// Linux/WSL, and Windows. It replaces reserved/control characters with '-',
// strips trailing dots and spaces (rejected by Windows), and guards the
// reserved device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9) by prefixing '_'.
func SanitizePathName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20: // control characters
			b.WriteRune('-')
		case strings.ContainsRune(windowsReservedChars, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimRight(b.String(), " .")
	if s == "" {
		return "_"
	}
	if isWindowsReservedName(s) {
		return "_" + s
	}
	return s
}

func isWindowsReservedName(s string) bool {
	base := s
	if i := strings.IndexByte(s, '.'); i >= 0 {
		base = s[:i]
	}
	up := strings.ToUpper(base)
	switch up {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(up) == 4 && (strings.HasPrefix(up, "COM") || strings.HasPrefix(up, "LPT")) {
		if up[3] >= '1' && up[3] <= '9' {
			return true
		}
	}
	return false
}
