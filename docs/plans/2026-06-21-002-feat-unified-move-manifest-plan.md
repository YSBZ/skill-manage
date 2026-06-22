---
type: feat
date: 2026-06-21
origin: 对话内需求（2026-06-21，统一移动 + 清单）
---

# feat: 统一「移动 skill」下拉 + 全局贡献清单 + git↔git 移动

## Summary

把分散的「备份进本地 / 贡献到 git 仓 / 移动」收敛成**一个目标下拉框**(默认 `local`,其余为各 git 源仓)。确定 = 把 skill 从当前位置移动到所选目标:目标是 `local` 只备份不推送;目标是 git 仓则 commit + push。新增**全局清单** `~/.skillmanage/contrib-manifest.yaml`(skill 名→{描述,位置}),作为 commit 简述来源,并在每次移动时同步。本期一并支持 **git↔git / git→local** 的双向移动。

## Key Decisions（已与用户确认）

- **清单 = 全局本地单文件**,纯本地不进 git 仓;commit 简述以清单为准,SKILL.md description 仅作初始默认。
- **本期含 git→git**(及对称的 git→local)。
- 统一下拉替代原两按钮(备份进本地/贡献到 git 仓)与独立「移动」弹窗。

## 移动矩阵（源 → 目标）

| 源 | 目标 local | 目标 git 仓 R |
|---|---|---|
| 手写(未备份, 在目标目录) | adopt 收编进 @local（现有 `/api/adopt`） | contribute 进 R（现有 `/api/contribute`） |
| @local 已备份 | no-op（已在 local） | move-local 进 R（现有 `/api/move-local`） |
| git 仓 A | **新**:从 A 移回 @local（A 内 git rm+push，迁入 store） | **新**:A→R 双向移动 |

新增的只有最后一行(源是 git 仓)→ 一个新端点 `/api/move`,其余复用已建好的 handler(各自补「同步清单」)。

## 双向移动失败策略（git 源的关键决策）

移动「源 git 仓 S 的 skill → 目标 D」(D 为另一 git 仓或 local):**目标先行,源清理后置**——任一步失败都不丢数据(最坏是 skill 短时同时存在于两端上游,可重试收敛):

```
1. 拷贝 S/<skill> → D/<skill>（保留 S，不立即删）
2. 迁移 enabled 记录 <S>/<skill> → <D 选择子>，reconcile（软链改指 D；D 已有该 skill 在磁盘）
3. 若 D 是 git 仓：在 D 内 add+commit+push
     push 失败 → D 内「已提交未推送」（U2 漂移保护），S 原样保留 → 报 partial，停止，不动 S
                 （用户在 D 卡片「快捷上传」重推；S 清理留待重试）
   若 D 是 local：仅落入 store，无 push
4. D 落定后，清理源 S：os.RemoveAll(S/<skill>) → git add <skill>（暂存删除）→ commit+push S
     S push 失败 → S 内删除已提交未推送（漂移保护），S 上游暂时仍有旧副本 → 报 partial
5. 同步清单：upsert {<skill>: {description, location:<D>}}
```

> 不 `--force`、不回滚;失败诚实报错并保留本地提交。

## Implementation Units

### U1. 清单存储 `internal/config/contribmanifest.go`（新增）
`ContribManifest{ Skills map[string]ContribEntry }`,`ContribEntry{ Description, Location string }`;`Load/Save`(0644),路径 `centralDir/contrib-manifest.yaml`;`Upsert(name, desc, location)`。复用 `credentials.go` 的 yaml load/save 形态。测试:round-trip、缺文件返回空、upsert 覆盖。

### U2. gitsync 源清理能力（复用既有 push）
不新增 git 子命令:删除用 `os.RemoveAll(dir/<skill>)` + 复用 `Add(ctx,dir,<skill>)`(暂存删除)+ `Commit` + `Push`。验证 fakeRunner/真实临时仓:删除被暂存并推送。

### U3. 后端统一移动 `internal/server/contribute.go`
- 给 `handleContribute`/`handleMoveLocal`/`handleAdopt` 各加「成功后 upsert 清单」。
- 新增 `POST /api/move` `{name, fromRepo, toRepo|"local", description}`(源为 git 仓):CSRF + 两仓 per-repo 锁(按目录序锁,避免死锁)+ 上面的失败策略 + 迁移 enabled 记录 + reconcile + 清单同步。
- description 缺省取清单条目→否则 SKILL.md。
**测试**:A→B 成功(B 有、A 无、清单 location=B);B push 失败→A 保留、B committed-unpushed、partial;git→local;非法仓/CSRF。

### U4. 前端统一下拉 + 卡片动作收进详情弹窗 footer `dist/app.js` + `dist/index.html`
- **卡片瘦身**:inventory 卡片只留 名称 + 徽章 + 一个**「操作」**按钮(由原「详情」改名)。移除卡上所有行内动作(备份/删除/停用/快捷上传/移动)。
- **动作进详情弹窗**:复用现有 `#modal-actions` 槽,新增 `setInventoryActions(i)` 按 kind 在弹窗 footer 渲染上下文动作——手写:移动/删除;@local:移动/停用;git:移动/快捷上传(脏时)/停用;skills.sh:更新(有 npx)/停用;unknown:删除;plugin:只读说明。「操作」按钮 = 打开详情并填充该 footer。
- **统一移动弹窗**:把 `#contribute-modal` 改造,移除两按钮,换成**目标下拉**(option `local` + 各 git 仓);标题「移动:<skill>」;简述 textarea 默认取清单/SKILL.md。footer 里的「移动」动作打开它。
- 确定按源+目标路由到 `/api/adopt`(→local 且源手写) / `/api/contribute`(手写→git) / `/api/move-local`(@local→git) / `/api/move`(git 源)。
- push 失败=partial(已提交未推送),刷新后弹窗 footer 出现「快捷上传」。
- 校验链 `node --check`。

## Scope Boundaries
- 清单仅本地、不进仓、不随导出。
- 不做 `--force`、不做 push 冲突自动 rebase/merge(诚实报错)。
- 不做批量移动(单 skill)。

## Risks
- **双向移动两次 push**:任一失败的中间态必须不丢数据(策略已定:目标先行、源后清理)。
- **死锁**:两仓锁按目录字典序获取。
- **reconcile 重扫**:`computeDesired` 每次新建 scanCache,移动后能扫到新位置(已验证)。
