"use strict";
const TOKEN = document.querySelector('meta[name="sm-token"]').content;
const $ = (s, el = document) => el.querySelector(s);
const ce = (t, props = {}) => Object.assign(document.createElement(t), props);

// Instant custom tooltip — the native `title` reveal is too slow. Shown only
// for descriptions that are actually truncated, follows the cursor, and is
// pointer-events:none so it never interferes with hover/click detection.
const tip = ce("div", { className: "tip hidden" });
document.body.append(tip);
function placeTip(x, y) {
  const pad = 14;
  let left = x + pad, top = y + pad;
  if (left + tip.offsetWidth > window.innerWidth - 8) left = x - tip.offsetWidth - pad;
  if (top + tip.offsetHeight > window.innerHeight - 8) top = y - tip.offsetHeight - pad;
  tip.style.left = Math.max(8, left) + "px";
  tip.style.top = Math.max(8, top) + "px";
}
document.addEventListener("mouseover", (e) => {
  const el = e.target.closest && e.target.closest(".skill-desc");
  if (!el || el.scrollHeight <= el.clientHeight + 1) return; // not truncated → no tip
  tip.textContent = el.textContent;
  tip.classList.remove("hidden");
  placeTip(e.clientX, e.clientY);
});
document.addEventListener("mousemove", (e) => {
  if (!tip.classList.contains("hidden")) placeTip(e.clientX, e.clientY);
});
document.addEventListener("mouseout", (e) => {
  if (e.target.closest && e.target.closest(".skill-desc")) tip.classList.add("hidden");
});

async function api(method, path, body) {
  const opts = { method, headers: { Authorization: "Bearer " + TOKEN } };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(path, opts);
  const txt = await r.text();
  let data = null;
  if (txt) { try { data = JSON.parse(txt); } catch { data = txt; } }
  if (!r.ok) throw new Error((data && data.error) || r.statusText || "HTTP " + r.status);
  return data;
}

// confirmModal replaces the native confirm() with an in-page dialog. Returns a
// Promise<bool>. okText/danger customize the confirm button.
function confirmModal(msg, okText, danger) {
  return new Promise((resolve) => {
    const m = $("#confirm-modal"), ok = $("#confirm-ok"), cancel = $("#confirm-cancel");
    $("#confirm-msg").textContent = msg;
    ok.textContent = okText || "确定";
    ok.className = danger ? "danger" : "";
    m.classList.remove("hidden");
    const done = (v) => { m.classList.add("hidden"); ok.onclick = cancel.onclick = m.onclick = null; resolve(v); };
    ok.onclick = () => done(true);
    cancel.onclick = () => done(false);
    m.onclick = (e) => { if (e.target.id === "confirm-modal") done(false); };
  });
}

// infoModal shows a read-only explanation popup. paras is an array of
// {h?, text} — h renders an emphasized lead line, text the body paragraph.
function infoModal(title, paras) {
  $("#info-title").textContent = title;
  const body = $("#info-body"); body.innerHTML = "";
  paras.forEach((p) => {
    const el = ce("p");
    if (p.h) el.append(ce("b", { textContent: p.h + " " }));
    el.append(document.createTextNode(p.text || ""));
    body.append(el);
  });
  $("#info-modal").classList.remove("hidden");
}

const state = {
  status: null,
  targets: [],
  npxAvailable: false,
  credHosts: {}, // host → username for hosts with a stored HTTPS credential
  credPending: null, // {url, branch} when filling creds before adding a repo
  skillsByRepo: {}, // catalog for the "+ 添加" drawer
  inventory: [], // current tab's directory inventory (phase 3 U6)
  pluginSkills: [], // plugin-provided skills (harness-tagged), injected into inventory
  pluginInstalled: null, // null=未加载；{available, list:[{name,id,scope}]} 已装插件（用于委托更新）
  pluginInstalledLoading: false,
  invScope: "",
  invLoading: false,
  invError: "",
  openGroup: undefined, // 手风琴：当前展开的组 key（undefined=尚未选择默认展开第一个；null=全部收起）
  dirSources: [], // 用户登记的本地目录源（status.localSources）：[{id,label,path,count}]
  activeTarget: undefined, // active 同步目录 tab (one tab per dir)
  search: "",
  // 在线搜索结果独立 state——只由显式触发更新，不挂在每次按键的渲染上。
  // gen 是世代计数器：触发时 +1，迟到的旧响应若 gen 不符则丢弃（防 stale 覆盖 fresh）。
  skillsShOnline: { term: "", loading: false, available: true, results: [], error: "", gen: 0 },
};

// 安装并发锁：同一时刻只允许一个 npx skills add 在跑，其它安装按钮禁用。
let installingPkg = null;

// error_code → 用户文案（收编不同失败点处置不同）
const ADOPT_ERR = {
  copy_failed: "原 skill 未动，请检查磁盘空间或权限",
  verify_failed: "原 skill 未动，复制不完整",
  link_failed: "已自动回滚，原 skill 完好",
  rollback_partial: "复制已入库但建链失败，请重试收编",
  invalid: "非法 skill 名",
  guarded: "受保护目录，不可收编",
  not_found: "skill 不存在",
  name_taken: "受管存储已有同名 skill（另一 agent 收编过），请先改名",
};

const harnessClass = (h) => (h === "codex" ? "st-linked-codex" : h === "cc" ? "st-cc" : "st-unknown");
const LOCAL_NS = "@local";
const AGENTS_NS = "@agents"; // skills.sh shared dir namespace (~/.agents/skills)
const dirNS = (id) => "@dir:" + id; // user-registered local directory source namespace

// SRC maps a classified source kind to its badge label + CSS class (phase 3 U8).
const SRC = {
  git: { label: "git", cls: "src-git" },
  local: { label: "本地", cls: "src-local" },
  dir: { label: "本地源", cls: "src-local" },
  "skills.sh": { label: "skills.sh", cls: "src-skillssh" },
  plugin: { label: "插件", cls: "src-plugin" },
  handwritten: { label: "未备份", cls: "src-handwritten" },
  unknown: { label: "未知软链", cls: "src-unknown" },
};

let bannerTimer = null;
function banner(msg, isErr) {
  const b = $("#banner");
  if (bannerTimer) { clearTimeout(bannerTimer); bannerTimer = null; }
  if (!msg) { b.classList.add("hidden"); return; }
  b.textContent = msg;
  b.title = "点击关闭";
  b.className = "banner" + (isErr ? " err" : "");
  b.onclick = () => b.classList.add("hidden");
  // 浮层提示自动消失，避免长期遮挡（错误停留更久）。点击可立即关闭。
  bannerTimer = setTimeout(() => b.classList.add("hidden"), isErr ? 8000 : 4000);
}

// toast shows a transient confirmation (build/teardown link feedback, R4.2).
let toastTimer = null;
// toast shows a transient top-center notice. kind ∈ ok|err|info (default ok)
// drives the color (green / red / blue).
function toast(msg, kind) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast toast-" + (kind || "ok"); // rebuilds class → also clears "hidden"
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add("hidden"), 3200);
}

function hostOf(u) { try { return new URL(u).hostname; } catch { return u; } }
function targetLabel(dir) {
  const t = state.targets.find((x) => x.dir === dir);
  return (t && t.alias) || dir;
}

const targetDirs = () => state.targets.map((t) => t.dir);
// currentTarget = the active tab's directory (each tab is one sync dir and keeps
// its own skill→dir mapping). Falls back to the first directory.
const currentTarget = () => {
  const dirs = targetDirs();
  if (state.activeTarget && dirs.includes(state.activeTarget)) return state.activeTarget;
  return dirs[0] || "";
};

const enabledFollow = (repo) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/*" && e.target === currentTarget());

// load refreshes all UI state from the daemon. Pass sync=true to first run a live
// reconcile so the footer reflects current reality (resolved skips/conflicts clear,
// newly-unblocked links get placed) instead of a stale last-sync snapshot.
async function load(sync) {
  if (sync) { try { await api("POST", "/api/apply"); } catch { /* status still loads */ } }
  try { state.status = await api("GET", "/api/status"); }
  catch (e) { banner("加载失败：" + e.message, true); return; }
  state.npxAvailable = !!state.status.npxAvailable;
  try { state.targets = (await api("GET", "/api/targets")) || []; }
  catch { state.targets = []; }
  try {
    const list = (await api("GET", "/api/credentials")) || [];
    state.credHosts = {};
    list.forEach((c) => { state.credHosts[c.host] = c.username || ""; });
  } catch { state.credHosts = {}; }
  const repos = state.status.repos || [];
  state.dirSources = state.status.localSources || []; // user-registered local folder sources
  // Catalog for the "管理" drawer: tracked-repo skills, the @local store, and each
  // registered local directory source ("@dir:<id>").
  const names = repos.map((r) => r.name)
    .concat(LOCAL_NS)
    .concat(state.dirSources.map((d) => "@dir:" + d.id));
  const entries = await Promise.all(names.map(async (name) => {
    try { return [name, (await api("GET", "/api/skills?repo=" + encodeURIComponent(name))) || []]; }
    catch { return [name, []]; }
  }));
  state.skillsByRepo = Object.fromEntries(entries);
  // 插件 skill（按 harness 分类，注入到「目录现状」底部分组）。
  try { state.pluginSkills = (await api("GET", "/api/plugins")) || []; }
  catch { state.pluginSkills = []; }
  // skills.sh skill（第三方，可在「+ 添加」里链接进目标，selector 用 @agents/<name>）。
  try { state.skillsShSkills = (await api("GET", "/api/skillssh")) || []; }
  catch { state.skillsShSkills = []; }
  renderStats(); renderRepos(); renderTabs(); renderSummary();
  await fetchInventory();
  if (state.status.gitError) {
    banner("未检测到 git：" + state.status.gitError + "。请安装 Git 并确保在 PATH 中，然后重启本工具——否则无法拉取/更新仓库。", true);
  } else {
    banner(repos.length === 0 && state.targets.length === 0 ? "还没有仓库。在左侧添加一个 git skill 仓开始。" : "");
  }
}

function renderStats() {
  const el = $("#stats");
  const repos = (state.status.repos || []).length;
  // 「收录」= SkillManage 一共管控/识别了多少个 skill，跨全部源汇总：git 源 + 本地源
  // （@local 受管存储 + @dir 登记目录，都在 skillsByRepo 里）+ npx skills.sh 源。
  // 比「某个目录里链接了几个」更能体现这个工具的整体盘子。
  let catalog = 0;
  for (const arr of Object.values(state.skillsByRepo || {})) catalog += (arr || []).length;
  const npx = (state.skillsShSkills || []).length;
  const total = catalog + npx;
  // 顶部「冲突」是库内跨源体检：名称重复 + 关键词重叠（与页脚「健康度」是两件事）。
  const cf = computeLibraryConflicts();
  const cfN = cf.dups.length + cf.overlaps.length;
  el.innerHTML = "";
  el.append(ce("span", { innerHTML: `仓库 <b>${repos}</b>`, title: "已登记的 git 仓数量" }));
  el.append(ce("span", { innerHTML: `收录 skills <b>${total}</b>`, title: `SkillManage 一共收录管控的 skill 数：git 源 + 本地源 ${catalog} 个，npx(skills.sh) 源 ${npx} 个。` }));
  const c = ce("span", { innerHTML: cfN ? `冲突 <b>${cfN}</b>` : `冲突 <b>无</b>`, title: "跨源体检：名称重复 / 关键词重叠——点击查看" });
  c.className = (cfN ? "stat-warn " : "") + "clickable-stat";
  c.onclick = openConflictModal;
  el.append(c);
}

// REPO_STATE localizes each repo sync state to a Chinese badge label + class.
// "stale" = cloned earlier but not pulled this session (auto-pull was removed),
// so it shows 未更新 rather than the misleading 未克隆.
const REPO_STATE = {
  "cloned": { label: "已克隆", cls: "ok" },
  "synced": { label: "已同步", cls: "ok" },
  "stale": { label: "未更新", cls: "never-synced" },
  "never-synced": { label: "未克隆", cls: "never-synced" },
  "dirty-skip": { label: "有本地改动", cls: "dirty-skip" },
  "failed": { label: "失败", cls: "failed" },
  "cloning": { label: "克隆中", cls: "cloning" },
  "sync-in-progress": { label: "同步中", cls: "sync-in-progress" },
};
function stateBadge(st) {
  const m = REPO_STATE[st] || { label: st, cls: "" };
  return ce("span", { className: "badge " + m.cls, textContent: m.label });
}

// repoDot classifies a repo into a connectivity/usability dot color: green =
// cloned & usable (its skills can be linked, even if not pulled this session),
// red = auth/connection failure, grey = never cloned yet or in progress. Note
// freshness ("未更新") is shown by the state BADGE, not the dot — a cloned repo
// is green regardless of whether it was pulled this session.
function repoDot(repo) {
  const st = repo.state || "never-synced";
  if (repo.error || st === "failed") return "err";
  if (st === "never-synced" || st === "cloning" || st === "sync-in-progress") return "idle";
  return "ok"; // cloned / synced / stale(已克隆未更新) / dirty-skip → usable → green
}

