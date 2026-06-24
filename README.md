# SkillManager

**English** | [中文](README-zh.md)

A "skill sync" tool: it tracks multiple git skill repos, keeps them up to date automatically every day, and links selected skills — via symlinks (macOS/WSL/Linux) or directory junctions (Windows) — into the skill directories of **Claude Code and Codex**. It ships in two forms: a **single-binary daemon with a built-in browser UI**, and a **desktop app (macOS / Windows)** that wraps the same UI in a native window.

Deliberately low-configuration: you provide just two things — **① a git remote (the skill source)** and **② the directory to sync into**. Everything else is the tool's job: auto-update, selective / whole-repo sync, adopting your hand-written local skills, and contributing local changes back upstream.

Core idea: links point at **live git mirrors**, and a daily `fetch` makes every linked skill instantly current — **zero install, zero update steps**. cc and codex skills are interchangeable; one source can map into both at once.

## Running

### Option A: Desktop app (macOS / Windows)

Download the package for your platform from the [**Releases** page](https://github.com/YSBZ/skill-manage/releases) — no build needed:

- **macOS**: `SkillManager-vX.Y.Z.dmg` — drag into `/Applications` and launch (**eject the dmg volume after installing**; don't run it from the mounted volume).
- **Windows**: `SkillManager-windows-desktop-vX.Y.Z.zip` — unzip and run `SkillManager.exe` (app icon embedded, no console window on double-click).

> **macOS note**: an unsigned / un-notarized binary is blocked by Gatekeeper on first launch — right-click → Open, or clear quarantine with `xattr -d com.apple.quarantine <app>`.

### Option B: Single-binary web version

Prefer not to build it yourself? Grab the prebuilt `skillmanager-<platform>-vX.Y.Z.zip` from the [**Releases** page](https://github.com/YSBZ/skill-manage/releases), unzip, and run the binary directly — no Go toolchain needed. Or build from source:

```sh
make build          # build the host binary ./skillmanager
./skillmanager       # start the daemon; the UI address is printed (default http://127.0.0.1:7799/)
```

- The central folder defaults to `~/.skillmanage` (config.yaml, manifest.yaml, the `local` managed store, lock, address, token); override with `--central <dir>`.
- Manage everything from the browser UI; the `?` next to the title has the full guide, and the changelog lives in the version popover.

## Concepts

**Targets (directories / tabs)** — where you sync skills into. Add a directory with the top `+`; pick a project root (containing `.claude` / `.codex`) and it expands into matching cc / codex tabs, labeled by path (unrecognized ones are marked `unknown`).

**Sources** — where skills come from, all managed on the left, in four kinds:

- **git repos**: tracked remote skill repos, for which the tool maintains a read-only mirror.
- **local sources**: ① the `~/.skillmanage/local` managed store (adopted / backed-up skills live here); ② any local folder you register (its skills are detected live — not copied, not modified).
- **npx skills**: skills installed via `npx skills` from either [skills.sh](https://skills.sh) or [skillsmp](https://skillsmp.com); the canonical copy lives in `~/.agents/skills`, read-only here, updated through npx (see **Online search & install** below).
- **plugins**: skills managed by the harness's own plugin system (`~/.claude/plugins`, etc.) — **global, read-only**, independent of any specific directory.

**Interaction model**: the left side is the sources — the only place to act on a skill's **body** (move / delete / whole-repo sync / enable); the right side is the skill list, read-only on the body, with a "Disable" quick action on each card.

## Features

- **Selective / whole-repo sync**: each tab keeps its own mapping — tick the skills to sync, or "auto-sync" a whole repo (new skills upstream get linked automatically, deletions get unlinked).
- **Update**: scheduled daily; manually via "Sync repo" on a repo card / repo popup.
  - No local changes → just pull upstream.
  - Local changes (added / modified / deleted) → a dialog with two choices:
    - **Confirm** = commit + push **all** local changes upstream, then pull;
    - **Update only** = pull upstream while **keeping** local changes (without uploading). Non-conflicting changes are preserved and can still be uploaded later; on a **conflict** with upstream the update fails and asks you to resolve with git — local changes are **never auto-discarded**.
- **Contribute upstream**: locally added / modified / deleted skills are committed + pushed back to the git remote via "Sync repo", with an editable commit message.
- **Secret / credential guard**: before uploading, if a secret-looking file is detected (`.env`, `*.pem`, `id_rsa`, `.npmrc`, files containing credential/secret, etc.), the dialog lists them in red and **gates the Confirm button** behind an explicit acknowledgement; the backend enforces this too (calling the API directly, bypassing the UI, is still blocked). Templates like `.env.example` don't count.
- **Online search & install**: the top search box queries your local sources plus two online marketplaces — [skills.sh](https://skills.sh) and [skillsmp](https://skillsmp.com) — at once. Online cards are badged by origin (skills.sh ↓ downloads / skillsmp ★ stars); installing routes through `npx skills add` into the canonical `~/.agents/skills` (the **npx skills** source), so update / disable behave like any other npx skill. A gear ⚙ filters by source / sort / count; the two sources are queried in parallel and one failing never blocks the other.
- **Adopt / back up local skills**: move a hand-written, unmanaged skill from a sync directory into the managed store and symlink it in place, making it a cross-harness reusable source. Optionally scan plugin directories and adopt by **copy-import** (never touching the plugin originals).
- **OS junk auto-ignored**: mirror sync writes `.DS_Store`, `._*`, `Thumbs.db`, editor swap files, `node_modules`, `__pycache__`, etc. into each mirror's `.git/info/exclude` (local ignore, never pushed upstream), so they're never mistaken for changes.
- **Auto-sync local changes**: you may add / remove / edit local skill files outside the tool; while the app is open it silently probes a disk fingerprint **every 15s** and repaints only on a real change (idle ticks are invisible — no flicker / scroll jump), pausing while a dialog is open or the tab is hidden and probing immediately on tab / window focus. A top-right **↻ Sync local** button forces a rescan on demand.
- **Import / export**: export / import the repo list to rebuild on a new machine (the manifest is not exported, to avoid deleting another machine's links).

## Git repos & authentication

Repo URLs support `https://…`, `ssh://…`, and scp-style `git@host:org/repo.git`; for safety, plaintext `http://`, local `file://`, and `ext::` (git's arbitrary-command transport) are rejected.

**git must be installed and on PATH** (a red banner warns when it's missing and sync is disabled). Auto-update runs **non-interactively** in the background — with `GIT_TERMINAL_PROMPT=0`, `ssh -o BatchMode=yes`, `GCM_INTERACTIVE=never` — so it **never pops a prompt or hangs**; private repos must therefore have non-interactive auth set up in advance.

- **Public repos**: add via `https`, no configuration needed.
- **Private repos · SSH (recommended)**: set up an SSH key for that git host and add it to `ssh-agent` (a passphrase-protected key must be unlocked first), with the public key registered on the server.
- **Private repos · HTTPS**: click "Set credentials" on the repo card, enter a username + personal access token (PAT) in-app, stored locally in `~/.skillmanage/credentials.yaml` (0600, never leaves the machine via export) and injected at fetch time through `GIT_ASKPASS`; or use a system credential helper (macOS Keychain / Git Credential Manager).

## Build & distribute

```sh
make build          # host web binary ./skillmanager
make package        # web zips under dist/ for darwin-arm64 / darwin-amd64 / windows-amd64 / linux-amd64
make desktop-dmg    # macOS desktop app (universal) → dist/SkillManager-vX.Y.Z.dmg
make desktop-win    # Windows desktop app (cross-compiled, with icon) → dist/pkg/…windows-desktop-…zip
make winres         # regenerate the Windows resource (icon + version info); run after a version bump
make test           # go test ./...
```

The UI is hand-written static assets, `//go:embed`-ed into the binary — no frontend build chain. WSL uses the linux build.

- **Web version**: a single file, no Go toolchain needed; the recipient runs it and opens the printed address in a browser.
- **Desktop version**: a Wails native window around the same UI. Windows uses pure-Go WebView2 bindings, so it needs **no CGO and no Windows machine** — it cross-compiles from macOS / Linux; the icon matches macOS (icns → png → syso, generated by go-winres).

## Security

- The local HTTP server binds loopback only; every API call needs a bearer token (generated once into `~/.skillmanage/token`, 0600, injected into the SPA), and the `Host` header is validated against DNS-rebinding.
- Repo URLs are allowlist-validated (https/ssh/git); `file://`/`ext::`/metacharacters are rejected.
- Sync disables a repo's own git hooks and system config.
- **Never overwrites a real directory at the target**; it only touches links it created (ownership manifest + filesystem reconciliation).
- **Secrets stay out of shared repos**: contribute/upload intercepts secret / credential-looking files and requires explicit confirmation.
- Codex's `.system` / `vendor_imports/skills` are guarded directories — never written to, copied, or adopted.
- Windows uses directory junctions (`mklink /J`) — no admin / Developer Mode required.

## Documentation

See `docs/plans/` for design and implementation details.
