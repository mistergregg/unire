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

async function openNew() {
  stopPolling();
  show("new");
  const [servers, agents, workspaces] = await Promise.all([
    api("GET", "/api/servers"), api("GET", "/api/agents"), api("GET", "/api/workspaces")]);
  serverList = servers; workspaceList = workspaces; selected = []; lastResults = [];
  $("new-form").reset();
  $("n-agent").replaceChildren(...agents.map((a) => el("option", { value: a }, a)));
  $("n-server").replaceChildren(...servers.map((s) => el("option", { value: s.id }, s.name)));
  fillWorkspaceSelect();
  $("n-results").replaceChildren(servers.length ? "" : el("p", { class: "muted" }, "Add a git server first (Git servers tab)."));
  renderSelected();
}

function fillWorkspaceSelect() {
  $("n-workspace").replaceChildren(
    el("option", { value: "" }, workspaceList.length ? "None" : "No saved workspaces yet"),
    ...workspaceList.map((w) => el("option", { value: w.id }, w.name + " (" + w.repos.length + " repos)")));
}

$("n-workspace").addEventListener("change", () => {
  const w = workspaceList.find((x) => x.id === $("n-workspace").value);
  if (!w) return;
  selected = w.repos.map((r) => ({ ...r, defaultBranch: "" }));
  renderSelected(); renderResults();
});

const key = (r) => r.serverId + "/" + r.fullName;

function renderSelected() {
  $("n-selected").replaceChildren(...(selected.length ? selected.map((r, i) => {
    const base = el("input", { placeholder: (r.defaultBranch || "default") + " (base branch)", value: r.base || "", maxlength: "100", "aria-label": "Base branch for " + r.fullName });
    base.addEventListener("input", () => { selected[i].base = base.value.trim(); });
    const remove = el("button", { type: "button", class: "danger" }, "Remove");
    remove.addEventListener("click", () => { selected.splice(i, 1); renderSelected(); renderResults(); });
    const srv = serverList.find((s) => s.id === r.serverId);
    return el("div", { class: "item" },
      el("div", { class: "item-head" }, el("span", { class: "name" }, r.fullName), el("span", { class: "badge" }, srv ? srv.name : r.serverId)),
      el("div", { class: "item-actions" }, base, remove));
  }) : [el("p", { class: "muted" }, "No repos chosen yet.")]));
}

function renderResults() {
  $("n-results").replaceChildren(...(lastResults.length ? lastResults.map((r) => {
    const already = selected.some((s) => key(s) === key(r));
    const add = el("button", { type: "button" }, already ? "Added" : "Add");
    add.disabled = already;
    add.addEventListener("click", () => {
      selected.push({ serverId: r.serverId, fullName: r.fullName, cloneUrl: r.cloneUrl, base: "", defaultBranch: r.defaultBranch });
      renderSelected(); renderResults();
    });
    return el("div", { class: "item" },
      el("div", { class: "item-head" },
        el("span", { class: "name" }, r.fullName),
        el("span", { class: "badge" }, (r.private ? "private" : "public") + " · " + (r.defaultBranch || "?"))),
      r.description ? el("div", { class: "muted" }, r.description) : "",
      el("div", { class: "item-actions" }, add));
  }) : []));
}

async function searchRepos() {
  const id = $("n-server").value;
  if (!id) throw new Error("Add a git server first.");
  const repos = await api("GET", "/api/servers/" + encodeURIComponent(id) + "/repos?q=" + encodeURIComponent($("n-query").value));
  lastResults = repos.map((r) => ({ ...r, serverId: id }));
  renderResults();
  if (!repos.length) $("n-results").replaceChildren(el("p", { class: "muted" }, "No matching repos."));
}
$("n-search").addEventListener("click", () => guarded($("n-search"), searchRepos));
$("n-query").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); guarded($("n-search"), searchRepos); } });

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
      task: $("n-task").value, agent: $("n-agent").value, prompt: $("n-prompt").value, repos: refsOf(),
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
  $("s-meta").textContent = meta.agent + " · " + meta.mode + " · started " + ago(meta.startedAt || meta.createdAt);
  $("s-error").hidden = !error;
  $("s-error").textContent = error || "";
  $("s-stop").hidden = isFinished(status);
  $("s-delete").hidden = !isFinished(status);
}

function appendLog(events) {
  const log = $("s-log");
  const nearBottom = window.innerHeight + window.scrollY >= document.body.scrollHeight - 80;
  for (const e of events) log.append(el("div", { class: "l-" + e.kind }, e.kind === "log" ? "· " + e.text : e.text));
  if (nearBottom && events.length) window.scrollTo(0, document.body.scrollHeight);
}

async function openSession(id) {
  stopPolling();
  show("session");
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
