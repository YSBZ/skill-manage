---
title: SkillManage 三期改造需求文档
status: draft
date: 2026-06-15
type: requirements
phase: 3
branch: feat/foundation
supersedes_context: docs/phase2-notes.md
---

# SkillManage 三期改造需求文档

## 一、问题陈述

二期把 SkillManage 做成了「跨 harness（Claude Code / Codex）的 git 仓 skill 映射与自动同步层」。三期由一次评审驱动，评委暴露出三类问题：

1. **现状视图有歧义**：选中一个同步目录（tab）后，主面板显示的是「来源库里可被映射进来的 skill」，而不是「这个目录里实际有什么 skill」。用户期望看目录现状，看到的却是来源候选，概念错位。
2. **映射交互不明显、无反馈**：勾选即建软链，但「勾选」语义不清；建/拆软链是真实改动文件系统，却没有任何提示；「立即更新」跑完也不告诉用户到底有没有更新。
3. **目录管理未对齐业内通用方案**：业内已形成开放标准（Agent Skills 规范 + `.agents/` 跨工具目录约定），并有 skills.sh（`npx skills`）等安装器在用同样的「canonical + 软链」模型。SkillManage 与它们共用同一批目录，必须确保**不破坏**别家约定、并**向通用方案靠近**。

更深一层：业内获取 skill 的路径已经多元化（git 仓克隆 / registry 安装器如 skills.sh / 官方插件市场 / 本地手写）。同一个 agent 目录里会**多工具共存**（用户机器实测：`~/.claude/skills/` 下 skills.sh 的链接与 SkillManage 的链接已干净并存）。SkillManage 若只认自己装的、看不见别家装的，就既有歧义、又谈不上「管理」。

## 二、定位升级（本期核心立场）

> **SkillManage 是 skill 的统一「控制面 / 管理者」，不是又一种安装器。**
> 它以**目录为中心**扫描所有标准位置，把每个 skill 按**来源**归类，统一回答「装了什么 · 谁装的 · 能不能更新 · 有没有撞名」；管理动作分档：自己 git 仓源 → 映射 + 后台自动更新；别家装的 → 只读识别 + 转交其原生更新；本地手写 → 收编。**绝不破坏业内通用目录规则，并主动向 Agent Skills / `.agents` 开放标准靠近。**

## 三、Actors

- **A1 · 个人 skill 维护者 / 使用者**：在本机用 Claude Code / Codex 等工具，skill 来自多种渠道（部门 git 共享仓、skills.sh、自己手写）。希望一处看清、一处管理，且不破坏其它工具装好的东西。
- **A2 · 共存的第三方工具**（被动角色）：skills.sh（`npx skills`）、官方插件系统等。SkillManage 必须与之安全共存——只读识别、绝不接管覆盖。

## 四、成功标准

- **SC1**：选中任一同步目录，主面板如实展示该目录**实际**包含的 skill，且每条标明来源（git 源 / skills.sh / 插件 / 本地手写 / 未知）。歧义消除。
- **SC2**：启用/停用某 skill 后，界面立即明确反馈「已在 `<目录>` 建立 / 移除软链 `<名>`」；「立即更新」结束后明确报「N 个源有更新 / 已是最新」。
- **SC3**：SkillManage 扫描与撞名行为符合 Agent Skills 标准；`~/.agents/skills`（及项目级 `.agents/skills`）被识别为一等来源；对别家装的 skill 零破坏（可在同一目录长期共存、互不误删）。
- **SC4**：mac / Windows / WSL 下，默认 skill 根能被正确探测；非标准位置可由用户手动登记，不依赖硬编码全覆盖。

## 五、需求

### R1 · 源 / 目标统一模型

把现有的「git 仓 + @local」抽象统一为 **源（Source）→ 目标（Target），软链连接** 一个模型。

