# SkillManage 桌面版（Windows）

跨 harness（Claude Code / Codex 等）的 skill 统一管理器。原生窗口版（不是浏览器标签）。

## 安装

1. 解压本压缩包到任意目录（如 `D:\Tools\SkillManage\`）。
2. 双击 **SkillManage.exe** 即可——它打开一个原生窗口。

> 建议放到一个固定目录，别放在压缩软件的临时解压区。开机自启会记住这个路径，挪了位置要重新设一次自启。

## 首次打开（重要）

这个 exe 未做数字签名，**第一次运行 Windows SmartScreen 可能拦一下**：
弹出「Windows 已保护你的电脑」时 → 点 **「更多信息」** → **「仍要运行」**。只需这一次。

## 运行前提

- **WebView2 运行时**：Win11 和较新的 Win10 自带。若窗口打不开（白屏/闪退），到微软官网装一个免费的 **WebView2 Evergreen Runtime** 即可。
- **git**（拉取 git 源）；如果要用 skills.sh 在线搜索/安装，还需 **npx**（Node）。App 会自动从你的用户环境变量 PATH 里找它们；没有会在顶部红条提示，但不影响其它功能。

## 用起来

- 打开后是一个**窗口**：左侧是 skill 的来源（git 仓 / 本地源 / skills.sh），右侧按目标目录看现状、启用/停用。
- 顶部「搜索」可同时搜本地 + skills.sh 线上可安装的 skill。
- 每天 09:40 自动更新一次全部源；也可随时点右上「全量更新」。
- 数据都在本机用户目录下的 `.skillmanage\`（即 `%USERPROFILE%\.skillmanage\`），不联网上传任何东西。

## 升级（覆盖旧版本）

桌面版是常驻后台进程（单实例）。**直接双击新 exe 可能仍打开旧窗口**，先关干净再换：

1. 退出 App（关窗口）。若仍有残留：`Ctrl + Shift + Esc` 打开任务管理器 → 「详细信息」→ 找到 **`SkillManage.exe`** → 全部「结束任务」。
2. 用新版 `SkillManage.exe` **覆盖**旧的（同一目录同名）。
3. 双击运行。

## 卸载

退出并在任务管理器结束 `SkillManage.exe` → 删掉 exe；如不想保留配置，再删 `%USERPROFILE%\.skillmanage\`。
工具只删它自己建立的软链，不会动你手写的真身 skill。
