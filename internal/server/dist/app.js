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

const state = {
  status: null,
  targets: [],
  adoptable: [],
  adoptError: false,
  skillsByRepo: {},
  expanded: undefined, // accordion: open group name; undefined=未初始化, null=用户主动全收起
  activeTarget: undefined, // active 同步目录 tab (one tab per dir)
  search: "",
};

// error_code → 用户文案（KTD7/U6：不同失败点处置不同）
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
// Reserved source namespace for adopted (收编) skills living in the personal
// store; surfaced in the main list alongside tracked repos so收编 is visible.
const LOCAL_NS = "@local";

function banner(msg, isErr) {
  const b = $("#banner");
  if (!msg) { b.classList.add("hidden"); return; }
  b.textContent = msg;
  b.className = "banner" + (isErr ? " err" : "");
}

const targetDirs = () => state.targets.map((t) => t.dir);
// currentTarget = the active tab's directory (each tab is one sync dir and keeps
// its own skill→dir mapping). Falls back to the first directory.
const currentTarget = () => {
  const dirs = targetDirs();
  if (state.activeTarget && dirs.includes(state.activeTarget)) return state.activeTarget;
  return dirs[0] || "~/.claude/skills/";
};

const enabledFollow = (repo) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/*" && e.target === currentTarget());
const enabledSnapshot = (repo, link) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/" + link && e.target === currentTarget());

// skillBadges returns only conflict/warning badges for a skill. Whether a skill
// is synced to the current directory is shown by its checkbox (sync is by
// directory/tab, NOT by cc/codex agent), so a per-agent "linked" badge would be
// both redundant and misleading.
function skillBadges(linkName) {
  const out = [];
  const confs = (state.status.lastSummary && state.status.lastSummary.conflicts) || [];
  if (confs.some((c) => c.kind === "collision" && c.linkName === linkName)) out.push({ cls: "st-conflict", text: "撞名" });
  if (confs.some((c) => c.kind === "shadow" && c.linkName === linkName)) out.push({ cls: "st-shadowed", text: "被遮蔽" });
  if (confs.some((c) => c.kind === "nested" && c.linkName === linkName)) out.push({ cls: "st-shadowed", text: "嵌套⚠" });
  return out;
}

async function load() {
  try { state.status = await api("GET", "/api/status"); }
  catch (e) { banner("加载失败：" + e.message, true); return; }
  try { state.targets = (await api("GET", "/api/targets")) || []; }
  catch { state.targets = []; }
  try { state.adoptable = ((await api("GET", "/api/adoptable")) || {}).skills || []; state.adoptError = false; }
  catch { state.adoptable = []; state.adoptError = true; }
  const repos = state.status.repos || [];
  // Fetch tracked-repo skills plus the @local store (adopted skills).
  const names = repos.map((r) => r.name).concat(LOCAL_NS);
  const entries = await Promise.all(names.map(async (name) => {
    try { return [name, (await api("GET", "/api/skills?repo=" + encodeURIComponent(name))) || []]; }
    catch { return [name, []]; }
  }));
  state.skillsByRepo = Object.fromEntries(entries);
  renderStats(); renderRepos(); renderTabs(); renderAdoptable(); renderSkills(); renderSummary(); loadAutostart();
  banner(repos.length === 0 ? "还没有仓库。在左侧添加一个 git skill 仓开始。" : "");
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

function renderRepos() {
  const ul = $("#repo-list"); ul.innerHTML = "";
  (state.status.repos || []).forEach((repo) => {
    const li = ce("li");
    const top = ce("div", { className: "repo-top" });
    top.append(ce("span", { className: "repo-name", textContent: repo.name }), stateBadge(repo.state || "never-synced"));
    li.append(top);
    li.append(ce("div", { className: "repo-url", textContent: repo.url }));
    const meta = ce("div", { className: "repo-meta" });
    const n = (state.skillsByRepo[repo.name] || []).length;
    meta.append(ce("span", { className: "badge count", textContent: n + " skill" }));
    meta.append(ce("span", { className: "group-spacer" }));
    const rm = ce("button", { className: "danger small", textContent: "移除" });
    rm.onclick = async () => {
      if (!confirm("移除仓库 " + repo.name + "？其链接会在下次应用时清理。")) return;
      await api("DELETE", "/api/repos", { url: repo.url });
      await apply();
    };
    meta.append(rm);
    li.append(meta);
    if (repo.error) li.append(ce("div", { className: "muted", style: "color:var(--err);font-size:12px;margin-top:6px;white-space:pre-wrap", textContent: repo.error }));
    ul.append(li);
  });
}

// renderTabs draws one tab per sync directory. The active tab is the current
// target: the skill list below reflects (and edits) that directory's own
// skill→dir mapping. Each tab carries a cc/codex badge and a remove ×.
function renderTabs() {
  const bar = $("#target-tabs"); bar.innerHTML = "";
  const active = currentTarget();
  if (state.targets.length === 0) {
    bar.append(ce("span", { className: "muted", style: "align-self:center", textContent: "还没有同步目录 →" }));
  }
  state.targets.forEach((t) => {
    const tab = ce("div", { className: "tab" + (t.dir === active ? " active" : ""), title: t.dir });
    tab.append(ce("span", { className: "badge " + harnessClass(t.harness), textContent: t.harness }));
    tab.append(ce("span", { className: "tab-dir", textContent: t.alias || t.dir }));
    const rm = ce("button", { className: "tab-x", textContent: "×", title: "移除此同步目录" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!confirm("移除同步目录 " + (t.alias || t.dir) + "？\n该目录下由本工具建立的链接会在下次同步时清理；目录里你自己的真身 skill 不受影响。")) return;
      if (state.activeTarget === t.dir) state.activeTarget = undefined;
      await api("DELETE", "/api/targets", { dir: t.dir });
      await apply();
    };
    tab.append(rm);
    tab.onclick = () => { state.activeTarget = t.dir; renderTabs(); renderSkills(); };
    bar.append(tab);
  });
  const add = ce("button", { className: "tab-add", textContent: "+", title: "添加同步目录" });
  add.onclick = openTargetModal;
  bar.append(add);
}

function openTargetModal() {
  $("#target-path").value = "";
  $("#target-alias").value = "";
  $("#target-modal").classList.remove("hidden");
  $("#target-path").focus();
}
function closeTargetModal() { $("#target-modal").classList.add("hidden"); }

function renderAdoptable() {
  const ul = $("#adopt-list"); ul.innerHTML = "";
  const allBtn = $("#adopt-all");
  if (allBtn) allBtn.classList.toggle("hidden", state.adoptable.length === 0 || state.adoptError);
  if (state.adoptError) {
    ul.append(ce("li", { className: "muted", style: "color:var(--err)", textContent: "加载失败，请刷新" }));
    return;
  }
  if (state.adoptable.length === 0) {
    ul.append(ce("li", { className: "muted", textContent: "同步目录下无未备份的真身 skill（已全部收编或均为软链）" }));
    return;
  }
  state.adoptable.forEach((a) => {
    const li = ce("li");
    const name = ce("span", { className: "path", textContent: a.name });
    const tag = HARNESS_LABEL[a.harness];
    if (tag) name.append(ce("span", { className: "badge " + harnessClass(a.harness), textContent: tag, style: "margin-left:6px" }));
    li.append(name);
    const btn = ce("button", { className: "small", textContent: "收编" });
    btn.onclick = async () => {
      btn.disabled = true; btn.textContent = "收编中…";
      try {
        await doAdopt(a.id, a.root);
        banner("已收编 " + a.name + "（原位已软链）");
        await load();
      } catch (e) {
        btn.disabled = false; btn.textContent = "收编";
        banner("收编 " + a.name + " 失败：" + (ADOPT_ERR[e.code] || e.message), true);
      }
    };
    li.append(btn); ul.append(li);
  });
}

// doAdopt posts to /api/adopt and surfaces the error_code so the caller can map
// it to a specific message (generic api() would drop the code). root addresses
// which personal source dir the skill lives under (CC vs Codex).
async function doAdopt(id, root) {
  const r = await fetch("/api/adopt", {
    method: "POST",
    headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
    body: JSON.stringify({ id, root }),
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) { const e = new Error(data.error || r.statusText); e.code = data.error_code; throw e; }
  return data;
}

function renderSkills() {
  const root = $("#skills"); root.innerHTML = "";
  const repos = state.status.repos || [];
  const term = state.search.trim().toLowerCase();
  // Sources = tracked repos, plus the @local store when it holds adopted skills.
  const sources = repos.map((r) => r.name);
  if ((state.skillsByRepo[LOCAL_NS] || []).length) sources.push(LOCAL_NS);
  if (sources.length === 0) { root.append(ce("div", { className: "empty", textContent: "无仓库" })); return; }

  // accordion: default to the first source only on first render (undefined).
  // A user collapse sets null and must be respected (not re-expanded).
  if (state.expanded === undefined) state.expanded = sources[0] || null;

  let anyShown = false;
  sources.forEach((name) => {
    const isLocal = name === LOCAL_NS;
    let skills = state.skillsByRepo[name] || [];
    if (term) skills = skills.filter((s) => s.linkName.toLowerCase().includes(term) || (s.description || "").toLowerCase().includes(term));
    if (term && skills.length === 0) return;
    anyShown = true;

    const collapsed = name !== state.expanded;
    const group = ce("div", { className: "group" + (collapsed ? " collapsed" : "") });

    const head = ce("div", { className: "group-head" });
    head.append(ce("span", { className: "caret", textContent: "▾" }));
    head.append(ce("span", { className: "group-title", textContent: isLocal ? "@local · 已收编" : name }));
    head.append(ce("span", { className: "badge count", textContent: skills.length + " skill" }));
    head.append(ce("span", { className: "group-spacer" }));
    const follow = enabledFollow(name);
    const fbtn = ce("button", { className: (follow ? "" : "ghost") + " small", textContent: follow ? "🔄 跟随中" : "全选并跟随" });
    fbtn.onclick = async (e) => {
      e.stopPropagation();
      if (follow) await api("DELETE", "/api/enabled", { skill: name + "/*", target: currentTarget() });
      else await api("POST", "/api/enabled", { skill: name + "/*", target: currentTarget(), mode: "follow" });
      await apply();
    };
    head.append(fbtn);
    head.onclick = () => {
      state.expanded = (state.expanded === name) ? null : name; // toggle; others close
      renderSkills();
    };
    group.append(head);

    const body = ce("div", { className: "group-body" });
    if (skills.length === 0) body.append(ce("div", { className: "empty", textContent: isLocal ? "暂无已收编 skill" : "此仓暂无 skill（可能尚未同步，点“立即更新”）" }));
    skills.forEach((sk) => body.append(skillCard(name, sk, follow)));
    group.append(body);
    root.append(group);
  });
  if (!anyShown) root.append(ce("div", { className: "empty", textContent: term ? "没有匹配的 skill" : "暂无 skill" }));
}

function skillCard(repo, sk, follow) {
  const row = ce("div", { className: "skill" });
  const cb = ce("input", { type: "checkbox" });
  cb.checked = follow || enabledSnapshot(repo, sk.linkName);
  cb.disabled = follow;
  cb.onchange = async () => {
    if (cb.checked) await api("POST", "/api/enabled", { skill: repo + "/" + sk.linkName, target: currentTarget(), mode: "snapshot" });
    else await api("DELETE", "/api/enabled", { skill: repo + "/" + sk.linkName, target: currentTarget() });
    await apply();
  };
  row.append(cb);

  const main = ce("div", { className: "skill-main" });
  const r1 = ce("div", { className: "skill-row1" });
  r1.append(ce("span", { className: "skill-name", textContent: sk.linkName }));
  if (sk.logicalName !== sk.linkName) r1.append(ce("span", { className: "skill-logical", textContent: "(" + sk.logicalName + ")" }));
  skillBadges(sk.linkName).forEach((b) => r1.append(ce("span", { className: "badge " + b.cls, textContent: b.text })));
  const detail = ce("button", { className: "skill-detail-btn", textContent: "详情" });
  detail.onclick = () => openDetail(repo, sk.linkName);
  r1.append(detail);
  // No per-card 停用: for a single-selected skill it is redundant with the
  // checkbox (both withhold the link). Group-level 停用 (follow) stays on the head.
  main.append(r1);
  if (sk.description) main.append(ce("div", { className: "skill-desc", textContent: sk.description }));
  row.append(main);
  return row;
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

async function apply() {
  try { await api("POST", "/api/apply"); }
  catch (e) { banner("应用失败：" + e.message, true); }
  await load();
}

async function updateNow(force) {
  banner("同步中…");
  try { await api("POST", "/api/update-now", { force: !!force }); banner(""); }
  catch (e) { banner("同步失败：" + e.message, true); }
  await load();
}

async function loadAutostart() {
  try {
    const a = await api("GET", "/api/autostart");
    const el = $("#autostart"); el.checked = a.registered; el.disabled = !a.supported;
  } catch { /* ignore */ }
}

