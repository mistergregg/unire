"use strict";

// Sessions pages: the list, the New session form with its repo picker, and
// a session's live log. Uses the helpers from app.js.

let pollTimer = null;
let pollToken = null;
function stopPolling() { clearTimeout(pollTimer); pollTimer = null; pollToken = null; }
function schedule(fn, ms) { clearTimeout(pollTimer); pollTimer = setTimeout(fn, ms); }

function statusBadge(status) { return el("span", { class: "badge st-" + status }, status); }
const isFinished = (status) => status !== "queued" && status !== "running";

// ---- list ----
async function openSessions() {
  stopPolling();
  show("sessions");
  const token = (pollToken = {});
  const refresh = async () => {
    if (pollToken !== token || currentView !== "sessions") return;
    try {
      const list = await api("GET", "/api/sessions");
      if (pollToken !== token || currentView !== "sessions") return;
      $("session-list").replaceChildren(...(list.length ? list.map(sessionRow)
        : [el("p", { class: "muted" }, "No sessions yet. Add a git server, then press New session.")]));
    } catch (e) { banner(e.message, true); }
    schedule(refresh, 4000);
  };
  await refresh();
}

function sessionRow(s) {
  const open = () => openSession(s.id);
  const row = el("div", { class: "item click", role: "button", tabindex: "0" },
    el("div", { class: "item-head" }, el("span", { class: "name" }, s.task), statusBadge(s.status)),
    el("div", { class: "muted" }, s.agent + " · " + s.repos.map((r) => r.fullName).join(", ")),
    el("div", { class: "muted" }, "started " + ago(s.startedAt || s.createdAt)));
  row.addEventListener("click", open);
  row.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
  return row;
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
  show("new");
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

// ---- one session ----
let currentSession = null;

function renderSessionHeader(s) {
  currentSession = s;
  $("s-title").textContent = s.task;
  $("s-prompt").textContent = s.prompt;
  $("s-repos").replaceChildren(...s.repos.map((r) => el("div", { class: "muted" },
    el("code", {}, r.fullName + (r.base ? "@" + r.base : "")), " → ", el("code", {}, s.branch))));
  setSessionStatus(s.status, s.error, s);
}

function setSessionStatus(status, error, s) {
  const badge = $("s-status");
  badge.className = "badge st-" + status;
  badge.textContent = status;
  const meta = currentSession || s;
  $("s-meta").textContent = meta.agent + " · " + meta.mode + " · network: " + meta.network + " · started " + ago(meta.startedAt || meta.createdAt);
  $("s-error").hidden = !error;
  $("s-error").textContent = error || "";
  $("s-stop").hidden = isFinished(status);
  $("s-delete").hidden = !isFinished(status);
  $("s-continue").hidden = !isFinished(status);
  if (isFinished(status) && currentSession && summaryShownFor !== currentSession.id) loadSummary(currentSession.id);
}

// The summary is saved just before a session is marked finished, so it can be
// fetched as soon as the status says so.
let summaryShownFor = null;
async function loadSummary(id) {
  summaryShownFor = id;
  const wrap = $("s-summary-wrap");
  wrap.hidden = true;
  try {
    const x = await api("GET", "/api/memory/summaries/" + encodeURIComponent(id));
    if (currentSession && currentSession.id !== id) return;
    $("s-summary").textContent = x.summary;
    $("s-summary-note").textContent = x.source === "agent"
      ? "Written by the agent when it finished. It is saved as memory for these repos."
      : "Unire wrote this because the agent did not. It is saved as memory for these repos.";
    wrap.hidden = false;
  } catch (_) { /* memory was off for this session, or none was saved */ }
}

$("s-continue").addEventListener("click", () => guarded($("s-continue"), async () => {
  const s = await api("GET", "/api/sessions/" + encodeURIComponent(currentSession.id));
  const pushed = new Set(s.pushed || []);
  const repos = s.repos.map((r) => (pushed.has(r.fullName) ? { ...r, base: s.branch, defaultBranch: s.branch } : { ...r, defaultBranch: "" }));
  const note = pushed.size
    ? "The base branch is set to this session's branch (" + s.branch + ") for the repos where it pushed one. Choose any agent; it starts with the project memory."
    : "That session pushed nothing, so the new one starts from the original base branches.";
  await openNew({ task: "Continue: " + s.task, repos, note });
}));

// One log line. A blocked connection gets an Allow button that adds the
// address to the owner's allowed list for future sessions.
function logLine(e) {
  if (e.kind === "net") return el("div", { class: "l-log" }, "· connected to " + e.text);
  if (e.kind === "blocked") {
    const m = /^blocked (\S+) \(/.exec(e.text);
    const line = el("div", { class: "l-blocked" }, "⛔ " + e.text + " ");
    if (m) {
      const btn = el("button", { type: "button", class: "small" }, "Allow " + m[1]);
      btn.addEventListener("click", () => guarded(btn, async () => {
        await api("POST", "/api/network/allow", { address: m[1] });
        btn.textContent = "Allowed for future sessions";
        btn.disabled = true;
      }));
      line.append(btn);
    }
    return line;
  }
  return el("div", { class: "l-" + e.kind }, e.kind === "log" ? "· " + e.text : e.text);
}

function appendLog(events) {
  const log = $("s-log");
  const nearBottom = window.innerHeight + window.scrollY >= document.body.scrollHeight - 80;
  for (const e of events) log.append(logLine(e));
  if (nearBottom && events.length) window.scrollTo(0, document.body.scrollHeight);
}

async function openSession(id) {
  stopPolling();
  show("session");
  summaryShownFor = null;
  $("s-summary-wrap").hidden = true;
  $("s-log").replaceChildren();
  const token = (pollToken = {});
  let after = 0;
  renderSessionHeader(await api("GET", "/api/sessions/" + encodeURIComponent(id)));

  const tick = async () => {
    if (pollToken !== token || currentView !== "session") return;
    let more = false;
    try {
      const r = await api("GET", "/api/sessions/" + encodeURIComponent(id) + "/logs?after=" + after);
      if (pollToken !== token) return;
      appendLog(r.events);
      if (r.events.length) after = r.events[r.events.length - 1].id;
      more = r.events.length === 500;
      setSessionStatus(r.status, r.error);
      if (r.finished && !more) return; // done: stop polling
    } catch (e) { banner(e.message, true); }
    schedule(tick, more ? 0 : 1000);
  };
  await tick();
}

$("s-stop").addEventListener("click", () => guarded($("s-stop"), async () => {
  await api("POST", "/api/sessions/" + encodeURIComponent(currentSession.id) + "/stop");
  banner("Stopping…");
}));
$("s-delete").addEventListener("click", () => {
  if (!confirm("Delete this session and its log?")) return;
  guarded($("s-delete"), async () => {
    await api("DELETE", "/api/sessions/" + encodeURIComponent(currentSession.id));
    await openSessions();
  });
});
