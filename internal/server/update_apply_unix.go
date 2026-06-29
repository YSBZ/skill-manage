//go:build !windows

package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// spawnUpdater writes a detached shell script that waits for THIS process (pid)
// to exit, then replaces the installed .app bundle with the freshly-downloaded,
// sha256-verified one and relaunches it. Detached via Setsid so it outlives our
// os.Exit. macOS only (the department's non-Windows desktop target). A
// self-downloaded zip carries no com.apple.quarantine, so the replaced app opens
// without a Gatekeeper prompt even unsigned; we also strip quarantine to be safe.
//
// Safe swap (no destructive window): the current bundle is RENAMED to a backup
// first, the new one moved into place, and only on success is the backup removed
// — if the install move fails, we roll the backup back so the user is never left
// without an app. All steps are logged to <central>/update.log so a silent
// failure (the updater is detached, no console) is diagnosable.
func spawnUpdater(zip, installPath string, pid int, centralDir string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("自更新暂仅支持 macOS 桌面端")
	}
	tmp := filepath.Join(centralDir, "update-tmp")
	work := filepath.Join(tmp, "stage")
	script := filepath.Join(tmp, "apply.sh")
	logPath := filepath.Join(centralDir, "update.log")
	body := `#!/bin/sh
ZIP="$1"; APP="$2"; PID="$3"; WORK="$4"; LOG="$5"
exec >>"$LOG" 2>&1
echo "[$(date)] updater start: pid=$PID app=$APP"
# wait (up to ~60s) for the running app to exit
i=0; while [ "$i" -lt 120 ]; do kill -0 "$PID" 2>/dev/null || break; sleep 0.5; i=$((i+1)); done
sleep 1
rm -rf "$WORK"; mkdir -p "$WORK"
if ! /usr/bin/ditto -x -k "$ZIP" "$WORK"; then
  echo "[$(date)] extract failed; relaunching existing app"; /usr/bin/open "$APP" 2>/dev/null; exit 1
fi
NEW=$(/usr/bin/find "$WORK" -maxdepth 4 -name "SkillManager.app" -type d | head -1)
if [ -z "$NEW" ]; then
  echo "[$(date)] SkillManager.app not found in package; relaunching existing app"; /usr/bin/open "$APP" 2>/dev/null; exit 1
fi
BAK="$APP.bak-$$"
# rename current out of the way first (only if it exists) — no destructive delete.
if [ -e "$APP" ]; then
  if ! /bin/mv "$APP" "$BAK"; then
    echo "[$(date)] backup move failed; relaunching existing app"; /usr/bin/open "$APP" 2>/dev/null; exit 1
  fi
fi
if /bin/mv "$NEW" "$APP"; then
  /usr/bin/xattr -dr com.apple.quarantine "$APP" 2>/dev/null
  rm -rf "$BAK"
  echo "[$(date)] swap ok"
else
  echo "[$(date)] install move failed; rolling back"
  rm -rf "$APP" 2>/dev/null
  [ -e "$BAK" ] && /bin/mv "$BAK" "$APP"
fi
/usr/bin/open "$APP"
rm -rf "$WORK" "$ZIP" "$0"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", script, zip, installPath, fmt.Sprint(pid), work, logPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach so it survives our exit
	return cmd.Start()
}