- **R1.1**：源分三类——
  - **git 源**：git 仓（部门共享仓 / 任意 repo），点「更新」拉取上游；canonical 在 `~/.skillmanage/repos/<repo>`。
  - **目录源（只读，别家管理）**：别家工具装的目录（首要为 skills.sh 的 `~/.agents/skills`），**只读识别**、绝不接管；canonical 即该目录自身，更新转交其原生工具（`npx skills`）。
  - **本地源**：能力相同、按**来源**分两种——
    - **受管存储（SkillManage 创建）**：收编 / 手写归入 `~/.skillmanage/local`，即 `@local` 命名空间（可删 skill）。
    - **登记目录（用户选择）**：用户挑选的任意本地文件夹，**实时识别、不复制**（软链直接指向原文件夹，绝不改动它）；用 `@dir:<id>` 命名空间，整源可移除。详见 [[#R1.5]]。
- **R1.2**：目标 = 各 harness 的 skill 目录。源与目标都是**一等可登记对象**。
- **R1.3**：canonical 一律保持自建在 `~/.skillmanage/{repos,local}` 下，**不**搬进 `~/.agents`（避免与 skills.sh 抢同一 canonical 的所有权）。
- **R1.4**：`~/.agents/skills`（用户级）与 `.agents/skills`（项目级）升为**一等来源**（默认被扫描识别）；但**默认不作为写入目标**——SkillManage 不主动把自己管的 skill 链进 `.agents`（同样为避免所有权冲突）。用户可手动把它登记为目标，属显式可选行为。
- **R1.5 · 登记本地目录源**：用户可「添加本地源」——选一个任意本地文件夹（其本身是 skill，或含 skill 子目录），登记为一个一等本地源：
  - **不复制**：实时扫描原文件夹；启用 / 整仓跟随时软链**直接指向原文件**，改了原文件夹即时生效（受 ② never-clobber 保护，绝不改动原文件夹）。
  - **可多个**：每个登记文件夹一个独立 `@dir:<id>`；可取**别名**。
  - **与 `@local` 同能力**：都是「本地源」，差别只在来源（一个 SkillManage 创建、一个用户选择）。
  - **禁止与目录源/npx 路径重叠**：拒绝把 `~/.agents/skills`（skills.sh 管辖）等已识别目录登记为本地源，避免双重所有权。

### R2 · 跨平台位置注册表

- **R2.1**：内置按平台 + 环境变量解析默认 skill 根的注册表：
  - mac / Linux：`$HOME/.claude/skills`、`$HOME/.codex/skills`（含 `$CODEX_HOME` 覆盖）、`$HOME/.agents/skills`。
  - Windows：`%USERPROFILE%\.claude\skills` 等；软链 = 目录 junction，跨卷回退为复制（二期已实现，复用）。
  - WSL：当作**独立 Linux 环境**，只管 WSL 自身 `$HOME/...`，**默认不触碰** `/mnt/c/...` 的 Windows host 路径。
- **R2.2**：启动时**自动探测**哪些默认根实际存在并可用。
- **R2.3**：任何平台差异 / 非标准位置 / 跨环境（如 WSL 想管 Windows host）路径，一律通过**用户手动登记源或目标**解决，不靠硬编码穷举。

### R3 · 目录现状视图（解评审点 1）

- **R3.1**：主面板由「来源库候选项」改为**按当前选中 tab（目标目录）实际扫描**得到的现状清单。
- **R3.2**：每个 skill 标注**来源归类**：git 源 / skills.sh（或其它目录源）/ 插件 / 本地手写（@local）/ 未知软链。
- **R3.3**：「从库添加 skill 到本目录」由「默认铺满主面板」降级为一个显式的 `+ 添加` 动作。
- **R3.4**：现状视图随 tab 切换而切换（与二期「未备份 skill 跟随 tab」一致的语义）。

### R4 · 启用 / 停用 + 操作反馈（解评审点 2）

- **R4.1**：把「勾选建链」改为语义明确的**启用 / 停用**开关——启用 = 在该目录建软链；停用 = 拆链。
- **R4.2**：启用 / 停用后**即时反馈**：「已在 `<目录>` 建立软链 `<名>`」/「已移除软链 `<名>`」。
- **R4.3**：「立即更新」结束后明确提示结果：「**N 个源有更新 / 已是最新**」（与按 hash 比对的业内做法一致）。

### R5 · scanner 对齐 Agent Skills 标准（解评审点 3 之一）

- **R5.1**：撞名优先级遵循业内统一约定——**项目级 > 用户级**；同 scope 内 first/last-found 任选其一并保持一致。
- **R5.2**：扫描含 `SKILL.md` 的子目录；跳过 `.git/`、`node_modules/`；设深度上限（4–6 层）与目录数上限（~2000），防止失控扫描。
- **R5.3**：撞名时**告警**（让用户知道哪个 skill 被遮蔽）。

### R6 · never-break 收口为正式第④条不变式（解评审点 3 之二）

在一期三条不变式（① git 仓只读镜像；② never-clobber 真身；③ manifest 所有权）之上，新增：

- **R6.1 · 第④条不变式「never-break 别家」**：对**非本工具建立**的链接与文件（skills.sh 的链接、插件 skill、用户手写真身、其它工具产物）一律**只读识别、绝不接管、绝不覆盖、绝不删除**。该不变式与「manifest 所有权」互补，正式写入设计文档与代码注释。

### R7 · 目录源只读识别与 skills.sh 互通（解评审点 3 之三）

- **R7.1**：识别目录源（首要为 skills.sh）装的 skill——读取其台账 `~/.agents/.skill-lock.json`（v3：含 `source` / `sourceUrl` / `skillFolderHash` / `installedAt` / `updatedAt`），结合软链指向判定归属。
- **R7.2**：在现状视图中只读展示这些 skill，标来源（skills.sh）与来源仓信息；不提供启用/停用（不属本工具所有）。
- **R7.3 · 更新转交**：默认在 UI 上**提示**「由 skills.sh 管理，请用 `npx skills update` 更新」；当检测到本机 `npx` 可用时，额外提供**一键代调** `npx skills update <name>` 的按钮。代调失败要清晰报错，且不破坏第④条不变式（由 skills.sh 自身工具改写其 canonical，SkillManage 不直接写 `~/.agents`）。

## 六、关键流程

- **F1 · 看清一个目录的现状**：选 tab → 主面板列出该目录所有 skill，每条带来源标签与状态（已映射 / 真身 / 别家管理 / 撞名告警）。
- **F2 · 启用一个 git 源 skill 到当前目录**：在 `+ 添加` 里选来源 skill → 启用 → 即时反馈「已建软链」。
- **F3 · 停用**：在现状视图把某 git/本地源 skill 切到停用 → 即时反馈「已移除软链」。
- **F4 · 看见并更新 skills.sh 装的 skill**：现状视图显示其为 skills.sh 来源 → 点更新 → 默认提示文案；若 npx 可用 → 一键代调。
- **F5 · 登记一个新源 / 目标**：手动登记 git 源、目录源（如 `~/.agents/skills`）或目标目录（应对跨平台 / 非标准位置）。
- **F6 · 立即更新**：触发 → 各 git 源拉取 → 结束提示「N 个源有更新 / 已是最新」。

## 七、验收示例

- **AE1**：`~/.claude/skills/` 下同时有 skills.sh 装的 `find-skills`（→ `~/.agents/skills`）和 SkillManage 管的若干 skill。选中该 tab，主面板**两类都列出**，`find-skills` 标「skills.sh」且无启用/停用开关，SkillManage 管的标「git 源 / 本地」且可停用。无任何一方链接被误删。
- **AE2**：对一个 git 源 skill 点「停用」→ 该目录下对应软链被移除，界面提示「已移除软链 `<名>`」，真身与其它工具的链接不受影响。
- **AE3**：同名 skill 同时存在于项目级与用户级目录 → 现状视图按「项目级 > 用户级」标明生效者，并对被遮蔽者给出撞名告警。
- **AE4**：在没有 `npx` 的机器上查看 skills.sh skill → 只显示提示文案，不出现一键代调按钮，也不报错。
- **AE5**：WSL 中运行 → 默认只探测并管理 WSL 自身 home 下的 skill 根；`/mnt/c/Users/...` 不被自动纳入，除非用户手动登记。

## 八、范围边界

### 本期明确排除（Deferred for later）
- MCP server 集中管理
- marketplace / 推荐仓注册表
- LLM 安全扫描
- 主动支持其它 harness（Cursor / Gemini 等）——但它们的目录可作为**目录源 / 目标被动登记**，不做专门适配。

### 不属于本产品身份（Outside this product's identity）
- SkillManage **不做安装器**：不取代 skills.sh / 插件市场的「获取 skill」职责，只做获取之后的统一管理与映射。
- SkillManage **不接管别家 canonical**：不把 `~/.agents` 等据为己有、不代别家工具改写其存储（除经用户显式触发、由别家原生工具执行的代调）。

## 九、依赖与假设

- **D1**：复用一期/二期既有能力——`internal/scanner`、`internal/linker`、`internal/reconcile`、`internal/pathutil` 可大幅复用；三期主要新增「源类型抽象 + 目录源只读识别 + 位置注册表 + UI 现状视图重构」。
- **D2 · 假设**：skills.sh 台账路径 `~/.agents/.skill-lock.json` 与字段（v3）稳定；其 canonical 默认在 `~/.agents/skills`。Windows / WSL 下 skills.sh 的默认根需在规划期实测确认（不同平台可能有差异）——若差异较大，靠 R2.3「手动登记」兜底。
- **D3 · 假设**：`.agents/skills` 作为跨工具中立目录的约定持续有效（Agent Skills 规范 + AAIF 治理）。

## 十、留待规划解决（Open Questions → ce-plan）

- **OQ1**：现状视图「来源归类」的判定算法——如何稳健区分 skills.sh 链接（指向 `~/.agents/skills` + 命中 lockfile）、插件 skill、本地手写真身、未知软链。
- **OQ2**：源类型抽象在 `config` / `manifest` 中的落地形态（如何与既有 enabled[]、repo 列表兼容，不破坏二期数据）。
- **OQ3**：位置注册表的具体探测顺序与 Windows / WSL 实测默认根。
- **OQ4**：一键代调 `npx skills` 的进程调用、超时与错误呈现细节。
- **OQ5**：现状视图与二期「未备份 skill 收编」侧栏的信息架构是否合并（两者都在描述「目录里有什么」）。

## 十一、实现期增补（2026-06-16，已实现并验证）

实现与试用过程中收敛的细化决策，回填为正式需求：

### R8 · 统一「源」概念与侧栏信息架构

- **R8.1**：侧栏标题为「源」；**git 仓 / npx skills / 本地源**三类**平级**展示，各有分区标题。
- **R8.2 · 来源专属动作落在分区标题行**：git 仓 → `导出 / 导入`（仅 git 仓库列表）+ 私有仓鉴权 `?`，且 **git 仓 URL/分支/添加表单**也归入 git 仓分区；npx skills → 卡片上的「更新」（代调 `npx skills update`）；本地源 → 「添加本地源」。
- **R8.3 · 本地源「展示分、使用合」**：侧栏中本地源**按文件夹分卡**（`local` 受管存储 + 每个登记目录各一卡，带别名与「移除」）；但在**使用面**（「管理」抽屉与目录现状视图）**合并为一个「本地源」类**，不按文件夹拆模块。合并展示时每个 skill **带来源标签徽章**（`local` / 文件夹别名）以区分归属。
- **R8.4**：原「+ 添加」入口更名为「**管理**」（启用 / 整仓跟随 / 取消跟随）。
- **R8.5 · npx skills 列表**：点开 skills.sh 来源弹窗，每个 skill 标**来源仓**（lockfile `source`，hover 看完整 `sourceUrl`）并给出 **`npx skills update <name>`** 命令。

### R9 · 在应用内帮助（`?`）随功能同步（标准）

- **R9.1**：凡功能改动，**必须同步更新 SkillManage 应用内的 `?` 帮助**——含「源」概念说明（`源` 标题旁 `?`）、git 仓鉴权说明、`SkillManage 使用指南`（`?` 总指南）、各分组的 `?`。帮助是产品的一部分，不允许与实际行为脱节。

### R10 · 整仓跟随的所有权一致性（缺陷修复）

- **R10.1**：启用某来源「整仓跟随」（`<ns>/*`）时，**收编**同目标下该来源已有的单条启用项（`<ns>/<skill>`），令 follow 成为这些软链的唯一所有者；否则取消跟随只删 `<ns>/*`、残留单条启用与其软链，逼用户逐个停用。已修复并加回归测试。

> 实现备注：登记本地目录源用 `@dir:<id>` 选择器；`config.local_sources`（含 `id`/`label`/`path`）持久化；reconcile 经 `SetDirSources(id→path)` 解析、`source.KindDir` 归类。早期曾用「复制进 `~/.skillmanage/local`」的实现（已废弃）——这会把所选文件夹的 skill 拷进受管存储、不产生独立卡片；如发现 `local` 存储里有此类残留副本，按孤儿副本清理即可。
