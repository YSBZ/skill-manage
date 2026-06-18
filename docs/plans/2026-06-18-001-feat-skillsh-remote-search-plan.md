---
title: "feat: SkillManage 五期（其一）接入 skill.sh —— 在线搜索 + 安装"
type: feat
status: completed
date: 2026-06-18
phase: 5
origin: docs/brainstorms/2026-06-18-skillmanage-phase5-skillsh-requirements.md
depth: standard
---

# feat: SkillManage 接入 skill.sh —— 在线搜索 + 安装

## 摘要

让 SkillManage 在应用内**搜到 skills.sh 上可安装的 skill**并**一键全局安装**到 canonical（`~/.agents/skills`），装完作为目录源被识别、可用现有启用/停用挂到任意 harness 目录。全程只调 skills.sh 自己的 CLI（`npx skills find` / `add`），不接管它的文件/链接（第④不变式），失败如实反馈，离线降级。

架构上与四期「插件委托更新」**同构**：shell-out 调外部 CLI → 剥 ANSI → 诚实判定 → 不接管。skills.sh 的 list/update 委托、npx 探测、目录源识别四期已具备，本期主要新增 find/add 两条链路与前端「在线搜索」入口。

（see origin: docs/brainstorms/2026-06-18-skillmanage-phase5-skillsh-requirements.md）

---

## 问题框定

四期 SkillManage 只能搜到「已经在本机」的 skill（git 镜像 / 本地 / 已装的 skills.sh）。用户想要一个还没装的 skill 时，得离开应用去 https://skills.sh/ 网站找、再手动 `npx skills add`（且该命令默认是交互式 TUI，会问 scope / 装到哪些 agent / 方式）。本期补上「发现 → 安装」这一步，把闭环收进应用，并**强制全局安装**以保证可控。

---

## 关键技术决策（KTD）

- **KTD1 — 安装强制全局 + 非交互。** `SkillsAdd` 固定带 `-g`（global，user-level，canonical 落 `~/.agents/skills`）+ `-y`（跳过所有确认/选择提示）。**绝不**用 project-level 安装——否则散落在各项目、无法统一管理（用户硬约束）。依据：实测 `npx skills add --help` 提供 `-g/--global`、`-y/--yes`、`-a/--agent`、`-s/--skill`、`--all`。**安全权衡（记录在案）**：`-y` 会同时跳过 skills.sh 交互流程里的安全风险评估（Snyk/Socket 扫描结果展示）。本期沿用三期边界**不做内容安全扫描**，以「安装数 + 来源」作为替代信任信号——此取舍是有意为之，写明于此以便日后审计/重审。
- **KTD2 — `find` 解析文本，非 JSON。** 实测 `npx skills find` **不支持 `--json`**（`--json` 被忽略，仍回文本）。输出格式：每条 `owner/repo@skill  <N> installs` + 下一行 `└ <url>`，ANSI 上色。解析方案：先剥 ANSI（复用 `ansiRe`），按行正则提取 `(owner/repo@skill)` + 安装数 + 紧随的 `└ url`。**格式漂移容错**：解析失败的行跳过而非整体报错；零解析结果但 exit 0 时提示「未找到 / 或格式已变」，不假装报错也不假装成功。
- **KTD3 — 去重/已装标注用 `list --json`。** 实测 `npx skills list --json` 提供 machine-readable 输出。在线结果与「本地已装（skills.sh + 其他源）」比对：已装的 skill 标「已安装」、不给「安装」按钮（OQ3）。已装判定优先用 `list --json`（skills.sh 侧）+ 现有 `state.skillsShSkills`/inventory（SkillManage 侧）。
- **KTD4 — 安装 = canonical-only，启用是另一步。** 沿用 origin 的两步决策：`add` 只把 skill 落到 canonical，**不**自动 symlink 到任何 harness 目录；是否启用到某 harness 由用户后续用现有启用/停用开关完成。`skills add` 默认会顺带 symlink 到所选 agent——本期目标是抑制该行为（只落 canonical）。**确切的 symlink 抑制 flag 组合留作 work 期实测**（见 Deferred）。
  - **降级策略**：若 CLI 无法完全抑制、强制至少一个 agent symlink，则把它建出来的 harness 链视为 **skills.sh 所有**，不接管、不违反第④不变式。
  - **兜底网的已知缺口（评审 P1）**：现有健康度/冲突分类器只扫描**已登记的同步目标**（`s.cfg.Targets`）。若 skills.sh 把软链建在**未登记**的 harness 目录（如用户没把 `~/.claude/skills` 加为目标），该软链对健康度**不可见**——「交由健康度收口」会落空。因此 U3 必须：`add` 成功后重扫，若发现 `~/.agents/skills` **之外**新增了 harness 软链，**在成功 toast 里显式提示**（如「已安装；注意 skills.sh 同时在 X 建立了软链」），而非默认依赖可能覆盖不到的健康度视图。work 期实测须同时回答：(1) 抑制 symlink 的 flag；(2) 若必须指定 `-a`，传哪个 agent、其目录是否一定是已登记目标。
