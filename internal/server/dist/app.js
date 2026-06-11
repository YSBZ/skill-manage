"use strict";
const TOKEN = document.querySelector('meta[name="sm-token"]').content;
const $ = (s, el = document) => el.querySelector(s);
const ce = (t, props = {}) => Object.assign(document.createElement(t), props);

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

const state = { status: null, selected: null, skills: [] };

function banner(msg, isErr) {
  const b = $("#banner");
  if (!msg) { b.classList.add("hidden"); return; }
  b.textContent = msg;
  b.className = "banner" + (isErr ? " err" : "");
}

function targetOptions() {
  const opts = ["~/.claude/skills/"];
  (state.status.projects || []).forEach((p) => opts.push(p.replace(/\/$/, "") + "/.claude/skills"));
  return opts;
}

async function load() {
  try { state.status = await api("GET", "/api/status"); }
  catch (e) { banner("加载失败：" + e.message, true); return; }
  renderRepos(); renderProjects(); renderRight(); renderSummary(); loadAutostart();
  banner((state.status.repos || []).length === 0 ? "还没有仓库。在左侧添加一个 git skill 仓开始。" : "");
}

function badge(st) { return ce("span", { className: "badge " + st, textContent: st }); }

function renderRepos() {
  const ul = $("#repo-list"); ul.innerHTML = "";
  (state.status.repos || []).forEach((repo) => {
    const li = ce("li");
    if (state.selected === repo.name) li.classList.add("selected");
    const top = ce("div", { className: "repo-top" });
    const left = ce("div");
    left.append(ce("div", { className: "repo-name", textContent: repo.name }),
                ce("div", { className: "repo-url", textContent: repo.url }));
    top.append(left, badge(repo.state || "never-synced"));
    top.onclick = () => selectRepo(repo.name);
    li.append(top);
    if (repo.error) li.append(ce("div", { className: "err-detail", textContent: repo.error }));
    const actions = ce("div", { className: "row", style: "margin-top:8px" });
    const rm = ce("button", { className: "danger small", textContent: "移除" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!confirm("移除仓库 " + repo.name + "？其链接会在下次应用时清理。")) return;
      await api("DELETE", "/api/repos", { url: repo.url });
      if (state.selected === repo.name) state.selected = null;
      await apply();
    };
    actions.append(rm); li.append(actions);
    ul.append(li);
  });
}

function renderProjects() {
  const ul = $("#project-list"); ul.innerHTML = "";
  (state.status.projects || []).forEach((p) => {
    const li = ce("li");
    const row = ce("div", { className: "skill-row" });
    row.append(ce("span", { textContent: p }));
    const rm = ce("button", { className: "danger small", textContent: "移除" });
    rm.onclick = async () => { await api("DELETE", "/api/projects", { path: p }); await apply(); };
    row.append(rm); li.append(row); ul.append(li);
  });
}

const enabledFollow = (repo, target) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/*" && e.target === target);
const enabledSnapshot = (repo, link, target) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/" + link && e.target === target);
const createdThisCycle = (name) => {
  const s = state.status.lastSummary;
  return s && s.created && s.created.some((c) => c.name === name);
};

async function selectRepo(name) {
  state.selected = name;
  try { state.skills = (await api("GET", "/api/skills?repo=" + encodeURIComponent(name))) || []; }
  catch { state.skills = []; }
  renderRepos(); renderRight();
}

const currentTarget = () => { const sel = $("#target"); return sel && sel.value ? sel.value : "~/.claude/skills/"; };

function renderRight() {
  const title = $("#right-title"), modeRow = $("#repo-mode-row"),
        targetRow = $("#target-row"), ul = $("#skill-list");
  modeRow.innerHTML = ""; ul.innerHTML = "";
  if (!state.selected) { title.textContent = "选择左侧一个仓库"; targetRow.classList.add("hidden"); return; }
  title.textContent = state.selected;
  targetRow.classList.remove("hidden");

  const sel = $("#target"); const prev = sel.value; sel.innerHTML = "";
  targetOptions().forEach((o) => sel.append(ce("option", { value: o, textContent: o })));
  if (prev && targetOptions().includes(prev)) sel.value = prev;
  sel.onchange = renderRight;
  const target = currentTarget();

  const follow = enabledFollow(state.selected, target);
  const fbtn = ce("button", { className: follow ? "" : "ghost", textContent: follow ? "🔄 跟随中（点此取消）" : "全选并跟随" });
  fbtn.onclick = async () => {
    if (follow) await api("DELETE", "/api/enabled", { skill: state.selected + "/*", target });
    else await api("POST", "/api/enabled", { skill: state.selected + "/*", target, mode: "follow" });
    await apply();
  };
  modeRow.append(fbtn);
  if (follow) modeRow.append(ce("span", { className: "follow-pill", textContent: "上游新增会自动链接" }));

  if (state.skills.length === 0) {
    ul.append(ce("li", { className: "muted", textContent: "此仓暂无 skill（可能尚未同步，点“立即更新全部”）" }));
    return;
  }
  state.skills.forEach((sk) => {
    const li = ce("li");
    const row = ce("div", { className: "skill-row" });
    const left = ce("div", { className: "skill-name" });
    const cb = ce("input", { type: "checkbox" });
    cb.checked = follow || enabledSnapshot(state.selected, sk.linkName, target);
    cb.disabled = follow; // follow links everything
    cb.onchange = async () => {
      if (cb.checked) await api("POST", "/api/enabled", { skill: state.selected + "/" + sk.linkName, target, mode: "snapshot" });
      else await api("DELETE", "/api/enabled", { skill: state.selected + "/" + sk.linkName, target });
      await apply();
    };
    left.append(cb, ce("span", { textContent: sk.linkName }));
    if (sk.logicalName !== sk.linkName) left.append(ce("span", { className: "muted", textContent: "(" + sk.logicalName + ")" }));
    if (createdThisCycle(sk.linkName)) left.append(ce("span", { className: "tag-new", textContent: "new" }));
    row.append(left); li.append(row); ul.append(li);
  });
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

async function apply() {
  try { await api("POST", "/api/apply"); }
  catch (e) { banner("应用失败：" + e.message, true); }
  await load();
  if (state.selected) await selectRepo(state.selected);
}

async function updateNow(force) {
  banner("同步中…");
  try { await api("POST", "/api/update-now", { force: !!force }); banner(""); }
  catch (e) { banner("同步失败：" + e.message, true); }
  await load();
  if (state.selected) await selectRepo(state.selected);
}

async function loadAutostart() {
  try {
    const a = await api("GET", "/api/autostart");
    const el = $("#autostart"); el.checked = a.registered; el.disabled = !a.supported;
  } catch { /* ignore */ }
}

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

load();
