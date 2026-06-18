# 竞品调研：cc-switch 功能比对与可借鉴点

> 日期：2026-06-17 ｜ 状态：调研结论
> 目标：摸清 [farion1231/cc-switch](https://github.com/farion1231/cc-switch) 全部功能，比对 SkillManage，筛出可借鉴项。
> 方法：抓取 cc-switch README_ZH / 用户手册 + 核对 SkillManage 现有代码（非凭印象）。

## 一、定性：两者不是同类

| | cc-switch | SkillManage |
|---|---|---|
| 定位 | all-in-one AI CLI 配置中心 | skill 同步控制面 |
| 核心信条 | 一个壳管全部（provider/MCP/prompt/proxy/usage） | 刻意减少定制 + 活 git 镜像零更新动作 |
| 技术栈 | Tauri 2 + SQLite(`~/.cc-switch/cc-switch.db`) | 单 Go 二进制 daemon + 浏览器 UI（四期加 Wails 壳）+ YAML |
| 覆盖工具 | 7 个（Claude/Codex/Gemini CLI/OpenCode/OpenClaw/Hermes） | 2 个（cc / codex） |

**结论**：cc-switch 的招牌功能（provider 热切换、MCP 面板、System Prompt 同步、本地代理熔断/故障转移、用量成本追踪、云同步）**全部在 SkillManage 范围之外**，抄进来会背叛「刻意减少定制」原则。只看 **Skills 管理 + 工程健壮性** 的交集。

## 二、cc-switch 全功能清单（备查）

- **Provider 管理**：50+ 预设、一键导入、托盘快速切换、拖拽排序、热切换（CC 免重启）、导入导出。
- **MCP 管理**：统一面板跨 5 工具、双向同步（live→db 回填）、模板/自定义、`ccswitch://` deep link 导入、各应用独立同步开关。
- **Prompts 管理**：Markdown 编辑器、跨应用同步到 CLAUDE.md/AGENTS.md/GEMINI.md、回填保护。
- **Skills 管理**：GitHub 仓/ZIP 一键安装、自定义仓库、**软链 or 复制两种安装方式**、`~/.cc-switch/skills/`、**卸载前自动备份、保留最近 20 份**（`~/.cc-switch/skill-backups/`）。
- **代理与故障转移**：本地代理、格式转换、熔断器、健康监控、应用/供应商级独立代理。
- **云同步**：Dropbox/OneDrive/iCloud/坚果云/WebDAV，同步 provider/MCP/prompts/skills。
- **托盘**：菜单内直接切换、立即生效。
- **健壮性**：原子写（temp+rename）、轮换备份（保留 10）、双层存储（SQLite+JSON）、Mutex 并发安全。
- **用量追踪**：跨供应商费用、请求/Token 统计、趋势图、自定义定价。
- **会话/工作区**：会话浏览搜索恢复、OpenClaw 工作区编辑、Agent 文件编辑、Markdown 预览。
- **系统**：深/浅/跟随主题、i18n（简繁英日）、开机自启、自动更新、Win10+/macOS12+（已签名公证）/Linux。

## 三、可借鉴点（已核对 SkillManage 现状，排序=推荐度）

### ① 破坏性删除前自动备份 + 保留最近 N 份 ⭐ 最推荐
- cc-switch：卸载 skill 前自动建备份，`skill-backups/` 保留 20 份。
- SkillManage 现状：删除「未备份真身」是 `internal/server/api.go:601` 的 `os.RemoveAll(dir)`，**永久删、不可恢复**（已有两道闸：仅删确含 SKILL.md 的目录 `api.go:1606` + 强确认）。
- 借鉴：删真身前先移到 `~/.skillmanage/.trash/<name>-<ts>/`，轮转保留 N 份。低成本、零理念冲突，消掉指南里「永久删除不可恢复」这个最硬的边角。**建议尽快做。**

### ② 扩展 harness 覆盖面
- cc-switch 覆盖 7 个工具。
- SkillManage 现状：`internal/harness/harness.go` 只有 `cc`+`codex`，harness 身份**靠路径推断、无持久字段**（KTD1），`Classify()` 是天然扩展点。
- 借鉴：等 OpenCode 等 harness 有 skill 目录约定时，加一个 `Classify` 分支 + 安装位置探测即可，**贴合「跨 harness 映射」核心卖点**。注意 Gemini 用 `GEMINI.md`（单文件 prompt）非 skill 目录，别误纳。

### ③ 精选/预设 skill 仓库一键添加
- cc-switch：50+ 预设 provider 降低上手摩擦。
- 借鉴：「添加 git 仓」弹窗放精选源列表（anthropics/skills、skills.sh 热门源），点一下填好 URL。符合「使用者只提供来源+目标」的减负思路。

### ④（可选）分享链接 / deep link
- cc-switch 有 `ccswitch://` 一键导入。SkillManage 已有 repo 列表文件 import/export，但换机/团队仍要传文件。可加 `skillmanage://add?repo=...` 或可粘贴分享串。锦上添花。

### ⑤（可选）Wails 托盘快捷
- 四期已上 Wails 壳。可加托盘「立即全量更新 / 打开 UI / 退出」，免开窗口。小投入小回报。

## 四、已做到 / 明确不抄

- **原子写**：cc-switch 吹的 temp+rename，SkillManage `internal/config/config.go:226 writeFileAtomic` 对 config/manifest **已做**。唯一缺口 `credentials.go:52` 用裸 `WriteFile`（0600 单文件，风险极低，要较真可一并换）。
- **导入/导出**：repo 列表 import/export 已有。
- **云同步（iCloud/WebDAV）**：会把「免配置单机工具」拖向「要接网盘账号」，**违背减负原则，不抄**。
- **provider 切换 / MCP / Prompts / 代理 / 用量**：均属 cc-switch 的 all-in-one 身份，**范围外，不抄**。

## 五、行动建议

**只做 ①（删除前备份）**，其余按节奏。①是纯健壮性、零理念冲突。