// events
$("#search").oninput = (e) => { state.search = e.target.value; renderSkills(); };
$("#add-repo").onsubmit = async (e) => {
  e.preventDefault();
  const url = $("#repo-url").value.trim(), branch = $("#repo-branch").value.trim();
  try {
    await api("POST", "/api/repos", { url, branch });
    $("#repo-url").value = ""; $("#repo-branch").value = "";
    await updateNow(false);
  } catch (err) { banner("添加失败：" + err.message, true); }
};
async function addTarget(dir, alias) {
  try { await api("POST", "/api/targets", { dir, alias }); state.activeTarget = dir; await load(); }
  catch (err) { banner("添加同步目录失败：" + err.message, true); }
}
$("#add-target").onsubmit = async (e) => {
  e.preventDefault();
  const dir = $("#target-path").value.trim();
  if (!dir) return;
  const alias = $("#target-alias").value.trim();
  closeTargetModal();
  await addTarget(dir, alias);
};
$("#target-modal-close").onclick = closeTargetModal;
$("#target-modal-cancel").onclick = closeTargetModal;
$("#target-modal").onclick = (e) => { if (e.target.id === "target-modal") closeTargetModal(); };
document.querySelectorAll("#add-target [data-fill]").forEach((b) => {
  b.onclick = () => { $("#target-path").value = b.getAttribute("data-fill"); $("#target-alias").focus(); };
});
$("#adopt-all").onclick = async () => {
  const items = state.adoptable.slice();
  if (!items.length) return;
  if (!confirm("全选收编 " + items.length + " 个未备份 skill？将逐个移入受管存储并原位软链。")) return;
  const btn = $("#adopt-all"); btn.disabled = true; btn.textContent = "收编中…";
  let ok = 0; const errs = [];
  for (const a of items) {
    try { await doAdopt(a.id, a.root); ok++; }
    catch (e) { errs.push(a.name + "：" + (ADOPT_ERR[e.code] || e.message)); }
  }
  btn.disabled = false; btn.textContent = "全选收编";
  if (errs.length === 0) banner("已全部收编 " + ok + " 个");
  else banner("收编完成 " + ok + " 个，失败 " + errs.length + " 个：" + errs.join("；"), true);
  await load();
};
$("#update-now").onclick = () => updateNow(false);
$("#update-force").onclick = () => { if (confirm("强制更新会丢弃所有本地改动，与上游一致。继续？")) updateNow(true); };
$("#autostart").onchange = async (e) => {
  try { await api("POST", "/api/autostart", { enabled: e.target.checked }); }
  catch (err) { banner("自启设置失败：" + err.message, true); e.target.checked = !e.target.checked; }
};
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