function httpsHost(u) {
  try { if (!/^https?:\/\//i.test(u)) return ""; return new URL(u).hostname; } catch { return ""; }
}

function openCredModal(host, username, pending) {
  $("#cred-host").textContent = host;
  $("#cred-host").dataset.host = host;
  $("#cred-user").value = username || "";
  $("#cred-token").value = "";
  state.credPending = pending || null;
  $("#cred-skip").classList.toggle("hidden", !pending);
  $("#cred-modal").classList.remove("hidden");
  $("#cred-user").focus();
}
function closeCredModal() {
  state.credPending = null;
  $("#cred-skip").classList.add("hidden");
  $("#cred-modal").classList.add("hidden");
}

// sourceDivider builds a sidebar section header. actions (optional) render on the
// right, after a filler line, so each source type's own controls live in its label.
function sourceDivider(label, actions, afterLabel) {
  const has = (actions && actions.length) || afterLabel;
  const d = ce("li", { className: "repo-divider" + (has ? " with-actions" : "") });
  d.append(ce("span", { textContent: label }));
  if (afterLabel) d.append(afterLabel); // 紧跟标签（如 git 仓的 ? 说明）
  if (has) {
    d.append(ce("div", { className: "divider-line" }));
    if (actions) actions.forEach((b) => d.append(b));
  }
  return d;
}

function renderRepos() {
  const ul = $("#repo-list"); ul.innerHTML = "";
  // 三类源平级展示：git 仓 / npx skills / 本地源（都是「源」）。每类一个分隔标题，
  // 该来源专属的动作放在标题行右侧。
  // git 仓分区标题行：全量更新（仅 git）。说明统一在「使用指南」里，不再单独放 ?。
  const gitFullUpdate = ce("button", { className: "ghost small", textContent: "全量更新", title: "更新全部 git 仓：拉取上游并重新同步（不含 npx）；有本地改动会提示是否还原并更新" });
  gitFullUpdate.onclick = (e) => { e.stopPropagation(); updateNow(false); };
  ul.append(sourceDivider("git 仓", [gitFullUpdate]));
  // 添加行：导出 / 导入 / 添加（「添加」点击打开弹窗输入 URL + 分支）。
  const addRow = ce("li", { className: "repo-addrow" });
  const gitExport = ce("button", { className: "ghost small", textContent: "导出", title: "导出 git 仓库列表（用于在另一台机器重建来源）" });
  gitExport.onclick = exportRepos;
  const gitImport = ce("button", { className: "ghost small", textContent: "导入", title: "从文件导入 git 仓库列表" });
  gitImport.onclick = importRepos;
  const addBtn = ce("button", { className: "small", textContent: "添加", title: "添加一个 git 仓作为源（弹窗输入地址与分支）" });
  addBtn.onclick = openGitRepoModal;
  // 导入 / 导出 / 添加 归到一起、都靠左。
  addRow.append(gitImport, gitExport, addBtn);
  ul.append(addRow);
  (state.status.repos || []).forEach((repo) => {
    const host = httpsHost(repo.url);
    // 整张卡片可点击打开「该仓库的 skill」弹窗；卡片内的按钮/圆点自行 stopPropagation。
    const li = ce("li", { className: "repo-card clickable", title: "查看该仓库内的 skill" });
    li.onclick = () => openRepoSkills(repo.name, {});
    const top = ce("div", { className: "repo-top" });
    const dotKind = repoDot(repo);
    const dot = ce("span", { className: "repo-dot " + dotKind });
    if (dotKind === "err" && host) {
      dot.title = "连接/鉴权失败，点击重填凭据";
      dot.classList.add("clickable");
      dot.onclick = (e) => { e.stopPropagation(); openCredModal(host, state.credHosts[host] || ""); };
    } else {
      dot.title = dotKind === "ok" ? "上次同步成功" : dotKind === "err" ? "上次同步失败" : "尚未同步";
    }
    top.append(dot, ce("span", { className: "repo-name", textContent: repo.name }), stateBadge(repo.state || "never-synced"));
    if (repo.hasUpdate) top.append(ce("span", { className: "badge has-update", textContent: "有更新", title: "上游有新提交，点「立即更新」拉取" }));
    li.append(top);
    li.append(ce("div", { className: "repo-url", textContent: repo.url }));
    const meta = ce("div", { className: "repo-meta" });
    const n = (state.skillsByRepo[repo.name] || []).length;
    meta.append(ce("span", { className: "badge count", textContent: n + " skill" }));
    if (host) {
      const has = Object.prototype.hasOwnProperty.call(state.credHosts, host);
      const cb = ce("button", { className: "ghost small", textContent: has ? "凭据✓" : "填写凭据", title: has ? ("已为 " + host + " 配置凭据，点此重填") : ("为私有仓 " + host + " 填写 HTTPS 令牌") });
      cb.onclick = (e) => { e.stopPropagation(); openCredModal(host, state.credHosts[host] || ""); };
      meta.append(cb);
    }
    meta.append(ce("span", { className: "group-spacer" }));
    const rm = ce("button", { className: "danger small", textContent: "删除" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmModal("删除仓库 " + repo.name + "？它建立的软链会立即清理。"))) return;
      await api("DELETE", "/api/repos", { url: repo.url });
      await load();
    };
    meta.append(rm); // 移除在左
    const up = ce("button", { className: "ghost small", textContent: "更新", title: "只拉取此仓上游并重新同步（单仓手动更新）" });
    up.onclick = (e) => { e.stopPropagation(); updateRepo(repo, up); };
    meta.append(up); // 更新在右
    li.append(meta);
    if (repo.error) li.append(ce("div", { className: "muted", style: "color:var(--err);font-size:12px;margin-top:6px;white-space:pre-wrap", textContent: repo.error }));
    if (repo.authHint) li.append(ce("div", { className: "repo-authhint", textContent: host ? "鉴权失败，无法自动更新：点上方「填写凭据」填个人令牌(PAT)，或改用 SSH。" : "鉴权失败，无法自动更新：私有仓需配置 SSH key（加入 ssh-agent）。详见标题旁 ? 指南。" }));
    ul.append(li);
  });
  // 目录源（别家管理）：skills.sh = vercel-labs/skills，canonical 在 ~/.agents/skills，
  // 归 npx skills 管。我们只读识别，更新转交其原生命令，绝不接管（第④不变式）。
  const sh = state.status.skillsSh;
  if (sh && sh.count > 0) {
    // npx 分区标题行：全量更新（代调 npx skills update），与 git 仓的全量更新对齐。
    let npxActions = [];
    if (state.npxAvailable) {
      const npxUpdate = ce("button", { className: "ghost small", textContent: "全量更新", title: "npx skills update：更新全部 skills.sh skill" });
      npxUpdate.onclick = (e) => { e.stopPropagation(); updateSkillsShAll(npxUpdate); };
      npxActions = [npxUpdate];
    }
    ul.append(sourceDivider("npx skills", npxActions));
    const shLi = ce("li", { className: "repo-card repo-skillssh clickable", title: "查看 skills.sh 管理的 skill" });
    shLi.onclick = () => openSkillsShModal();
    const stop = ce("div", { className: "repo-top" });
    stop.append(ce("span", { className: "repo-dot ok", title: "skills.sh 目录源" }));
    stop.append(ce("span", { className: "repo-name", textContent: "skills.sh" }));
    stop.append(ce("span", { className: "src-badge src-skillssh", textContent: "只读" }));
    shLi.append(stop);
    shLi.append(ce("div", { className: "repo-url", textContent: (sh.root || "~/.agents/skills") + " · vercel-labs/skills（npx skills 管理）" }));
    const smeta = ce("div", { className: "repo-meta" });
    smeta.append(ce("span", { className: "badge count", textContent: sh.count + " skill" }));
    if (!state.npxAvailable) {
      smeta.append(ce("span", { className: "group-spacer" }));
      smeta.append(ce("span", { className: "inv-hint", textContent: "npx 不可用" }));
    }
    shLi.append(smeta);
    ul.append(shLi);
  }

  // 本地源：同一类能力——都是「本地源」。区别只在来源：
  //   · local —— SkillManage 创建的受管存储（备份/手写归此，可删 skill）
  //   · 其余 —— 用户选择的文件夹（实时识别，不复制，整源可移除）
  // 「添加本地源」放在分区标题行右侧（选一个文件夹作为来源）。
  const addLocal = ce("button", { className: "ghost small", textContent: "添加本地源", title: "选择一个文件夹作为本地源（实时识别其中的 skill，不复制、不改动原文件）" });
  addLocal.onclick = openLocalSrcModal;
  ul.append(sourceDivider("本地源", [addLocal]));
  const localLi = ce("li", { className: "repo-card repo-local clickable", title: "查看本地（已备份）skill" });
  localLi.onclick = () => openRepoSkills(LOCAL_NS, { local: true });
  const ltop = ce("div", { className: "repo-top" });
  ltop.append(ce("span", { className: "repo-dot ok", title: "本地受管存储（SkillManage 创建）" }));
  ltop.append(ce("span", { className: "repo-name", textContent: "local" }));
  ltop.append(ce("span", { className: "src-badge src-local", textContent: "本地源" }));
  localLi.append(ltop);
  localLi.append(ce("div", { className: "repo-url", textContent: "~/.skillmanage/local · SkillManage 创建（备份/手写归此）" }));
  const lmeta = ce("div", { className: "repo-meta" });
  lmeta.append(ce("span", { className: "badge count", textContent: (state.skillsByRepo[LOCAL_NS] || []).length + " skill" }));
  localLi.append(lmeta);
  ul.append(localLi);

  // 用户登记的本地目录源：每个文件夹一张卡片（实时识别，不复制）。
  (state.dirSources || []).forEach((d) => {
    const ns = dirNS(d.id);
    const li = ce("li", { className: "repo-card repo-local clickable", title: "查看该本地源里的 skill" });
    li.onclick = () => openRepoSkills(ns, { title: d.label + " · 本地源", hint: "本地源，只读（移除整个源请用侧栏「移除」）" });
    const top = ce("div", { className: "repo-top" });
    top.append(ce("span", { className: "repo-dot ok", title: "本地源（你选择的文件夹，实时识别，不复制）" }));
    top.append(ce("span", { className: "repo-name", textContent: d.label }));
    top.append(ce("span", { className: "src-badge src-local", textContent: "本地源" }));
    li.append(top);
    li.append(ce("div", { className: "repo-url", textContent: d.path + " · 你选择的文件夹（实时识别，不复制）" }));
    const m = ce("div", { className: "repo-meta" });
    m.append(ce("span", { className: "badge count", textContent: (d.count || 0) + " skill" }));
    m.append(ce("span", { className: "group-spacer" }));
    const rm = ce("button", { className: "danger small", textContent: "移除" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmModal("移除本地源「" + d.label + "」？它建立的软链会清理，原文件夹不动。"))) return;
      try { await api("DELETE", "/api/local-source", { id: d.id }); }
      catch (err) { banner("移除失败：" + err.message, true); return; }
      toast("已移除本地源 " + d.label);
      await load();
    };
    m.append(rm);
    li.append(m);
    ul.append(li);
  });
}

// openSkillsShModal lists skills.sh-managed skills. They are read-only at the
// source (updates go through the sidebar card's「更新」/ npx), but you CAN enable
// them into the current target / 自动同步 here (selector namespace "@agents"),
// same as any other source.
async function openSkillsShModal() {
  $("#repo-skills-title").textContent = "skills.sh · npx skills";
  const body = $("#repo-skills-body");
  const render = (skills) => {
    body.innerHTML = "";
    const target = currentTarget();
    const follow = enabledFollow(AGENTS_NS);
    const bar = ce("div", { className: "rs-toolbar" });
    if (target) {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "启用到当前目录：" + targetLabel(target) + "（只读源，更新走 npx）" }));
      bar.append(ce("span", { className: "group-spacer" }));
      const fb = ce("button", {
        className: (follow ? "" : "ghost") + " small",
        textContent: follow ? "🔄 同步中" : "自动同步",
        title: follow ? "关闭自动同步" : "自动同步：skills.sh 现有及将来的全部 skill 自动启用进当前目录",
      });
      fb.onclick = async () => {
        fb.disabled = true;
        if (follow) await api("DELETE", "/api/enabled", { skill: AGENTS_NS + "/*", target });
        else await api("POST", "/api/enabled", { skill: AGENTS_NS + "/*", target, mode: "follow" });
        await api("POST", "/api/apply");
        toast(follow ? "已关闭自动同步" : "已开启自动同步");
        await load(); render(skills);
      };
      bar.append(fb);
    } else {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "由 npx skills 管理。先选一个目录 tab 才能启用；更新请用侧栏卡片上的「更新」。" }));
    }
    body.append(bar);
    const list = ce("div", { className: "rs-list" });
    body.append(list);
    if (!skills) { list.append(ce("div", { className: "empty", textContent: "加载中…" })); return; }
    if (!skills.length) { list.append(ce("div", { className: "empty", textContent: "暂无 skills.sh skill。" })); return; }
    const present = new Set(state.inventory.filter((i) => i.managed).map((i) => i.name));
    skills.forEach((sk) => {
      const name = sk.linkName || sk.logicalName;
      const card = ce("div", { className: "skill rs-card clickable", title: "查看详情" });
      // Card opens detail; skip clicks on buttons, links, and the selectable
      // update-command code so they keep their own behavior.
      card.onclick = (e) => { if (e.target.closest("button, a, code")) return; openDetail(AGENTS_NS, name); };
      const main = ce("div", { className: "skill-main" });
      const r1 = ce("div", { className: "skill-row1" });
      r1.append(ce("span", { className: "skill-name", textContent: name }));
      { const vt = verTag(sk.version); if (vt) r1.append(vt); }
      // 来源徽章：lockfile 里的 owner/repo（hover 看完整 URL）。
      const srcText = sk.source || repoFromUrl(sk.sourceUrl) || hostOf(sk.sourceUrl || "");
      if (srcText) {
        const sb = ce("span", { className: "src-badge src-skillssh", textContent: srcText });
        if (sk.sourceUrl) sb.title = sk.sourceUrl;
        r1.append(sb);
      }
      r1.append(ce("span", { className: "group-spacer" }));
      r1.append(enableControl(AGENTS_NS, name, follow, present.has(name), target, () => render(skills)));
      main.append(r1);
      if (sk.description) main.append(ce("div", { className: "skill-desc", textContent: sk.description }));
      // 来源 URL（可见、可选中）——更新就从这里拉取，但命令按 skill 名走，
      // URL 由 skills.sh 自己的台账（~/.agents/.skill-lock.json）记录，不用手填。
      if (sk.sourceUrl) main.append(ce("div", { className: "rs-srcurl", textContent: "来源 " + sk.sourceUrl }));
      const cmd = ce("div", { className: "rs-cmdline" });
      cmd.append(ce("code", { className: "rs-cmd", textContent: "npx skills update " + name }));
      cmd.append(ce("span", { className: "rs-cmd-note", textContent: "按名更新，URL 由 skills.sh 台账记录" }));
      main.append(cmd);
      card.append(main); list.append(card);
    });
  };
  render(null);
  $("#repo-skills-modal").classList.remove("hidden");
  try {
    render((await api("GET", "/api/skillssh")) || []);
  } catch (e) {
    body.innerHTML = "";
    body.append(ce("div", { className: "empty", style: "color:var(--err)", textContent: "加载失败：" + e.message }));
  }
}

