package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// IsDesktop is set true by the desktop (Wails) main; the web daemon leaves it
// false. Self-update only targets the desktop app (the department's distribution
// form) — the web single-binary is for devs who rebuild from source.
var IsDesktop bool

// desktopAssetKey maps the running desktop app to its latest.json asset key
// ("desktop-macos" / "desktop-windows"). ok=false → self-update not supported
// here (web build, or an unhandled OS).
func desktopAssetKey() (string, bool) {
	if !IsDesktop {
		return "", false
	}
	switch runtime.GOOS {
	case "darwin":
		return "desktop-macos", true
	case "windows":
		return "desktop-windows", true
	}
	return "", false
}

// handleUpdateApply performs the actual self-update: re-check the feed, download
// THIS platform's package, verify its sha256, then spawn a DETACHED updater that
// waits for this process to exit, swaps the installed app, and relaunches it.
// The process exits ~1.5s after responding so the updater can take over. Refuses
// unless a newer version exists and a sha256 is present (never replaces the app
// with an unverified download). CSRF-guarded.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	if UpdateFeed == "" {
		writeErr(w, http.StatusBadRequest, "disabled", "未配置更新源")
		return
	}
	key, ok := desktopAssetKey()
	if !ok {
		writeErr(w, http.StatusBadRequest, "unsupported", "当前形态/平台暂不支持自更新（网页版请手动更新）")
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 5*time.Minute)
	defer cancel()
	m, err := fetchLatestManifest(ctx, UpdateFeed, UpdateToken)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "feed", "查询更新失败："+err.Error())
		return
	}
	if !versionLess(Version, m.Version) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "upToDate": true, "current": Version})
		return
	}
	url, sum := m.Assets[key], m.SHA256[key]
	if url == "" || sum == "" {
		writeErr(w, http.StatusBadRequest, "no_asset", "更新源缺少本平台的安装包或校验值（"+key+"）")
		return
	}
	installPath, err := desktopInstallPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "locate", "无法定位安装位置："+err.Error())
		return
	}

	zip, err := downloadToTemp(ctx, s.centralDir, url, UpdateToken)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "download", "下载失败："+err.Error())
		return
	}
	got, err := sha256File(zip)
	if err != nil || !strings.EqualFold(got, strings.TrimSpace(sum)) {
		_ = os.Remove(zip)
		writeErr(w, http.StatusBadRequest, "verify", "校验失败（sha256 不符），已中止更新")
		return
	}

	// Log from the Go side BEFORE spawning, so update.log always exists if we got
	// this far. If the file later contains only this line (no updater-script
	// lines), the helper process was created but never executed — a Job-Object
	// kill or a policy block — which pinpoints the failure mode.
	appendUpdateLog(s.centralDir, fmt.Sprintf("apply: latest=%s pid=%d install=%s zip=%s", m.Version, os.Getpid(), installPath, zip))
	if err := spawnUpdater(zip, installPath, os.Getpid(), s.centralDir); err != nil {
		appendUpdateLog(s.centralDir, "spawnUpdater error: "+err.Error())
		_ = os.Remove(zip)
		writeErr(w, http.StatusInternalServerError, "spawn", "启动更新器失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarting": true, "latest": m.Version})
	// Give the response time to flush, then exit so the detached updater (which is
	// waiting on our PID) can swap the app and relaunch it.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		os.Exit(0)
	}()
}

// downloadToTemp streams the package to <central>/update-tmp/download.zip.
func downloadToTemp(ctx context.Context, centralDir, url, token string) (string, error) {
	dir := filepath.Join(centralDir, "update-tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "download.zip")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return dst, nil
}

// appendUpdateLog appends a timestamped line to <central>/update.log. The
// updater scripts also write here; the Go side writes first (pre-spawn) so the
// file's presence/contents distinguish "never spawned" vs "spawned but the
// helper process never ran" vs "ran and failed at step X". Best-effort.
func appendUpdateLog(centralDir, msg string) {
	f, err := os.OpenFile(filepath.Join(centralDir, "update.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// desktopInstallPath resolves what the updater must replace: the .app bundle on
// macOS, or the .exe on Windows.
func desktopInstallPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	switch runtime.GOOS {
	case "darwin":
		// …/SkillManager.app/Contents/MacOS/skillmanager-desktop → …/SkillManager.app
		app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		if filepath.Ext(app) != ".app" {
			return "", fmt.Errorf("不是 .app 包结构：%s", app)
		}
		return app, nil
	case "windows":
		return exe, nil
	}
	return "", fmt.Errorf("unsupported os")
}
