# SkillManage 需求文档

> 状态：需求已对齐，待进入实施计划（ce-plan）
> 一期范围：仅 Claude Code（CC）。Codex 支持为二期。

## 1. 背景与问题

团队里多个团队（如后端团队、前端团队）各自维护 skill 的 git 仓，持续更新并推送到 git。

当前的痛点：

1. skill 由成员逐个单独安装，安装之后基本不再更新；而 git 上的仓在持续更新，本地长期处于陈旧状态。
2. skill 本质上是一组文件（含 `SKILL.md` 的目录），不存在"编译/安装"概念，在 CC 与 Codex 下结构通用。
3. 缺少一个统一的管理入口来跟踪多个 skill 仓、保持最新、并按需启用到目标目录。

## 2. 目标

做一个 **SkillManage** 工具：管理一个中央文件夹，文件夹下可放多个 git 仓；定时更新这些仓；把选中的 skill 软链到指定目录；提供可视化管理页面；在 Windows / macOS / WSL 下都可用。

核心理念：**软链 + git 镜像 = 零安装、永远最新**。中央仓定时 `pull`，目标目录里的 skill 通过软链始终指向最新内容，从根本上消除"安装后不更新"的问题。

## 3. 形态与技术选型

- **语言/分发**：Go 单二进制，使用 `embed` 将 Web UI 静态资源打包进可执行文件，下载单文件即可运行，无运行时依赖。
- **架构**：一个常驻 daemon = 内置定时器（调度）+ 内置 HTTP server（服务 Web UI）+ 链接引擎。
- **git 操作**：直接 shell out 调用系统 `git`（前提：使用 skill 仓的人必然已装 git）。
- **跨平台链接**：
  - macOS / WSL：使用 symlink。
  - Windows：使用目录联接（junction，`mklink /J`，免管理员权限）；跨卷等 junction 失效场景退回 copy 同步。
  - 这是有意接受的分叉实现：放弃"全平台统一软链"的前提。
- **端口**：固定默认端口（如 7799），被占用时自动顺延到下一个空闲端口；启动后在终端/通知中告知实际访问地址。
- **中央文件夹位置**：首次启动让用户选择，默认 `~/.skillmanage`。
- **开机自启**：安装/首次运行时自动注册到对应平台的自启机制（macOS launchd / Windows 启动项 / WSL shell 钩子）。调度逻辑跨平台统一，自启注册为三平台各一次性脚本。

## 4. 配置模型

单个本地配置文件 `config.yaml`，分两块职责：

```yaml
repos:                              # 第一块：跟踪哪些 git 仓
  - url: git@xxx/backend-skills.git
    branch: main
  - url: git@xxx/frontend-skills.git

enabled:                            # 第二块：启用哪些 skill、链到哪个 target
  - skill: backend-skills/*         # 全选 = 跟随模式
    target: ~/.claude/skills/
  - skill: frontend-skills/foo      # 单选 = 快照模式
    target: /path/to/projectX/.claude/skills/

projects:                           # 项目级 target 的登记（手动添加）
  - /path/to/projectX

schedule:
  daily_at: "09:00"
```

说明：

- 不存在"共享的团队配置仓"这层抽象。`repos` 就是不同团队各自的 skill 仓 URL 列表。
- **仓清单分发**：每个人在自己 UI 里粘 URL 添加；同时支持把仓清单导出为文件 / 导入清单文件（新人一键拿到全部仓）。两种方式并存，不冲突。

## 5. skill 识别与选择语义

- **skill 边界识别**：扫仓时，凡含 `SKILL.md` 的目录即视为一个 skill 单元，软链整个目录。符合 CC/Codex 的实际结构（`SKILL.md` + 脚本 + 资源）。
- **全选 = 跟随模式**：记录"我要这个仓的全部"。上游新增 skill，下次 pull 后**静默自动建链**，无需再操作。这是治"装了不更新"的关键。
- **单选 = 快照模式**：明确挑选的若干 skill，上游新增不自动纳入。
- 两种语义共存，UI 上明确区分（全选项标"🔄 跟随"角标）。
- **target 范围**：同时支持
  - 全局 `~/.claude/skills/`；
  - 项目级 `<project>/.claude/skills/`，项目路径在 UI 手动登记。

## 6. 更新与同步策略

- **触发**：每天定时更新 + UI "立即更新全部"按钮。
- **更新方式**：中央仓作为严格只读镜像，使用 `git fetch + reset --hard origin`。约定中央仓内容不许手改（要改去上游改），脏了直接被覆盖，更新永不卡住。
- **跟随模式下的增删**：
  - 上游新增 skill → 静默自动建链；UI 变更记录中可见"新增了某 skill"。
  - 上游删除/重命名 skill → 源目录消失，软链变悬空 → 自动清理悬空链接 + UI 变更记录里标"某 skill 已被上游移除"。
- **更新失败**（网络断 / 认证过期 / 仓不存在）→ UI 状态页对该仓标红并写明原因，不阻塞其他仓更新。
- **私有仓认证**：复用系统已配置的 git 凭证（SSH agent / git credential helper），工具不额外管理凭证。

## 7. 冲突与安全边界

- **多仓同名 skill**（链到同一 target 会撞名）→ 建链前扫描检测，UI 提示冲突，让用户给其中一个起别名再链。保留原始 skill 名，仅必要时改。
- **target 已有非本工具创建的真实目录**（用户以前手动装的 skill）→ 停下并在 UI 报警，**绝不自动覆盖**，避免静默干掉用户手动安装的内容。

## 8. UI

- 单页布局：
  - 左侧：仓列表（含每个仓的更新状态、失败标红）。
  - 右侧：选中仓下的 skill 列表，全选/单选勾选、target 选择、各 skill 的链接状态。
- 操作覆盖：加/删仓、导入导出仓清单、勾选启用 skill、选 target、立即更新、查看变更记录与失败原因、登记项目路径、开机自启开关。

## 9. 范围与待验证项

**一期范围**：仅 Claude Code。

**二期 / 实现时待验证**：

1. Codex 的 skill 格式与目录约定是否与 CC 完全一致 —— 二期开工前用真实 skill 验证后再决定双 host 支持方式。
2. Windows junction 的 copy 兜底具体触发条件（跨卷时 junction 失效退 copy）—— 实现时确定。

## 10. 决策记录摘要

| 决策点 | 结论 |
|---|---|
| 语言/分发 | Go 单二进制 + embed UI |
| 跨平台链接 | Mac/WSL symlink；Win junction/copy |
| git 操作 | shell out 系统 git |
| 调度 | 进程内定时器；装时自动注册自启 |
| UI 形态 | 浏览器，单页 |
| host 范围 | 一期仅 CC，Codex 二期 |
| 全选语义 | 跟随（新增自动建链） |
| 单选语义 | 快照 |
| 更新方式 | fetch + reset --hard（只读镜像） |
| 更新触发 | 定时 + UI 一键立即 |
| 悬空链接 | 自动清理 + UI 记录 |
| 私有仓认证 | 复用系统 git 凭证 |
| 命名冲突 | UI 改链接名 |
| 覆盖保护 | 停下报警，绝不自动覆盖真实目录 |
| target 范围 | 全局 + 项目级（项目手动登记） |
| 中央文件夹 | 首次启动选，默认 ~/.skillmanage |
| 端口 | 固定默认，被占顺延 |
| 仓清单分发 | 各人 UI 加 + 导出/导入清单文件 |
| skill 识别 | 含 SKILL.md 的目录 |
| 更新失败告警 | UI 状态页标红 + 原因 |