async function updateSkillsShAll(btn) {
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  try {
    const d = await api("POST", "/api/skillssh/update-all", {});
    if (d && d.ok) toast("skills.sh 已全部更新");
    else banner("skills.sh 更新失败：" + ((d && (d.stderr || d.error)) || "未知错误"), true);
  } catch (e) {
    banner("skills.sh 更新失败：" + e.message, true);
  }
  btn.disabled = false; btn.textContent = old;
}

// openRepoSkills shows a modal listing every skill in a source, and IS the place
// to enable skills / 自动同步 into the current target (replacing the old top「管理」
// drawer). opts.ns overrides the selector namespace (defaults to repoName);
// opts.local → @local store skills are also deletable; opts.title overrides the
// heading. Enable/follow act on the current target tab.
function openRepoSkills(repoName, opts) {
  opts = opts || {};
  const ns = opts.ns || repoName; // selector namespace for enable / 自动同步
  $("#repo-skills-title").textContent = opts.title || (opts.local ? "local · 本地源" : repoName);
  const body = $("#repo-skills-body");
  const render = () => {
    body.innerHTML = "";
    const target = currentTarget();
    const follow = enabledFollow(ns);
    // Toolbar: which target we enable into + the 自动同步 toggle for this source.
    const bar = ce("div", { className: "rs-toolbar" });
    if (target) {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "启用到当前目录：" + targetLabel(target) }));
      bar.append(ce("span", { className: "group-spacer" }));
      const fb = ce("button", {
        className: (follow ? "" : "ghost") + " small",
        textContent: follow ? "🔄 同步中" : "自动同步",
        title: follow ? "关闭自动同步：不再自动纳入该源的 skill" : "自动同步：该源现有及将来新增的全部 skill 自动启用进当前目录",
      });
      fb.onclick = async () => {
        fb.disabled = true;
        if (follow) await api("DELETE", "/api/enabled", { skill: ns + "/*", target });
        else await api("POST", "/api/enabled", { skill: ns + "/*", target, mode: "follow" });
        await api("POST", "/api/apply");
        toast(follow ? "已关闭自动同步" : "已开启自动同步");
        await load(); render();
      };
      bar.append(fb);
    } else {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "未选择同步目录——先在上方选一个目录 tab，再启用。" }));
    }
    body.append(bar);

    // The cards live in their own scrolling list; the toolbar above stays fixed.
    const list = ce("div", { className: "rs-list" });
    body.append(list);
    const skills = state.skillsByRepo[repoName] || [];
    if (skills.length === 0) { list.append(ce("div", { className: "empty", textContent: "该源暂无 skill。" })); return; }
    const present = new Set(state.inventory.filter((i) => i.managed).map((i) => i.name));
    skills.forEach((sk) => {
      const nm = sk.linkName || sk.logicalName;
      const card = ce("div", { className: "skill rs-card clickable", title: "查看详情" });
      // Clicking the card opens the skill's detail (SKILL.md); clicks on the
      // action buttons are excluded so they don't double-fire.
      card.onclick = (e) => { if (e.target.closest("button, a")) return; openDetail(repoName, nm); };
      const main = ce("div", { className: "skill-main" });
      const r1 = ce("div", { className: "skill-row1" });
      r1.append(ce("span", { className: "skill-name", textContent: nm }));
      { const vt = verTag(sk.version); if (vt) r1.append(vt); }
      r1.append(ce("span", { className: "group-spacer" }));
      if (opts.local) {
        const del = ce("button", { className: "danger small", textContent: "删除", title: "永久删除该本地 skill 的受管副本，并拆除它建立的所有软链（不可恢复）" });
        del.onclick = () => deleteLocalSkill(nm, del, render);
        r1.append(del);
      }
      r1.append(enableControl(ns, nm, follow, present.has(nm), target, render));
      main.append(r1);
      if (sk.description) main.append(ce("div", { className: "skill-desc", textContent: sk.description }));
      card.append(main);
      list.append(card);
    });
  };
  render();
  $("#repo-skills-modal").classList.remove("hidden");
}

// enableControl returns the per-skill action used inside the source modals: a
// SINGLE button that reflects the current state and flips it — 「停用」when the
// skill is enabled in the target (click → tear down the link), 「启用」when it is
// not (click → build the link, with same-name shadow confirmation). The two are
// mutually exclusive: only one shows at a time. Under 自动同步 (or with no target)
// individual toggling is unavailable, so a hint shows instead. render re-renders
// the modal.
function enableControl(ns, name, follow, enabled, target, render) {
  if (!target) return ce("span", { className: "inv-hint", textContent: "未选目录" });
  if (follow) return ce("span", { className: "inv-hint", textContent: "自动同步中" });
  const sel = ns + "/" + name;
  if (enabled) {
    const off = ce("button", { className: "danger small", textContent: "停用", title: "拆除当前目录下的软链（不影响真身与其它目录）" });
    off.onclick = async () => {
      off.disabled = true;
      await api("DELETE", "/api/enabled", { skill: sel, target });
      const sum = await api("POST", "/api/apply");
      summaryToast(sum, name, false);
      await load(); render();
    };
    return off;
  }
  const on = ce("button", { className: "small", textContent: "启用", title: "在当前目录建立软链" });
  on.onclick = async () => {
    if (!(await confirmShadowEnable(name, target))) return;
    on.disabled = true;
    await api("POST", "/api/enabled", { skill: sel, target, mode: "snapshot" });
    const sum = await api("POST", "/api/apply");
    summaryToast(sum, name, true);
    await load(); render();
  };
  return on;
}

// updateRepo pulls a single git repo's upstream and re-syncs (per-repo manual
// update). Header「全量更新」does all repos; this does just one.
async function updateRepo(repo, btn, force) {
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  try {
    const resp = await api("POST", "/api/repos/update", { url: repo.url, force: !!force });
    // The mirror is a read-only clone, but local drift can happen (e.g. a file
    // deleted through a symlink that pointed into the mirror). git refuses to
    // reset over it unless forced — so ask before discarding.
    if (resp && resp.dirty) {
      btn.disabled = false; btn.textContent = old;
      const ok = await confirmModal(
        "镜像「" + repo.name + "」有本地改动，更新已暂停以免覆盖：\n\n" + (resp.error || "working tree has local modifications") +
        "\n\n镜像是只读副本，正常不该有本地改动。点「恢复并更新」将丢弃这些改动（git reset --hard + clean -fd），把镜像恢复成上游最新。",
        "恢复并更新", true);
      if (ok) await updateRepo(repo, btn, true);
      return;
    }
    const sum = (resp && resp.summary) || resp;
    if (sum && sum.errors && sum.errors.length) banner("更新 " + repo.name + "：" + sum.errors.join("；"), true);
    else toast(force ? "已恢复并更新 " + repo.name : "已更新 " + repo.name);
    await load();
  } catch (e) {
    btn.disabled = false; btn.textContent = old;
    banner("更新 " + repo.name + " 失败：" + e.message, true);
  }
}

async function deleteLocalSkill(name, btn, rerender) {
  if (!(await confirmModal(
    "永久删除本地 skill「" + name + "」？\n\n会删除 ~/.skillmanage/local 里的受管副本，并拆除它在各目录建立的软链。此操作不可恢复。",
    "永久删除", true))) return;
  btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/local-skill", { name });
    toast("已删除本地 skill " + name);
    await load();        // refresh repo catalog + counts + inventory
    if (rerender) rerender();
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除";
    banner("删除 " + name + " 失败：" + e.message, true);
  }
}

// renderTabs draws one tab per sync directory. Switching a tab re-scans that
// directory's inventory.
// defaultTabLabel derives a meaningful default name from a target path: the
// dir is always ".../<harness>/skills", so the leaf "skills" is useless — drop
// it and the harness folder (.claude/.codex/.agents) and use the parent (the
// project / home dir name) instead.
function defaultTabLabel(dir) {
  const parts = dir.replace(/\/+$/, "").split("/").filter(Boolean);
  if (parts.length && parts[parts.length - 1].toLowerCase() === "skills") parts.pop();
  if (parts.length && /^\.(claude|codex|agents)$/.test(parts[parts.length - 1])) parts.pop();
  return parts[parts.length - 1] || dir;
}

// startAliasEdit turns a tab's label into an inline text input (no native prompt,
// which WKWebView blocks). Enter / blur saves via /api/targets/alias; Esc cancels.
// An empty value clears the alias (tab reverts to showing the path).
function startAliasEdit(t, tab) {
  const span = tab.querySelector(".tab-dir");
  if (!span || tab.querySelector(".tab-alias-input")) return;
  const input = ce("input", { className: "tab-alias-input", value: t.alias || "" });
  input.placeholder = defaultTabLabel(t.dir);
  span.replaceWith(input);
  input.focus(); input.select();
  let done = false;
  const commit = async (save) => {
    if (done) return; done = true;
    if (save) {
      try { await api("POST", "/api/targets/alias", { dir: t.dir, alias: input.value.trim() }); await load(); return; }
      catch (err) { banner("保存别名失败：" + err.message, true); }
    }
    renderTabs();
  };
  input.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); commit(true); }
    else if (e.key === "Escape") { e.preventDefault(); commit(false); }
  };
  input.onblur = () => commit(true);
  // Keep clicks inside the input from bubbling to the tab (which would switch
  // tabs and re-render, destroying the input mid-edit).
  input.onclick = (e) => e.stopPropagation();
  input.onmousedown = (e) => e.stopPropagation();
  input.ondblclick = (e) => e.stopPropagation();
}

function renderTabs() {
  const bar = $("#target-tabs"); bar.innerHTML = "";
  const active = currentTarget();
  if (state.targets.length === 0) {
    bar.append(ce("span", { className: "muted", style: "align-self:center", textContent: "还没有同步目录 →" }));
  }
  let activeEl = null;
  state.targets.forEach((t) => {
    const tab = ce("div", { className: "tab" + (t.dir === active ? " active" : ""), title: t.dir });
    if (t.dir === active) activeEl = tab;
    tab.append(ce("span", { className: "badge " + harnessClass(t.harness), textContent: t.harness }));
    tab.append(ce("span", { className: "tab-dir", textContent: t.alias || t.dir }));
    const rm = ce("button", { className: "tab-x", textContent: "×", title: "移除此同步目录" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmModal("移除同步目录 " + (t.alias || t.dir) + "？\n该目录下由本工具建立的链接会立即清理；目录里你自己的真身 skill 不受影响。"))) return;
      if (state.activeTarget === t.dir) state.activeTarget = undefined;
      await api("DELETE", "/api/targets", { dir: t.dir });
      await load();
    };
    tab.append(rm);
    // Switch active in place (don't rebuild the bar): a full renderTabs() here
    // would destroy this element before a double-click could land on it, so
    // dblclick-to-rename never fired. Swap the active class instead.
    tab.onclick = () => {
      if (state.activeTarget === t.dir) return;
      state.activeTarget = t.dir;
      $("#target-tabs").querySelectorAll(".tab.active").forEach((el) => el.classList.remove("active"));
      tab.classList.add("active");
      fetchInventory();
    };
    // Double-click the tab to rename its alias inline (path still shows on hover).
    tab.ondblclick = (e) => { e.stopPropagation(); startAliasEdit(t, tab); };
    // Drag to reorder tabs (persisted via /api/targets/reorder).
    tab.draggable = true;
    tab.ondragstart = (e) => { state.dragDir = t.dir; tab.classList.add("dragging"); e.dataTransfer.effectAllowed = "move"; };
    tab.ondragend = () => { tab.classList.remove("dragging"); document.querySelectorAll(".tab.drop-to").forEach((x) => x.classList.remove("drop-to")); };
    tab.ondragover = (e) => { if (state.dragDir && state.dragDir !== t.dir) { e.preventDefault(); tab.classList.add("drop-to"); } };
    tab.ondragleave = () => tab.classList.remove("drop-to");
    tab.ondrop = (e) => { e.preventDefault(); tab.classList.remove("drop-to"); reorderTabs(state.dragDir, t.dir); };
    bar.append(tab);
  });
  if (activeEl) {
    const cRect = bar.getBoundingClientRect();
    const tRect = activeEl.getBoundingClientRect();
    const delta = (tRect.left - cRect.left) - (bar.clientWidth - activeEl.offsetWidth) / 2;
    bar.scrollTo({ left: bar.scrollLeft + delta, behavior: "smooth" });
  }
}

// reorderTabs moves the dragged tab to where it was dropped and persists order.
async function reorderTabs(fromDir, toDir) {
  state.dragDir = null;
  if (!fromDir || fromDir === toDir) return;
  const arr = state.targets.slice();
  const fi = arr.findIndex((t) => t.dir === fromDir);
  const ti = arr.findIndex((t) => t.dir === toDir);
  if (fi < 0 || ti < 0) return;
  const [moved] = arr.splice(fi, 1);
  arr.splice(ti, 0, moved);
  state.targets = arr;
  renderTabs();
  try { await api("POST", "/api/targets/reorder", { dirs: arr.map((t) => t.dir) }); }
  catch (e) { banner("保存标签顺序失败：" + e.message, true); }
}

function openTargetModal() {
  $("#target-path").value = "";
  $("#target-alias").value = "";
  $("#target-modal").classList.remove("hidden");
  $("#target-path").focus();
  browseTo("");
}
function closeTargetModal() { $("#target-modal").classList.add("hidden"); }

// browseTo lists a directory's subfolders into a picker. boxSel/pathSel let it
// drive either the「添加同步目录」or the「添加本地源」modal (same widget, two homes).
async function browseTo(path, boxSel = "#target-browser", pathSel = "#target-path") {
  const box = $(boxSel);
  box.innerHTML = "";
  let resp;
  try { resp = await api("GET", "/api/browse?path=" + encodeURIComponent(path)); }
  catch (err) { box.append(ce("div", { className: "dir-empty err", textContent: err.message })); return; }
  $(pathSel).value = resp.path;
  if (resp.parent) {
    const up = ce("div", { className: "dir-row up" });
    up.append(ce("span", { className: "ic", textContent: "⬆" }), ce("span", { textContent: "上级目录" }));
    up.onclick = () => browseTo(resp.parent, boxSel, pathSel);
    box.append(up);
  }
  if (resp.dirs.length === 0) { box.append(ce("div", { className: "dir-empty", textContent: "（无子目录）" })); return; }
  resp.dirs.forEach((d) => {
    const row = ce("div", { className: "dir-row" });
    row.append(ce("span", { className: "ic", textContent: "📁" }), ce("span", { textContent: d.name }));
    row.onclick = () => browseTo(d.path, boxSel, pathSel);
    box.append(row);
  });
}

