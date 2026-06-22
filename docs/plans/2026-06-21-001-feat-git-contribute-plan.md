---
type: feat
date: 2026-06-21
origin: docs/brainstorms/2026-06-21-skillmanage-git-contribute-requirements.md
---

# feat: 贡献到 git 仓（备份功能扩展）

## Summary

为 SkillManage 新增「贡献到 git 仓」：把手写 skill 转移进一个已登记的 git 源仓、`git add` + commit（`MIC-0 …`）+ push，并原位建软链使其转为 git 来源；仓内已有 skill 被本地改动后支持一键「快捷上传」。同时把「更新」改为**提交感知**的漂移让路：存在未推送的本地新增/修改/提交时让路、提示先上传，绝不静默销毁。

---

## Problem Frame

gitsync 当前是纯 pull-only 只读镜像：`Sync()`（`internal/gitsync/gitsync.go:99-161`）用 `git status --porcelain` 检测本地改动，默认 `Force=false` 返回 `ErrDirty`、不动文件；`Force=true` 才 `reset --hard origin/HEAD` + `clean -fd`。系统**完全没有 add/commit/push 能力**。

要支持贡献就要新增 push，并让「更新」对未推送的本地改动让路。**关键纠正（来自评审）：现有 `git status --porcelain` 脏检测看不到「已提交未推送」状态**——而贡献流程会先 commit，push 失败后 skill 正处于这个状态（工作区干净、HEAD 领先 origin）。若沿用 porcelain 检测，下次 `reset --hard` + `clean -fd` 会把它销毁，正好打脸本应保护它的 AE1/AE3。因此漂移检测必须**提交感知**（commit-aware）——这是新增机制，不是复用现有检测。

---

## Key Technical Decisions

- **push 能力新增在 `internal/gitsync`，复用现有 `run()`。** 复用 `run()`（`gitsync.go:196-233`）的 daemon-safe 执行：`GIT_ASKPASS` 凭据注入、`GIT_TERMINAL_PROMPT=0`、Windows `hideConsole`、`cmdTimeout`。push 路径额外设 `LC_ALL=C`，使 stderr 为稳定英文供模式匹配。
- **漂移检测改为提交感知（新增机制）。** 在 `git status --porcelain`（工作区/未跟踪）之外，新增「HEAD 是否领先 upstream」检测：`git rev-list --count origin/<分支>..HEAD > 0` 即为「已提交未推送」漂移。更新（`Force=false`）在工作区脏 **或** HEAD 领先时一律让路，不 `reset/clean`。`Force=true`（用户显式放弃）才走旧销毁路径。同步还需修订 `CheckUpdate`（`gitsync.go:163-170`）的「local == 上次拉取的 remote」假设——本地提交会打破它。
- **「贡献」与 adopt 同构，且必须登记 enabled 记录。** 复用 adopt 三阶段安全迁移（copy → verify → atomic rename，`adopt.go:186-277`）把 skill 移进 `~/.skillmanage/repos/<repo>/<skill名>`，`linker.Link()` 原位软链 + 写 manifest，**并追加 `<repo>/<skill名>` 的 `cfg.Enabled` 记录**（仿 `handleAdopt` `api.go:2265-2278`）——否则 reconcile 删除遍历（`reconcile.go:177-190`）会在下次同步剪掉这条「无 enabled 对应」的软链。
- **推送分支显式解析，与 reset 目标一致。** 不读 `rev-parse --abbrev-ref HEAD`（镜像可能 detached HEAD、无本地跟踪分支）。push 目标 = `repo.Branch`，为空则 `git symbolic-ref refs/remotes/origin/HEAD` 取默认分支；push `HEAD:refs/heads/<分支>`。push 的分支必须等于 sync reset 的 ref（`gitsync.go:145-148`），保证「push 成功后下次 reset 对齐远端无损」成立。
- **per-repo 串行化。** contribute/quickupload 的「迁移→commit→push」与每日 `SyncAll`（`server.go:335-354` 在锁外跑 git I/O）会竞态同一仓目录。需 per-repo 锁（按仓目录键），contribute/quickupload 与 Sync 共用，避免 `clean -fd` 删掉刚迁入的 skill 或 reset 撞 commit。
- **push 不强制、诚实判定、不泄漏凭据。** push 失败（非快进/无写权限/网络）解析输出诚实报错、保留本地提交、绝不 `--force`。返回前**擦除 stderr 中可能内嵌的凭据**（`https?://[^:@/]+:[^@/]+@` → 脱敏），只把结构化 error_code + 脱敏摘要给到 HTTP 层。
- **新写端点带 CSRF 守卫。** `/api/contribute`、`/api/quickupload` 在处理前调 `originLoopbackOK(r)`（仿 `handleSkillsShAdd` `api.go:407`）——它们触发 push，是比 adopt 更高危的写操作。
- **commit 信息消毒。** `MIC-0 <skill名> <description>`；`description` 默认取 `SKILL.md` 的 `description`、可改；组装前**折叠为单行、去换行/空字节、限长（≤200）**，空时回退为 `MIC-0 <skill名>`（去尾空格）。
- **快捷上传按单个 skill 粒度**；「本地改动」含「已提交未推送」（push 失败重试时无工作区改动，只需重推已有 commit，不再 commit）。

