//go:build windows

package daemon

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procName returns the lowercased base image name of pid (e.g. "skillmanage.exe"),
// or "" if the process can't be opened/queried. Used as the SAFETY GATE before any
// termination: we only ever kill a process we've confirmed is SkillManage's own.
func procName(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	full := windows.UTF16ToString(buf[:n])
	if i := strings.LastIndexAny(full, `\/`); i >= 0 {
		full = full[i+1:]
	}
	return strings.ToLower(full)
}

// isOurProcess reports whether pid is a live SkillManage process. The name check
// is the guard that makes force-termination safe against pid reuse: a recycled pid
// now owned by some unrelated program will not match and will never be killed.
func isOurProcess(pid int) bool {
	if pid <= 0 {
		return false
	}
	return strings.Contains(procName(pid), "skillmanage")
}

// alivePlatform reports whether pid is a running process. WAIT_TIMEOUT from a
// zero-timeout wait means the process object is not signaled, i.e. still running.
func alivePlatform(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return ev == uint32(windows.WAIT_TIMEOUT)
}

// terminate force-kills pid IFF it is confirmed to be a SkillManage process.
func terminate(pid int) {
	if !isOurProcess(pid) {
		return // safety: never terminate a non-SkillManage process
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.TerminateProcess(h, 1)
}

// signalPidPlatform stops pid. Windows has no graceful process signal, so both the
// graceful and forced paths perform the same name-checked termination.
func signalPidPlatform(pid int, force bool) { terminate(pid) }

// killOtherInstancesPlatform enumerates running processes and terminates every
// SkillManage instance EXCEPT self. This is the force-takeover safety net for when
// the pid file is stale / wrong (so the normal pid-targeted stop can't free the
// lock). It is safe by construction: the image-name filter means only SkillManage's
// own processes are ever touched. Returns how many it terminated.
func killOtherInstancesPlatform(self int) int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return 0
	}
	killed := 0
	for {
		pid := int(e.ProcessID)
		if pid != self && pid > 0 {
			name := strings.ToLower(windows.UTF16ToString(e.ExeFile[:]))
			if strings.Contains(name, "skillmanage") {
				if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
					if windows.TerminateProcess(h, 1) == nil {
						killed++
					}
					windows.CloseHandle(h)
				}
			}
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			break
		}
	}
	return killed
}
