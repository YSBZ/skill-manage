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
  invScope: "",
  invLoading: false,
  invError: "",
  openGroup: undefined, // 手风琴：当前展开的组 key（undefined=尚未选择默认展开第一个；null=全部收起）
  dirSources: [], // 用户登记的本地目录源（status.localSources）：[{id,label,path,count}]
  activeTarget: undefined, // active 同步目录 tab (one tab per dir)
  search: "",
};

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

function banner(msg, isErr) {
  const b = $("#banner");
  if (!msg) { b.classList.add("hidden"); return; }
  b.textContent = msg;
  b.className = "banner" + (isErr ? " err" : "");
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
  const conflicts = ((state.status.lastSummary && state.status.lastSummary.conflicts) || []).length;
  // 「收录」= SkillManage 一共管控/识别了多少个 skill，跨全部源汇总：git 源 + 本地源
  // （@local 受管存储 + @dir 登记目录，都在 skillsByRepo 里）+ npx skills.sh 源。
  // 比「某个目录里链接了几个」更能体现这个工具的整体盘子。
  let catalog = 0;
  for (const arr of Object.values(state.skillsByRepo || {})) catalog += (arr || []).length;
  const npx = (state.skillsShSkills || []).length;
  const total = catalog + npx;
  el.innerHTML = "";
  el.append(ce("span", { innerHTML: `仓库 <b>${repos}</b>`, title: "已登记的 git 仓数量" }));
  el.append(ce("span", { innerHTML: `收录 skills <b>${total}</b>`, title: `SkillManage 一共收录管控的 skill 数：git 源 + 本地源 ${catalog} 个，npx(skills.sh) 源 ${npx} 个。` }));
  const c = ce("span", { innerHTML: `冲突 <b>${conflicts}</b>`, title: "撞名 / 嵌套等需要你处理的冲突数量" });
  if (conflicts) c.className = "stat-warn";
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
  const gitExport = ce("button", { className: "ghost small", textContent: "导出", title: "导出 git 仓库列表（用于在另一台机器重建来源）" });
  gitExport.onclick = exportRepos;
  const gitImport = ce("button", { className: "ghost small", textContent: "导入", title: "从文件导入 git 仓库列表" });
  gitImport.onclick = importRepos;
  const gitHelp = ce("button", { className: "help-btn", textContent: "?", title: "私有仓更新与鉴权" });
  gitHelp.onclick = (e) => { e.stopPropagation(); $("#repo-hint-modal").classList.remove("hidden"); };
  ul.append(sourceDivider("git 仓", [gitExport, gitImport], gitHelp));
  // git 源的添加表单（URL + 分支 + 添加）属于 git，放在 git 仓分区里。
  const addRow = ce("li", { className: "repo-addrow" });
  const form = ce("form", { id: "add-repo" });
  const urlIn = ce("input", { id: "repo-url", placeholder: "git 仓 URL (https / ssh / git@…)", required: true });
  const brIn = ce("input", { id: "repo-branch", placeholder: "分支(可选)", className: "branch" });
  const addBtn = ce("button", { type: "submit", textContent: "添加" });
  form.append(urlIn, brIn, addBtn);
  form.onsubmit = async (e) => {
    e.preventDefault();
    const url = urlIn.value.trim(), branch = brIn.value.trim();
    if (!url) return;
    urlIn.value = ""; brIn.value = "";
    await submitGitRepo(url, branch);
  };
  addRow.append(form);
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
    ul.append(sourceDivider("npx skills"));
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
    smeta.append(ce("span", { className: "group-spacer" }));
    if (state.npxAvailable) {
      const su = ce("button", { className: "ghost small", textContent: "更新", title: "npx skills update：更新全部 skills.sh skill" });
      su.onclick = (e) => { e.stopPropagation(); updateSkillsShAll(su); };
      smeta.append(su);
    } else {
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
// them into the current target / 整仓跟随 here (selector namespace "@agents"),
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
        textContent: follow ? "🔄 跟随中" : "整仓跟随",
        title: follow ? "取消整仓跟随" : "整仓跟随：skills.sh 现有及将来的全部 skill 自动启用进当前目录",
      });
      fb.onclick = async () => {
        fb.disabled = true;
        if (follow) await api("DELETE", "/api/enabled", { skill: AGENTS_NS + "/*", target });
        else await api("POST", "/api/enabled", { skill: AGENTS_NS + "/*", target, mode: "follow" });
        await api("POST", "/api/apply");
        toast(follow ? "已取消整仓跟随" : "已整仓跟随 skills.sh");
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
// to enable skills / 整仓跟随 into the current target (replacing the old top「管理」
// drawer). opts.ns overrides the selector namespace (defaults to repoName);
// opts.local → @local store skills are also deletable; opts.title overrides the
// heading. Enable/follow act on the current target tab.
function openRepoSkills(repoName, opts) {
  opts = opts || {};
  const ns = opts.ns || repoName; // selector namespace for enable / 整仓跟随
  $("#repo-skills-title").textContent = opts.title || (opts.local ? "local · 本地源" : repoName);
  const body = $("#repo-skills-body");
  const render = () => {
    body.innerHTML = "";
    const target = currentTarget();
    const follow = enabledFollow(ns);
    // Toolbar: which target we enable into + the 整仓跟随 toggle for this source.
    const bar = ce("div", { className: "rs-toolbar" });
    if (target) {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "启用到当前目录：" + targetLabel(target) }));
      bar.append(ce("span", { className: "group-spacer" }));
      const fb = ce("button", {
        className: (follow ? "" : "ghost") + " small",
        textContent: follow ? "🔄 跟随中" : "整仓跟随",
        title: follow ? "取消整仓跟随：不再自动纳入该源的 skill" : "整仓跟随：该源现有及将来新增的全部 skill 自动启用进当前目录",
      });
      fb.onclick = async () => {
        fb.disabled = true;
        if (follow) await api("DELETE", "/api/enabled", { skill: ns + "/*", target });
        else await api("POST", "/api/enabled", { skill: ns + "/*", target, mode: "follow" });
        await api("POST", "/api/apply");
        toast(follow ? "已取消整仓跟随" : "已整仓跟随");
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
// mutually exclusive: only one shows at a time. Under 整仓跟随 (or with no target)
// individual toggling is unavailable, so a hint shows instead. render re-renders
// the modal.
function enableControl(ns, name, follow, enabled, target, render) {
  if (!target) return ce("span", { className: "inv-hint", textContent: "未选目录" });
  if (follow) return ce("span", { className: "inv-hint", textContent: "整仓跟随中" });
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
async function updateRepo(repo, btn) {
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  try {
    const sum = await api("POST", "/api/repos/update", { url: repo.url });
    if (sum && sum.errors && sum.errors.length) banner("更新 " + repo.name + "：" + sum.errors.join("；"), true);
    else toast("已更新 " + repo.name);
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
    tab.onclick = () => { state.activeTarget = t.dir; renderTabs(); fetchInventory(); };
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
  let items = state.inventory.slice();
  // 注入当前 tab 所属 harness 的插件 skill（只读），作为「目录现状」底部「插件」组。
  const curHarness = (state.targets.find((t) => t.dir === currentTarget()) || {}).harness;
  (state.pluginSkills || []).forEach((p) => {
    if (curHarness && p.harness && p.harness !== curHarness) return;
    items.push({ kind: "plugin", name: p.name, description: p.description, plugin: p.plugin, version: p.version, harness: p.harness });
  });
  if (term) items = items.filter((i) => i.name.toLowerCase().includes(term) || (i.description || "").toLowerCase().includes(term));
  if (items.length === 0) {
    if (term) { root.append(ce("div", { className: "empty", textContent: "没有匹配的 skill" })); return; }
    const box = ce("div", { className: "empty" });
    box.append(ce("div", { textContent: "该目录暂无 skill。" }));
    box.append(ce("div", { className: "muted", style: "margin-top:6px", textContent: "点上方「管理」从库选取，或在目录里创建 SKILL.md 后刷新。" }));
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

  // 徽章默认显示全名——位置够就不缩写（CSS 在真正放不下时才 ellipsis 截断，全名见 hover）。
  let badgeText = s.label;
  if (i.kind === "skills.sh" && i.sourceUrl) badgeText = repoFromUrl(i.sourceUrl) || hostOf(i.sourceUrl);
  if (i.kind === "plugin" && i.plugin) badgeText = i.plugin;
  // 本地源合并成一个组后，徽章带上各自来源：local（受管存储）或某个本地源文件夹名。
  if (i.kind === "local") badgeText = "local";
  if (i.kind === "dir") badgeText = dirLabel(i.repo);
  const badge = ce("span", { className: "src-badge " + s.cls, textContent: badgeText });
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

// --- enable / 整仓跟随 helpers (used by the per-source skill modals) ---

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
  // 我们的链被「占用」跳过的名字：这些链根本没建成，就不能再报它「遮蔽」（否则与下方 ✗ 自相矛盾）。
  const skipped = new Set();
  (s.errors || []).forEach((e) => {
    const m = String(e).match(/^link\s+(.+?)\s+->/);
    if (m) skipped.add(m[1].split("/").pop());
  });
  // collision/nested 逐条列（少且可操作）；shadow 折叠成一条汇总——逐项处理在「目录现状」里做，
  // footer 不糊一墙重复告警（每张卡片已有「遮蔽」标记 + 停用按钮）。
  let shadowN = 0;
  (s.conflicts || []).forEach((c) => {
    if (c.kind === "collision") f.append(ce("div", { className: "conflict", textContent: "⚠ 撞名 " + c.linkName + "（多个仓，需起别名）" }));
    else if (c.kind === "nested") f.append(ce("div", { className: "conflict", textContent: "⚠ 嵌套 " + c.linkName + "（已链接到 Codex，含嵌套子 skill，可能污染 Codex 列表）" }));
    else if (!skipped.has(c.linkName)) shadowN++; // 链未建成的不计（避免与下方 ✗ 矛盾）
  });
  if (shadowN) {
    f.append(ce("div", { className: "conflict", textContent:
      "⚠ " + shadowN + " 个 skill 在同一 harness 的全局与项目目录重复（遮蔽，项目级生效、另一份冗余）——在「目录现状」里把多余的一份「停用」即可消除（外部工具装的用其原生方式移除）。" }));
  }
  (s.errors || []).forEach((e) => {
    const h = humanizeSyncError(e);
    const row = ce("div", { className: "error", title: e });
    row.append(ce("span", { className: "err-why", textContent: "✗ " + h.why }));
    if (h.fix) row.append(ce("span", { className: "err-fix", textContent: "建议：" + h.fix }));
    f.append(row);
  });
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

async function openDetail(repo, name) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/skill?repo=" + encodeURIComponent(repo) + "&name=" + encodeURIComponent(name));
    $("#modal-desc").textContent = d.description || "";
    $("#modal-content").textContent = d.content || "(空)";
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

// openPluginDetail reads a plugin skill's SKILL.md from its install tree.
async function openPluginDetail(plugin, name, harness) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
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
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/skill-at?target=" + encodeURIComponent(currentTarget()) + "&name=" + encodeURIComponent(name));
    $("#modal-desc").textContent = d.description || "";
    $("#modal-content").textContent = d.content || "(空)";
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

async function updateNow(force) {
  banner("同步中…");
  let sum = null;
  try { sum = await api("POST", "/api/update-now", { force: !!force }); }
  catch (e) { banner("同步失败：" + e.message, true); await load(); return; }
  await load();
  // Report whether anything changed (R4.3). 全量更新 covers BOTH git sources
  // (sum.created/removed/pruned) AND npx/skills.sh (sum.npx — delegated update).
  const changed = sum && ((sum.created || []).length + (sum.removed || []).length + (sum.pruned || []).length);
  const npx = sum && sum.npx;
  let msg;
  if (changed) msg = "更新完成：新增 " + (sum.created || []).length + " · 移除 " + (sum.removed || []).length + (sum.pruned && sum.pruned.length ? " · 清理 " + sum.pruned.length : "");
  else msg = "git 源已是最新，无变化";
  if (npx && npx.ran) msg += npx.ok ? " · npx 源已更新" : " · npx 更新失败";
  toast(msg, npx && npx.ran && !npx.ok ? "err" : "ok");
  if (npx && npx.ran && !npx.ok && npx.error) banner("npx skills update 失败：" + npx.error, true);
}

// events
$("#search").oninput = (e) => { state.search = e.target.value; renderInventory(); };

// submitGitRepo handles the git-source add form (rendered inside the git 仓
// section by renderRepos). Private https hosts without stored creds route through
// the credential modal first; everything else adds + syncs immediately.
async function submitGitRepo(url, branch) {
  if (!url) return;
  const host = httpsHost(url);
  if (host && !Object.prototype.hasOwnProperty.call(state.credHosts, host)) {
    openCredModal(host, "", { url, branch });
    return;
  }
  await addRepoAndSync(url, branch);
}

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
$("#repo-hint-btn").onclick = () => infoModal("什么是「源」？", [
  { text: "「源」是 skill 的来源。SkillManage 把各来源的 skill 映射（建软链）进你的目标目录，统一管理，绝不改动别家工具与你的原文件。共三类，侧栏平级展示：" },
  { h: "git 仓", text: "git 仓库作为来源，受 SkillManage 管理；点「更新」拉取上游。私有仓需先配好免交互鉴权（见 git 仓分区的 ? 说明）。" },
  { h: "npx skills（skills.sh）", text: "别家工具 npx skills 装在 ~/.agents/skills 的 skill，只读识别——绝不接管，更新交给它自己（卡片上的「更新」代调 npx skills update）。" },
  { h: "本地源", text: "本地文件夹里的 skill，能力相同、只是来源不同：「local」是 SkillManage 创建的受管存储（备份 / 手写归此，可删 skill）；「添加本地源」可把你选的任意文件夹登记为源（实时识别、不复制，软链直接指向原文件）。" },
  { h: "怎么用", text: "选好目标目录（顶部 tab）后，点开左侧任一源卡片，在弹窗里点「启用」做单个映射，或「整仓跟随」让整个源自动跟随（源里增删 skill 自动加 / 清软链）。" },
]);
$("#repo-hint-close").onclick = () => $("#repo-hint-modal").classList.add("hidden");
$("#repo-hint-modal").onclick = (e) => { if (e.target.id === "repo-hint-modal") $("#repo-hint-modal").classList.add("hidden"); };
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
// checkUpdates contacts each repo's remote (ls-remote, no pull). silent=true is
// the on-page-load auto-check: no banners, only a toast if updates are found and
// failures are swallowed; silent=false is the manual button with full feedback.
async function checkUpdates(silent) {
  if (!state.status || (state.status.repos || []).length === 0) {
    if (!silent) toast("还没有仓库");
    return;
  }
  const btn = $("#check-updates"); const old = btn.textContent;
  btn.disabled = true; btn.textContent = "检查中…";
  if (!silent) toast("检查更新中…", "info");
  try {
    const r = await api("POST", "/api/check-updates");
    if (r && r.error) { if (!silent) toast("检查更新失败（git 不可用）：" + r.error, "err"); return; }
    await load();
    const n = (r && r.updates) || 0;
    if (n > 0) toast(n + " 个仓有更新，点「全量更新」拉取");
    else if (!silent) toast("所有仓都是最新");
  } catch (e) { if (!silent) toast("检查更新失败：" + e.message, "err"); }
  finally { btn.disabled = false; btn.textContent = old; }
}
$("#check-updates").onclick = () => checkUpdates(false);
$("#update-now").onclick = () => updateNow(false);
$("#update-force").onclick = async () => { if (await confirmModal("强制更新会丢弃所有本地改动，与上游一致。继续？")) updateNow(true); };
// 导出 / 导入仅针对 git 仓库列表（用于在另一台机器重建来源）——属于 git 源的能力，
// 故渲染在「git 仓」分区里（见 renderRepos），不挂在顶层「源」标题上。
async function exportRepos() {
  const repos = await api("GET", "/api/repos/export");
  const blob = new Blob([JSON.stringify(repos, null, 2)], { type: "application/json" });
  const a = ce("a", { href: URL.createObjectURL(blob), download: "skillmanage-repos.json" });
  a.click();
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
// updates once (ls-remote only, no pull).
load(true).then(() => checkUpdates(true));
