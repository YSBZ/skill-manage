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

const state = {
  status: null,
  targets: [],
  npxAvailable: false,
  credHosts: {}, // host → username for hosts with a stored HTTPS credential
  credPending: null, // {url, branch} when filling creds before adding a repo
  skillsByRepo: {}, // catalog for the "+ 添加" drawer
  inventory: [], // current tab's directory inventory (phase 3 U6)
  invScope: "",
  invLoading: false,
  invError: "",
  activeTarget: undefined, // active 同步目录 tab (one tab per dir)
  search: "",
  addSearch: "",
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

const HARNESS_LABEL = { cc: "cc", codex: "codex", unknown: "unknown" };
const harnessClass = (h) => (h === "codex" ? "st-linked-codex" : h === "cc" ? "st-cc" : "st-unknown");
const LOCAL_NS = "@local";

// SRC maps a classified source kind to its badge label + CSS class (phase 3 U8).
const SRC = {
  git: { label: "git", cls: "src-git" },
  local: { label: "本地", cls: "src-local" },
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
function toast(msg) {
  const t = $("#toast");
  t.textContent = msg;
  t.classList.remove("hidden");
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

function banner_(msg) { banner(msg); }

async function load() {
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
  // Catalog for the "+ 添加" drawer: tracked-repo skills plus the @local store.
  const names = repos.map((r) => r.name).concat(LOCAL_NS);
  const entries = await Promise.all(names.map(async (name) => {
    try { return [name, (await api("GET", "/api/skills?repo=" + encodeURIComponent(name))) || []]; }
    catch { return [name, []]; }
  }));
  state.skillsByRepo = Object.fromEntries(entries);
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
  const linked = (state.status.links || []).length;
  const conflicts = ((state.status.lastSummary && state.status.lastSummary.conflicts) || []).length;
  el.innerHTML = "";
  el.append(ce("span", { innerHTML: `仓库 <b>${repos}</b>` }));
  el.append(ce("span", { innerHTML: `已链接 <b>${linked}</b>` }));
  const c = ce("span", { innerHTML: `冲突 <b>${conflicts}</b>` });
  if (conflicts) c.className = "stat-warn";
  el.append(c);
}

function stateBadge(st) { return ce("span", { className: "badge " + st, textContent: st }); }

// repoDot classifies a repo's last-sync result into a status dot color.
function repoDot(repo) {
  const st = repo.state || "never-synced";
  if (repo.error || st === "failed") return "err";
  if (st === "never-synced" || st === "cloning" || st === "sync-in-progress") return "idle";
  return "ok";
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

function renderRepos() {
  const ul = $("#repo-list"); ul.innerHTML = "";
  (state.status.repos || []).forEach((repo) => {
    const host = httpsHost(repo.url);
    const li = ce("li");
    const top = ce("div", { className: "repo-top" });
    const dotKind = repoDot(repo);
    const dot = ce("span", { className: "repo-dot " + dotKind });
    if (dotKind === "err" && host) {
      dot.title = "连接/鉴权失败，点击重填凭据";
      dot.classList.add("clickable");
      dot.onclick = () => openCredModal(host, state.credHosts[host] || "");
    } else {
      dot.title = dotKind === "ok" ? "上次同步成功" : dotKind === "err" ? "上次同步失败" : "尚未同步";
    }
    top.append(dot, ce("span", { className: "repo-name", textContent: repo.name }), stateBadge(repo.state || "never-synced"));
    li.append(top);
    li.append(ce("div", { className: "repo-url", textContent: repo.url }));
    const meta = ce("div", { className: "repo-meta" });
    const n = (state.skillsByRepo[repo.name] || []).length;
    meta.append(ce("span", { className: "badge count", textContent: n + " skill" }));
    if (host) {
      const has = Object.prototype.hasOwnProperty.call(state.credHosts, host);
      const cb = ce("button", { className: "ghost small", textContent: has ? "凭据✓" : "填写凭据", title: has ? ("已为 " + host + " 配置凭据，点此重填") : ("为私有仓 " + host + " 填写 HTTPS 令牌") });
      cb.onclick = () => openCredModal(host, state.credHosts[host] || "");
      meta.append(cb);
    }
    meta.append(ce("span", { className: "group-spacer" }));
    const rm = ce("button", { className: "danger small", textContent: "移除" });
    rm.onclick = async () => {
      if (!(await confirmModal("移除仓库 " + repo.name + "？它建立的软链会立即清理。"))) return;
      await api("DELETE", "/api/repos", { url: repo.url });
      await load();
    };
    meta.append(rm);
    li.append(meta);
    if (repo.error) li.append(ce("div", { className: "muted", style: "color:var(--err);font-size:12px;margin-top:6px;white-space:pre-wrap", textContent: repo.error }));
    if (repo.authHint) li.append(ce("div", { className: "repo-authhint", textContent: host ? "鉴权失败，无法自动更新：点上方「填写凭据」填个人令牌(PAT)，或改用 SSH。" : "鉴权失败，无法自动更新：私有仓需配置 SSH key（加入 ssh-agent）。详见标题旁 ? 指南。" }));
    ul.append(li);
  });
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

async function browseTo(path) {
  const box = $("#target-browser");
  box.innerHTML = "";
  let resp;
  try { resp = await api("GET", "/api/browse?path=" + encodeURIComponent(path)); }
  catch (err) { box.append(ce("div", { className: "dir-empty err", textContent: err.message })); return; }
  $("#target-path").value = resp.path;
  if (resp.parent) {
    const up = ce("div", { className: "dir-row up" });
    up.append(ce("span", { className: "ic", textContent: "⬆" }), ce("span", { textContent: "上级目录" }));
    up.onclick = () => browseTo(resp.parent);
    box.append(up);
  }
  if (resp.dirs.length === 0) { box.append(ce("div", { className: "dir-empty", textContent: "（无子目录）" })); return; }
  resp.dirs.forEach((d) => {
    const row = ce("div", { className: "dir-row" });
    row.append(ce("span", { className: "ic", textContent: "📁" }), ce("span", { textContent: d.name }));
    row.onclick = () => browseTo(d.path);
    box.append(row);
  });
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
  let items = state.inventory;
  if (term) items = items.filter((i) => i.name.toLowerCase().includes(term) || (i.description || "").toLowerCase().includes(term));
  if (items.length === 0) {
    if (term) { root.append(ce("div", { className: "empty", textContent: "没有匹配的 skill" })); return; }
    const box = ce("div", { className: "empty" });
    box.append(ce("div", { textContent: "该目录暂无 skill。" }));
    box.append(ce("div", { className: "muted", style: "margin-top:6px", textContent: "点上方「+ 添加」从库选取，或在目录里创建 SKILL.md 后刷新。" }));
    root.append(box);
    return;
  }
  // Group by source (R3.2 / 用户要求「按仓库分类」): 本地 → 各 git 仓 → skills.sh →
  // 插件 → 未备份 → 未知软链.
  const groups = new Map();
  items.forEach((i) => {
    const g = groupOf(i);
    if (!groups.has(g.key)) groups.set(g.key, { title: g.title, order: g.order, items: [] });
    groups.get(g.key).items.push(i);
  });
  const ordered = [...groups.values()].sort((a, b) => a.order - b.order || a.title.localeCompare(b.title));
  ordered.forEach((g) => {
    const grp = ce("div", { className: "inv-group" });
    const head = ce("div", { className: "inv-group-head" });
    head.append(ce("span", { className: "group-title", textContent: g.title }));
    head.append(ce("span", { className: "badge count", textContent: g.items.length + " skill" }));
    grp.append(head);
    const body = ce("div", { className: "inv-group-body" });
    g.items.forEach((i) => body.append(inventoryCard(i)));
    grp.append(body);
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

// groupOf maps an inventory item to its source group (title + sort order).
function groupOf(i) {
  switch (i.kind) {
    case "local": return { key: "local", title: "本地（已备份）", order: 0 };
    case "git": return { key: "git:" + (i.repo || ""), title: i.repo || "git 仓", order: 1 };
    case "skills.sh": {
      // Group title is just the source repo; provenance (skills.sh) shows on each
      // card's source badge, matching how git repos are titled by repo name.
      const repo = repoFromUrl(i.sourceUrl);
      return { key: "skillssh:" + repo, title: repo || "skills.sh", order: 2 };
    }
    case "plugin": return { key: "plugin", title: "插件", order: 3 };
    case "handwritten": return { key: "hand", title: "未备份（可备份）", order: 4 };
    default: return { key: "unknown", title: "未知软链", order: 5 };
  }
}

function inventoryCard(i) {
  const s = SRC[i.kind] || SRC.unknown;
  const row = ce("div", { className: "skill inv " + s.cls });
  const main = ce("div", { className: "skill-main" });
  const r1 = ce("div", { className: "skill-row1" });
  r1.append(ce("span", { className: "skill-name", textContent: i.name }));

  let badgeText = s.label;
  if (i.kind === "skills.sh" && i.sourceUrl) badgeText = "skills.sh · " + hostOf(i.sourceUrl);
  const badge = ce("span", { className: "src-badge " + s.cls, textContent: badgeText });
  if (i.kind === "skills.sh" && i.sourceUrl) badge.title = i.sourceUrl; // title is text-safe (no innerHTML)
  r1.append(badge);

  if (i.collision) {
    const c = ce("span", { className: "src-badge src-shadow", textContent: "遮蔽" });
    c.title = "被同名 skill 遮蔽，实际不生效（项目级 > 用户级）";
    r1.append(c);
  }

  r1.append(ce("span", { className: "group-spacer" }));

  // actions / state (right side)
  if (i.managed && i.selector) {
    const d = ce("button", { className: "skill-detail-btn", textContent: "详情" });
    d.onclick = () => openDetail(i.selector.split("/")[0], i.name);
    r1.append(d);
    if (i.follow) {
      // covered by a whole-source follow — can't disable individually
      r1.append(ce("span", { className: "inv-state following", title: "整仓跟随中——在「+ 添加」里取消该来源的「整仓跟随」", textContent: "跟随中" }));
    } else {
      const off = ce("button", { className: "ghost small inv-off", textContent: "停用", title: "拆除此目录下的软链（不影响真身与其它目录）" });
      off.onclick = () => disableSkill(i, off);
      r1.append(off);
    }
  }
  if (i.kind === "handwritten") {
    const ad = ce("button", { className: "small", textContent: "备份", title: "移入受管存储并原位软链，纳入自动更新（未备份 → 已备份）" });
    ad.onclick = () => adoptHandwritten(i, ad);
    r1.append(ad);
  }
  if (i.kind === "skills.sh") {
    if (state.npxAvailable) {
      const u = ce("button", { className: "ghost small", textContent: "更新", title: "调用 npx skills update 更新（由 skills.sh 管理）" });
      u.onclick = () => updateSkillSh(i.name, u);
      r1.append(u);
    } else {
      r1.append(ce("span", { className: "inv-hint", textContent: "skills.sh 管理，用 npx skills update 更新" }));
    }
  }
  if (i.kind === "unknown") {
    const del = ce("button", { className: "danger small", textContent: "删除软链", title: "只删此软链，不动它指向的目标" });
    del.onclick = () => deleteStrayLink(i, del);
    r1.append(del);
  }
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
    await fetchInventory();
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除软链";
    banner("删除软链 " + i.name + " 失败：" + e.message, true);
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

// --- "+ 添加" drawer: enable catalog skills into the current tab (phase 3 U8) ---

function openAddDrawer() {
  if (state.targets.length === 0) { banner("先添加一个同步目录（点 tab 行的 +）", true); return; }
  $("#add-target-name").textContent = targetLabel(currentTarget());
  state.addSearch = "";
  $("#add-search").value = "";
  $("#add-modal").classList.remove("hidden");
  $("#add-search").focus();
  renderAddDrawer();
}
function closeAddDrawer() { $("#add-modal").classList.add("hidden"); }

function renderAddDrawer() {
  const box = $("#add-list"); box.innerHTML = "";
  const repos = state.status.repos || [];
  const sources = repos.map((r) => r.name);
  if ((state.skillsByRepo[LOCAL_NS] || []).length) sources.push(LOCAL_NS);
  const term = (state.addSearch || "").trim().toLowerCase();
  const present = new Set(state.inventory.filter((i) => i.managed).map((i) => i.name));
  let any = false;
  sources.forEach((name) => {
    const isLocal = name === LOCAL_NS;
    let skills = (state.skillsByRepo[name] || []).filter((s) => !present.has(s.linkName));
    if (term) skills = skills.filter((s) => s.linkName.toLowerCase().includes(term) || (s.description || "").toLowerCase().includes(term));
    const follow = enabledFollow(name);
    if (skills.length === 0 && !follow) return;
    any = true;
    const g = ce("div", { className: "add-group" });
    const h = ce("div", { className: "add-group-head" });
    h.append(ce("span", { className: "group-title", textContent: isLocal ? "@local · 已收编" : name }));
    h.append(ce("span", { className: "group-spacer" }));
    const fb = ce("button", { className: (follow ? "" : "ghost") + " small", textContent: follow ? "🔄 跟随中" : "整仓跟随" });
    fb.onclick = async () => {
      if (follow) await api("DELETE", "/api/enabled", { skill: name + "/*", target: currentTarget() });
      else await api("POST", "/api/enabled", { skill: name + "/*", target: currentTarget(), mode: "follow" });
      await api("POST", "/api/apply");
      toast(follow ? "已取消跟随 " + name : "已整仓跟随 " + name);
      await load(); renderAddDrawer();
    };
    h.append(fb); g.append(h);
    skills.forEach((sk) => {
      const r = ce("div", { className: "add-row" });
      r.append(ce("span", { className: "skill-name", textContent: sk.linkName }));
      if (sk.description) r.append(ce("span", { className: "add-desc skill-desc", textContent: sk.description }));
      r.append(ce("span", { className: "group-spacer" }));
      const sel = (isLocal ? LOCAL_NS : name) + "/" + sk.linkName;
      const b = ce("button", { className: "small", textContent: "启用" });
      b.disabled = follow;
      b.title = follow ? "整仓跟随中，已自动包含" : "";
      b.onclick = async () => {
        b.disabled = true;
        await api("POST", "/api/enabled", { skill: sel, target: currentTarget(), mode: "snapshot" });
        const sum = await api("POST", "/api/apply");
        summaryToast(sum, sk.linkName, true);
        await load(); renderAddDrawer();
      };
      r.append(b); g.append(r);
    });
    box.append(g);
  });
  if (!any) box.append(ce("div", { className: "empty", textContent: term ? "没有匹配的 skill" : "库里的 skill 都已启用进此目录（或还没有仓库）" }));
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
  (s.conflicts || []).forEach((c) => {
    let msg;
    if (c.kind === "collision") msg = "撞名 " + c.linkName + "（多个仓，需起别名）";
    else if (c.kind === "nested") msg = "嵌套 " + c.linkName + "（已链接到 Codex，含嵌套子 skill，可能污染 Codex 列表）";
    else msg = "遮蔽 " + c.linkName + "（同一 agent 下全局与项目同名，项目被遮蔽）";
    f.append(ce("div", { className: "conflict", textContent: "⚠ " + msg }));
  });
  (s.errors || []).forEach((e) => f.append(ce("div", { className: "error", textContent: "✗ " + e })));
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

async function updateNow(force) {
  banner("同步中…");
  let sum = null;
  try { sum = await api("POST", "/api/update-now", { force: !!force }); }
  catch (e) { banner("同步失败：" + e.message, true); await load(); return; }
  await load();
  // Report whether anything changed (R4.3).
  const changed = sum && ((sum.created || []).length + (sum.removed || []).length + (sum.pruned || []).length);
  if (changed) toast("更新完成：新增 " + (sum.created || []).length + " · 移除 " + (sum.removed || []).length + (sum.pruned && sum.pruned.length ? " · 清理 " + sum.pruned.length : ""));
  else toast("已是最新，无变化");
}

// events
$("#search").oninput = (e) => { state.search = e.target.value; renderInventory(); };
$("#add-search").oninput = (e) => { state.addSearch = e.target.value; renderAddDrawer(); };
$("#add-skill").onclick = openAddDrawer;
$("#add-modal-close").onclick = closeAddDrawer;
$("#add-modal").onclick = (e) => { if (e.target.id === "add-modal") closeAddDrawer(); };

$("#add-repo").onsubmit = async (e) => {
  e.preventDefault();
  const url = $("#repo-url").value.trim(), branch = $("#repo-branch").value.trim();
  if (!url) return;
  $("#repo-url").value = ""; $("#repo-branch").value = "";
  const host = httpsHost(url);
  if (host && !Object.prototype.hasOwnProperty.call(state.credHosts, host)) {
    openCredModal(host, "", { url, branch });
    return;
  }
  await addRepoAndSync(url, branch);
};

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
$("#repo-hint-btn").onclick = () => $("#repo-hint-modal").classList.remove("hidden");
$("#repo-hint-close").onclick = () => $("#repo-hint-modal").classList.add("hidden");
$("#repo-hint-modal").onclick = (e) => { if (e.target.id === "repo-hint-modal") $("#repo-hint-modal").classList.add("hidden"); };
$("#help-btn").onclick = () => $("#help-modal").classList.remove("hidden");
$("#help-close").onclick = () => $("#help-modal").classList.add("hidden");
$("#help-modal").onclick = (e) => { if (e.target.id === "help-modal") $("#help-modal").classList.add("hidden"); };
$("#tab-add").onclick = openTargetModal;
$("#target-modal-close").onclick = closeTargetModal;
$("#target-modal-cancel").onclick = closeTargetModal;
$("#target-modal").onclick = (e) => { if (e.target.id === "target-modal") closeTargetModal(); };
$("#update-now").onclick = () => updateNow(false);
$("#update-force").onclick = async () => { if (await confirmModal("强制更新会丢弃所有本地改动，与上游一致。继续？")) updateNow(true); };
$("#export").onclick = async () => {
  const repos = await api("GET", "/api/repos/export");
  const blob = new Blob([JSON.stringify(repos, null, 2)], { type: "application/json" });
  const a = ce("a", { href: URL.createObjectURL(blob), download: "skillmanage-repos.json" });
  a.click();
};
$("#import").onclick = () => {
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
};
$("#modal-close").onclick = () => $("#modal").classList.add("hidden");
$("#modal").onclick = (e) => { if (e.target.id === "modal") $("#modal").classList.add("hidden"); };

load();