---

## High-Level Technical Design

**贡献流程（U3，持 per-repo 锁）：**

```
选 skill + 选目标仓 + 确认简述（消毒）
  → adopt 三阶段迁移 skill 进 repos/<repo>/<skill名>
  → linker.Link 原位软链 + 写 manifest + 追加 <repo>/<skill名> 的 enabled 记录
  → git add <skill名>; commit -m "MIC-0 <skill名> <简述>"; push HEAD:refs/heads/<解析分支>
  → 成功：立即 reset --hard origin/<分支> 自检工作区干净（防 .gitattributes 规范化造成幽灵漂移）
     失败：保留本地提交，擦除凭据后诚实报错（push_auth/push_rejected/push_network），不 --force、不回滚
```

**提交感知漂移让路决策门（U2）：**

```
对某 git 仓执行更新（持 per-repo 锁）
  脏态 = (git status --porcelain 非空)  OR  (git rev-list --count origin/<分支>..HEAD > 0)
  → 干净        → 照旧 fetch + reset --hard 对齐（行为不变）
  → 脏（任一）  → Force=false：不 reset/clean；输出漂移明细（新增/修改/已提交未推送）
                  「修改/已提交未推送」→ 提示「先上传（快捷上传）再更新」
                  「新增未推送」→ 保留、不删、不阻断
                  Force=true（用户显式放弃）→ 旧 reset --hard + clean -fd 路径
```

> 以上为方向性示意，非实现规格。

---

## Requirements

来源 `docs/brainstorms/2026-06-21-skillmanage-git-contribute-requirements.md`（R1–R11、AE1–AE3）。

**贡献流程**
R1. 「备份」在「收编进 `@local`」之外新增「贡献到 git 仓」目标：选一个已登记 git 源仓，转移 skill 进去并原位建软链，此后由该 git 镜像供应。
R2. 转移后 `git add` + commit + push 一次完成。
R3. commit 信息 `MIC-0 <skill名称> <skill简述>`；`MIC-0` 固定字面；简述默认取 `SKILL.md` 的 `description`、提交前可改。
R4. push 复用已存凭据，目标为该镜像当前跟踪分支。
R5. push 失败诚实报错、保留本地提交、绝不 `--force`。

**快捷上传**
R6. 仓内 skill 被本地修改时提供「快捷上传」：add + commit（同 R3）+ push。
R7. 依据 `git status --porcelain`（工作区）+ HEAD 领先 upstream（已提交未推送）识别本地改动。

**更新与本地改动共存**
R8. 更新对存在未推送改动（含已提交未推送）的仓不再 reset --hard + clean -fd 销毁改动。
R9. 「新增未推送」的 skill 在更新时保留、不删除。
R10. 「修改/已提交未推送」时更新前提示「先上传再更新」；上传后继续或显式放弃后更新。
R11. 语义对齐正常 git：绝不静默丢失本地新增/修改/未推送提交。

