package server

import (
	"archive/zip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"skillmanage/internal/harness"
	"skillmanage/internal/scanner"
)

// handleExportSkill packages a skill's real directory into a zip under
// ~/.skillmanage/exports/ and returns the saved path. The skill is identified
// EITHER by {repo,name} — a source: git repo / @local / @dir:<id> / @agents —
// or by {target,name} — its physical location inside a sync dir. A project-side
// skill is usually a symlink (junction / copy on Windows); the link is resolved
// to its real target before zipping, so export works whether the skill is a real
// directory or a managed link. CSRF-guarded.
func (s *Server) handleExportSkill(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		Repo   string `json:"repo"`
		Target string `json:"target"`
		Name   string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Repo = strings.TrimSpace(req.Repo)
	req.Target = strings.TrimSpace(req.Target)
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || (req.Repo == "" && req.Target == "") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	skillDir, linkName, version, ok := s.resolveSkillDir(req.Repo, req.Target, req.Name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "未找到该 skill")
		return
	}
	// Resolve symlinks/junctions so we zip the real files, not a link node.
	realDir, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "无法定位 skill 真身："+err.Error())
		return
	}
	if harness.Guarded(realDir) {
		writeErr(w, http.StatusBadRequest, "guarded", "该目录受保护，不能导出")
		return
	}
	if fi, statErr := os.Stat(filepath.Join(realDir, "SKILL.md")); statErr != nil || fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "invalid", "该目录不是有效 skill（缺 SKILL.md）")
		return
	}

	exportsDir := filepath.Join(s.centralDir, "exports")
	if err := os.MkdirAll(exportsDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	base := linkName
	if version != "" {
		base += "-v" + version
	}
	zipName := base + "-" + time.Now().Format("20060102-150405") + ".zip"
	zipPath := filepath.Join(exportsDir, zipName)
	if err := zipSkillDir(realDir, linkName, zipPath); err != nil {
		_ = os.Remove(zipPath)
		writeErr(w, http.StatusInternalServerError, "zip_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": zipPath, "file": zipName, "dir": exportsDir})
}

// resolveSkillDir finds a skill's on-disk directory (+ its link name and version)
// by source repo OR by sync target, mirroring handleSkillDetail / handleSkillAt.
func (s *Server) resolveSkillDir(repo, target, name string) (dir, linkName, version string, ok bool) {
	if repo != "" {
		root, rok := s.sourceRoot(repo)
		if !rok {
			return "", "", "", false
		}
		skills, err := scanner.Scan(root)
		if err != nil {
			return "", "", "", false
		}
		for _, sk := range skills {
			if sk.LinkName == name || sk.LogicalName == name {
				return sk.Dir, sk.LinkName, sk.Version, true
			}
		}
		return "", "", "", false
	}
	want := harness.Expand(target)
	s.mu.Lock()
	known := false
	for _, d := range s.cfg.Targets {
		if harness.Expand(d) == want {
			known = true
			break
		}
	}
	s.mu.Unlock()
	if !known {
		return "", "", "", false
	}
	inv, err := scanner.ScanInventory(want)
	if err != nil {
		return "", "", "", false
	}
	for _, sk := range inv {
		if sk.LinkName == name || sk.LogicalName == name {
			return sk.Dir, sk.LinkName, sk.Version, true
		}
	}
	return "", "", "", false
}

// zipSkillDir writes realDir's tree into zipPath, every entry prefixed by
// baseName/ so unzipping yields a single top-level skill folder. .git and
// node_modules are skipped (a skill dir should not carry them, but be safe), and
// nested symlinks are not followed (zip stores regular files + dirs only).
func zipSkillDir(realDir, baseName, zipPath string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	return filepath.Walk(realDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(realDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			if seg == ".git" || seg == "node_modules" {
				if fi.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		name := filepath.ToSlash(filepath.Join(baseName, rel))
		if fi.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		hdr, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		wtr, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(wtr, src)
		return err
	})
}