// --- 添加本地源: pick a folder; import its skill(s) into the @local store ---
function openLocalSrcModal() {
  $("#localsrc-path").value = "";
  $("#localsrc-alias").value = "";
  $("#localsrc-modal").classList.remove("hidden");
  $("#localsrc-path").focus();
  browseTo("", "#localsrc-browser", "#localsrc-path");
}
function closeLocalSrcModal() { $("#localsrc-modal").classList.add("hidden"); }

async function addLocalSource(dir, label) {
  let res;
  try { res = await api("POST", "/api/local-source", { dir, label }); }
  catch (err) { banner("添加本地源失败：" + err.message, true); return; }
  toast("已添加本地源「" + (res.label || dir) + "」（识别到 " + (res.count || 0) + " 个 skill）");
  await load();
}

// --- inventory (目录现状视图, phase 3 U8) ---

async function fetchInventory() {
  const t = currentTarget();
  if (!t) { state.inventory = []; state.invLoading = false; state.invError = ""; renderInventory(); return; }
  state.invLoading = true; state.invError = ""; renderInventory();
  try {
    const r = await api("GET", "/api/inventory?target=" + encodeURIComponent(t));
    state.inventory = r.items || [];
    state.invScope = r.scope || "";
  } catch (e) { state.inventory = []; state.invError = e.message; }
  state.invLoading = false;
  renderInventory();
}

// abbrevLabel shortens a long source label to its hyphen-segment initials
// (compound-engineering → ce), per the team convention. Only labels longer than
// 10 chars that actually contain a hyphen are abbreviated; everything else is
// returned as-is. Callers keep the full label in the element's title (hover).
function abbrevLabel(s) {
  if (!s || s.length <= 10 || !s.includes("-")) return s;
  return s.split("-").filter(Boolean).map((p) => p[0]).join("");
}

// verTag renders a version badge, or null when there's no STANDARD version.
// Only a semver-shaped value (e.g. 3.13.1, 1.0, 2.1.0-beta.1, optional "v"
// prefix) counts as a version. Anything else — a git commit hash (commit-pinned
// plugins), "unknown"/"undefined", or empty — is NOT a version and shows no
// badge at all.
function verTag(v) {
  v = (v == null ? "" : String(v)).trim().replace(/^v/i, "");
  if (!/^\d+\.\d+(\.\d+)?([-+][0-9A-Za-z.-]+)?$/.test(v)) return null;
  return ce("span", { className: "ver-tag", textContent: "v" + v, title: "版本 " + v });
}

// sourceMeta maps a source namespace to a display label + badge class.
function sourceMeta(ns) {
  if (ns === AGENTS_NS) return { label: "skills.sh", cls: "src-skillssh" };
  if (ns === LOCAL_NS) return { label: "local", cls: "src-local" };
  if (ns.startsWith("@dir:")) return { label: dirLabel(ns.slice(5)), cls: "src-local" };
  return { label: ns, cls: "src-git" }; // git repo name
}

// renderSearchResults is the GLOBAL skill search: instead of filtering only the
// current directory, it searches every enable-able skill across all sources
// (git mirrors + local sources + skills.sh) and lets you enable/disable each
// into the current target in real time. Driven by the search box (non-empty term).
function renderSearchResults(root, term) {
  const target = currentTarget();
  // selectors already enabled (linked) in the current target.
  const enabledSel = new Set((state.inventory || []).filter((i) => i.managed && i.selector).map((i) => i.selector));
  const all = [];
  for (const [ns, skills] of Object.entries(state.skillsByRepo || {})) {
    (skills || []).forEach((sk) => all.push({ ns, name: sk.linkName || sk.logicalName, description: sk.description, version: sk.version }));
  }
  (state.skillsShSkills || []).forEach((sk) => all.push({ ns: AGENTS_NS, name: sk.linkName || sk.logicalName, description: sk.description, version: sk.version }));
  const items = all
    .filter((r) => r.name.toLowerCase().includes(term) || (r.description || "").toLowerCase().includes(term))
    .sort((a, b) => a.name.localeCompare(b.name) || a.ns.localeCompare(b.ns));

  const head = ce("div", { className: "search-head" });
  head.append(ce("span", { className: "group-title", textContent: "搜索结果" }));
  head.append(ce("span", { className: "badge count", textContent: items.length + " skill" }));
  head.append(ce("span", { className: "group-spacer" }));
  head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: target ? "启用到当前目录：" + targetLabel(target) : "先选一个目录 tab 才能启用" }));
  root.append(head);

  if (!items.length) {
    root.append(ce("div", { className: "empty", textContent: "没有匹配的本地 skill（已搜索全部 git / 本地 / skills.sh 源）" }));
  } else {
    const body = ce("div", { className: "inv-group-body" });
    items.forEach((r) => {
      const enabled = enabledSel.has(r.ns + "/" + r.name);
      const follow = enabledFollow(r.ns);
      const meta = sourceMeta(r.ns);
      const card = ce("div", { className: "skill inv clickable", title: "查看详情" });
      card.onclick = (e) => { if (e.target.closest("button, a")) return; openDetail(r.ns, r.name); };
      const main = ce("div", { className: "skill-main" });
      const r1 = ce("div", { className: "skill-row1" });
      r1.append(ce("span", { className: "skill-name", textContent: r.name }));
      { const vt = verTag(r.version); if (vt) r1.append(vt); }
      r1.append(ce("span", { className: "src-badge " + meta.cls, textContent: meta.label }));
      r1.append(ce("span", { className: "group-spacer" }));
      r1.append(enableControl(r.ns, r.name, follow, enabled, target, renderInventory));
      main.append(r1);
      if (r.description) main.append(ce("div", { className: "skill-desc", textContent: r.description }));
      card.append(main);
      body.append(card);
    });
    root.append(body);
  }
  // 「在线（skills.sh）」分区——始终渲染（只渲染 state.skillsShOnline、绝不在此发请求；
  // 线上查询只由显式「搜索」/回车触发，避免每次按键打 npx）。
  renderOnlineSection(root, all, enabledSel);
}

// installedSkillsShNames returns lowercased skill names present in the skills.sh
// canonical (~/.agents/skills), used to dedup online results against what's
// already installed.
function installedSkillsShNames() {
  const s = new Set();
  (state.skillsShSkills || []).forEach((sk) => s.add((sk.linkName || sk.logicalName || "").toLowerCase()));
  return s;
}

// runOnlineSearch is the ONLY place that fetches online results (explicit trigger,
// never per-keystroke — KTD6). Bumps gen so a slow earlier response can't clobber
// a newer one.
async function runOnlineSearch() {
  const term = state.search.trim();
  const o = state.skillsShOnline;
  if (!term) { o.term = ""; o.results = []; o.error = ""; o.loading = false; o.available = true; renderInventory(); return; }
  const gen = ++o.gen;
  o.term = term; o.loading = true; o.error = ""; o.available = true;
  renderInventory();
  try {
    const d = await api("GET", "/api/skillssh/find?q=" + encodeURIComponent(term));
    if (gen !== o.gen) return; // a newer search superseded this one
    o.available = d && d.available !== false;
    o.results = (d && d.results) || [];
    o.error = (d && d.error) || "";
  } catch (e) {
    if (gen !== o.gen) return;
    o.available = true; o.results = []; o.error = e.message;
  } finally {
    if (gen === o.gen) { o.loading = false; renderInventory(); }
  }
}

// renderOnlineSection renders state.skillsShOnline (render-only; no fetch). Dedup
// against local install/enable state drives which control each result shows.
function renderOnlineSection(root, allLocal, enabledSel) {
  const o = state.skillsShOnline;
  const head = ce("div", { className: "search-head online-head" });
  head.append(ce("span", { className: "group-title", textContent: "在线（skills.sh）" }));
  if (o.loading) head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "搜索中…" }));
  else if (o.term) head.append(ce("span", { className: "badge count", textContent: o.results.length + " skill" }));
  root.append(head);

  if (!o.available) { root.append(ce("div", { className: "empty", textContent: "在线搜索当前不可用（需联网，且本机要有 npx）。本地搜索不受影响。" })); return; }
  if (o.loading) { root.append(ce("div", { className: "empty", textContent: "正在向 skills.sh 查询…" })); return; }
  if (o.error) { root.append(ce("div", { className: "empty", style: "color:var(--err)", textContent: "在线搜索失败：" + o.error })); return; }
  if (!o.term) { root.append(ce("div", { className: "empty", textContent: "勾选了「包含线上」。输入关键词后点「在线搜索」或回车，查 skills.sh 上可安装的 skill。" })); return; }
  if (!o.results.length) { root.append(ce("div", { className: "empty", textContent: "skills.sh 上没有匹配「" + o.term + "」的 skill。" })); return; }

  const installed = installedSkillsShNames();
  const body = ce("div", { className: "inv-group-body" });
  o.results.forEach((r) => {
    const nm = (r.skill || "").toLowerCase();
    const isInstalled = installed.has(nm);
    const isEnabled = enabledSel.has(AGENTS_NS + "/" + (r.skill || ""));
    const card = ce("div", { className: "skill inv online" });
    const main = ce("div", { className: "skill-main" });
    const r1 = ce("div", { className: "skill-row1" });
    r1.append(ce("span", { className: "skill-name", textContent: r.skill || r.pkg }));
    if (r.installsRaw) r1.append(ce("span", { className: "install-count", title: "安装数", textContent: "↓ " + r.installsRaw }));
    r1.append(ce("span", { className: "src-badge src-skillssh", textContent: r.owner ? r.owner + "/" + r.repo : "skills.sh" }));
    r1.append(ce("span", { className: "group-spacer" }));
    r1.append(onlineAction(r, isInstalled, isEnabled));
    main.append(r1);
    main.append(ce("div", { className: "skill-desc online-sub", textContent: r.pkg }));
    if (r.url) {
      // 「↗ skills.sh」钉在卡片左下角（footer + margin-top:auto，同行卡片对齐）。
      const foot = ce("div", { className: "online-foot" });
      foot.append(ce("a", { href: r.url, target: "_blank", rel: "noopener", textContent: "↗ skills.sh", className: "online-link" }));
      main.append(foot);
    }
    card.append(main);
    body.append(card);
  });
  root.append(body);
}

// onlineAction returns the right control for one online result: an install button
// when not installed, or a status label when already installed (distinguishing
// installed-but-not-enabled from installed-and-enabled).
function onlineAction(r, isInstalled, isEnabled) {
  if (isInstalled && isEnabled) return ce("span", { className: "src-badge src-ok", textContent: "已安装且已启用" });
  if (isInstalled) return ce("span", { className: "muted online-installed", title: "已装到 skills.sh，可在本地搜索结果里启用到当前目录", textContent: "已安装（未启用）" });
  const btn = ce("button", { className: "small", textContent: "安装" });
  if (installingPkg) btn.disabled = true; // 并发锁：有安装在跑时其它按钮禁用
  btn.onclick = () => installOnline(r, btn);
  return btn;
}

// installOnline runs the two-layer install:
//   1. npx skills add -g -y -a universal  → install ONLY to canonical (~/.agents/skills)
//   2. SkillManage's own linker            → symlink (enable) into the CURRENT tab dir
// Layer 2 is SkillManage's capability, not npx's: the link is manifest-owned and
// can be 停用 like any other. So "install where you are" at least links to the
// directory you're looking at. Button state machine: idle→安装中→已安装 / fail(复原)。
async function installOnline(r, btn) {
  if (installingPkg) return;
  const target = currentTarget();
  installingPkg = r.pkg;
  btn.disabled = true; btn.textContent = "安装中…";
  try {
    const d = await api("POST", "/api/skillssh/add", { pkg: r.pkg });
    if (!(d && d.ok)) throw new Error((d && (d.error || d.stderr)) || "未知错误");
    // Installed to canonical. Now enable into the current directory via our linker.
    let enabled = false, enableErr = "";
    if (target) {
      try {
        await api("POST", "/api/enabled", { skill: AGENTS_NS + "/" + r.skill, target, mode: "snapshot" });
        await api("POST", "/api/apply");
        enabled = true;
      } catch (e) { enableErr = e.message; }
    }
    btn.textContent = "已安装"; // success：不复原，靠 load() 重渲成去重态
    installingPkg = null;
    if (enabled) toast("已安装 " + (r.skill || r.pkg) + " 并启用到 " + targetLabel(target));
    else if (target) banner("已安装 " + (r.skill || r.pkg) + "，但启用到当前目录失败：" + enableErr, true);
    else toast("已安装 " + (r.skill || r.pkg) + " 到 skills.sh（未选目录，未启用）");
    await load(); // 刷新 skills.sh + 现状 + 状态，去重/启用态生效
  } catch (e) {
    installingPkg = null;
    btn.disabled = false; btn.textContent = "安装"; // fail：复原可重试
    banner("安装 " + (r.skill || r.pkg) + " 失败：" + e.message, true);
  }
}