---

## Implementation Units

### U1. gitsync 新增 add/commit/push + 分支解析 + 诚实判定

**Goal:** 给 gitsync 加 push 封装，显式解析推送分支，诚实失败、擦除凭据、绝不 `--force`。
**Requirements:** R2, R3, R4, R5。
**Dependencies:** 无。
**Files:** `internal/gitsync/push.go`（新增）、`internal/gitsync/push_test.go`（新增）；`internal/gitsync/gitsync.go`（暴露 `run()`、push 路径 `LC_ALL=C`）。
**Approach:** 仓目录内 `git add <相对路径...>` → `git commit -m <msg>` → `git push HEAD:refs/heads/<分支>`。分支解析：`repo.Branch`，空则 `git symbolic-ref refs/remotes/origin/HEAD` 取默认分支（绝不用 `abbrev-ref HEAD`，可能 detached）。push 路径加 `LC_ALL=C`。解析退出码 + stderr 映射 `push_rejected`（`! [rejected]`/`non-fast-forward`）、`push_auth`（`403`/`Permission ... denied`/`Authentication failed`/`could not read Username`）、`push_network`；返回前用正则擦除内嵌凭据。无改动可提交 → 可识别状态，不报错。commit 信息由调用方传入（已消毒）。
**Patterns to follow:** `gitsync.go:196-233`（`run()`/凭据注入）；`handleSkillsShAdd`/`handlePluginUpdate` 诚实判定。
**Test scenarios:**
- 正常：add+commit+push 成功 → 结果 OK，commit 信息等于传入。
- `Covers AE3.` push 鉴权失败（403/permission denied 的真实 push stderr 样本）→ `push_auth`，本地提交保留，无 `--force`，stderr 中若含 `user:token@` 被脱敏。
- push 非快进 → `push_rejected`，不自动 rebase/merge。
- 镜像 detached HEAD / 无 upstream → 分支解析回退到 `origin/HEAD` 默认分支，push 成功。
- 无改动可提交 → 「无改动」状态，不报错。
**Verification:** `go test ./internal/gitsync/` 通过；fakeRunner 覆盖以上。

### U2. 提交感知漂移检测 + 更新让路

**Goal:** 让「更新」对未推送本地改动（含已提交未推送）让路；输出漂移明细供 UI。
**Requirements:** R8, R9, R10, R11。
**Dependencies:** 无。
**Files:** `internal/gitsync/gitsync.go`（脏态 = porcelain 非空 OR `rev-list --count origin/<分支>..HEAD > 0`；漂移明细区分 新增/修改/已提交未推送；修订 `CheckUpdate` 不变式）、`internal/gitsync/gitsync_test.go`；`internal/server/server.go:335-354`（SyncAll 消费漂移明细，持 per-repo 锁）、`internal/server/api.go`（更新结果带漂移明细 + 每仓脏态）。
**Approach:** 默认 `Force=false`：脏态为真则本次跳过 reset/clean。脏态新增「HEAD 领先 upstream」分支（这是修复 P0 数据丢失的核心）。漂移明细：未跟踪目录=「新增」，工作区已跟踪改动=「修改」，`origin/<分支>..HEAD` 的提交涉及路径=「已提交未推送」。`SyncAll` 透出每仓脏态/明细到更新结果。不实现 ff/merge。
**Patterns to follow:** `gitsync.go:136-143`（现有 `ErrDirty`/`ActionDirtySkip`/`Force` 路径，在其上加 commit-ahead 判定）。
**Test scenarios:**
- 干净仓 → 照旧 reset 对齐（行为不变）。
- 仅新增未推送（未跟踪目录）→ 跳过、目录仍在；明细「新增」。
- `Covers AE1/AE3.` **已提交未推送**（commit 后工作区干净、HEAD 领先）→ 脏态为真、跳过 reset/clean、commit 与 skill 目录仍在；明细「已提交未推送」。
- 工作区修改未推送 → 跳过；明细「修改」。
- `Force=true` → 旧 reset --hard + clean -fd 丢弃改动。
- 同一仓同时有「已提交未推送」与另一 skill「工作区修改」→ 脏态为两者并集，明细分别归属。
**Verification:** `go test ./internal/gitsync/ ./internal/server/` 通过；用真实临时 git 仓覆盖 commit-ahead 场景。

