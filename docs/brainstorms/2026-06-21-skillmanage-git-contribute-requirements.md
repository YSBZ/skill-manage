---
date: 2026-06-21
topic: skillmanage-git-contribute
---

# SkillManage 贡献到 git 仓（备份功能扩展）

## Summary

把现有「备份」从「只能收编进本地受管库（`@local`）」扩展为「可贡献到一个已登记的 git 源仓」：将 skill 转移进选定仓库、`git add` + commit + push，并支持仓内已有 skill 被本地改动后的一键「快捷上传」。同时把「更新」从无条件 `reset --hard` 改为「像正常 git 一样」的漂移感知行为，使本地新增/修改不被静默销毁。

## Problem Frame

一期第①条不变式是「git 仓只读镜像」：源仓克隆到 `~/.skillmanage/repos/<name>`，每次更新（每日 09:40 + 手动全量）执行 `git reset --hard` + `clean -fd` 强制对齐远端。系统当前**完全没有 push 能力**（gitsync 是纯 pull-only）。

这带来两个缺口：(1) 用户写了一个 skill，想贡献回团队仓（如 `fe-skills`）让同事也能用，目前只能手动在仓里操作；(2) 即使能往镜像里写，下一次更新的 `reset --hard` + `clean -fd` 会把未推送的本地新增/修改直接清掉。要支持贡献，就必须让「更新」对未推送的本地改动让路——否则贡献的成果随时被自动更新抹掉。

## Key Decisions

- **复用现有 git 源仓，提交即推送。** 贡献目标从已登记的 git 源仓里选，不新增仓类型。靠 commit + push 原子完成：push 成功后远端已含该提交，下次更新对齐远端无损失。
- **放宽不变式①为「漂移感知只读」。** git 源仓不再无条件 `reset --hard`；当存在未推送的本地改动时，更新让路、绝不静默销毁，语义对齐正常 git（先处理再拉取）。这是本期对一期不变式的唯一改动，仅在存在本地漂移时才与旧行为不同。
- **commit 信息固定模板。** `MIC-0 <skill名称> <skill简述>`：`MIC-0` 为固定字面，简述自动取 `SKILL.md` 的 `description`、提交前可改。
- **push 不强制。** 失败（无写权限 / 非快进 / 网络）一律诚实报错并保留本地提交，绝不 `--force`。

## Requirements

**贡献流程**

R1. 「备份」在原有「收编进 `@local`」之外新增目标「贡献到 git 仓」：从已登记的 git 源仓中选一个，把手写 skill 转移进该仓库工作树后建软链回原位，此后该 skill 由该 git 镜像供应（不再标为未备份手写）。
R2. 转移后执行 `git add` + commit + push 一次完成。
R3. commit 信息为 `MIC-0 <skill名称> <skill简述>`；`MIC-0` 固定字面；简述默认取 `SKILL.md` 的 `description`，提交前可改。
R4. push 复用已存凭据（`~/.skillmanage/credentials.yaml`，经 GIT_ASKPASS 注入），目标为该镜像当前跟踪的分支。
R5. push 失败时诚实报错、保留本地提交、绝不 `--force`，由用户处理后重试。

**快捷上传**

R6. 当 git 仓内某 skill 被本地修改，提供「快捷上传」：`git add` + commit（同 R3 模板）+ push。
R7. 「快捷上传」依据 `git status --porcelain` 识别本地改动。

**更新与本地改动共存**

R8. 「更新」（每日 09:40 + 手动全量）对存在未推送本地改动的 git 仓，不再执行 `reset --hard` + `clean -fd` 销毁本地改动。
R9. 本地有「新增但未推送」的 skill 时，更新保留该 skill，不删除。
R10. 本地有「修改未推送」时，更新前提示「先上传（快捷上传）再更新」；用户上传后继续，或显式选择放弃本地改动后再更新。
R11. 总体语义对齐正常 git：绝不静默丢失本地新增/修改。

## Key Flows