- **KTD7 — 新命令端点强制 origin 守卫。** `GET /api/skillssh/find` 与 `POST /api/skillssh/add` 都是**执行外部进程**的端点，必须在 handler 首行加 `originLoopbackOK(r)`（与 `handleDirSourceUpdate`/`handlePluginUpdate` 一致），作为 `requireAuth` 之外的 CSRF 纵深防御。`add` 尤其关键——它无二次确认即安装软件到本机文件系统。这是**强制要求**，不是「跟随模式」的隐含项（评审 P1）。
- **KTD5 — 诚实判定，复用四期教训。** `find`/`add` 都不能只看退出码（CLI 失败可能仍 exit 0）。`add` 判定：识别 `✔/Installed` 为成功、`✘/Error/Failed` 为失败、零变化为已存在；失败如实回传原因（参照 `handlePluginUpdate` 的 `firstFailureLine` 思路）。
- **KTD6 — 显式触发 + 离线降级。** 在线搜索不随每次按键打 npx（冷启动慢）；由「包含线上」勾选 + 显式动作（搜索按钮/回车）触发，带 loading。npx 不可用 / 离线时，「在线」分区显示不可用提示，本地搜索不受影响（复用 status 的 `npxAvailable`）。

---

## 需求追溯

| 需求 | 落点 |
|---|---|
| R1 在线搜索开关（融入全局搜索） | U4 |
| R2 显式触发、本地不受影响 | U4（前端触发）+ U2（后端按需调用） |
| R3 在线结果展示（安装数/来源/链接/排序） | U2（数据）+ U4（渲染） |
| R4 安装（`add` 全局、非接管） | U1 + U3（KTD1/KTD4） |
| R5 安装后识别（重扫、安装≠启用） | U3（重扫）+ U4（刷新） |
| R6 操作反馈（loading / 成功 / 诚实失败） | U2 + U3（诚实判定）+ U4（toast） |
| R7 健壮性与降级（离线 / npx 不可用） | U2 + U3 + U4（KTD6） |
| OQ3 在线/本地去重标注 | U4（KTD3） |
| OQ4 「包含线上」勾选持久化 | U4 |

---

## 实现单元

### U1. 后端 runner：SkillsFind / SkillsAdd

**Goal**：在 `skillsRunner` 接口与 unix/windows 实现、fakeRunner 上新增 `SkillsFind` 与 `SkillsAdd`，照抄现有 `npx skills` shell-out 模式。

**Requirements**：R4（add 基础）。

**Dependencies**：无。

**Files**：
- `internal/server/runner_unix.go`（修改：加 `SkillsFind` / `SkillsAdd`）
- `internal/server/runner_windows.go`（修改：同上）
- `internal/server/api.go`（修改：`skillsRunner` 接口加两个方法签名）
- `internal/server/dirsource_update_test.go`（修改：fakeRunner 实现两个新方法）

**Approach**：
- `SkillsFind(ctx, npxPath, query) (stdout, stderr string, err error)` → `npx skills find <query>`（参照现有 `UpdateAll` 的 `exec.CommandContext(ctx, npxPath, "skills", ...)`）。
- `SkillsAdd(ctx, npxPath, pkg) (stdout, stderr string, err error)` → `npx skills add <pkg> -g -y`（KTD1）。argv 离散、无 shell 元字符。
- 接口方法签名与现有 `UpdateAll(ctx, npxPath, ...)` 一致风格。