### U3. 后端：贡献 + 快捷上传 API（CSRF + 锁 + enabled 记录）

**Goal:** 提供两个端点，复用 adopt 安全迁移 + U1 push，登记 enabled、持锁、带 CSRF。
**Requirements:** R1, R2, R3, R6, R7。
**Dependencies:** U1。
**Files:** `internal/server/api.go`（`handleContribute`、`handleQuickUpload` + 路由）、`internal/server/contribute_test.go`（新增）；`internal/adopt/adopt.go`（把 copy→verify→rename 抽成可指定目标根的 `relocate(src, dstRoot, id, mgr, manifest)`，定义 repo 目标的 `name_taken`/崩溃重入语义）。
**Approach:**
- 两端点开头均 `originLoopbackOK(r)` 守卫；取 per-repo 锁后操作。
- `POST /api/contribute` `{id, root, repo, description}`：`repo` 按**名等值**校验在已登记源（用服务端规范名构造 `filepath.Join(reposRoot, 名, skillName)`，绝不用原始请求串拼路径，防 `../` 注入）；`root` 校验在已配目标（仿 `handleAdopt` `api.go:2241-2255`）→ 消毒 `description` → `relocate` 迁入 → `linker.Link` 原位软链 + manifest + **追加 `<repo>/<skillName>` enabled 记录并 `persistConfigLocked`** → U1 add+commit+push → 成功后自检 reset 干净。
- `POST /api/quickupload` `{repo, skill}`：若工作区脏→add+commit；**只要 HEAD 领先 upstream 就 push**（push 失败重试时不再 commit，重推已有提交）。
- push 失败：skill 已在仓内、提交保留、**不回滚**，返回脱敏 error_code（`push_*`）。
**Patterns to follow:** `handleAdopt`（`api.go:2224-2280`，路径校验 + enabled 记录 + error_code）；`handleSkillsShAdd`（`api.go:407` CSRF）；`reconcile.go:286-303`（git 胜过同名 @local 的优先级，处理曾被 adopt 的同名 skill）。
**Test scenarios:**
- 贡献成功（fakeRunner）→ skill 落仓、软链建好、manifest + enabled 记录都在、commit 信息正确、自检干净。
- 模拟一次 SyncAll/Apply 后软链仍在（验证 enabled 记录防剪枝）。
- `Covers AE3.` push 被拒 → skill 仍在仓内、提交保留、`push_auth`、未回滚、未 `--force`、错误不含明文 token。
- 快捷上传：工作区脏→commit+push；push 失败后重试（无工作区改动）→ 重推已有 commit、不新建 commit。
- 非法 `repo`（非已登记/含 `../`）/ 非法 `root` → 拒绝 `invalid`。
- 缺 `originLoopbackOK` 的跨源请求被拒。
- `description` 含换行/超长 → 被折叠/截断；空 → 回退 `MIC-0 <名>`。
**Verification:** `go test ./internal/server/` 通过。

### U4. 前端：「贡献到 git 仓」目标