function renderInventory() {
  renderStats(); // keep the header「收录 skills」count in sync on every re-render
  const root = $("#skills"); root.innerHTML = "";
  if (state.targets.length === 0) {
    root.append(ce("div", { className: "empty", textContent: "还没有同步目录。点右上角「+」添加一个，再回到这里查看现状。" }));
    return;
  }
  if (state.invLoading) { root.append(ce("div", { className: "empty", textContent: "正在扫描…" })); return; }
  if (state.invError) {
    const e = ce("div", { className: "empty", style: "color:var(--err)" });
    e.append(ce("span", { textContent: "扫描失败：" + state.invError }));
    const retry = ce("button", { className: "small", textContent: "重试", style: "margin-left:10px" });
    retry.onclick = fetchInventory;
    e.append(retry);
    root.append(e);
    return;
  }
  const term = state.search.trim().toLowerCase();
  // A search term switches to GLOBAL search across all sources (not just this
  // directory) with real-time enable; empty term shows the current directory.
  if (term) { renderSearchResults(root, term); return; }
  let items = state.inventory.slice();
  // 注入当前 tab 所属 harness 的插件 skill（只读），作为「目录现状」底部「插件」组。
  const curHarness = (state.targets.find((t) => t.dir === currentTarget()) || {}).harness;
  (state.pluginSkills || []).forEach((p) => {
    if (curHarness && p.harness && p.harness !== curHarness) return;
    items.push({ kind: "plugin", name: p.name, description: p.description, plugin: p.plugin, version: p.version, harness: p.harness });
  });
  if (items.length === 0) {
    const box = ce("div", { className: "empty" });
    box.append(ce("div", { textContent: "该目录暂无 skill。" }));
    box.append(ce("div", { className: "muted", style: "margin-top:6px", textContent: "用上方搜索框跨所有源查找并启用，或在目录里创建 SKILL.md 后刷新。" }));
    root.append(box);
    return;
  }
  // Group by source (R3.2 / 用户要求「按仓库分类」): 本地 → 各 git 仓 → skills.sh →
  // 插件 → 未备份 → 未知软链.
  const groups = new Map();
  items.forEach((i) => {
    const g = groupOf(i);
    if (!groups.has(g.key)) groups.set(g.key, { key: g.key, title: g.title, order: g.order, help: g.help, items: [] });
    groups.get(g.key).items.push(i);
  });
  const ordered = [...groups.values()].sort((a, b) => a.order - b.order || a.title.localeCompare(b.title));
  // 手风琴：每次只展开一个组。openGroup===undefined 时默认展开第一个；===null 时全部收起。
  // 若当前展开的组在本次筛选后已不存在，回落到默认（展开第一个）。
  let open = state.openGroup;
  if (open === undefined || (open !== null && !ordered.some((g) => g.key === open))) {
    open = ordered.length ? ordered[0].key : null;
    state.openGroup = open;
  }
  ordered.forEach((g) => {
    const collapsed = g.key !== open;
    const grp = ce("div", { className: "inv-group" + (collapsed ? " collapsed" : "") });
    const head = ce("div", { className: "inv-group-head", title: collapsed ? "展开" : "收起" });
    head.append(ce("span", { className: "group-title", textContent: g.title }));
    if (g.help) {
      const q = ce("span", { className: "group-help", textContent: "?", title: "点击查看说明" });
      q.onclick = (e) => { e.stopPropagation(); infoModal(g.help.title, g.help.paras); }; // 点 ? 弹窗说明，不触发折叠
      head.append(q);
    }
    head.append(ce("span", { className: "badge count", textContent: g.items.length + " skill" }));
    // 插件组：在标题后说明「无法检测更新」，与 skills.sh 的组说明同位置。
    if (g.key === "plugin" && state.status && state.status.claudeCli) {
      head.append(ce("span", { className: "group-note", textContent: "无法检测是否有更新，按需手动委托更新（第一行）" }));
    }
    // skills.sh 组：它归 npx skills 自己的台账管理，本工具只读、不主动联网比对，
    // 因此无法显示「是否有更新」。在组头给出说明 + 一个手动「更新」入口（代调 npx）。
    if (g.key === "skillssh") {
      head.append(ce("span", {
        className: "group-note",
        textContent: "由 skills.sh 自管、无更新检查接口，无法获取实时更新状态，请在卡片里手动更新",
        title: "skills.sh(npx skills) 不提供「检查是否有更新」的接口，本工具又不接管它，所以无法像 git 源那样预判更新。更新请在各 skill 卡片上点「更新」（代调 npx skills update），每日定时更新也会自动刷新它。",
      }));
    }
    // 右侧上下箭头：展开时朝上（点击收起），收起时朝下（点击展开）。
    head.append(ce("span", { className: "group-chevron", textContent: collapsed ? "▾" : "▴" }));
    head.onclick = () => {
      // 点已展开的组 → 全部收起；点其它组 → 只展开它（手风琴）。
      state.openGroup = collapsed ? g.key : null;
      renderInventory();
    };
    grp.append(head);
    if (!collapsed) {
      const body = ce("div", { className: "inv-group-body" });
      if (g.key === "plugin") body.append(pluginToolbar(g.items)); // 第一行：逐插件更新 + 全部更新
      g.items.forEach((i) => body.append(inventoryCard(i)));
      grp.append(body);
    }
    root.append(grp);
  });
}

// repoFromUrl derives "owner/repo" from a git URL (skills.sh sourceUrl), so a
// skills.sh skill is grouped under the repo it came from — it's a 库 too, just
// installed by a different tool.
function repoFromUrl(u) {
  if (!u) return "";
  let p = u.replace(/^[a-z]+:\/\//i, "").replace(/\.git$/i, "");
  const slash = p.indexOf("/");
  if (slash >= 0) p = p.slice(slash + 1); // drop host
  return p;
}

// SOURCE_ORDER is the single global source ordering used by EVERY surface
// (inventory groups, the「+ 添加」drawer, the sidebar) so sources always list the
// same way: 未知软链(unknown) → git → npx skills(skills.sh) → 本地未备份(handwritten)
// → 本地(local) → plugins。未知排最上（异常项最该先看到）。改这一处即可全站统一。
const SOURCE_ORDER = { unknown: 1, git: 2, "skills.sh": 3, handwritten: 4, local: 5, dir: 6, plugin: 7 };

// dirLabel resolves a local directory-source id to its display label (falls back
// to the id when the source list hasn't loaded yet).
function dirLabel(id) {
  const d = (state.dirSources || []).find((x) => x.id === id);
  return d ? d.label : id;
}

// groupOf maps an inventory item to its source group (title + sort order from SOURCE_ORDER).
function groupOf(i) {
  switch (i.kind) {
    case "git": return { key: "git:" + (i.repo || ""), title: i.repo || "git 仓", order: SOURCE_ORDER.git };
    case "skills.sh":
      // 全部归到一个「skills.sh」组——它们都归 npx skills 管，不按来源仓拆分。
      // 具体来源仓（owner/repo）显示在每张卡片的徽章上。
      return { key: "skillssh", title: "skills.sh", order: SOURCE_ORDER["skills.sh"] };
    // 本地（受管存储）与各本地目录源在「使用」时合并为一个「本地源」类，不按目录拆组。
    case "local":
    case "dir": return { key: "local", title: "本地源", order: SOURCE_ORDER.local };
    case "plugin": return { key: "plugin", title: "插件（plugins）", order: SOURCE_ORDER.plugin };
    case "handwritten": return { key: "hand", title: "未备份（可备份）", order: SOURCE_ORDER.handwritten, help: {
      title: "「备份」是做什么的？",
      paras: [
        { text: "这一组是直接在该目录里手写的 skill——真身文件就放在当前目录里，没有副本。它不在 SkillManage 的受管范围，也不跟随自动更新；一旦误删目录或机器故障，就彻底丢了。" },
        { h: "点卡片上的「备份」会：", text: "① 把真身目录移动到受管存储 ~/.skillmanage/local/<名字>；② 在原位置建一个软链指回去，所以各 harness 看到的路径和内容完全不变，照常生效；③ 从此它归 SkillManage 管理，纳入统一的启用/停用与同步。" },
        { h: "影响：", text: "原目录会从「真实文件」变成「软链」，内容不变、不会中断使用。备份后它会从「未备份」移到「本地（已备份）」分组。这一步只搬运、不删除、不联网，可随时再停用拆掉软链。" },
      ],
    } };
    default: return { key: "unknown", title: "未知软链", order: SOURCE_ORDER.unknown };
  }
}

function inventoryCard(i) {
  const s = SRC[i.kind] || SRC.unknown;
  const row = ce("div", { className: "skill inv clickable " + s.cls, title: "查看详情" });
  // openDetailFor opens this skill's SKILL.md the right way for its kind
  // (managed → source repo; plugin → plugin tree; else → physical path).
  const openDetailFor = () =>
    i.managed && i.selector ? openDetail(i.selector.split("/")[0], i.name)
    : i.kind === "plugin" ? openPluginDetail(i.plugin, i.name, i.harness)
    : openDetailAt(i.name);
  // Clicking the card === 详情; clicks on the action buttons are excluded.
  row.onclick = (e) => { if (e.target.closest("button, a")) return; openDetailFor(); };
  const main = ce("div", { className: "skill-main" });
  const r1 = ce("div", { className: "skill-row1" });
  r1.append(ce("span", { className: "skill-name", textContent: i.name }));
  { const vt = verTag(i.version); if (vt) r1.append(vt); }

  // 徽章默认显示全名——位置够就不缩写（CSS 在真正放不下时才 ellipsis 截断，全名见 hover）。
  let badgeText = s.label;
  if (i.kind === "skills.sh" && i.sourceUrl) badgeText = repoFromUrl(i.sourceUrl) || hostOf(i.sourceUrl);
  if (i.kind === "plugin" && i.plugin) badgeText = i.plugin;
  // 本地源合并成一个组后，徽章带上各自来源：local（受管存储）或某个本地源文件夹名。
  if (i.kind === "local") badgeText = "local";
  if (i.kind === "dir") badgeText = dirLabel(i.repo);
  // 缩写只针对 plugin（名字常含连字符且偏长，如 compound-engineering → ce）；npx /
  // git / 本地源标签本身不长，保持全名。全名见 hover（plugin 的 title 在下面设置）。
  const badge = ce("span", { className: "src-badge " + s.cls, textContent: i.kind === "plugin" ? abbrevLabel(badgeText) : badgeText });
  if (i.kind === "skills.sh" && i.sourceUrl) badge.title = i.sourceUrl; // title is text-safe (no innerHTML)
  if (i.kind === "plugin" && i.plugin) badge.title = i.plugin;
  if (i.kind === "dir") badge.title = dirLabel(i.repo) + "（本地源）";
  r1.append(badge);

  if (i.collision) {
    const c = ce("span", { className: "src-badge src-shadow", textContent: "遮蔽" });
    c.title = "同名 skill 在全局与项目目录下各有一份软链，互相遮蔽（项目级生效）。若由 skills.sh/外部工具安装则只读，请用其原生方式或手动移除其一。";
    r1.append(c);
  }
  // 状态标签统一作为左侧 src-badge（如插件的「只读」），而不是右侧散文字/单独样式：
  // 右侧只放动作（详情 / 停用 / 更新 / 删除）。
  if (i.kind === "plugin") {
    const ro = ce("span", { className: "src-badge src-readonly", textContent: "只读" });
    ro.title = "由 harness 插件系统管理，SkillManage 只读不接管";
    r1.append(ro);
  }

  r1.append(ce("span", { className: "group-spacer" }));

  // actions (right side) — 受管优先（停用），再按来源给专属动作；「详情」恒在最右。
  if (i.managed && i.selector && !i.follow) {
    const off = ce("button", { className: "danger small inv-off", textContent: "停用", title: "拆除此目录下的软链（不影响真身与其它目录）" });
    off.onclick = () => disableSkill(i, off);
    r1.append(off);
  } else if (i.kind === "handwritten") {
    const ad = ce("button", { className: "ghost small", textContent: "备份", title: "移入受管存储并原位软链，纳入自动更新（未备份 → 已备份）" });
    ad.onclick = () => adoptHandwritten(i, ad);
    r1.append(ad);
    const del = ce("button", { className: "danger small", textContent: "删除", title: "永久删除该手写 skill 的真身目录（不可恢复）" });
    del.onclick = () => deleteHandwritten(i, del);
    r1.append(del);
  } else if (i.kind === "skills.sh") {
    if (state.npxAvailable) {
      const u = ce("button", { className: "ghost small", textContent: "更新", title: "调用 npx skills update 更新（由 skills.sh 管理）" });
      u.onclick = () => updateSkillSh(i.name, u);
      r1.append(u);
    }
    const del = ce("button", { className: "danger small", textContent: "删除", title: "只删此目录下的软链，不动 ~/.agents 里的真身（skills.sh 下次 update 可能重新投影）" });
    del.onclick = () => deleteSkillsShLink(i, del);
    r1.append(del);
  } else if (i.kind === "unknown") {
    const del = ce("button", { className: "danger small", textContent: "删除", title: "只删此软链，不动它指向的目标" });
    del.onclick = () => deleteStrayLink(i, del);
    r1.append(del);
  }
  // 详情（最右）：受管走源仓，插件走插件树，其余按目录真实路径读 SKILL.md。
  const d = ce("button", { className: "skill-detail-btn", textContent: "详情" });
  d.onclick = openDetailFor;
  r1.append(d);

  main.append(r1);
  // 反查：show where an unknown stray symlink points.
  if (i.kind === "unknown" && i.linkTarget) main.append(ce("div", { className: "inv-linktarget", title: i.linkTarget, textContent: "→ " + i.linkTarget }));
  if (i.description) main.append(ce("div", { className: "skill-desc", textContent: i.description }));
  row.append(main);
  return row;
}

// deleteStrayLink removes a single unknown/stray symlink (the symlink only, never
// its target). Explicit, confirmed — the user-initiated exception to never-break.
async function deleteStrayLink(i, btn) {
  const where = i.linkTarget ? "（指向 " + i.linkTarget + "）" : "";
  if (!(await confirmModal("删除软链 " + i.name + " " + where + "？\n只删这条软链本身，不会动它指向的目标，也不影响其它目录。", "删除软链", true))) return;
  btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/inventory/link", { target: currentTarget(), name: i.name });
    toast("已删除软链 " + i.name);
    await load(true); // 移除占用项后 reconcile：补建之前被挡住的链，并刷新 footer
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除";
    banner("删除软链 " + i.name + " 失败：" + e.message, true);
  }
}

// deleteSkillsShLink removes a skills.sh projection (the symlink in this target
// only) — never the canonical install under ~/.agents/skills. Same backend guard
// as a stray link (refuses real dirs and our own managed links). skills.sh may
// re-project it on its next update, so we say so up front.
async function deleteSkillsShLink(i, btn) {
  if (!(await confirmModal(
    "删除此目录下的软链「" + i.name + "」？\n\n只删这一处的软链，不动 ~/.agents/skills 里的真身，也不影响其它目录。\n注意：skills.sh 下次 update 可能会把它重新投影回来——彻底移除请用 skills.sh 自己的命令。",
    "删除软链", true))) return;
  btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/inventory/link", { target: currentTarget(), name: i.name });
    toast("已删除软链 " + i.name);
    await load(true); // 移除占用项后 reconcile：补建之前被挡住的链，并刷新 footer
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除";
    banner("删除软链 " + i.name + " 失败：" + e.message, true);
  }
}

