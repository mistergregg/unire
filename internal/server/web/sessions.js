"use strict";

// Sessions pages: the list, the New session form with its repo picker, and
// a session's live log. Uses the helpers from app.js.

let pollTimer = null;
let pollToken = null;
function stopPolling() { clearTimeout(pollTimer); pollTimer = null; pollToken = null; }
function schedule(fn, ms) { clearTimeout(pollTimer); pollTimer = setTimeout(fn, ms); }

function statusBadge(status) { return el("span", { class: "badge st-" + status }, status); }
const isFinished = (status) => status !== "queued" && status !== "running";

// ---- the list of conversations (the sidebar) ----
// The Chats page is one page: every conversation in a list on the left, and the
// open one (or the new-chat form) on the right. The list is refreshed every few
// seconds by attention.js, and right away when something changes.
let sideList = [];       // the last list from the server
let selectedRoot = null; // the conversation open on the right

async function openSessions() {
  stopPolling();
  selectedRoot = null;
  setHash("");
  show("sessions");
  await refreshSidebar();
}

async function refreshSidebar() {
  try { renderSidebar(await api("GET", "/api/sessions")); } catch (e) { banner(e.message, true); }
}

// Waiting on you first, then running, then the rest, newest first.
function sidebarRank(s) {
  const st = s.latestStatus || s.status;
  return s.waiting ? 0 : (st === "running" || st === "queued") ? 1 : 2;
}

function renderSidebar(list) {
  sideList = list;
  const sorted = [...list].sort((a, b) => sidebarRank(a) - sidebarRank(b) || (b.createdAt - a.createdAt));
  $("session-list").replaceChildren(...(sorted.length ? sorted.map(sessionRow)
    : [el("p", { class: "muted" }, "No chats yet. Add a git server, then press New chat.")]));
}

function sessionRow(s) {
  const open = () => guarded(null, () => openSession(s.id));
  const status = s.latestStatus || s.status;
  const row = el("div", { class: "side-item" + (s.id === selectedRoot ? " selected" : ""), role: "button", tabindex: "0" },
    el("div", { class: "item-head" }, el("span", { class: "name" }, s.task),
      s.waiting ? el("span", { class: "badge st-attention" }, "needs you") : statusBadge(status)),
    el("div", { class: "muted small-line" }, s.agent + " · " + s.repos.map((r) => r.fullName.split("/").pop()).join(", ")),
    el("div", { class: "muted small-line" }, ago(s.startedAt || s.createdAt)));
  row.addEventListener("click", open);
  row.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
  return row;
}

// The address shows which chat is open, so a refresh or a link comes back to it.
function setHash(id) {
  try { history.replaceState(null, "", id ? "#c=" + encodeURIComponent(id) : location.pathname + location.search); } catch (_) { /* not important */ }
}

$("new-session").addEventListener("click", () => guarded(null, openNew));
$("new-back").addEventListener("click", () => guarded(null, openSessions));
$("s-back").addEventListener("click", () => guarded(null, openSessions));

// ---- new session ----
let selected = []; // { serverId, fullName, cloneUrl, base, defaultBranch }
let serverList = [];
let workspaceList = [];
let lastResults = [];

// prefill (optional): { task, repos, note } for continuing an earlier session.
async function openNew(prefill) {
  stopPolling();
  selectedRoot = null;
  setHash("");
  show("new");
  refreshSidebar();
  $("n-note").hidden = true;
  const [servers, agents, workspaces, net] = await Promise.all([
    api("GET", "/api/servers"), api("GET", "/api/agents"), api("GET", "/api/workspaces"), api("GET", "/api/network")]);
  serverList = servers; workspaceList = workspaces; selected = []; lastResults = []; networkInfo = net;
  $("new-form").reset();
  const def = net.modes.find((m) => m.id === net.mode);
  $("n-network").replaceChildren(
    el("option", { value: "" }, "Default: " + (def ? def.label : net.mode)),
    ...net.modes.map((m) => el("option", { value: m.id }, m.label)));
  updateNetworkDesc();
  $("n-agent").replaceChildren(...agents.map((a) => el("option", { value: a.id },
    a.label + (a.loggedIn === false ? " (not signed in)" : ""))));
  $("n-server").replaceChildren(...servers.map((s) => el("option", { value: s.id }, s.name)));
  $("n-server").hidden = servers.length < 2; // nothing to choose with one server
  fillWorkspaceSelect();
  $("n-results").replaceChildren(); $("n-results").hidden = true;
  $("n-query").disabled = !servers.length;
  $("n-query").placeholder = servers.length ? "Add a repo: search your git servers" : "Add a git server first (Git servers tab)";
  if (prefill) {
    $("n-task").value = prefill.task;
    selected = prefill.repos;
    if (prefill.note) { $("n-note").textContent = prefill.note; $("n-note").hidden = false; }
  }
  renderSelected();
}