**Goal:** 在「备份」入口增加「贡献到 git 仓」：选仓 + 编辑简述 + 调 `/api/contribute`，覆盖全部失败态。
**Requirements:** R1, R3, R4。
**Dependencies:** U3。
**Files:** `internal/server/dist/app.js`（贡献入口 + `doContribute` + 错误码字典）、`internal/server/dist/index.html`（**新增专用贡献模态** `#contribute-modal`，含仓 `<select>` + 简述 `<input>` + 进行中态，不复用两按钮的 `#confirm-modal`）、`internal/server/dist/app.css`。
**Approach:** 手写 skill 卡片的「备份」改为可展开两选项（「备份进本地」/「贡献到 git 仓」），避免第三个按钮挤爆右侧按钮区。「贡献」打开 `#contribute-modal`：`<select>` 列已登记 git 源仓（**零仓时禁用该入口并提示先加 git 源**），简述输入默认带出 `SKILL.md` `description`（空则占位、允许空提交）。提交时锁输入 + 按钮显「贡献中…」（push 多秒）。成功 toast + 该 skill 转为该仓来源；失败按 error_code 映射中文：`push_auth`→「推送被拒，可能凭据无写权限」、`push_rejected`→「推送被拒（远端有新提交），请先快捷上传同步后重试」、`push_network`→「推送失败（网络错误），本地提交已保留，可稍后重试」。skill 名显示用 `cleanSkillName`。
**Patterns to follow:** `app.js:1430-1476`（`adoptHandwritten`/`doAdopt`/ADOPT_ERR 字典）；`enableControl` 等按钮态。
**Test scenarios:** 前端无单测；纳入手动验收；构建校验 `node --check internal/server/dist/app.js`。
**Verification:** 选仓贡献后该 skill 在现状视图转为该 git 仓来源；三种 push 失败文案正确；零仓时入口禁用。

### U5. 前端：快捷上传 + 更新让路提示

**Goal:** 仓内 skill 有改动（含已提交未推送）时显示「快捷上传」；更新遇修改/未推送提交时给三选项让路弹窗。
**Requirements:** R6, R7, R10, R11。
**Dependencies:** U2, U3。
**Files:** `internal/server/dist/app.js`（消费 U2 每仓脏态/明细渲染卡片「快捷上传」按钮 + `doQuickUpload`；更新结果处理）、`internal/server/dist/index.html`（三按钮让路弹窗）、`internal/server/dist/app.css`。
**Approach:** 卡片脏态来自 U2 在 inventory/更新响应里新增的字段（如每仓 `dirtySkills:[{skill,kind:新增|修改|已提交未推送}]`）——**「本地改动」含已提交未推送**，故 push 失败后的贡献产物也能显示「快捷上传」重试。按钮带 `title` 解释（系统既有词是备份/更新/停用，"上传"需提示）。全量更新结果含「修改/已提交未推送」仓时：弹**三按钮**弹窗——「快捷上传」(主)/「放弃本地改动并更新」(danger，走 `Force=true`)/「取消」；替换旧 `updateRepo`（`app.js:686-690`）「镜像是只读副本，正常不该有本地改动」的文案（与新心智冲突）。「新增未推送」仅在更新结果 toast 加「保留了 N 个本地新增 skill」，不阻断。
**Patterns to follow:** skills.sh 卡片按钮渲染；`updateRepo` 的 danger 确认（`app.js:686-690`）但换文案。
**Test scenarios:** 手动验收（AE1/AE2）；`node --check internal/server/dist/app.js`。
**Verification:** 改/贡献失败的仓内 skill 显示「快捷上传」并能重推;全量更新对脏仓给三选项而非静默清除。

---

## Scope Boundaries

- 不新增独立「可写工作仓」类型；贡献复用现有 git 源仓。
- 不做临时 clone-即用即弃 的贡献方式。
- 不支持向未登记的任意 git URL 贡献。
- 不接工单系统：`MIC-0` 固定字面，不输入工单号。
- 不做 push 冲突的自动 rebase/merge；非快进诚实报错交用户处理。
- 不做批量快捷上传（单 skill 粒度）。
- SMB（局域网共享盘）本期搁置。

### Deferred to Follow-Up Work

- 目标仓自带 skill 目录约定（如 `skills/`）的落点自适应——本期固定仓根下 skill 名目录；后续按需。
- 一键批量上传当前仓所有本地改动。
- 贡献模态的焦点陷阱 / Esc 关闭 / 键盘导航等无障碍打磨。

---

## Risks & Dependencies

