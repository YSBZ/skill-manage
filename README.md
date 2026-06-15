# SkillManage

一个「同步 skill」的工具：跟踪多个 git skill 仓、每天自动保持最新，并把选中的 skill 软链（macOS/WSL/Linux）或目录联接（Windows）进 **Claude Code 和 Codex** 的 skill 目录——由内置浏览器 UI 驱动的单二进制 daemon。

刻意减少定制：使用者只需提供两样东西——**① git 远程仓（skill 来源）** 和 **② 要同步进去的目录**。其余都是工具的活：自动更新、选择性 / 整仓同步、把本地手写 skill 反向收编。

核心机制：链接指向**活的 git 镜像**，每天 `fetch + reset --hard` 让被链接的 skill 立即最新，**零安装、零更新动作**。cc 与 codex 的 skill 通用，同一来源可同时映射进两边。

## 运行

```sh
make build          # 构建 host 二进制 ./skillmanage
./skillmanage       # 启动 daemon；终端打印 UI 地址（默认 http://127.0.0.1:7799/）
```

- 中央文件夹默认 `~/.skillmanage`（含 config.yaml、manifest.yaml、local 受管存储、lock、address、token）；用 `--central <dir>` 覆盖。
- 首次运行自动注册开机自启；`--no-autostart` 关闭。
- 浏览器 UI 里管理一切；标题旁的 `?` 有完整使用指南。

## 功能

- **同步目录（tab）**：顶部 `+` 添加目录，可手输 / 粘贴或点选浏览。选一个项目根（含 `.claude` / `.codex`）会自动展开成对应的 cc / codex 两个 tab，标签按路径自动识别（认不出标 `unknown`）。删除一个 tab 会拆除该目录下由本工具建立的全部软链，你自己的真身 skill 不受影响。
- **选择性 / 整仓同步**：每个 tab 独立维护映射——勾选要同步的 skill，或对仓库「全选并跟随」整仓（上游新增自动加链、删除自动清链）。
- **收编本地 skill**：把同步目录里你手写、未纳管的真身 skill 移入受管存储并原位软链，成为可跨 harness 复用的来源。可选「忽略 plugin 里的 skill」（默认忽略）；取消勾选会额外扫描该 tab 的插件目录（如 `~/.claude/plugins`）并列出，对它们「收编」走复制导入、不改动插件原件。
- **更新**：每天定时自动；「立即更新」手动触发；「强制更新」丢弃本地改动与上游一致。
- **开机自启**：登录时自动拉起 daemon。
- **导入 / 导出**：导出 / 导入仓库列表，便于换机重建（manifest 不随导出，避免误删别机链接）。

## 跨平台分发

```sh
make build-all      # 产出 dist/ 下 darwin-arm64 / darwin-amd64 / windows-amd64.exe / linux-amd64
```

UI 是手写静态资源、`//go:embed` 进二进制，无前端构建链。WSL 用 linux 构建。

分发给他人时，直接把对应平台的单文件发过去即可（无需 Go 工具链）。对方运行后用浏览器打开打印的地址，加自己的 git 仓 + 同步目录即可。

> **macOS 注意**：未签名/未公证的二进制首次会被 Gatekeeper 拦截，对方需 `xattr -d com.apple.quarantine <binary>` 解隔离，或右键→打开。

## 安全

- 本地 HTTP server 只绑回环，每次 API 调用需 bearer token（首次生成存 `~/.skillmanage/token`，0600，注入 SPA），并校验 `Host` 头防 DNS-rebinding。
- git 仓 URL 按白名单校验（https/ssh/git），拒 `file://`/`ext::`/元字符。
- 同步禁用仓自带 git hook 与系统 config。
- 绝不覆盖目标处的真实目录；只动自己创建的链接（所有权 manifest + 文件系统对账）。
- Codex 的 `.system` / `vendor_imports/skills` 为守卫目录，永不写入、复制或收编。

## 文档

设计与实施细节见 `docs/plans/`。
