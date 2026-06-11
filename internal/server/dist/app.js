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
  skillsByRepo: {},
  collapsed: new Set(),
  search: "",
};

function banner(msg, isErr) {
  const b = $("#banner");
  if (!msg) { b.classList.add("hidden"); return; }
  b.textContent = msg;
  b.className = "banner" + (isErr ? " err" : "");
}

const targetOptions = () => {
  const opts = ["~/.claude/skills/"];
  (state.status.projects || []).forEach((p) => opts.push(p.replace(/\/$/, "") + "/.claude/skills"));
  return opts;
};
const currentTarget = () => { const s = $("#target"); return s && s.value ? s.value : "~/.claude/skills/"; };

const enabledFollow = (repo) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/*" && e.target === currentTarget());
const enabledSnapshot = (repo, link) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/" + link && e.target === currentTarget());

function skillBadge(linkName) {
  const s = state.status.lastSummary;
  const confs = (s && s.conflicts) || [];
  if (confs.some((c) => c.kind === "collision" && c.linkName === linkName))
    return { cls: "st-conflict", text: "撞名" };
  if (confs.some((c) => c.kind === "shadow" && c.linkName === linkName))
    return { cls: "st-shadowed", text: "被遮蔽" };
  if ((state.status.links || []).some((l) => l.name === linkName))
    return { cls: "st-linked", text: "已链接" };
  return { cls: "", text: "未链接" };
}

async function load() {
  try { state.status = await api("GET", "/api/status"); }
  catch (e) { banner("加载失败：" + e.message, true); return; }
  const repos = state.status.repos || [];
  const entries = await Promise.all(repos.map(async (r) => {
    try { return [r.name, (await api("GET", "/api/skills?repo=" + encodeURIComponent(r.name))) || []]; }
    catch { return [r.name, []]; }
  }));
  state.skillsByRepo = Object.fromEntries(entries);
  renderStats(); renderRepos(); renderTarget(); renderProjects(); renderSkills(); renderSummary(); loadAutostart();
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

function renderTarget() {
  const sel = $("#target"); const prev = sel.value; sel.innerHTML = "";
  targetOptions().forEach((o) => sel.append(ce("option", { value: o, textContent: o })));
  if (prev && targetOptions().includes(prev)) sel.value = prev;
  sel.onchange = () => { renderSkills(); };
}

function renderProjects() {
  const ul = $("#project-list"); ul.innerHTML = "";
  (state.status.projects || []).forEach((p) => {
    const li = ce("li");
    li.append(ce("span", { className: "path", textContent: p }));
    const rm = ce("button", { className: "danger small", textContent: "移除" });
    rm.onclick = async () => { await api("DELETE", "/api/projects", { path: p }); await apply(); };
    li.append(rm); ul.append(li);
  });
}

function renderSkills() {
  const root = $("#skills"); root.innerHTML = "";
  const repos = state.status.repos || [];
  const term = state.search.trim().toLowerCase();
  if (repos.length === 0) { root.append(ce("div", { className: "empty", textContent: "无仓库" })); return; }

  let anyShown = false;
  repos.forEach((repo) => {
    let skills = state.skillsByRepo[repo.name] || [];
    if (term) skills = skills.filter((s) => s.linkName.toLowerCase().includes(term) || (s.description || "").toLowerCase().includes(term));
    if (term && skills.length === 0) return;
    anyShown = true;

    const collapsed = state.collapsed.has(repo.name);
    const group = ce("div", { className: "group" + (collapsed ? " collapsed" : "") });

    const head = ce("div", { className: "group-head" });
    head.append(ce("span", { className: "caret", textContent: "▾" }));
    head.append(ce("span", { className: "group-title", textContent: repo.name }));
    head.append(ce("span", { className: "badge count", textContent: skills.length + " skill" }));
    head.append(ce("span", { className: "group-spacer" }));
    const follow = enabledFollow(repo.name);
    const fbtn = ce("button", { className: (follow ? "" : "ghost") + " small", textContent: follow ? "🔄 跟随中" : "全选并跟随" });
    fbtn.onclick = async (e) => {
      e.stopPropagation();
      if (follow) await api("DELETE", "/api/enabled", { skill: repo.name + "/*", target: currentTarget() });
      else await api("POST", "/api/enabled", { skill: repo.name + "/*", target: currentTarget(), mode: "follow" });
      await apply();
    };
    head.append(fbtn);
    head.onclick = () => {
      if (collapsed) state.collapsed.delete(repo.name); else state.collapsed.add(repo.name);
      group.classList.toggle("collapsed");
    };
    group.append(head);

    const body = ce("div", { className: "group-body" });
    if (skills.length === 0) body.append(ce("div", { className: "empty", textContent: "此仓暂无 skill（可能尚未同步，点“立即更新”）" }));
    skills.forEach((sk) => body.append(skillCard(repo.name, sk, follow)));
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
  const b = skillBadge(sk.linkName);
  r1.append(ce("span", { className: "badge " + b.cls, textContent: b.text }));
  const detail = ce("button", { className: "skill-detail-btn", textContent: "详情" });
  detail.onclick = () => openDetail(repo, sk.linkName);
  r1.append(detail);
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
    const msg = c.kind === "collision"
      ? "撞名 " + c.linkName + "（多个仓，需起别名）"
      : "遮蔽 " + c.linkName + "（全局与项目同名，项目被遮蔽）";
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
$("#add-project").onsubmit = async (e) => {
  e.preventDefault();
  const path = $("#project-path").value.trim();
  try { await api("POST", "/api/projects", { path }); $("#project-path").value = ""; await load(); }
  catch (err) { banner("登记失败：" + err.message, true); }
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
