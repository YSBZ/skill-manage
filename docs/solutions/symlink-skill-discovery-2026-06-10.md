---
module: linker
tags: [claude-code, skill-discovery, symlink, junction, cross-platform]
problem_type: integration_decision
date: 2026-06-10
---

# Claude Code 是否跟随软链/junction 的 skill 目录（U1 spike）

SkillManage 的整个模型建立在一个未文档化的行为上：CC 会不会把 `~/.claude/skills/<name>`（一个指向别处的软链 / junction）当作正常 skill 加载。U1 在任何代码依赖它之前先去风险。

## macOS — 软链

**OS 层解析：已确认（2026-06-10，本机 darwin 25.5.0）。**

实测步骤：
- 在 `/tmp/sm-spike-src/sm-spike-demo/` 建真实 skill 目录 + `SKILL.md`（合法 frontmatter）。
- `ln -s /tmp/sm-spike-src/sm-spike-demo ~/.claude/skills/sm-spike-demo`。
- 透过软链读 `~/.claude/skills/sm-spike-demo/SKILL.md` → 可读（直接子项 + 文件解析都成立）。
- `os.Lstat` 报 `is_symlink: True`，`os.Readlink` 返回正确目标——这正是悬空检测（KTD10）要 key 的信号。

**CC 发现行为：强证据支持，建议一次 10 秒人工确认。**
- 团队既有 solution `native-plugin-install-strategy`（marketplace 库）已实证软链式 skill 发现可用（Superpowers 模式），并指出关键约束：skill 必须是 skills 根的**直接子项**（嵌套/打包布局不被各 host 可移植发现）——SkillManage 的 `<root>/<name> → 仓/<skill-dir>` 正满足。
- 会话内无法自证 CC 的扫描器是否发现该软链（可用 skill 列表在会话开始时注入）。最终确认方式：在新 CC 会话里看 `/sm-spike-demo` 是否出现在 slash 菜单。

## Windows — 目录 junction

**未验证（本机无 Windows）。** junction 是与软链不同的 reparse 机制，且 Go 1.23 `winsymlink` 改了模式报告。按计划应急方案：**在 Windows 机/VM 上跑同样的 spike（`mklink /J` 建 junction）通过之前，一期 Windows 链接收窄为 copy 兜底（KTD12）**，不在未验证的 junction 路径上建东西。

## WSL — 软链

**未验证（本机非 WSL）。** 预期与 macOS 软链一致（ext4 内），但 `/mnt/c` 不可靠（KTD9，已排除）。需要时在 WSL 内跑同样 spike。

## 影响

- U5（链接引擎）的 Unix（macOS/WSL）路径可基于"软链被跟随"推进——OS 解析已确认 + 团队实证。
- Windows junction 路径在 Windows 验证通过前，按 copy 兜底落地。
- 这是无兼容契约的行为：记录验证时的 CC 版本（待补：CC CLI 版本号），并在 CC 升级后用运行时 canary 自检（reconcile 后确认至少一个被链接 skill 仍可见）防回归。