function updateNetworkDesc() {
  const id = $("n-network").value || networkInfo.mode;
  const warn = id === "full" ? " Use this only for repos you trust." : "";
  const off = !networkInfo.protection.available && id !== "full" ? " Not available right now: " + networkInfo.protection.reason + "." : "";
  $("n-network-desc").textContent = modeText(id) + warn + off;
  $("n-network-desc").className = id === "full" || off ? "err" : "muted";
}
$("n-network").addEventListener("change", updateNetworkDesc);

function fillWorkspaceSelect() {
  $("n-workspace").hidden = !workspaceList.length;
  $("n-workspace").replaceChildren(
    el("option", { value: "" }, "Load a saved workspace…"),
    ...workspaceList.map((w) => el("option", { value: w.id }, w.name + " (" + w.repos.length + " repos)")));
}

$("n-workspace").addEventListener("change", () => {
  const w = workspaceList.find((x) => x.id === $("n-workspace").value);
  if (!w) return;
  selected = w.repos.map((r) => ({ ...r, defaultBranch: "" }));
  renderSelected(); renderResults();
});

const key = (r) => r.serverId + "/" + r.fullName;

// The chosen repos, as small chips inside the prompt box. The branch field is
// optional: empty means the repo's default branch.
function renderSelected() {
  $("n-selected").hidden = !selected.length;
  $("n-selected").replaceChildren(...selected.map((r, i) => {
    const base = el("input", { placeholder: r.defaultBranch || "branch", value: r.base || "", maxlength: "100", title: "Base branch (empty: the default)", "aria-label": "Base branch for " + r.fullName });
    base.addEventListener("input", () => { selected[i].base = base.value.trim(); });
    const remove = el("button", { type: "button", class: "ghost", title: "Remove", "aria-label": "Remove " + r.fullName }, "×");
    remove.addEventListener("click", () => { selected.splice(i, 1); renderSelected(); renderResults(); });
    return el("span", { class: "chip" }, el("span", { class: "name" }, r.fullName), base, remove);
  }));
}

// Search results open under the search field and close when it loses focus.
let resultsOpen = false;
function renderResults() {
  const box = $("n-results");
  box.replaceChildren(...lastResults.map((r) => {
    const already = selected.some((s) => key(s) === key(r));
    const row = el("button", { type: "button", class: "result" },
      el("span", { class: "name" }, r.fullName),
      el("span", { class: "muted" }, (already ? "added" : (r.private ? "private" : "public")) + (r.description ? " · " + r.description : "")));
    row.disabled = already;
    row.addEventListener("click", () => {
      selected.push({ serverId: r.serverId, fullName: r.fullName, cloneUrl: r.cloneUrl, base: "", defaultBranch: r.defaultBranch });
      renderSelected(); renderResults();
    });
    return row;
  }));
  box.hidden = !lastResults.length || !resultsOpen;
}
$("n-results").addEventListener("mousedown", (e) => e.preventDefault()); // keep the search field focused while clicking

async function searchRepos() {
  const id = $("n-server").value;
  if (!id) throw new Error("Add a git server first.");
  const repos = await api("GET", "/api/servers/" + encodeURIComponent(id) + "/repos?q=" + encodeURIComponent($("n-query").value));
  lastResults = repos.map((r) => ({ ...r, serverId: id }));
  renderResults();
  if (!repos.length && resultsOpen) {
    $("n-results").replaceChildren(el("p", { class: "muted" }, "No matching repos."));
    $("n-results").hidden = false;
  }
}
// Search as you type (and once on opening the box), so there is no Search button.
let searchTimer = null;
const searchSoon = (ms) => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => searchRepos().catch((e) => banner(e.message, true)), ms);
};
$("n-query").addEventListener("input", () => searchSoon(300));
$("n-query").addEventListener("focus", () => { resultsOpen = true; renderResults(); if (!lastResults.length) searchSoon(0); });
$("n-query").addEventListener("blur", () => { resultsOpen = false; $("n-results").hidden = true; });
$("n-query").addEventListener("keydown", (e) => { if (e.key === "Enter") e.preventDefault(); });
$("n-server").addEventListener("change", () => searchSoon(0));

const refsOf = () => selected.map(({ serverId, fullName, cloneUrl, base }) => ({ serverId, fullName, cloneUrl, base }));

$("n-ws-save").addEventListener("click", () => guarded($("n-ws-save"), async () => {
  const name = $("n-ws-name").value.trim();
  if (!name) throw new Error("Give the workspace a name.");
  if (!selected.length) throw new Error("Choose at least one repo first.");
  await api("POST", "/api/workspaces", { name, repos: refsOf() });
  workspaceList = await api("GET", "/api/workspaces");
  fillWorkspaceSelect();
  $("n-ws-name").value = "";
  banner("Workspace saved.");
}));

$("new-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("n-submit"), async () => {
    if (!selected.length) throw new Error("Choose at least one repo.");
    const s = await api("POST", "/api/sessions", {
      task: $("n-task").value, agent: $("n-agent").value, prompt: $("n-prompt").value,
      network: $("n-network").value, memory: $("n-memory").checked, repos: refsOf(),
    });
    await openSession(s.id);
  });
});
