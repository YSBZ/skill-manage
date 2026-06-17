# SkillManage 桌面版（macOS）

跨 harness（Claude Code / Codex 等）的 skill 统一管理器。通用版，Apple 芯片和 Intel Mac 都能跑。

## 安装

1. 解压本压缩包。
2. 把 **SkillManage.app** 拖进「应用程序」（Applications）。

## 首次打开（重要）

这个 App 是自签名、未经 Apple 公证的，所以**第一次打开会被系统拦住**。二选一放行：

- **推荐**：在「应用程序」里 **右键点 SkillManage → 打开 → 再点「打开」**。（只需这一次，之后正常双击。）
- 如果提示「已损坏，无法打开」，打开「终端」运行下面这行再打开：
  ```
  xattr -dr com.apple.quarantine /Applications/SkillManage.app
  ```

## 用起来

- 打开后是一个**窗口**（不是浏览器）：左侧是 skill 的来源（git 仓 / 本地源 / npx skills.sh），右侧按目标目录看现状、启用/停用。
- 需要系统里装了 **git**（拉取 git 源）。如果用到 skills.sh，需要 **npx**（Node）；App 会自动从你的登录 shell 里找它们。
- 每天 09:40 会自动更新一次全部源；也可随时点右上「全量更新」。
- 数据都在本机 `~/.skillmanage/`，不联网上传任何东西。

## 卸载

退出 App，把 `/Applications/SkillManage.app` 删掉即可；如不想保留配置，再删 `~/.skillmanage/`。