async function deleteHandwritten(i, btn) {
  if (!(await confirmModal(
    "永久删除手写 skill「" + i.name + "」的真身目录？\n\n它没有备份，删除后无法恢复。若只是想移出当前目录但保留，请改用「备份」。",
    "永久删除", true))) return;
  btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/inventory/handwritten", { target: currentTarget(), name: i.name });
    toast("已删除 " + i.name);
    await fetchInventory();
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除";
    banner("删除 " + i.name + " 失败：" + e.message, true);
  }
}

function summaryToast(sum, name, enable) {
  if (enable) {
    const made = (sum && sum.created || []).find((x) => x.name === name);
    toast(made ? "已在 " + targetLabel(currentTarget()) + " 建立软链 " + name : "已启用 " + name);
  } else {
    toast("已移除软链 " + name);
  }
}

// disableSkill tears down a managed skill's link in the current target. Enabling
// happens in the "+ 添加" drawer, so inventory only ever disables.
async function disableSkill(i, btn) {
  btn.disabled = true; btn.textContent = "停用中…";
  try {
    await api("DELETE", "/api/enabled", { skill: i.selector, target: currentTarget() });
    const sum = await api("POST", "/api/apply"); // returns reconcile Summary
    summaryToast(sum, i.name, false);
    await load();
  } catch (e) {
    btn.disabled = false; btn.textContent = "停用";
    banner("停用 " + i.name + " 失败：" + e.message, true);
  }
}

async function adoptHandwritten(i, btn) {
  if (!(await confirmModal("将 " + i.name + " 备份进受管存储（~/.skillmanage/local）并在原位建软链？\n此操作会移动原目录。", "备份"))) return;
  btn.disabled = true; btn.textContent = "备份中…";
  try {
    await doAdopt({ id: i.name, root: currentTarget() });
    toast("已备份 " + i.name + "（原位已软链）");
    await load();
  } catch (e) {
    btn.disabled = false; btn.textContent = "备份";
    banner("备份 " + i.name + " 失败：" + (ADOPT_ERR[e.code] || e.message), true);
  }
}

async function updateSkillSh(name, btn) {
  const old = btn.textContent;
  btn.disabled = true; btn.textContent = "更新中…";
  try {
    const r = await fetch("/api/dirsource/update", {
      method: "POST",
      headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || r.statusText);
    if (d.ok) toast("已更新 " + name);
    else banner("更新 " + name + " 失败：" + (d.stderr || d.error || "未知错误"), true);
  } catch (e) {
    banner("更新 " + name + " 失败：" + e.message, true);
  }
  btn.disabled = false; btn.textContent = old;
  await fetchInventory();
}

// doAdopt posts to /api/adopt and surfaces error_code for specific messaging.
async function doAdopt(a) {
  const body = a.plugin
    ? { id: a.id, src: a.dir, target: currentTarget(), plugin: true }
    : { id: a.id, root: a.root };
  const r = await fetch("/api/adopt", {
    method: "POST",
    headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) { const e = new Error(data.error || r.statusText); e.code = data.error_code; throw e; }
  return data;
}

// --- enable / 自动同步 helpers (used by the per-source skill modals) ---

// shadowTargetForEnable returns the dir of an already-enabled same-name skill in
// another target of the SAME harness — i.e. enabling linkName into `target` now
// would create a global↔project shadow (project wins, the other becomes dead).
// Returns null when no shadow would result. Only considers OUR managed enables.
function shadowTargetForEnable(linkName, target) {
  const tgt = state.targets.find((t) => t.dir === target);
  const h = tgt && tgt.harness;
  if (!h) return null;
  for (const e of state.status.enabled || []) {
    if (e.target === target) continue;
    const ot = state.targets.find((t) => t.dir === e.target);
    if (!ot || ot.harness !== h) continue;
    const nm = String(e.skill).split("/").pop();
    if (nm !== "*" && nm === linkName) return e.target;
  }
  return null;
}

// confirmShadowEnable warns before linking when the enable would shadow a same-name
// skill already enabled in another same-harness directory (Q2: confirm before linking).
async function confirmShadowEnable(linkName, target) {
  const other = shadowTargetForEnable(linkName, target);
  if (!other) return true;
  return confirmModal(
    "「" + linkName + "」已在 " + targetLabel(other) + " 启用。\n" +
      "再在 " + targetLabel(target) + " 启用会形成同名遮蔽：同一 harness 下只有项目级目录里的那份生效，另一份冗余。\n仍要继续？",
    "仍然启用"
  );
}

function renderSummary() {
  const f = $("#summary"); f.innerHTML = "";
  const s = state.status.lastSummary;
  if (!s) { f.textContent = "尚未运行同步。"; return; }
  const parts = [];
  if (s.created && s.created.length) parts.push("新增 " + s.created.length);
  if (s.removed && s.removed.length) parts.push("移除 " + s.removed.length);
  if (s.pruned && s.pruned.length) parts.push("清理悬空 " + s.pruned.length);
  f.append(ce("span", { textContent: parts.length ? "上次同步：" + parts.join("，") : "上次同步：无变化" }));
  // 不再在页脚堆砌每条冲突/错误（无价值的一墙告警）。只在有问题时显示一个可点击的
  // 「健康度」入口，详情与解决都在弹窗里完成。
  const issues = ((s.conflicts || []).length) + ((s.errors || []).length);
  if (issues) {
    const h = ce("span", { className: "health-warn", textContent: "⚠ 健康度 " + issues + " 项待处理", title: "撞名 / 遮蔽 / 嵌套 / 同步错误——点击查看并解决" });
    h.onclick = openHealthModal;
    f.append(h);
  }
}

// humanizeSyncError turns a raw reconcile error into a clear cause + suggestion.
// The raw string stays available via the row's title (hover) for debugging.
function humanizeSyncError(raw) {
  const r = String(raw || "");
  // link <target>/<name> -> <source>: <inner>
  const m = r.match(/^link\s+(.+?)\s+->\s+(.+?):\s+([\s\S]+)$/);
  if (m) {
    const name = m[1].split("/").pop();
    const inner = m[3];
    if (/non-owned path|foreign link|occupied/.test(inner)) {
      return {
        why: `跳过链接「${name}」：目标位置已被一个非本工具创建的文件/链接占用，按「绝不覆盖真身」原则未做改动。`,
        fix: `若要让 SkillManage 接管，请先手动移除该位置的占用项；或为本工具的 skill 起一个别名避开撞名。`,
      };
    }
    if (/real path|diverged/.test(inner)) {
      return {
        why: `跳过链接「${name}」：原本的软链位置已变成真实文件/目录（可能被其它工具替换）。`,
        fix: `确认该内容是否还需要；如需重新链接，请先移除它再同步。`,
      };
    }
    return { why: `链接「${name}」失败。`, fix: `查看本行提示（悬停可见原始信息）后重试。` };
  }
  // unlink <path>: <inner>
  const u = r.match(/^unlink\s+(.+?):\s+([\s\S]+)$/);
  if (u) {
    const name = u[1].split("/").pop();
    return { why: `移除软链「${name}」失败。`, fix: `检查文件权限或它是否被其它程序占用。` };
  }
  if (/^prune:/.test(r)) {
    return { why: `清理悬空链接时出错。`, fix: `稍后重试；若反复出现，检查目标目录权限。` };
  }
  return { why: r, fix: "" };
}

// setDetailSource shows the skills.sh lockfile source (台账来源) in the detail
// modal, or hides the line for skills that have none (git / local / plugin).
function setDetailSource(d) {
  const el = $("#modal-source");
  el.innerHTML = "";
  if (d && d.source) {
    el.append(ce("span", { className: "ms-label", textContent: "台账来源" }));
    el.append(ce("span", { className: "ms-val", textContent: d.source }));
    if (d.sourceUrl) {
      el.append(ce("span", { className: "ms-sep", textContent: "·" }));
      el.append(ce("span", { className: "ms-url", textContent: d.sourceUrl }));
    }
    el.title = d.sourceUrl || d.source;
    el.classList.remove("hidden");
  } else {
    el.classList.add("hidden");
  }
}

// setPluginActions fills the detail modal's action row for a plugin skill: a
// delegated「更新插件」button (only when the claude CLI is available, cc only)
// plus a note. Hidden / cleared for non-plugin skills.
function setPluginActions(plugin, harness) {
  const el = $("#modal-actions");
  el.innerHTML = "";
  if (!plugin) { el.classList.add("hidden"); return; }
  // 更新入口统一放在「插件」组第一行的工具条（只对确有更新的插件显示），详情里不再放
  // 按钮，避免对无法检测/无法更新的插件给出无效操作。
  el.append(ce("span", { className: "ma-note", textContent: "只读 · 由 harness 插件系统管理。有更新时在「插件」组第一行委托更新。" }));
  el.classList.remove("hidden");
}
function clearModalActions() { const el = $("#modal-actions"); el.innerHTML = ""; el.classList.add("hidden"); }

// updatePlugin delegates a plugin update to the harness CLI (claude plugin update
// <id> -s <scope>). It never takes ownership; effect applies after a Claude Code
// restart. `t` is {name,id,scope} from the outdated check — the exact id + scope
// the CLI needs (a bare name / wrong scope makes it report "not found").
async function updatePlugin(t, btn) {
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  try {
    const d = await api("POST", "/api/plugin/update", { id: t.id, scope: t.scope, harness: "cc" });
    if (d && d.ok) toast(d.status === "current" ? t.name + " 已是最新版本，无需更新" : "已更新 " + t.name + "，重启 Claude Code 后生效");
    else banner("更新插件 " + t.name + " 失败：" + ((d && (d.stderr || d.error)) || "未知错误"), true);
  } catch (e) {
    banner("更新插件 " + t.name + " 失败：" + e.message, true);
  } finally {
    btn.disabled = false; btn.textContent = old;
  }
}

// pluginToolbar is the first row of the「插件」组: one「更新」button per installed
// plugin in this group + 「全部更新」right-aligned. This is a MANUAL model (like
// skills.sh): the CLI can't tell us which plugins have updates (no comparable
// marketplace version), so we don't claim — we just offer to委托 update each, with
// a note. id + scope come from `claude plugin list`.
function pluginToolbar(items) {
  const bar = ce("div", { className: "plugin-toolbar" });
  if (!(state.status && state.status.claudeCli)) {
    bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "未找到 claude CLI，无法在此更新插件（可用 /plugin 更新）" }));
    return bar;
  }
  const pi = state.pluginInstalled;
  if (pi === null) { fetchPluginInstalled(); bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "加载插件信息中…" })); return bar; }
  if (!pi.available) { bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "无法获取插件信息，暂不提供委托更新" })); return bar; }
  const names = new Set(items.filter((x) => x.harness === "cc" && x.plugin).map((x) => x.plugin));
  const targets = (pi.list || []).filter((t) => names.has(t.name));
  if (!targets.length) { bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "本组无可委托更新的 Claude Code 插件" })); return bar; }
  // 逐插件按钮放在一个会换行的容器里（占满左侧），「全部更新」固定在最右、不随换行下移。
  const btns = ce("div", { className: "plugin-toolbar-btns" });
  targets.forEach((t) => {
    const b = ce("button", { className: "ghost small", textContent: "更新 " + abbrevLabel(t.name), title: "委托 claude plugin update 更新「" + t.name + "」插件，重启后生效" });
    b.onclick = () => updatePlugin(t, b);
    btns.append(b);
  });
  bar.append(btns);
  const allBtn = ce("button", { className: "small", textContent: "全部更新", title: "逐个委托更新这 " + targets.length + " 个插件" });
  allBtn.onclick = () => updateAllPlugins(targets, allBtn);
  bar.append(allBtn);
  return bar;
}

async function fetchPluginInstalled() {
  if (state.pluginInstalledLoading) return;
  state.pluginInstalledLoading = true;
  try {
    const d = await api("GET", "/api/plugins/installed");
    state.pluginInstalled = d && d.available ? { available: true, list: d.plugins || [] } : { available: false, list: [] };
  } catch { state.pluginInstalled = { available: false, list: [] }; }
  finally { state.pluginInstalledLoading = false; renderInventory(); }
}

async function updateAllPlugins(targets, btn) {
  if (!targets || !targets.length) return;
  const names = targets.map((t) => t.name);
  if (!(await confirmModal("将逐个委托 claude plugin update 更新 " + targets.length + " 个插件：\n" + names.join("、") + "\n更新后重启 Claude Code 生效。继续？", "全部更新"))) return;
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  let updated = 0, current = 0; const fail = [];
  for (const t of targets) {
    try { const d = await api("POST", "/api/plugin/update", { id: t.id, scope: t.scope, harness: "cc" }); if (d && d.ok) { if (d.status === "current") current++; else updated++; } else fail.push(t.name); }
    catch { fail.push(t.name); }
  }
  btn.disabled = false; btn.textContent = old;
  if (fail.length) banner("插件更新：更新 " + updated + " 个，已最新 " + current + " 个，失败 " + fail.length + "（" + fail.join("、") + "）", true);
  else if (updated) toast("已更新 " + updated + " 个插件（另 " + current + " 个已最新），重启 Claude Code 后生效");
  else toast(current + " 个插件均已是最新，无需更新");
}

async function openDetail(repo, name) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
  setDetailSource(null);
  clearModalActions();
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/skill?repo=" + encodeURIComponent(repo) + "&name=" + encodeURIComponent(name));
    $("#modal-desc").textContent = d.description || "";
    setDetailSource(d);
    $("#modal-content").textContent = d.content || "(空)";
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

// openPluginDetail reads a plugin skill's SKILL.md from its install tree.
async function openPluginDetail(plugin, name, harness) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
  setDetailSource(null);
  setPluginActions(plugin, harness);
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/plugin-skill?plugin=" + encodeURIComponent(plugin) + "&name=" + encodeURIComponent(name) + "&harness=" + encodeURIComponent(harness || ""));
    $("#modal-desc").textContent = d.description || "";
    $("#modal-content").textContent = d.content || "(空)";
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