**Patterns to follow**：`internal/server/runner_unix.go` 现有 `UpdateAll`（`npx skills update -g --yes`）；`skillsRunner` 接口现有 `UpdatePlugin` 等。

**Test scenarios**：
- fakeRunner 能注入 `SkillsFind`/`SkillsAdd` 的桩输出与错误（供 U2/U3 测试用）。
- 无行为逻辑，本单元 `Test expectation: none -- 纯 runner 透传，行为测试在 U2/U3`。

**Verification**：`go build ./...` 通过；U2/U3 测试可通过 fakeRunner 驱动。

### U2. 后端 find handler：在线搜索 + 文本解析 + 诚实判定

**Goal**：新增 `GET /api/skillssh/find?q=<query>`，调用 `SkillsFind`、解析文本结果、返回结构化在线结果；npx 不可用时优雅降级。

**Requirements**：R2、R3、R6、R7、KTD2。

**Dependencies**：U1。

**Files**：
- `internal/server/api.go`（修改：路由 + handler + 解析函数 + 结果结构体）
- `internal/server/skillssh_find_test.go`（新建：解析与降级测试）

**Approach**：
- **handler 首行 `originLoopbackOK(r)`（KTD7）** + `requireAuth`（路由层）。
- **入参守卫**：`q` trim 后为空 → 直接返回 `{available:true, results:[]}`，**不调 npx**（避免误触发慢/超大查询）。`q` 以 `-` 开头 → 拒绝（HTTP 400），堵住 flag-injection（query 作为 argv 传给 `skills find`，前导 `-` 会被 CLI 当 flag 解析；评审 P2）。注释写明此守卫与 `pkg` 校验的非对称是有意的。
- **解析（KTD2）**：先抓 2–3 条真实 `npx skills find` 输出**逐字写进测试 fixture**，再据此写正则——不要凭格式形状空写。剥 ANSI 后：**pkg 捕获用严格锚定**（`^owner/repo@skill`，载荷，喂给 `add` 与去重，错不得），**安装数用宽松解析**（`2.6K`/`144.8K`/`1K`/纯数字 → 数值仅供排序；解析失败跳过、不影响该条）；保留 `installsRaw` 原样给前端。
- 返回 `{available: bool, results: [{pkg, owner, repo, skill, installs, installsRaw, url}]}`；按安装数降序。
- npx 不可用（复用 status 的 npx 探测）→ `{available:false}`，前端据此显示不可用提示（R7）。
- 格式漂移容错（KTD2）：无法解析的行跳过；总解析 0 条但 exit 0 → `results:[]`（前端显示「未找到」）。
- 诚实判定：`err != nil` 或输出含失败标记 → 返回错误信息，不静默成功。

**Patterns to follow**：`handleListSkillsSh` / `handleUpdateSkillsShAll`（`internal/server/api.go`）；`originLoopbackOK`、`extractJSON`/`ansiRe`/`firstFailureLine`。

**Test scenarios**：
- happy：用 fixture 中的真实样例文本（多条 `@skill + installs + └ url`）→ 解析出正确条数、字段、按安装数降序。
- **pkg 回环**：每条解析出的 pkg 都能通过 U3 的 pkg 校验正则（保证「搜到的一定可装」）。
- 安装数单位解析：`2.6K` / `144.8K` / `1K` / 纯数字 → 排序正确；`installsRaw` 原样保留。
- **空/非法输入**：`q` 空 → 不调 npx、`results:[]`；`q` 以 `-` 开头 → 400。
- 边界：空结果（exit 0、无匹配）→ `results:[]`、`available:true`。
- 格式漂移：夹杂无法解析的行 → 跳过坏行、返回可解析的、不整体失败。
- 降级：npx 不可用 → `available:false`。
- 诚实失败：runner 返回 err / 失败标记 → handler 返回错误、不报成功。
- **跨域守卫**：非 loopback Origin → 拒绝。

**Verification**：`go test ./internal/server/` 通过；样例文本解析结果与预期字段逐一匹配；解析出的 pkg 均可通过 add 校验。