- **放宽一期不变式①（只读镜像）。** 本期对一期不变式的唯一改动，仅在存在未推送漂移（含已提交未推送）时与旧行为不同；`Force=true` 显式逃生口保留。
- **提交感知漂移是修 P0 的核心。** 评审验证：若漂移检测只用 `git status --porcelain`，commit 后工作区变干净，push 失败的「已提交未推送」skill 会被下次 reset --hard 销毁（打脸 AE1/AE3）。U2 的 commit-ahead 检测是该洞的修复，必须随 U3「提交即推送」一起落地，否则「不回滚」反而导致丢失。
- **推送分支 == reset 目标。** detached HEAD/无跟踪分支时 `abbrev-ref HEAD` 不可靠；U1 显式解析并保证 push 分支等于 sync reset 的 ref，否则「成功 push 后下次 reset 对齐无损」不成立。
- **enabled 记录防剪枝。** 仅写 manifest 不够，reconcile 会剪掉无 enabled 对应的软链（U3 已含修复）。
- **并发竞态。** SyncAll 在锁外跑 git I/O，与 contribute/quickupload 竞态同仓 → per-repo 锁（U2/U3 含）。
- **PAT 写权限。** 登记仓 PAT 可能只读，首次 push 被拒 → 诚实提示，不视为 bug。
- **凭据不外泄。** push stderr 可能含内嵌凭据 → U1 擦除后才返回。
- **CSRF。** 新写端点触发 push，须 `originLoopbackOK` 守卫。
- **幽灵漂移。** `.gitattributes`（autocrlf/text）可能使 push 后工作区与 origin 不一致 → U3 push 成功后自检 reset 干净并暴露异常。

---

## Acceptance Examples

来源 origin AE1–AE3。

AE1. **新增/未推送时更新。** 仓 A 有刚贡献、push 失败仍在本地（已提交未推送）的新 skill。触发全量更新 → 该 skill 仍在、未被 reset/clean 删除；其余更新照常获得。（U2，R8/R9/R11）
AE2. **修改未推送时更新。** 仓 A 内某 skill 被本地改过未上传。触发全量更新 → 不 reset --hard，三按钮弹窗提示「先上传再更新」；快捷上传成功后再更新，本地改动以提交保留。（U2/U5，R10/R11）
AE3. **PAT 只读导致 push 失败。** 登记仓 PAT 仅读权限。贡献时 commit 成功、push 被拒 → 提示「推送被拒（可能凭据无写权限）」，本地提交保留，不 `--force`，错误不含明文 token，且该提交在后续更新中不被销毁。（U1/U2/U3/U4，R5）

---

## Sources & Research

- Origin 需求文档：`docs/brainstorms/2026-06-21-skillmanage-git-contribute-requirements.md`。
- Grounding 档案：`/tmp/compound-engineering/ce-brainstorm/git-contribute/grounding.md`。
- 关键代码：`internal/gitsync/gitsync.go`（`Sync`/`run`/脏检测 `:136-158`、`CheckUpdate` `:163-170`）、`internal/adopt/adopt.go:186-277`、`internal/server/api.go`（`handleAdopt:2224-2280`、`ensureEnabled:2480`、`persistConfigLocked:3034`、`handleSkillsShAdd:407` CSRF、`handleSetCredential:2826-2865`）、`internal/reconcile/reconcile.go`（孤儿剪枝 `:177-190`、`sourceRoot:78-89`、git 优先 `:286-303`）、`internal/server/server.go:335-354`（SyncAll 锁外 git I/O）、`internal/config/credentials.go`、`internal/server/dist/app.js:686-690,1430-1476`、`main.go:60-85`（askpass 主机解析）。
- 计划评审（2026-06-21，5 persona）：feasibility + adversarial 双双对照代码验证出「porcelain 看不到已提交未推送 → reset 销毁」P0；security 提出 CSRF/描述消毒/凭据擦除；design 提出选仓控件/进行中态/三按钮让路/脏态数据形等 UI 缺口——均已并入上文。
