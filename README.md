# SkillManage

跟踪多个 git skill 仓、每天保持最新，并把选中的 skill 软链（macOS/WSL）或目录联接（Windows）进 Claude Code 的 skill 目录——由内置浏览器 UI 驱动的单二进制 daemon。

核心机制：链接指向**活的 git 镜像**，每天 `fetch + reset --hard` 让被链接的 skill 立即最新，**零安装、零更新动作**。

## 运行

```sh
make build          # 构建 host 二进制 ./skillmanage
./skillmanage       # 启动 daemon；终端打印 UI 地址（默认 http://127.0.0.1:7799/）
```

- 中央文件夹默认 `~/.skillmanage`（含 config.yaml、manifest.yaml、lock、address、token）；用 `--central <dir>` 覆盖。
- 首次运行自动注册开机自启；`--no-autostart` 关闭。
- daemon 在浏览器里管理：加 git 仓、选 skill（全选=跟随 / 单选=快照）、选目标（全局 `~/.claude/skills/` 或登记的项目）、立即更新、查看冲突与悬空清理。

## 跨平台分发

```sh
make build-all      # 产出 dist/ 下 darwin-arm64 / darwin-amd64 / windows-amd64.exe / linux-amd64
```

UI 是手写静态资源、`//go:embed` 进二进制，无前端构建链。WSL 用 linux 构建。

## 安全

- 本地 HTTP server 只绑回环，每次 API 调用需 bearer token（首次生成存 `~/.skillmanage/token`，0600，注入 SPA），并校验 `Host` 头防 DNS-rebinding。
- git 仓 URL 按白名单校验（https/ssh/git），拒 `file://`/`ext::`/元字符。
- 同步禁用仓自带 git hook 与系统 config。
- 绝不覆盖目标处的真实目录；只动自己创建的链接（所有权 manifest + 文件系统对账）。

## 一期范围

仅 Claude Code。Codex 支持为二期（需先验证其 skill 格式）。详见 `docs/plans/`。