### U3. 后端 add handler：安装 + 诚实判定 + 重扫

**Goal**：新增 `POST /api/skillssh/add`（body `{pkg}`），调用 `SkillsAdd` 全局安装、诚实判定结果、成功后重扫 `~/.agents` 让新 skill 作为目录源出现。

**Requirements**：R4、R5、R6、KTD1、KTD4、KTD5。

**Dependencies**：U1。

**Files**：
- `internal/server/api.go`（修改：路由 + handler + pkg 校验）
- `internal/server/skillssh_add_test.go`（新建：判定与校验测试）

**Approach**：
- **handler 首行 `originLoopbackOK(r)`（KTD7）** + `requireAuth`。
- **pkg 入参校验用新正则**——`pluginRefRe`/`skillNameRe` 都**不收斜杠**，无法复用（照抄会把所有合法 pkg 变 400；评审 P2）。新建专用正则，形如 `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$`（各段不以 `-` 开头、要求 `@`-form 杜绝路径穿越）。注释写明「只借 `pluginRefRe` 的构造风格，不复用其模式」。
- 调 `SkillsAdd`（`-g -y`，KTD1）。诚实判定（KTD5）：剥 ANSI 后识别 `✔/Installed`=成功、`✘/Error/Failed/not found`=失败、已存在=already；`ok` 与 `status` 分离，失败经 `firstFailureLine` 透出原因。
- 成功后重扫 skills.sh 目录源（复用 `listSkillsShLocked`），使新 skill 立即可在现状视图/搜索中看到（R5）。**不**自动 symlink 到 harness（KTD4）。
- **降级网兜补强（KTD4 评审 P1）**：重扫时若检测到 `~/.agents/skills` 之外新增的 harness 软链，在返回里带上该信息，供前端在成功 toast 显式提示，而非默认依赖健康度视图。
- 返回 `{ok, status, stdout, stderr, error?, strayLinks?}`。

**Patterns to follow**：`handlePluginUpdate`（诚实判定 + `firstFailureLine` + `originLoopbackOK`）；`pluginRefRe` 仅借**构造风格**（不复用）；`listSkillsShLocked`（重扫）。

**Test scenarios**：
- happy：runner 返回成功输出 → `ok:true, status:"installed"`；触发重扫。
- 已存在：输出含 already/已装 → `status:"current"`、`ok:true`。
- 诚实失败：exit 0 但输出含 `✘/Failed/not found` → `ok:false` + 原因（covers KTD5）。
- **pkg 校验**：合法 `owner/repo@skill` 通过；非法（含 `;`、空格、前导 `-`、路径穿越 `../`、缺 `@`）→ 400，不执行命令。
- 降级：npx 不可用 → 412 + 提示手动 `npx skills add`。
- **跨域守卫**：非 loopback Origin → 拒绝。

**Verification**：`go test ./internal/server/` 通过；合法 pkg 放行、非法被拦；失败用例不报成功。

### U4. 前端：在线搜索入口、在线分区、安装、去重标注、持久化

**Goal**：在现有全局搜索（`renderSearchResults`）扩展「包含线上（skills.sh）」勾选与「在线」结果分区，提供安装按钮、已装去重标注、勾选持久化、loading 与诚实 toast。

**Requirements**：R1、R2、R3、R5（刷新）、R6、R7、OQ3、OQ4。

**Dependencies**：U2、U3。

**Files**：
- `internal/server/dist/index.html`（修改：搜索框旁加「包含线上」勾选 + 更新日志条目）
- `internal/server/dist/app.js`（修改：`renderSearchResults` 扩展、在线搜索调用、在线卡片渲染、安装动作、勾选持久化）
- `internal/server/dist/app.css`（修改：在线分区/卡片/安装态样式，复用现有 `.src-badge`/`.skill` 等）

