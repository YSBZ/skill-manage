//go:build windows

package linker

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"skillmanage/internal/config"
)

// createPrimitive creates a directory junction on the same volume, or falls
// back to a copied tree when source and target are on different volumes
// (junctions cannot span volumes reliably) (KTD2/KTD12).
//
// Junction creation shells out to `cmd /c mklink /J` (unprivileged,
// directory-scoped — no admin / Developer Mode needed). The target is read back
// via the reparse point (readLinkTarget), so ownership/idempotency and
// signature adoption work the same as symlinks on unix.
func createPrimitive(source, target string) (config.LinkType, error) {
	if filepath.VolumeName(source) != filepath.VolumeName(target) {
		if err := CopyTree(source, target); err != nil {
			return "", fmt.Errorf("copy fallback (cross-volume): %w", err)
		}
		return config.LinkCopy, nil
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", target, source)
	// Windowless host (-H=windowsgui): suppress the cmd console flash on every link.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
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

// maxReparseBuf is the maximum size of a reparse-point data buffer
// (MAXIMUM_REPARSE_DATA_BUFFER_SIZE = 16 KiB).
const maxReparseBuf = 16 * 1024

// readLinkTarget reads a link's target. It first tries os.Readlink (real
// symlinks), then falls back to reading the reparse point directly — directory
// junctions (mklink /J, IO_REPARSE_TAG_MOUNT_POINT) are NOT resolved by
// os.Readlink, and junctions are exactly what createPrimitive makes. Reading
// them back is what lets linkPointsAt (idempotency) and signature adoption
// (OwnedRoot/looksOurs) work on Windows instead of silently degrading.
func readLinkTarget(path string) (string, error) {
	if t, err := os.Readlink(path); err == nil && t != "" {
		return t, nil
	}
	return readReparseTarget(path)
}

// readReparseTarget opens path as a reparse point (without following it) and
// returns the substitute-name target of a mount-point (junction) or symlink
// reparse buffer, normalized to a plain path (NT prefixes stripped).
func readReparseTarget(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(
		p,
		0, // no access needed to query the reparse point
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, // open the link itself; allow dir handles
		0,
	)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	buf := make([]byte, maxReparseBuf)
	var n uint32
	if err := windows.DeviceIoControl(
		h, windows.FSCTL_GET_REPARSE_POINT,
		nil, 0, &buf[0], uint32(len(buf)), &n, nil,
	); err != nil {
		return "", err
	}
	return parseReparseTarget(buf[:n])
}

// parseReparseTarget extracts the substitute name from a REPARSE_DATA_BUFFER for
// a junction (mount point) or symlink, and strips NT object-manager prefixes so
// the result is a normal path (e.g. C:\path). Layout (little-endian):
//
//	ReparseTag u32 | ReparseDataLength u16 | Reserved u16            (8-byte header)
//	then SubstituteNameOffset u16 | SubstituteNameLength u16 |
//	     PrintNameOffset u16 | PrintNameLength u16                   (8 bytes)
//	symlink only: Flags u32                                          (4 bytes)
//	then PathBuffer (UTF-16), names located by the offsets/lengths above.
func parseReparseTarget(b []byte) (string, error) {
	if len(b) < 8 {
		return "", fmt.Errorf("reparse buffer too small (%d bytes)", len(b))
	}
	tag := binary.LittleEndian.Uint32(b[0:4])
	rest := b[8:] // skip the fixed header

	var pathStart int
	switch tag {
	case windows.IO_REPARSE_TAG_MOUNT_POINT:
		pathStart = 8 // 4 × u16, no Flags
	case windows.IO_REPARSE_TAG_SYMLINK:
		pathStart = 12 // 4 × u16 + Flags u32
	default:
		return "", fmt.Errorf("unsupported reparse tag 0x%08x", tag)
	}
	if len(rest) < pathStart {
		return "", fmt.Errorf("reparse buffer truncated (tag 0x%08x)", tag)
	}
	subOff := int(binary.LittleEndian.Uint16(rest[0:2]))
	subLen := int(binary.LittleEndian.Uint16(rest[2:4]))
	pathBuf := rest[pathStart:]
	if subOff+subLen > len(pathBuf) || subLen == 0 {
		return "", fmt.Errorf("reparse name out of range")
	}
	name := windows.UTF16ToString(utf16Bytes(pathBuf[subOff : subOff+subLen]))
	// Strip NT prefixes so callers compare against normal drive paths.
	name = strings.TrimPrefix(name, `\??\`)
	name = strings.TrimPrefix(name, `\\?\`)
	return name, nil
}

// utf16Bytes reinterprets a little-endian UTF-16 byte slice as []uint16.
func utf16Bytes(b []byte) []uint16 {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return u
}
