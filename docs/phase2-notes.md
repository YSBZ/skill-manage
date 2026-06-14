# SkillManage 二期 — 调研与需求草稿

> 状态：草稿，功能清单收集中。待清单齐全后走 ce-brainstorm/ce-plan 正式立项。
> 一期已完成并提交于分支 `feat/foundation`。

## 一、Codex CLI 调研结论（已完成）

实测本机 `codex-cli 0.136.0-alpha.2` + 官方文档/openai-codex 仓/issue 交叉验证。

**总判定：可行，但软链可靠性差，是最大风险点。**

### 与 Claude Code 的对照

| 维度 | Claude Code（一期） | Codex CLI |
|---|---|---|
| skill 格式 | `SKILL.md`（name/description） | 完全相同；可选多一个 `agents/openai.yaml` 兄弟文件（声明 MCP 依赖、关闭隐式调用等，CC 无对应） |
| 用户级目录 | `~/.claude/skills/` | `~/.codex/skills/`（= `$CODEX_HOME/skills`，可被 `CODEX_HOME` 覆盖） |
| 项目级目录 | `.claude/skills/` | **有歧义**：官方文档写 `.agents/skills`，但 openai/codex 仓自身用 `.codex/skills`，维护者也建议 `.codex` → 工具侧两条都要兼容 |
| 撞名 | first-found（项目被全局遮蔽） | 两个都显示，不遮蔽（更乱） |
| 系统 skill | — | `~/.codex/skills/.system`（Codex 自有，会重写/删，**绝不能碰**，靠 `.codex-system-skills.marker` 标记） |
| 托管 skill 仓 | — | `~/.codex/vendor_imports/skills`（Codex 托管的 git clone，只读勿动） |
| 停用不删 | 无 | `config.toml` `[[skills.config]] enabled=false` |
| 调用语法 | `/skill-name` | `$skill-name` |

### ✅ 软链 spike 实测（2026-06-14，本机 codex 0.136.0-alpha.2）

**核心思路验证：skillmanage 作为纯映射目录成立。** 用 `codex debug prompt-input`（渲染模型可见的 `### Available skills` 目录）作确定性探针：

- 把外部目录（`/tmp/...`，模拟 skillmanage 受管存储）**软链**进 `~/.codex/skills/<name>` → Codex **正常识别并列入 Available skills** ✓。即 **#9365 在 0.136 已修复**，软链可用，无需 copy 作默认策略。
- 结论：同一份 SKILL.md，软链映射到 `~/.claude/skills/` 与 `~/.codex/skills/` 两处即可，两个 agent 都认。**Codex 支持 ≈ 多一个链接目标，零格式转换、零改写**（呼应"本工具不修改 skill，只做映射"）。

**唯一需处理的坑 — #22275 递归扫描仍存在**：源 skill 目录里若含**嵌套 SKILL.md**（如 `skill/nested-child/SKILL.md`），Codex 会把嵌套的也注册成独立 skill，污染列表。实测嵌套 `zzz-sm-spike-nested` 确实被单列。
- 设计应对：reconcile/scan 时检测源 skill 目录内是否有嵌套 SKILL.md，若有则对 Codex 目标**告警**（或在收编/链接前提示），避免污染。CC 侧无此问题。

（#11314「软链整个 skills 父目录不加载」属 won't-fix，但我们只软链单个 skill 子目录，不受影响。）

### 对一期代码的复用结论

- **scanner**：原样复用（SKILL.md 格式两端一致）。
- **linker**：软链 primitive 原样复用于 Codex；copy 仍只作跨卷回退（KTD12），不必为 Codex 默认。
- **新增**：UI 链接目标下拉里加 `~/.codex/skills/`；项目级先支持 `.codex/skills`（仓实践）并兼容 `.agents/skills`（文档）。
- **守护区**：`~/.codex/skills/.system`、`~/.codex/vendor_imports/skills` 只读勿碰。
- **嵌套 SKILL.md 守卫**：见上 #22275。

### 其它注意点

- skills 注入系统提示有 ~8000 字符上限；软链过多 + 描述冗长会导致靠后的 skill 被静默丢弃。
- `AGENTS.md` 可按名引用 skill 载入上下文（与目录发现机制并行，CC 无对应）；二期暂不涉及。

## 二、二期范围（已收敛 2026-06-14）

**方案标尺**：skillmanage = **纯映射层**（只建链接、不改 skill 内容）+ 一期三条不变式（① git 仓只读镜像 `reset --hard`；② never-clobber 真实目录；③ manifest 所有权）。所有二期任务须与此一致。

### 核心（必做）

- **F2 · Codex 个人级链接目标**：链接目标加 `~/.codex/skills/`，软链（已实测 0.136 可用）。守护区 `~/.codex/skills/.system`、`~/.codex/vendor_imports/skills` 只读勿碰。
- **F3 · 嵌套 SKILL.md 守卫**：链接到 Codex 目标前，检测源 skill 目录内是否存在嵌套 `SKILL.md`；有则告警（防 #22275 递归注册污染 Codex 列表）。CC 侧无此问题。
- **F4 · 跨-harness 选择**：一个源 skill 可选择映射到 CC / Codex / 两者，链接状态合并展示。映射层的核心交互。
- **F5 · 项目级 Codex 目标**：项目内 `.codex/skills`（openai/codex 仓实践），兼容 `.agents/skills`（官方文档）。类比一期项目级 `.claude/skills`，与 F2 一并实现。

### 纳入

- **F1 · 收编本地 skill（仅个人文件夹目的地）**：选中本地 CC skill → 移动到 skillmanage 受管的**非 git 个人文件夹**（`centralDir/local/<name>/`）→ 原位置默认软链回去。本质是"受管存储当作另一个链接源"，与 repos 同构。
  - **采集来源**：仅 Claude Code（`~/.claude/skills/` 及项目 `.claude/skills/`）。
  - **目的地**：仅个人文件夹。**「移动到 git 仓」目的地已砍**——它破坏只读镜像不变式（移进去会被 `reset --hard`+`clean -fd` 清掉），且超出"纯映射层"范畴；若将来要做需单独立项（引入与只读镜像并存的"可写仓"双模式）。
  - 细化点（plan 阶段）：① 采集时区分"本地真身 skill"与"skillmanage 已建的软链"，只收编真身不重复收编；② manifest 记录新源类型"个人存储"，linker 的 `looksOurs` 签名扩展到个人存储根；③ 移动原子性——先复制校验 → 再建链 → 最后删原位，任一步失败可回滚，绝不丢数据；④ UI 收编入口。
- **F6 · 停用不删（约束实现）**：skillmanage 层 enable/disable，临时关掉一个 skill 而不删源文件、不丢选择。**实现约束**：靠链接的增删（移除/恢复软链）实现，**不写 agent 配置**（不改 Codex `config.toml`），以守住"纯映射层"。
- **F8 · 跨-harness 搜索**：一期已有的搜索/过滤扩展到覆盖 CC + Codex 两端已链接 skill。有 F4 后基本是免费扩展。

### 推后到三期（二期不做）

- **F7 · 健康诊断面板 + 一键修复**：信号多数一期 UI 已有（冲突摘要 + PruneDangling），独立面板属增量价值。
- MCP server 管理、slash command/prompt 管理、marketplace、LLM 安全扫描、其它 harness（Cursor/Gemini）。