// openDetailAt reads a skill's SKILL.md by its physical location in the current
// target dir — used for non-managed inventory kinds (skills.sh / 未备份 / 未知)
// that have no source-repo selector.
async function openDetailAt(name) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
  setDetailSource(null);
  clearModalActions();
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/skill-at?target=" + encodeURIComponent(currentTarget()) + "&name=" + encodeURIComponent(name));
    $("#modal-desc").textContent = d.description || "";
    setDetailSource(d);
    $("#modal-content").textContent = d.content || "(空)";
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

// updateNow is the GIT update-all action (git 仓 group's「全量更新」). npx/skills.sh
// has its own update (updateSkillsShAll) — one git path, one npx path.
async function updateNow(force) {
  toast("同步中…", "info");
  let sum = null;
  try { sum = await api("POST", "/api/update-now", { force: !!force }); }
  catch (e) { banner("同步失败：" + e.message, true); await load(); return; }
  await load();
  // Dirty mirrors were skipped (force=false) → offer to restore + update them
  // (this replaced the old separate 强制更新 button). Skip when already forcing.
  if (!force && sum && sum.dirtyRepos && sum.dirtyRepos.length) {
    if (await confirmModal(
      "以下镜像有本地改动，已跳过未更新：\n\n" + sum.dirtyRepos.join("、") +
      "\n\n镜像是只读副本，正常不该有本地改动。是否还原本地改动并更新（git reset --hard + clean -fd）？",
      "还原并更新", true)) {
      await updateNow(true);
      return;
    }
  }
  const changed = sum && ((sum.created || []).length + (sum.removed || []).length + (sum.pruned || []).length);
  if (changed) toast("更新完成：新增 " + (sum.created || []).length + " · 移除 " + (sum.removed || []).length + (sum.pruned && sum.pruned.length ? " · 清理 " + sum.pruned.length : ""));
  else toast("git 源已是最新，无变化");
}

// events
$("#search").oninput = (e) => { state.search = e.target.value; renderInventory(); };
// Local search is real-time on input. The「搜索」button (and Enter) additionally
// fire the skills.sh online query — always, no opt-in (KTD6: online is explicit,
// never per-keystroke, because npx cold-start is slow).
$("#search").addEventListener("keydown", (e) => { if (e.key === "Enter") runOnlineSearch(); });
$("#online-search-btn").onclick = runOnlineSearch;

// submitGitRepo handles the git-source add form. The HTTPS credential is entered
// INLINE in the same modal (no second popup): if the URL is an https host we have
// no stored cred for and the user typed a token, save it first so the very first
// sync authenticates. A host we already have a cred for is reused silently. Empty
// token = treat as public — the per-repo「填写凭据」button remains the fallback if
// it turns out private (addRepoAndSync surfaces authHint).
async function submitGitRepo(url, branch) {
  if (!url) return;
  const host = httpsHost(url);
  if (host && !Object.prototype.hasOwnProperty.call(state.credHosts, host)) {
    const token = $("#repo-cred-token").value;
    if (token) {
      const username = $("#repo-cred-user").value.trim();
      try { await api("POST", "/api/credentials", { host, username, token }); }
      catch (err) { banner("保存凭据失败：" + err.message, true); return; }
    }
  }
  await addRepoAndSync(url, branch);
}

// updateRepoCredSection reacts to the URL field: shows the inline credential
// inputs only for an https host without a stored cred, a reuse note when one
// already exists, and nothing for SSH/git@ (which authenticates via SSH key).
function updateRepoCredSection() {
  const host = httpsHost($("#repo-url").value.trim());
  const sec = $("#repo-cred-section"), fields = $("#repo-cred-fields"), status = $("#repo-cred-status");
  if (!host) { sec.classList.add("hidden"); return; }
  sec.classList.remove("hidden");
  if (Object.prototype.hasOwnProperty.call(state.credHosts, host)) {
    status.textContent = "将复用 " + host + " 的已存凭据，无需重复填写。";
    fields.classList.add("hidden");
  } else {
    status.textContent = host + "：私有仓在下面填令牌；公开仓留空直接添加。";
    fields.classList.remove("hidden");
  }
}

function openGitRepoModal() {
  $("#repo-url").value = "";
  $("#repo-branch").value = "";
  $("#repo-cred-user").value = "";
  $("#repo-cred-token").value = "";
  updateRepoCredSection();
  $("#git-repo-modal").classList.remove("hidden");
  $("#repo-url").focus();
}
function closeGitRepoModal() { $("#git-repo-modal").classList.add("hidden"); }

// --- 健康度 (health): 同步层冲突 撞名 / 遮蔽 / 嵌套 + 同步错误 ---
// 这些是「手动启用/自动同步」操作落到目标目录后才会出现的链接层问题，可在弹窗里
// 逐条停用解决。与顶部「冲突」（库内跨源重复/重叠，见下方）是两件事。
const CONFLICT_KIND = {
  collision: "撞名（多个源争用同一目录下的同名 skill，只能保留一份）",
  shadow: "遮蔽（同一 harness 的全局与项目目录都有同名，项目级生效、另一份冗余）",
  nested: "嵌套（链到 Codex 的源含子 skill，可能污染 Codex 列表）",
};
function badgeClassForLabel(label) {
  return label === "skills.sh" ? "src-skillssh" : label === "local" ? "src-local" : "src-git";
}
async function openHealthModal() {
  const body = $("#health-body");
  body.innerHTML = "加载中…";
  $("#health-modal").classList.remove("hidden");
  try { renderHealthBody(await api("GET", "/api/conflicts")); }
  catch (e) { body.innerHTML = ""; body.append(ce("div", { className: "empty", textContent: "加载失败：" + e.message })); }
}
function closeHealthModal() { $("#health-modal").classList.add("hidden"); }
function renderHealthBody(list) {
  const body = $("#health-body"); body.innerHTML = "";
  const errors = (state.status.lastSummary && state.status.lastSummary.errors) || [];
  list = list || [];
  if (!list.length && !errors.length) { body.append(ce("div", { className: "empty", textContent: "一切正常 🎉" })); return; }
  // 遮蔽冲突（父↔子成对）按【文件夹对】分组：每组是「父 ⇄ 一个子」的关系，框里列出
  // 这一对之间所有同名 skill，并给两个一键取舍按钮（保留父 / 保留子）。撞名（同目录
  // 多源）、嵌套不属于文件夹对，仍按单条处理。
  const shadowPairs = list.filter((c) => c.kind === "shadow" && c.candidates && new Set(c.candidates.map((x) => x.target)).size === 2);
  const others = list.filter((c) => !shadowPairs.includes(c));

  // group shadow conflicts by their unordered (父,子) target pair.
  const groups = new Map();
  shadowPairs.forEach((c) => {
    const ts = [...new Set(c.candidates.map((x) => x.target))].sort();
    const key = ts.join(" ");
    let g = groups.get(key);
    if (!g) {
      const meta = {};
      c.candidates.forEach((x) => { meta[x.target] = { label: x.targetLabel, scope: x.scope, follow: x.follow, source: x.sourceLabel }; });
      g = { targets: ts, meta, items: [] };
      groups.set(key, g);
    }
    g.items.push(c);
  });

  // roleOf maps a target's scope to a 父/子 label, or "" when scope is unknown
  // (defensive: a normal shadow pair is always one user + one project, but stale
  // or malformed data must not mislabel both sides — then we show paths only).
  const roleOf = (scope) => (scope === "user" ? "父" : scope === "project" ? "子" : "");
  for (const g of groups.values()) {
    // order父(user) first, 子(project) second; fall back to given order.
    const ordered = g.targets.slice().sort((a, b) => (g.meta[a].scope === "user" ? -1 : 1) - (g.meta[b].scope === "user" ? -1 : 1));
    const box = ce("div", { className: "cf-pair" });
    const head = ce("div", { className: "cf-pair-head" });
    ordered.forEach((t, i) => {
      if (i) head.append(ce("span", { className: "cf-pair-arrow", textContent: "⇄" }));
      const tag = ce("span", { className: "cf-pair-folder" });
      const role = roleOf(g.meta[t].scope);
      if (role) tag.append(ce("span", { className: "cf-pair-role", textContent: role }));
      tag.append(ce("span", { textContent: g.meta[t].label }));
      head.append(tag);
    });
    box.append(head);
    box.append(ce("div", { className: "cf-pair-sub", textContent: g.items.length + " 个同名 skill 在这两处重复（项目级生效，另一份冗余）" }));
    const btns = ce("div", { className: "cf-batch-btns" });
    ordered.forEach((t) => {
      const role = roleOf(g.meta[t].scope);
      const b = ce("button", { className: "small", textContent: "全部保留 " + (role ? role + "（" + g.meta[t].label + "）" : g.meta[t].label) });
      b.onclick = () => resolveHealthBatch(g.items, t);
      btns.append(b);
    });
    box.append(btns);
    const sk = ce("div", { className: "cf-pair-skills" });
    g.items.forEach((c) => {
      const row = ce("div", { className: "cf-pair-skill" });
      const src = (c.candidates[0] && c.candidates[0].sourceLabel) || "";
      row.append(ce("span", { className: "cf-skill-name", textContent: c.linkName }));
      if (src) row.append(ce("span", { className: "src-badge " + badgeClassForLabel(src), textContent: src }));
      if (g.meta[g.targets[0]].follow || g.meta[g.targets[1]].follow) row.append(ce("span", { className: "muted", style: "font-size:11px", textContent: "自动同步" }));
      sk.append(row);
    });
    box.append(sk);
    body.append(box);
  }

  // collision (撞名) / nested / anything not a folder-pair → per-item resolution.
  others.forEach((c) => {
    const sec = ce("div", { className: "cf-item" });
    const head = ce("div", { className: "cf-head" });
    head.append(ce("span", { className: "cf-name", textContent: c.linkName }));
    head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: CONFLICT_KIND[c.kind] || c.kind }));
    sec.append(head);
    if (!c.candidates || !c.candidates.length) {
      sec.append(ce("div", { className: "muted", style: "font-size:12px;margin-top:6px", textContent: "找不到可操作的来源条目（可能来自整仓自动同步，请到对应源弹窗里调整）。" }));
      body.append(sec); return;
    }
    const multi = c.candidates.length > 1;
    c.candidates.forEach((cand) => {
      const row = ce("div", { className: "cf-cand" });
      const info = ce("div", { className: "cf-cand-info" });
      info.append(ce("span", { className: "src-badge " + badgeClassForLabel(cand.sourceLabel), textContent: cand.sourceLabel }));
      info.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "→ " + cand.targetLabel + (cand.follow ? " · 自动同步" : "") }));
      row.append(info);
      const btn = ce("button", { className: multi ? "small" : "danger small", textContent: multi ? "保留这个" : "停用", title: multi ? "保留这一份，停用其余" : "停用这一份" });
      btn.onclick = () => resolveConflict(c, cand);
      row.append(btn);
      sec.append(row);
    });
    body.append(sec);
  });
}
async function resolveConflict(c, keep) {
  const drop = c.candidates.length > 1
    ? c.candidates.filter((x) => !(x.selector === keep.selector && x.target === keep.target))
    : [keep];
  const followDrop = drop.find((d) => d.follow);
  if (followDrop && !(await confirmModal(
    "「" + followDrop.sourceLabel + "」是整仓自动同步，停用它会移除该源在「" + followDrop.targetLabel + "」的全部 skill（不止这一个）。继续？",
    "继续停用", true))) return;
  try {
    for (const d of drop) await api("DELETE", "/api/enabled", { skill: d.selector, target: d.target });
    await api("POST", "/api/apply");
  } catch (e) { banner("解决冲突失败：" + e.message, true); return; }
  toast("已处理冲突 " + c.linkName);
  await load();
  try { renderHealthBody(await api("GET", "/api/conflicts")); }
  catch { closeHealthModal(); }
}
// resolveHealthBatch keeps one target folder across many cross-folder conflicts and
// drops the same-named candidates in every other folder in one pass.
async function resolveHealthBatch(batchable, keepTarget) {
  const keepLabel = (batchable[0].candidates.find((x) => x.target === keepTarget) || {}).targetLabel || keepTarget;
  const drop = [];
  batchable.forEach((c) => {
    if (!c.candidates.some((x) => x.target === keepTarget)) return; // 该冲突不含保留目录，跳过
    c.candidates.filter((x) => x.target !== keepTarget).forEach((d) => drop.push({ ...d, linkName: c.linkName }));
  });
  if (!drop.length) { toast("没有可批量停用的项"); return; }
  const follows = drop.filter((d) => d.follow);
  const followNote = follows.length ? "\n其中 " + follows.length + " 项来自整仓自动同步，停用会移除该源在对应目录的全部 skill。" : "";
  if (!(await confirmModal(
    "将保留「" + keepLabel + "」中的同名 skill，停用其它文件夹里的 " + drop.length + " 项。" + followNote + "\n继续？",
    "批量停用", true))) return;
  let ok = 0;
  try {
    for (const d of drop) { await api("DELETE", "/api/enabled", { skill: d.selector, target: d.target }); ok++; }
    await api("POST", "/api/apply");
  } catch (e) { banner("批量处理失败（已停用 " + ok + " 项）：" + e.message, true); }
  toast("已批量保留「" + keepLabel + "」，停用 " + ok + " 项");
  await load();
  try { renderHealthBody(await api("GET", "/api/conflicts")); }
  catch { closeHealthModal(); }
}