F1. **贡献一个手写 skill。** **Trigger:** 用户在某 skill 上选「备份 → 贡献到 git 仓」。选目标仓 → 确认/编辑简述 → 转移进仓 + commit（`MIC-0 …`）+ push → 原位建软链 → 该 skill 变为该 git 仓来源。**Covers R1–R5.**

F2. **快捷上传仓内改动。** **Trigger:** 用户改了某个来自 git 仓的 skill 后点「快捷上传」。`git status --porcelain` 确认改动 → commit（`MIC-0 …`）+ push → 报告成功/失败。**Covers R6, R7.**

F3. **更新遇到本地改动。** **Trigger:** 每日定时或手动「全量更新」对某 git 仓执行。先查本地漂移：无漂移 → 照旧拉取对齐；有「新增未推送」→ 保留、不删；有「修改未推送」→ 提示先上传再更新（或用户显式放弃改动后更新）。**Covers R8–R11.**

## Acceptance Examples

AE1. **新增未推送时更新。** 仓 A 本地有一个刚贡献、push 失败仍在本地的新 skill。触发全量更新 → 该新 skill 仍在、未被 `clean -fd` 删除；远端其余更新照常获得。**Covers R8, R9.**

AE2. **修改未推送时更新。** 仓 A 内某 skill 被本地改过、尚未上传。触发全量更新 → 不直接 `reset --hard`，而是提示「先上传再更新」；用户「快捷上传」成功后再更新，本地改动以提交形式保留。**Covers R10, R11.**

AE3. **PAT 只读导致 push 失败。** 用户登记仓时填的 HTTPS PAT 仅有读权限。贡献时 commit 成功、push 被拒 → 诚实提示「推送被拒（可能凭据无写权限）」，本地提交保留，不 `--force`。**Covers R5.**

## Scope Boundaries

- 不新增「可写工作仓」这一独立仓类型；贡献复用现有 git 源仓。
- 不做临时 clone-即用即弃 的贡献方式。
- 不支持往未登记的任意 git URL 贡献（只面向已登记源仓）。
- 不做工单号输入（`MIC-0` 固定字面，本期不接工单系统）。
- 不做合并冲突的自动解决：push 非快进时诚实报错交用户处理，不自动 rebase/merge。
- SMB（局域网共享盘）本期已搁置。

## Dependencies / Assumptions

- **登记仓的凭据需具备写权限。** 消费 skill 只需读，用户登记时填的 HTTPS PAT 可能仅有读 scope；贡献需要写。首次 push 可能因「凭据无写权限」失败——属凭据 scope 问题，需在 UI 明确提示，而非视为 bug。
- 复用现有 gitsync 的 git shell-out、GIT_ASKPASS 凭据注入、`git status --porcelain` 脏检测；push 能力为净新增（当前无任何 add/commit/push 封装）。
- 复用现有 adopt 的三阶段安全模式（copy → verify → atomic rename）与原位软链。

## Outstanding Questions

**Deferred to Planning**

- 「漂移感知更新」的精确实现：检测到改动时是整仓跳过本次更新、还是对未冲突部分做 fast-forward/merge 拉取？倾向「有漂移则本次该仓让路 + 提示」，细节在 plan 期定。
- skill 在目标仓内的落点：默认仓根下以 skill 名建目录（贴合 scanner 扫描）；若仓库自带 skill 目录约定（如 `skills/`）是否自适应，plan 期勘察仓库结构后定。
- 「快捷上传」是单个 skill 粒度还是允许批量上传当前仓所有本地改动，plan 期定。

## Sources / Research

- Grounding 档案：`/tmp/compound-engineering/ce-brainstorm/git-contribute/grounding.md`（adopt 流程、gitsync 只读镜像、凭据系统、源识别、前端备份入口的 file:line 摘录）。
- 关键代码位置：`internal/adopt/`（收编三阶段 + 原位软链）、`internal/gitsync/gitsync.go`（`reset --hard`/`clean -fd`、`git status --porcelain`、GIT_ASKPASS）、`internal/server/api.go`（`handleAdopt`/`handleAdoptable`）、`~/.skillmanage/credentials.yaml`（凭据 0600）、`internal/server/dist/app.js`（`adoptHandwritten`/`doAdopt` → `/api/adopt`）。
