---
title: SkillManage 源与获取模型补全 —— 团队 git 源 / 社区多源（skills.sh 及其它）
status: design-note
date: 2026-06-18
phase: 5
relates: [[2026-06-15-skillmanage-phase3-requirements]], [[2026-06-18-skillmanage-phase5-skillsh-requirements]]
---

# 源与获取模型补全：团队 git 源 vs 社区多源

> 这是对 [[2026-06-15-skillmanage-phase3-requirements]]（三期源模型）与 [[2026-06-18-skillmanage-phase5-skillsh-requirements]]（五期其一：skills.sh 在线搜索+安装）的概念补全，把「社区多源」想清楚。本文是设计笔记，不是需求文档；落地时仍需 brainstorm → plan。

## 一、两条调研事实（决定了模型边界）

源自三期调研与本轮 CLI 实测：

1. **业内获取路径本就多元**（三期调研）：git 仓克隆 / registry 安装器（skills.sh）/ 官方插件市场 / 本地手写。同一 agent 目录多工具共存。开放标准是 **Agent Skills 规范 + `.agents/` 跨工具约定（AAIF 治理）**。
2. **skills.sh 的台账记了来源**（三期 R7.1）：`~/.agents/.skill-lock.json`(v3) 每个 skill 带 `source`（owner/repo）、`sourceUrl`、`sourceType`、`skillFolderHash`、`installedAt`/`updatedAt`。
3. **`npx skills` 是源无关的安装器，但搜索锁死 skills.sh**（本轮实测）：
   - `npx skills add <任意 git 仓>` 能装（`-l` 列举整仓、`-s` 选装、`-a universal` 只落 canonical 不软链 harness）。
   - `npx skills find <词>` **只搜 skills.sh 一个中心索引**——CLI 没有「换/加 registry 搜」的口子。

## 二、补全后的模型：两个正交轴

把「远端」这个含糊词拆成两个正交轴，模型就干净了。

### 轴 A — 获取方式（谁把 skill 弄到本机、canonical 归谁）

| 方式 | 面向 | canonical | 更新归谁 | 默认行为 |
|---|---|---|---|---|
| **git 全量镜像** | **团队内部仓** | `~/.skillmanage/repos`（我们建） | 我们（git pull + reset --hard） | 整仓 clone、本地维护、自动更新 |
| **npx 选择性安装** | **社区 / 外部** | `~/.agents/skills`（skills.sh 建） | skills.sh（`npx skills update`，我们转交） | 精选、不全量、归别家管 |
| **本地** | 手写 / 收编 | `~/.skillmanage/local` | 无 / 用户 | 受管 |

### 轴 B — 源（仓库 / registry），是「npx 安装方式」的参数

在「npx 安装」这条方式里，**源是可配置的参数**。关键认知（本轮修正）：**每个源都有自己的「搜索 / 枚举入口」，只是机制和成本不同**：

| 源 | 自带的发现入口 | 成本 |
|---|---|---|
| **skills.sh registry** | 关键词搜索 `npx skills find <词>`（托管索引） | 快（一次 HTTP 查询） |
| **任意 git 仓** | 枚举整仓 `npx skills add <url> -l` → 我们本地按关键词过滤 | 较重（瞬时 clone 整仓 + 本地过滤） |

→ **「跨源搜索」是可达的，但不在 CLI 层、而在我们架构层**：一次查询 **fan-out 到每个已配置源各自的入口**，结果**聚合 + 按源标注**呈现。没有「一个 upstream 帮你搜全部 registry」的东西，编排由 SkillManage 自己做。

## 三、定位区分（用户语言）

- **git 源 = 团队仓**：默认全量 clone、本地维护、自动更新。你要整套、要自管、要私有仓/分支/凭据 → 走这条。
- **社区源 = skills.sh 及其它可添加的 git 仓**：偏「社区」概念，挑着装。你只要某仓一两个 skill、且不在意更新归 skills.sh → 走这条。
- **同一个仓只能走一条路**（全量镜像 xor 精选安装），否则两份 canonical、撞名。

## 四、社区「多源」怎么落（建议形态）

1. **一份可添加的「社区源清单」**：每条是一个源；skills.sh registry 作为内置的特殊一条，其它是 git 仓 URL。
2. **每个源 = 一个搜索/枚举入口**（统一抽象）：
   - skills.sh registry：`find` 关键词搜（快）。
   - git 仓：`-l` 枚举 + 本地关键词过滤（需 clone，较重）。
   - 安装一律 `-s -g -y -a universal`（只落 canonical），再由我们 linker 软链。
3. **跨源搜索 = fan-out 聚合**：一次查询并发调用清单里每个源的入口，结果合并、按源分组呈现。性能随源数与源类型而变（registry 快、git 仓需 clone）——所以要**缓存枚举结果**、并允许**按源开关**（不必每次全打）。
4. **按源分卡隔离 = 可行**：装好的 skill 读 `.skill-lock.json` 的 `source`/`sourceUrl` 即可按来源分组成不同卡片/分区（当前在线卡上的 `owner/repo` 徽章已是这条数据）。「放一起还是分卡」纯属呈现选择，数据上随时能隔离。

## 五、硬约束与取舍（落地前必须认的）

- **跨源搜索由我们编排，不是 CLI 给的**：没有「一个 upstream 搜全部 registry」的东西。我们对每个源调它自己的入口（registry → `find`；git 仓 → `-l` 枚举 + 本地过滤）再聚合。**成本随源数与类型上升**——git 仓源每次都要 clone 枚举，所以必须缓存 + 按源开关，不能天真地每次 fan-out 全部。
- **私有仓的 npx 选装鉴权受限**：`npx skills add <url>` 走系统 git 凭据（SSH agent / credential helper），**用不上 SkillManage 存的 PAT**。私有仓要么走「git 全量镜像」（已支持凭据），要么得把凭据喂给 npx 的 git（复杂度高一档）。
- **第④不变式不变**：npx 装的归 skills.sh 管 → 只读识别、更新转交、绝不接管覆盖（三期 R6.1）。
- **canonical 不搬家**：我们的 canonical 仍在 `~/.skillmanage/{repos,local}`，不抢 `~/.agents`（三期 R1.3/R1.4）。

## 六、与已交付（五期其一）的关系

五期其一已落「skills.sh 在线搜索 + 安装（`-a universal` 只装 canonical，再由 SkillManage 自有 linker 软链到当前目录）」。本模型把它**推广**为：

- 搜索面：从「只搜 skills.sh」**放开到「多源 fan-out 聚合」**——每个源调自己的入口（registry 搜 / git 仓枚举过滤），合并按源呈现。
- 安装面：从「只接受 skills.sh 搜索结果的 pkg」**放开到「任意 git 仓 URL 浏览选装」**——即「可添加的社区源」。
- 展示面：按 `.skill-lock.json` 来源分卡。

## 七、后续（建议拆成五期其二/其三）

- **其二**：安装源放开 —— 「从 git 仓挑装」入口（贴 URL → `-l` 列举 → 勾选 → `-s -g -y -a universal` 装 → 自有 linker 软链当前目录）。
- **其三**：社区源清单管理 + 按源分卡 —— 维护可添加的源清单，现状/搜索视图按 `source` 分组隔离。
- 私有仓 npx 鉴权（若需要）单独评估，复杂度最高，可最后做或不做（私有仓走 git 全量镜像即可）。