// --- 冲突 (conflict): 库内跨源「重复 / 重叠」检测 ---
// 针对全部数据源（git 源 + 本地源 + npx skills.sh）汇总后的 skill 库，纯前端启发式
// 识别两类问题：① 名称重复（同名 skill 来自 ≥2 个不同源）；② 关键词重叠（名称+描述
// 分词后 Jaccard ≥ 0.5 的不同名 skill 对，可能功能/提示词重叠）。这是「库里有没有冗余」
// 的体检，和「健康度」（链接层撞名/遮蔽）不同——这里只识别+指引，不自动改库（只读镜像）。
const OVERLAP_THRESHOLD = 0.5;
const STOPWORDS = new Set([
  "the", "a", "an", "and", "or", "of", "to", "for", "in", "on", "with", "skill",
  "this", "that", "use", "used", "using", "when", "你", "的", "了", "和", "与",
]);
function tokenSet(s) {
  const out = new Set();
  for (const t of (s || "").toLowerCase().split(/[^a-z0-9一-鿿]+/)) {
    if (t && t.length > 1 && !STOPWORDS.has(t)) out.add(t);
  }
  return out;
}
function jaccard(a, b) {
  if (!a.size || !b.size) return 0;
  let inter = 0;
  for (const t of a) if (b.has(t)) inter++;
  return inter / (a.size + b.size - inter);
}
// computeLibraryConflicts gathers every skill across all sources (same shape as
// the global search) and returns { dups, overlaps } for the top「冲突」stat + modal.
function computeLibraryConflicts() {
  const all = [];
  for (const [ns, skills] of Object.entries(state.skillsByRepo || {})) {
    (skills || []).forEach((sk) => all.push({ ns, name: sk.linkName || sk.logicalName, description: sk.description }));
  }
  (state.skillsShSkills || []).forEach((sk) => all.push({ ns: AGENTS_NS, name: sk.linkName || sk.logicalName, description: sk.description }));
  // ① 名称重复：同一 lowercase 名称、来自 ≥2 个不同源。
  const byName = new Map();
  for (const r of all) {
    const k = (r.name || "").toLowerCase();
    if (!k) continue;
    (byName.get(k) || byName.set(k, []).get(k)).push(r);
  }
  const dups = [];
  const dupNames = new Set();
  for (const [k, items] of byName) {
    const sources = new Set(items.map((i) => i.ns));
    if (items.length > 1 && sources.size > 1) { dups.push({ name: items[0].name, items }); dupNames.add(k); }
  }
  // ② 关键词重叠：不同名 skill 对，Jaccard(名称+描述分词) ≥ 阈值。
  const toks = all.map((r) => tokenSet((r.name || "") + " " + (r.description || "")));
  const overlaps = [];
  for (let i = 0; i < all.length; i++) {
    for (let j = i + 1; j < all.length; j++) {
      if ((all[i].name || "").toLowerCase() === (all[j].name || "").toLowerCase()) continue; // 同名归入 dup
      const score = jaccard(toks[i], toks[j]);
      if (score >= OVERLAP_THRESHOLD) overlaps.push({ a: all[i], b: all[j], score });
    }
  }
  overlaps.sort((x, y) => y.score - x.score);
  return { dups, overlaps };
}
function openConflictModal() {
  const body = $("#conflict-body"); body.innerHTML = "";
  $("#conflict-modal").classList.remove("hidden");
  const { dups, overlaps } = computeLibraryConflicts();
  if (!dups.length && !overlaps.length) {
    body.append(ce("div", { className: "empty", textContent: "库里没有发现跨源重复或明显重叠 🎉" }));
    return;
  }
  const srcChip = (ns) => { const m = sourceMeta(ns); return ce("span", { className: "src-badge " + m.cls, textContent: m.label }); };
  if (dups.length) {
    body.append(ce("div", { className: "cf-sec-head", textContent: "名称重复（" + dups.length + "）" }));
    body.append(ce("div", { className: "muted", style: "font-size:12px;margin:-4px 0 8px", textContent: "同名 skill 来自多个源，启用时只能保留一份。" }));
    dups.forEach((d) => {
      const sec = ce("div", { className: "cf-item" });
      const dh = ce("div", { className: "cf-head" });
      dh.append(ce("span", { className: "cf-name", textContent: d.name }));
      sec.append(dh);
      d.items.forEach((it) => {
        const row = ce("div", { className: "cf-cand clickable" });
        row.onclick = () => { closeConflictModal(); openDetail(it.ns, it.name); };
        const info = ce("div", { className: "cf-cand-info" });
        info.append(srcChip(it.ns));
        if (it.description) info.append(ce("span", { className: "muted", style: "font-size:12px", textContent: it.description.slice(0, 60) }));
        row.append(info);
        row.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "查看 ›" }));
        sec.append(row);
      });
      body.append(sec);
    });
  }
  if (overlaps.length) {
    body.append(ce("div", { className: "cf-sec-head", textContent: "可能重叠（" + overlaps.length + "）" }));
    body.append(ce("div", { className: "muted", style: "font-size:12px;margin:-4px 0 8px", textContent: "名称/描述关键词高度重合，可能功能或提示词重复，建议人工确认。" }));
    overlaps.forEach((o) => {
      const sec = ce("div", { className: "cf-item" });
      const oh = ce("div", { className: "cf-head" });
      oh.append(ce("span", { className: "cf-name", textContent: o.a.name + "  ⇄  " + o.b.name }));
      oh.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "重合 " + Math.round(o.score * 100) + "%" }));
      sec.append(oh);
      [o.a, o.b].forEach((it) => {
        const row = ce("div", { className: "cf-cand clickable" });
        row.onclick = () => { closeConflictModal(); openDetail(it.ns, it.name); };
        const info = ce("div", { className: "cf-cand-info" });
        info.append(srcChip(it.ns));
        info.append(ce("span", { className: "muted", style: "font-size:12px", textContent: it.name }));
        row.append(info);
        row.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "查看 ›" }));
        sec.append(row);
      });
      body.append(sec);
    });
  }
}
function closeConflictModal() { $("#conflict-modal").classList.add("hidden"); }

async function addRepoAndSync(url, branch) {
  try {
    await api("POST", "/api/repos", { url, branch });
    await updateNow(false);
    const host = httpsHost(url);
    if (host) {
      const repo = (state.status.repos || []).find((r) => r.url === url);
      if (repo && repo.authHint) {
        banner("该仓库需要有效凭据，请填写。", true);
        openCredModal(host, state.credHosts[host] || "");
      }
    }
  } catch (err) { banner("添加失败：" + err.message, true); }
}
async function addTarget(dir, alias) {
  try {
    const res = await api("POST", "/api/targets", { dir, alias });
    if (res && res.added && res.added.length) state.activeTarget = res.added[0];
    await load();
  } catch (err) { banner("添加同步目录失败：" + err.message, true); }
}
$("#add-target").onsubmit = async (e) => {
  e.preventDefault();
  const dir = $("#target-path").value.trim();
  if (!dir) return;
  const alias = $("#target-alias").value.trim();
  closeTargetModal();
  await addTarget(dir, alias);
};
$("#target-path").onkeydown = (e) => {
  if (e.key === "Enter") { e.preventDefault(); browseTo($("#target-path").value.trim()); }
};
$("#target-path").onpaste = () => { setTimeout(() => browseTo($("#target-path").value.trim()), 0); };
$("#do-export").onsubmit = async (e) => {
  e.preventDefault();
  const dir = $("#export-path").value.trim();
  closeExportModal();
  await doExport(dir);
};
$("#export-path").onkeydown = (e) => {
  if (e.key === "Enter") { e.preventDefault(); browseTo($("#export-path").value.trim(), "#export-browser", "#export-path"); }
};
$("#export-path").onpaste = () => { setTimeout(() => browseTo($("#export-path").value.trim(), "#export-browser", "#export-path"), 0); };
$("#export-modal-close").onclick = closeExportModal;
$("#export-modal-cancel").onclick = closeExportModal;
$("#export-modal").onclick = (e) => { if (e.target.id === "export-modal") closeExportModal(); };
$("#add-repo").onsubmit = async (e) => {
  e.preventDefault();
  const url = $("#repo-url").value.trim(), branch = $("#repo-branch").value.trim();
  if (!url) return;
  closeGitRepoModal();
  await submitGitRepo(url, branch);
};
$("#repo-url").oninput = updateRepoCredSection;
$("#git-repo-modal-close").onclick = closeGitRepoModal;
$("#git-repo-modal-cancel").onclick = closeGitRepoModal;
$("#git-repo-modal").onclick = (e) => { if (e.target.id === "git-repo-modal") closeGitRepoModal(); };
$("#health-modal-close").onclick = closeHealthModal;
$("#health-modal").onclick = (e) => { if (e.target.id === "health-modal") closeHealthModal(); };
$("#conflict-modal-close").onclick = closeConflictModal;
$("#conflict-modal").onclick = (e) => { if (e.target.id === "conflict-modal") closeConflictModal(); };
$("#cred-form").onsubmit = async (e) => {
  e.preventDefault();
  const host = $("#cred-host").dataset.host;
  const username = $("#cred-user").value.trim();
  const token = $("#cred-token").value;
  if (!host || !token) { banner("密码 / 令牌不能为空（公开仓可点「跳过」）", true); return; }
  const pending = state.credPending;
  try { await api("POST", "/api/credentials", { host, username, token }); }
  catch (err) { banner("保存凭据失败：" + err.message, true); return; }
  closeCredModal();
  if (pending) { await addRepoAndSync(pending.url, pending.branch); }
  else { banner("凭据已保存，正在重试更新…"); await api("POST", "/api/update-now", {}); await load(); }
};
$("#cred-skip").onclick = async () => {
  const pending = state.credPending;
  closeCredModal();
  if (pending) await addRepoAndSync(pending.url, pending.branch);
};
$("#cred-close").onclick = closeCredModal;
$("#cred-cancel").onclick = closeCredModal;
$("#cred-modal").onclick = (e) => { if (e.target.id === "cred-modal") closeCredModal(); };
$("#info-close").onclick = () => $("#info-modal").classList.add("hidden");
$("#info-modal").onclick = (e) => { if (e.target.id === "info-modal") $("#info-modal").classList.add("hidden"); };
$("#repo-skills-close").onclick = () => $("#repo-skills-modal").classList.add("hidden");
$("#repo-skills-modal").onclick = (e) => { if (e.target.id === "repo-skills-modal") $("#repo-skills-modal").classList.add("hidden"); };
$("#help-btn").onclick = () => $("#help-modal").classList.remove("hidden");
$("#help-close").onclick = () => $("#help-modal").classList.add("hidden");
$("#help-modal").onclick = (e) => { if (e.target.id === "help-modal") $("#help-modal").classList.add("hidden"); };
$("#tab-add").onclick = openTargetModal;
$("#target-modal-close").onclick = closeTargetModal;
$("#target-modal-cancel").onclick = closeTargetModal;
$("#add-localsrc").onsubmit = async (e) => {
  e.preventDefault();
  const dir = $("#localsrc-path").value.trim();
  if (!dir) return;
  const label = $("#localsrc-alias").value.trim();
  closeLocalSrcModal();
  await addLocalSource(dir, label);
};
$("#localsrc-path").onkeydown = (e) => {
  if (e.key === "Enter") { e.preventDefault(); browseTo($("#localsrc-path").value.trim(), "#localsrc-browser", "#localsrc-path"); }
};
$("#localsrc-path").onpaste = () => { setTimeout(() => browseTo($("#localsrc-path").value.trim(), "#localsrc-browser", "#localsrc-path"), 0); };
$("#localsrc-modal-close").onclick = closeLocalSrcModal;
$("#localsrc-modal-cancel").onclick = closeLocalSrcModal;
$("#localsrc-modal").onclick = (e) => { if (e.target.id === "localsrc-modal") closeLocalSrcModal(); };
$("#target-modal").onclick = (e) => { if (e.target.id === "target-modal") closeTargetModal(); };
// checkUpdates contacts each repo's remote (ls-remote, no pull) to populate the
// 「有更新」badges. There is no manual button anymore: it runs once on page load
// and then automatically every hour while the app is open (see the interval at
// the bottom). Always silent — passive, failures swallowed, a toast only when
// updates are found.
async function checkUpdates() {
  if (!state.status || (state.status.repos || []).length === 0) return;
  try {
    const r = await api("POST", "/api/check-updates");
    if (r && r.error) return;
    await load();
    const n = (r && r.updates) || 0;
    if (n > 0) toast(n + " 个仓有更新，点「全量更新」拉取");
  } catch (e) { /* passive check — swallow */ }
}
// 导出 / 导入仅针对 git 仓库列表（用于在另一台机器重建来源）——属于 git 源的能力，
// 故渲染在「git 仓」分区里（见 renderRepos），不挂在顶层「源」标题上。
// exportRepos opens a folder-picker (same in-app directory browser as「添加同步目录」)
// so the user chooses where the export lands. The backend writes the file and
// reveals it in the file manager — we do NOT build a Blob + <a download>, which
// the desktop app's WKWebView silently ignores.
function exportRepos() {
  $("#export-path").value = "";
  $("#export-modal").classList.remove("hidden");
  $("#export-path").focus();
  browseTo("~/Downloads", "#export-browser", "#export-path");
}
function closeExportModal() { $("#export-modal").classList.add("hidden"); }
async function doExport(dir) {
  try {
    const q = dir ? "?dir=" + encodeURIComponent(dir) : "";
    const res = await api("GET", "/api/repos/export" + q);
    banner("已导出 " + res.count + " 个 git 源到 " + res.path + "（已在文件管理器中显示）");
  } catch (err) {
    banner("导出失败：" + err.message, true);
  }
}
function importRepos() {
  const inp = ce("input", { type: "file", accept: ".json" });
  inp.onchange = async () => {
    const txt = await inp.files[0].text();
    let repos;
    try { repos = JSON.parse(txt); } catch { banner("导入文件不是合法 JSON", true); return; }
    try {
      const res = await api("POST", "/api/repos/import", { repos });
      banner("导入：新增 " + res.added + "，跳过 " + res.skipped);
      await load();
    } catch (err) { banner("导入被拒（含非法 URL，整体拒绝）：" + err.message, true); }
  };
  inp.click();
}
$("#modal-close").onclick = () => $("#modal").classList.add("hidden");
$("#modal").onclick = (e) => { if (e.target.id === "modal") $("#modal").classList.add("hidden"); };

// 首次使用自动弹出使用指南（localStorage 记一次，之后不再自动弹）。
(function maybeShowGuide() {
  try {
    if (!localStorage.getItem("sm_guide_seen")) {
      $("#help-modal").classList.remove("hidden");
      localStorage.setItem("sm_guide_seen", "1");
    }
  } catch { /* localStorage 不可用时静默跳过 */ }
})();

// On page entry: reconcile-then-render so the footer is current, then auto-check
// updates once (ls-remote only, no pull). While the app stays open, re-check
// every hour so the「有更新」badges stay current without a manual button.
load(true).then(() => checkUpdates());
setInterval(() => checkUpdates(), 60 * 60 * 1000);
