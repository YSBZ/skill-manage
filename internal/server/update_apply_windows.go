//go:build windows

package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// createBreakawayFromJob lets the updater escape any kill-on-close Job Object the
// app (or its WebView2 host) belongs to — without it a helper can be terminated
// the instant the parent exits, which looks like "update said restarting but
// nothing happened". createNoWindow (from runner_windows.go) gives the helper a
// HIDDEN console so the cmd batch's console tools (tasklist/tar/where/copy) work
// while nothing flashes on screen. We deliberately do NOT use DETACHED_PROCESS
// here: a batch driving console utilities needs a console.
const createBreakawayFromJob = 0x01000000

// spawnUpdater writes a detached cmd BATCH that waits for THIS process (pid) to
// exit, extracts the freshly-downloaded, sha256-verified package, swaps the
// installed SkillManager.exe, and relaunches it.
//
// Why a .bat (not PowerShell): on locked-down corporate Windows, Group Policy /
// AppLocker / Constrained Language Mode can refuse to run an unsigned .ps1 even
// with -ExecutionPolicy Bypass — powershell launches but does nothing, so the
// swap silently never happens (observed in the wild: apply.ps1 + download.zip
// present, but no extraction, no relaunch, no log). cmd batch is not subject to
// PowerShell execution policy, and extraction uses the built-in tar.exe (Win10
// 1803+), so no PowerShell at all. Robustness: capped wait (guards PID reuse),
// copy retries (exe can stay briefly locked by WebView2 children / AV), backup +
// rollback, and always relaunch. Everything is logged to <central>/update.log.
func spawnUpdater(zip, installPath string, pid int, centralDir string) error {
	tmp := filepath.Join(centralDir, "update-tmp")
	work := filepath.Join(tmp, "stage")
	script := filepath.Join(tmp, "apply.bat")
	logPath := filepath.Join(centralDir, "update.log")
	body := `@echo off
setlocal enabledelayedexpansion
set "ZIP=%~1"
set "EXE=%~2"
set "PID=%~3"
set "WORK=%~4"
set "LOG=%~5"
call :log updater start pid=%PID% exe=%EXE%
set /a n=0
:wait
tasklist /fi "PID eq %PID%" /nh 2>nul | find "%PID%" >nul
if errorlevel 1 goto exited
set /a n+=1
if %n% geq 60 goto exited
ping -n 2 127.0.0.1 >nul
goto wait
:exited
call :log waited n=%n% for pid to exit
ping -n 2 127.0.0.1 >nul
rmdir /s /q "%WORK%" 2>nul
mkdir "%WORK%" 2>nul
call :log extracting with tar
tar -xf "%ZIP%" -C "%WORK%" 2>nul
if errorlevel 1 call :log WARN tar returned error
set "NEW="
for /f "delims=" %%F in ('where /r "%WORK%" SkillManager.exe 2^>nul') do if not defined NEW set "NEW=%%F"
if not defined NEW (
  call :log ERROR new exe not found in package; relaunching old
  start "" "%EXE%"
  goto done
)
call :log new exe=!NEW!
copy /y "%EXE%" "%EXE%.bak" >nul 2>&1
set /a c=0
:copy
copy /y "!NEW!" "%EXE%" >nul 2>&1
if not errorlevel 1 goto copyok
set /a c+=1
call :log copy attempt !c! failed
if !c! geq 10 goto copyfail
ping -n 2 127.0.0.1 >nul
goto copy
:copyfail
call :log ERROR all copy attempts failed; rolling back
copy /y "%EXE%.bak" "%EXE%" >nul 2>&1
goto relaunch
:copyok
call :log copied new exe ok
:relaunch
del /q "%EXE%.bak" >nul 2>&1
call :log relaunching %EXE%
start "" "%EXE%"
:done
rmdir /s /q "%WORK%" 2>nul
del /q "%ZIP%" >nul 2>&1
call :log updater done
endlocal
exit /b
:log
echo [%date% %time%] %*>> "%LOG%"
goto :eof
`
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		return err
	}
	args := []string{"/c", script, zip, installPath, fmt.Sprint(pid), work, logPath}
	// Try to break away from any kill-on-close job first; if the job forbids
	// breakaway, CreateProcess fails — fall back to a plain hidden-console launch.
	mk := func(flags uint32) *exec.Cmd {
		c := exec.Command("cmd", args...)
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		return c
	}
	cmd := mk(createNoWindow | createBreakawayFromJob)
	if err := cmd.Start(); err != nil {
		return mk(createNoWindow).Start()
	}
	return nil
}
