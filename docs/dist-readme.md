# SkillManage 使用说明

一个「同步 skill」的小工具：跟踪你的 git skill 仓、每天自动保持最新，并把选中的 skill 软链进 Claude Code / Codex 的 skill 目录。零安装、零更新动作——单个可执行文件，UI 内置。

## 0. 前置：需要 git

本工具靠 git 拉取/更新 skill 仓，所以机器上**必须装有 Git 且在 PATH 中**。

- Windows：装 [Git for Windows](https://git-scm.com/download/win)，安装时选「Git from the command line…」让它进 PATH，装完**重开终端/重启工具**。
- macOS：`git --version`（没有会提示装 Xcode Command Line Tools），或 `brew install git`。
- Linux/WSL：`sudo apt install git` 等。

没装 git 工具也能起来，但顶部会红条提示、且无法同步仓库。

## 1. 运行

这个 zip 里有一个可执行文件（`skillmanage` 或 `skillmanage.exe`）。启动后会**自动在默认浏览器打开** UI（默认 `http://127.0.0.1:7799/`）；它是常驻后台进程，再次运行只会打开已在跑的那个实例的 UI，不会重复启动。

**Windows：** 双击 `skillmanage.exe` 即可——它在后台运行、不弹命令行窗口，浏览器会自动打开。

> Windows 首次运行 SmartScreen 可能提示"已保护你的电脑"，点「更多信息」→「仍要运行」。
> 若双击后浏览器没自动打开，手动访问 `http://127.0.0.1:7799/`；启动失败的原因会写进 `%USERPROFILE%\.skillmanage\skillmanage.log`。

**macOS / Linux / WSL：**
```sh
chmod +x skillmanage
./skillmanage
```

> **macOS 第一次会被拦**（"无法验证开发者 / 来自身份不明的开发者"），这是未签名工具的正常现象。解隔离一次即可：
> ```sh
> xattr -d com.apple.quarantine skillmanage
> ```
> 或：右键点二进制 →「打开」→ 在弹窗里再点「打开」。

## 2. 配置（你只需要提供两样东西）

在浏览器 UI 里：

1. **加 git skill 仓**：左上角填你的 git 仓 URL，支持 `https://`、`ssh://`、`git@host:org/repo.git`，可选分支。
   - 公开仓直接加，无需配置。
   - **私有仓**的自动更新是后台非交互拉取（不会弹窗输密码），需你先配好免交互鉴权：**SSH** 把 key 加入 `ssh-agent`（推荐），或 **HTTPS** 用系统凭据助手/个人令牌（PAT）。没配好该仓会报错但不影响工具运行。
2. **加同步目录**：顶部 `+` 选/粘贴要同步进去的目录。选项目根（含 `.claude` / `.codex`）会自动识别成 cc / codex 两个 tab。
3. 在每个 tab 里**勾选要同步的 skill**，或对仓库点「全选并跟随」整仓同步。

其余都是工具自理：每天自动 `fetch` 拉新、自动建/删软链、首次运行自动注册开机自启。

标题旁的 `?` 有完整使用指南。

## 3. 数据 / 卸载

- 配置与状态都在 `~/.skillmanage/`（Windows 为用户目录下 `.skillmanage`）。
- 关掉开机自启：UI 右上角取消「开机自启」勾选。
- 想完全卸载：取消开机自启 → 关闭进程 → 删掉可执行文件和 `~/.skillmanage/` 即可。工具只动它自己建立的软链，不会动你手写的真身 skill。

## 4. 安全

- 本地 HTTP 服务只绑回环（127.0.0.1），每次 API 调用需要 token（自动生成存 `~/.skillmanage/token`），并校验 Host 头防 DNS-rebinding。
- 绝不覆盖目标目录里你已有的真实文件，只增删自己创建的链接。
