# SkillManager

[English](README.md) | **中文**

一个「同步 skill」的工具：跟踪多个 git skill 仓、每天自动保持最新，并把选中的 skill 软链（macOS/WSL/Linux）或目录联接（Windows）进 **Claude Code 和 Codex** 的 skill 目录。提供两种形态——**内置浏览器 UI 的单二进制 daemon**，以及把同一 UI 包进原生窗口的 **桌面 app（macOS / Windows）**。

刻意减少定制：使用者只需提供两样东西——**① git 远程仓（skill 来源）** 和 **② 要同步进去的目录**。其余都是工具的活：自动更新、选择性 / 整仓同步、把本地手写 skill 反向收编、把本地改动贡献回上游。

核心机制：链接指向**活的 git 镜像**，每天 `fetch` 让被链接的 skill 立即最新，**零安装、零更新动作**。cc 与 codex 的 skill 通用，同一来源可同时映射进两边。

## 运行

### 方式一：桌面 app（macOS / Windows）

从 Releases 下载对应平台的包：

- **macOS**：`SkillManager-vX.Y.Z.dmg`，拖进 `/Applications` 后启动（**装好后请弹出 dmg 卷**，别从挂载卷里直接运行）。
- **Windows**：`SkillManager-windows-desktop-vX.Y.Z.zip`，解压后运行 `SkillManager.exe`（已嵌入应用图标，双击无控制台窗口）。

> **macOS 注意**：未签名 / 未公证的二进制首次会被 Gatekeeper 拦截，需右键 → 打开，或 `xattr -d com.apple.quarantine <app>` 解隔离。

### 方式二：网页版单二进制

```sh
make build          # 构建 host 二进制 ./skillmanager
./skillmanager       # 启动 daemon；终端打印 UI 地址（默认 http://127.0.0.1:7799/）
```

- 中央文件夹默认 `~/.skillmanage`（含 config.yaml、manifest.yaml、local 受管存储、lock、address、token）；用 `--central <dir>` 覆盖。
- 浏览器 UI 里管理一切；标题旁的 `?` 有完整使用指南，更新日志在版本号弹层里。

## 概念

**目标（目录 / tab）** — 你要把 skill 同步进去的地方。顶部 `+` 添加目录，选一个项目根（含 `.claude` / `.codex`）会自动展开成对应的 cc / codex 两个 tab，标签按路径自动识别（认不出标 `unknown`）。

**源** — skill 的来源，统一在左侧管理，分四类：