**Approach**：
- 勾选「包含线上」默认关；勾选状态持久化（localStorage，OQ4）。未勾选时行为与现状完全一致、不调 npx。
- **在线结果独立 state，不挂在每次按键的渲染上（评审 P1）**：现搜索是 `$("#search").oninput → renderInventory → renderSearchResults`，**每次按键重渲染**。新增 `state.skillsShOnline = { term, loading, available, results, gen }`。`renderSearchResults` 只**渲染**这块、**绝不发起**在线请求；在线请求只由**显式触发**发起。
- **显式触发需新增 DOM**：现 `#search` 无回车/按钮处理。U4 在搜索框旁加「在线搜索」按钮（或绑 `#search` 的 Enter），仅勾选「包含线上」时可用。触发时调 `GET /api/skillssh/find?q=`（R2/KTD6），带 loading。
- **按键竞态处理（评审 P1）**：在线分区 DOM 归「最后一次显式触发」所有——按键重渲染本地时，若 `state.skillsShOnline.term !== 当前词` 则**保留**在线分区不动（不清空、不重发）。新触发时整体替换在线分区，并用 `gen` 计数器丢弃迟到的旧响应（防 stale 覆盖 fresh）。
- **空 query 守卫**：勾选「包含线上」+ 触发但 query 为空 → 不调 API，在线分区内联提示「请输入关键词后再搜索」。
- 「在线（skills.sh）」独立分区：每条显示 `owner/repo@skill`、**安装数**、来源、skills.sh 链接；按安装数降序（R3）。`installsRaw` 缺失/为 0 时**省略**安装数元素（不渲染空白；评审 P2）。
- **去重分两态（OQ3/KTD3，评审 P2）**：在线结果与本地比对——①本地无 → 显示**「安装」按钮**；②已装到 canonical 但当前 harness 未启用 → 标「已安装（未启用）」+ 提示「可在本地搜索中启用」，无安装按钮；③已装且当前 harness 已启用 → 标「已安装且已启用」，无动作。
- **「安装」按钮状态机（评审 P1）**：仿 `updatePlugin`（**非** `enableControl`——那是启用控件，不适用未安装项）。四态：idle=「安装」可点；installing=「安装中…」禁用；success=**替换为「已安装」徽章、不复原**（靠 R5 刷新重渲成去重态）；failed=**复原为 idle + banner**（允许重试，无需刷新，因没装上）。
- **并发安装加锁（评审 P2）**：模块级 `installing` 锁——有安装进行时禁用其它安装按钮，完成（成功/失败）释放并重渲。
- 安装成功：toast「已安装 X 到 skills.sh，可在搜索/现状视图中启用」并刷新（重拉 `/api/skillssh` + inventory，R5）；若返回 `strayLinks`（KTD4 网兜）则额外提示「skills.sh 同时在 X 建立了软链」。失败弹诚实原因（R6，复用 `banner`）。
- 离线 / npx 不可用（`find` 返回 `available:false` 或 `status.npxAvailable` 假）→ 在线分区显示「当前不可用（需联网 / npx 不可用）」，本地结果照常（R7）。

**Patterns to follow**：`renderSearchResults`（行约 887，仅渲染在线块）；安装按钮仿 `updatePlugin`（行约 1402，loading/banner/toast）**而非** `enableControl`；`sourceMeta`/`verTag`；`fetchPluginInstalled` 的 state 缓存模式；新增搜索触发参考 `$("#search").oninput`（行约 1551）。

**Test scenarios**（前端，手动 + 可脚本校验为主）：
- 勾选关：搜索行为与四期一致（仅本地），无 npx 调用。
- 勾选开 + 触发：本地实时出现；在线分区 loading → 出现结果，按安装数降序。
- **按键竞态**：在线搜索 in-flight 时改 query → 在线分区不被清空/不重发；新触发整体替换、旧响应被 `gen` 丢弃。
- **空 query**：勾选 + 触发但 query 空 → 不调 API、显示内联提示。
- **去重三态**：未装→显示安装按钮；已装未启用→「已安装（未启用）」+ 提示；已装已启用→「已安装且已启用」无动作。
- **安装按钮状态机**：idle→installing→success（变「已安装」不回弹）；idle→installing→failed（回弹 + banner 可重试）。
- **并发**：连点两个安装按钮 → 第二个被锁禁用，不并发触发。
- 安装成功：toast 正确 + 列表刷新出现新 skill（作为目录源）；有 strayLinks 时额外提示。
- 安装失败：弹真实原因，不假成功。
- 安装数缺失：`installsRaw` 空/0 → 不渲染空白。
- 离线/npx 不可用：在线分区显示不可用，本地不受影响。
- 持久化：勾选后刷新页面，勾选状态保留。
- 校验链：`node --check internal/server/dist/app.js` 通过。

