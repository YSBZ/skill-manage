---
title: SkillManage 五期需求（其一）—— 接入 skill.sh：在线搜索 + 安装
status: requirements
date: 2026-06-18
phase: 5
scope: standard
excludes: [SMB, marketplace, MCP, LLM-scan, 其它harness主动支持]
related: [[2026-06-15-skillmanage-phase3-requirements]]
---

# SkillManage 五期需求（其一）：接入 skill.sh —— 在线搜索 + 安装

## 一、背景与问题

四期（v4.3.3）SkillManage 已是跨 harness 的 skill 统一控制面：能管 git 源、目录源、本地源，能在「目录现状视图」里按目标目录扫描、归类来源、启用/停用，并能委托更新插件。

但有一个缺口：**用户想要一个还没装的 skill 时，SkillManage 帮不上忙**。当前只能搜到「已经在本机的」skill（git 镜像 / 本地 / 已装的 skills.sh）。要发现并安装新 skill，用户得离开应用、去 https://skills.sh/ 网站找、再手动 `npx skills add`。

skill.sh（`vercel-labs/skills` 的 `npx skills`）是开放 skill 生态的事实包管理器，canonical 在 `~/.agents/skills`，台账 `~/.agents/.skill-lock.json`(v3)。四期已把它当**目录源只读识别**——但只是「认得已装的」，没有「帮你找和装新的」。

**五期其一要补的就是这一步：让 SkillManage 内就能搜 skills.sh 在线目录、并一键安装。** 这让 SkillManage 从「管理已有」升级到「发现 + 获取 + 管理」的闭环，且不抢 skills.sh 的所有权。

## 二、目标用户与价值

- **主要用户**：本机使用 SkillManage 管理跨 harness skill 的开发者（即当前用户本人及同类）。
- **价值**：想要某能力的 skill 时，不必跳出应用——在 SkillManage 内搜到、看安装数/来源判断质量、一键装到 canonical，然后用现有启用/停用挂到任意 harness 目录。把「发现 → 安装 → 启用」收进一个界面。

## 三、目标与成功标准

1. **能搜到**：在 SkillManage 内搜到 skills.sh 上可安装的 skill，无需离开应用去网站。
2. **能装到**：在线结果一键安装到 canonical（`~/.agents/skills`），安装后立即在现状视图/本地搜索里看到，并可用现有机制启用到某 harness 目录。
3. **不破坏**：全程只调 skills.sh 自己的 CLI，不接管它的文件/链接/台账（第④不变式）；更新仍转交其原生 `npx skills update`。
4. **诚实**：安装成功/失败如实反馈，绝不假成功（沿用四期委托更新的教训）。

## 四、需求

### R1 在线搜索开关（融入现有全局搜索）
全局搜索框增加一个 **「包含线上（skills.sh）」勾选**，默认**关**。勾选后，搜索的范围扩展到 skills.sh 在线结果。不新增独立 tab——复用现有 `renderSearchResults` 全局搜索面板。

### R2 显式触发，本地不受影响
在线搜索**不随每次按键实时打 npx**（冷启动慢）。勾选「包含线上」后，由**显式动作触发**（搜索按钮 / 回车），带 loading 态。本地搜索仍保持实时。

### R3 在线结果展示
在线结果放在搜索面板内的独立 **「在线（skills.sh）」分区**。每条显示：
- skill 标识 `owner/repo@skill`
- **安装数**（质量信号）
- 来源 `owner/repo`
- 指向 skills.sh 的链接
按安装数从高到低排序。（质量把关交给用户看安装数/来源，**不做内容安全扫描**——沿用三期边界。）

### R4 安装（两步之第一步）
在线结果上提供 **「安装」** 动作 → 调用 `npx skills add <owner/repo@skill>`，装到 skills.sh canonical（`~/.agents/skills`）。
- 安装是**非接管**：装完它就是 skills.sh 的目录源，归 skills.sh 所有；其更新转交原生 `npx skills update`（第④不变式）。

### R5 安装后识别（安装 ≠ 启用）
安装成功后**重扫** `~/.agents/.skill-lock.json` + `~/.agents/skills`，新 skill 作为**目录源**出现在现状视图与本地搜索里。
**安装与启用解耦**：是否把它启用（建软链）到某个 harness 目录，是用户**后续单独的动作**，复用四期现有的启用/停用开关。

### R6 操作反馈
- 在线搜索、安装过程都有 loading 态。
- 安装成功提示：「已安装 X 到 skills.sh，可在搜索/现状视图中启用」。
- 安装失败**如实**提示原因（不假成功）。

### R7 健壮性与降级
- 离线 / npx 不可用时，「在线」分区显示不可用提示，**本地搜索不受影响**。
- 优先使用全局已装的 `skills` 命令，回退 `npx -y skills`，并处理冷启动延迟（loading）。

## 五、关键流程

1. **发现并安装**：搜索框输入关键词 → 勾选「包含线上」→ 点搜索 → 本地结果实时出现、在线结果 loading 后出现在「在线」分区 → 用户看安装数/来源选中一条 → 点「安装」→ `npx skills add` → 成功提示 → 新 skill 进入目录源。
2. **安装后启用**：在现状视图或本地搜索里找到刚装的 skill → 用现有启用开关挂到当前 tab 的 harness 目录。
3. **离线降级**：未联网 → 勾选「包含线上」后点搜索 → 「在线」分区显示「当前不可用（需联网）」，本地结果照常。

## 六、范围边界

**本次做**：在线搜索（`find`）、安装（`add`）、安装后识别与反馈。

**本次不做 / 延后**：
- **SMB（局域网共享盘 skill）**——五期其二，排后。
- 在 SkillManage 内编排 skills.sh 的**更新 UI**（更新仍转交原生 `npx skills update`；四期已有委托更新模式，可后续接，但本次不强求）。
- marketplace、MCP、LLM 安全扫描、主动支持其它 harness（沿用三期边界 [[2026-06-15-skillmanage-phase3-requirements]]）。

## 七、依赖与假设

- **A1（格式漂移风险）**：依赖 `npx skills find` 输出格式稳定。当前**无 `--json`**，靠解析文本（`owner/repo@skill` + 安装数 + URL，ANSI 可剥）；上游改格式会破——plan 期需评估容错/格式探测。
- **A2（非交互安装）**：假设 `npx skills add` 可无人值守执行；**未实测**，plan/work 期须验证（可能需 `-y` 或环境变量、scope 参数）。
- **A3**：假设用户机器有 node/npx（与现有 npx 调用一致）。
- **A4**：仅网络可用时才有在线搜索。

## 八、Open Questions（留给 ce-plan）

- **OQ1**：`npx skills add` 的确切非交互参数与落点（需 `-y`？装到哪个 scope？能否指定目标？）——plan/work 实测确定。
- **OQ2**：在线结果解析的健壮性——是否值得探测 `--json` 或锁定 `skills` 版本以防格式漂移。
- **OQ3**：在线结果与本地结果**去重/标注**——同一 skill 本地已装且在线也搜到时，应标「已安装」而非再给「安装」。
- **OQ4**：「包含线上」勾选状态是否**持久化**（记住用户偏好）。