- **git 仓**：跟踪的远程 skill 仓，本工具维护其只读镜像。
- **本地源**：① `~/.skillmanage/local` 受管存储（收编 / 备份的真身归此）；② 你登记的任意本地文件夹（实时识别其中的 skill，不复制、不改动）。
- **npx skills**：通过 `npx skills` 从 [skills.sh](https://skills.sh) 或 [skillsmp](https://skillsmp.com) 安装的 skill，canonical 在 `~/.agents/skills`，本工具只读、更新走 npx（见下「在线搜索并安装」）。
- **插件（plugins）**：harness 自带插件系统管理的 skill（`~/.claude/plugins` 等），**全局、只读**，与具体目录无关。

**交互模型**：左侧是源，是操作 skill **本体**（移动 / 删除 / 整仓同步 / 启用）的唯一入口；右侧是 skill 列表，对本体只读，但卡片上带「停用」快捷操作。

## 功能

- **选择性 / 整仓同步**：每个 tab 独立维护映射——勾选要同步的 skill，或对仓库「自动同步」整仓（上游新增自动加链、删除自动清链）。
- **更新**：每天定时自动；仓卡片 / 仓弹窗的「同步仓库」手动触发。
  - 无本地改动 → 直接拉取上游。
  - 有本地改动（新增 / 修改 / 删除）→ 弹窗二选一：
    - **确认** = 把本地改动**全部** commit + push 到上游，再拉取更新；
    - **仅更新** = 只拉取上游更新并**保留**本地改动（不上传）。非冲突的改动会保留，下次还能再同步上传；与上游**冲突**则更新失败，提示用 git 手动解决——**绝不自动丢弃本地改动**。
- **贡献回上游**：本地新增 / 修改 / 删除的 skill 通过「同步仓库」commit + push 回 git 远程，提交信息可编辑。
- **密钥 / 凭据保护**：上传前若检出疑似密钥文件（`.env`、`*.pem`、`id_rsa`、`.npmrc`、含 credential/secret 的文件等），弹窗标红列出并**闸住「确认」**，必须显式勾选才推；后端也强制拦截（绕过 UI 直接打 API 同样拦得住）。`.env.example` 等模板不算。
- **在线搜索并安装**：顶部搜索框同时查本地各源与两个在线市场——[skills.sh](https://skills.sh) 与 [skillsmp](https://skillsmp.com)。在线卡片按来源打徽章（skills.sh ↓下载数 / skillsmp ★星数）；安装统一走 `npx skills add`，装进 canonical `~/.agents/skills`（归「npx skills」源），更新 / 停用与其它 npx skill 一致。齿轮 ⚙ 按来源 / 排序 / 数量筛选；两源并行查询，任一源失败不影响另一源。
- **收编 / 备份本地 skill**：把同步目录里手写、未纳管的真身 skill 移入受管存储并原位软链，成为可跨 harness 复用的来源。可选扫描插件目录并以**复制导入**方式收编（不改动插件原件）。
- **OS 垃圾自动忽略**：镜像同步把 `.DS_Store`、`._*`、`Thumbs.db`、编辑器 swap、`node_modules`、`__pycache__` 等写入各镜像 `.git/info/exclude`（本地忽略、不推上游），不会被当改动误传。
- **自动同步本地改动**：你可能在工具外直接增删 / 改动本地 skill 文件。打开期间每 15 秒后台静默探测磁盘指纹，仅在确有变化时刷新（无变化绝不重绘，空闲无感、不闪烁不跳滚动条）；打开弹窗 / 输入时与标签页不可见时暂停，切回标签页或窗口聚焦时立即探测。右上角「↻ 同步本地」按钮可随时手动重扫。
- **导入 / 导出**：导出 / 导入仓库列表，便于换机重建（manifest 不随导出，避免误删别机链接）。

## git 仓与鉴权

仓库 URL 支持 `https://…`、`ssh://…`，以及 scp 式 `git@host:org/repo.git`；出于安全拒绝明文 `http://`、本地 `file://` 和 `ext::`（git 的任意命令传输）。

机器上必须装有 **git 且在 PATH 中**（缺失时 UI 顶部会红条提示、无法同步）。自动更新在后台**非交互**运行——设了 `GIT_TERMINAL_PROMPT=0`、`ssh -o BatchMode=yes`、`GCM_INTERACTIVE=never`，**绝不弹窗或卡住**；因此私有仓必须预先配好免交互鉴权。

- **公开仓**：用 `https` 直接添加，无需任何配置。
- **私有仓 · SSH（推荐）**：配好该 git 主机的 SSH key 并加入 `ssh-agent`（带 passphrase 的 key 需先解锁），公钥登记到 git 服务器。
- **私有仓 · HTTPS**：点仓库卡片上的「填写凭据」，在应用内填用户名 + 个人访问令牌(PAT)，存于本机 `~/.skillmanage/credentials.yaml`（0600，不随导出离开本机），拉取时经 `GIT_ASKPASS` 自动注入；也可改用系统凭据助手（macOS 钥匙串 / Git Credential Manager）。

## 构建与分发

```sh
make build          # host 网页版二进制 ./skillmanager
make package        # dist/ 下 darwin-arm64 / darwin-amd64 / windows-amd64 / linux-amd64 网页版 zip
make desktop-dmg    # macOS 桌面 app（universal）→ dist/SkillManager-vX.Y.Z.dmg
make desktop-win    # Windows 桌面 app（交叉编译，带图标）→ dist/pkg/…windows-desktop-…zip
make winres         # 重新生成 Windows 资源（图标 + 版本信息），版本号升级后跑
make test           # go test ./...
```

UI 是手写静态资源、`//go:embed` 进二进制，无前端构建链。WSL 用 linux 构建。

- **网页版**：单文件、无需 Go 工具链，对方运行后浏览器打开打印的地址即可。
- **桌面版**：Wails 原生窗口包同一 UI。Windows 用纯 Go WebView2 绑定，**无需 CGO、无需 Windows 机**，可从 macOS / Linux 交叉编译；图标与 macOS 同款（icns → png → syso，go-winres 生成）。

## 安全

- 本地 HTTP server 只绑回环，每次 API 调用需 bearer token（首次生成存 `~/.skillmanage/token`，0600，注入 SPA），并校验 `Host` 头防 DNS-rebinding。
- git 仓 URL 按白名单校验（https/ssh/git），拒 `file://`/`ext::`/元字符。
- 同步禁用仓自带 git hook 与系统 config。
- **绝不覆盖目标处的真实目录**；只动自己创建的链接（所有权 manifest + 文件系统对账）。
- **密钥不入共享仓**：贡献上传前拦截疑似密钥 / 凭据文件，需显式确认。
- Codex 的 `.system` / `vendor_imports/skills` 为守卫目录，永不写入、复制或收编。
- Windows 用目录联接（`mklink /J`），无需管理员 / 开发者模式。

## 文档

设计与实施细节见 `docs/plans/`。