**Verification**：构建校验链通过；上述场景在桌面端实测符合预期。

### U5. 收口：端到端验证与边界

**Goal**：跑通全链路构建/测试，补齐 U2/U3 未覆盖的边界，更新使用指南更新日志。

**Requirements**：全部（验证）。

**Dependencies**：U1–U4。

**Files**：
- `internal/server/dist/index.html`（修改：更新日志加本期条目，若 U4 未含）
- 相关测试文件（补边界）

**Approach**：跑 `node --check internal/server/dist/app.js && go build ./... && go test ./internal/server/`；桌面端实测「搜索→安装→识别→启用」闭环；确认离线/失败路径诚实。

**Test scenarios**：端到端冒烟（见 U4 场景）；全量 `go test ./internal/server/` 绿。

**Verification**：构建校验链全绿；闭环实测通过。

---

## 范围边界

**本期做**：在线搜索（`find`）、全局安装（`add -g -y`）、安装后识别与诚实反馈、离线降级。

### Deferred to Implementation（work 期实测确定）
- **`skills add` 的 symlink 抑制**（KTD4）：确定「只落 canonical、不自动 symlink 到 harness」的确切 flag 组合（候选：省略 `-a` 的默认行为、或 `-a` 指向某最小集合）。若 CLI 强制至少一个 agent symlink，按 KTD4 降级策略（视为 skills.sh 所有 + 健康度收口）。
- **`add` 成功/失败/已存在的确切输出标记**：在真实运行中确认 `✔/✘` 及文案，校准 KTD5 判定串。
- **安装数字符串的全部单位形态**（`K`/`M`/纯数字/可能的本地化）：U2 解析需覆盖实际出现的形态。

### Deferred to Follow-Up Work
- skills.sh 更新 UI 编排（更新仍转交已有 `npx skills update` / `handleUpdateSkillsShAll`，本期不新增 UI）。

### 本产品边界外（沿用三期）
- SMB（局域网共享盘 skill，五期其二）、marketplace、MCP、LLM 安全扫描、主动支持其它 harness。

---

## 依赖与假设

- **A1（已实测确认）**：`find` 无 `--json`，靠解析文本（KTD2）。
- **A2（已实测确认）**：`add` 支持 `-g`（全局）+ `-y`（非交互），全局安装可纯指令实现（KTD1）。
- **A3**：`list --json` 可用于去重（KTD3）。
- **A4**：用户机器有 node/npx（与现有 npx 调用一致）；离线时无在线搜索（KTD6 降级）。

---

## 系统级影响

- 不改动一期四条不变式；新增链路全程「只调 CLI、不接管」，符合第④不变式。
- 复用现有 npx 探测、skills.sh 目录源识别、全局搜索与启用机制；新增表面集中在 `api.go`（两个 handler）+ `runner_*.go`（两个方法）+ 前端搜索区（新增「包含线上」勾选 + 「在线搜索」触发 DOM）。
- **安全**：两个新端点都执行外部进程 → 强制 `originLoopbackOK` + `requireAuth`（KTD7）；query 前导 `-` 守卫、pkg 专用正则校验（U2/U3）。`-y` 跳过上游 Snyk/Socket 评估，以安装数+来源为替代信任信号（KTD1，沿用三期不做内容扫描的边界）。
- 安装后 skills.sh 若产生 harness 符号链接（KTD4 降级路径）：在已登记目标内由健康度/冲突可见；在**未登记**目标则靠 U3 重扫检测 + 成功 toast 显式提示兜底。

### 观察项（fyi，当前规模无需处理）
- `listSkillsShLocked` 每次重扫全量 `scanner.Scan(~/.agents/skills)`，成本随已装总数增长（非增量）。个人规模可忽略；若日后数百 skill 后安装出现卡顿，再考虑增量重扫。
- 在线结果无分页/截断（与现有本地搜索一致）。在线对宽泛关键词可能返回很多条；如体验差再考虑「仅显示前 N 条」。
