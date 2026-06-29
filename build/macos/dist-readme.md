# SkillManager 桌面版（macOS）

跨 harness（Claude Code / Codex 等）的 skill 统一管理器。通用版，Apple 芯片和 Intel Mac 都能跑。

## 安装

- **dmg**：双击打开，把 **SkillManager.app** 拖到「应用程序」（Applications）。
- **zip**：解压后把 **SkillManager.app** 拖进「应用程序」。

> 建议放到「应用程序」再打开，不要在解压/挂载出来的目录里直接双击（macOS 的 App Translocation 会把它放到只读临时路径运行，导致自更新等功能异常）。

## 首次打开（重要）

这个 App 是自签名、未经 Apple 公证的，所以**第一次打开会被系统拦住**（可能直接闪退/打不开）。装到「应用程序」后，用下面**最可靠**的一行清掉隔离标记，再正常双击即可（只需一次）：

```
xattr -dr com.apple.quarantine /Applications/SkillManager.app
```

（旧系统也可以试「右键 SkillManager → 打开 → 再点打开」；新版 macOS 该入口已收紧，优先用上面的命令。）

## 用起来

- 打开后是一个**窗口**（不是浏览器）：左侧是 skill 的来源（git 仓 / 本地源 / npx skills.sh），右侧按目标目录看现状、启用/停用。
- 需要系统里装了 **git**（拉取 git 源）。如果用到 skills.sh，需要 **npx**（Node）；App 会自动从你的登录 shell 里找它们。
- 每天 09:40 会自动更新一次全部源；也可随时点右上「全量更新」。
- **在线更新**：联网时，App 会自动检查有没有新版本，有则顶部出现「↑ 更新到 vX.Y.Z」，点一下就会下载、校验、自动重启升级。无法访问更新源时静默跳过，不影响使用。
- 数据都在本机 `~/.skillmanage/`，不上传任何数据（只会向更新源检查 / 下载更新本身）。

## 卸载

退出 App，把 `/Applications/SkillManager.app` 删掉即可；如不想保留配置，再删 `~/.skillmanage/`。
