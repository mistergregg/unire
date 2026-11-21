"use strict";

// Sessions pages: the list, the New session form with its repo picker, and
// a session's live log. Uses the helpers from app.js.

let pollToken = null;
function stopPolling() { pollToken = null; }

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

// One list, the chat you acted on last first. The server says when that was (lastActive:
// a message you sent or an answer you gave, a reply, or its agent finishing). A chat whose
// agent is working has the pulsing blue dot, and one that waits for you says so, wherever
// it is in the list.
const lastTouched = (s) => s.lastActive || s.startedAt || s.createdAt;


let sideSignature = "";

function renderSidebar(list) {
  sideList = list;
  const q = ($("side-search").value || "").trim().toLowerCase();
  const shown = !q ? list : list.filter((s) =>
    (s.task + " " + s.agent + " " + s.repos.map((r) => r.fullName).join(" ")).toLowerCase().includes(q));
  const sorted = [...shown].sort((a, b) => lastTouched(b) - lastTouched(a) || b.createdAt - a.createdAt);
  // Draw again only when something changed, so the little lights keep pulsing
  // instead of starting over every few seconds.
  const sig = q + "|" + selectedRoot + "|" + Math.floor(Date.now() / 60000) + "|" +
    sorted.map((s) => [s.id, s.task, s.status, s.latestStatus, s.waiting, s.lastActive].join(":")).join(",");
  if (sig === sideSignature) return;
  sideSignature = sig;
  $("session-list").replaceChildren(...(sorted.length ? sorted.map(sessionRow)
    : [el("p", { class: "muted" }, q ? "No chats match." : "No chats yet. Add a git server, then press New chat.")]));
}
$("side-search").addEventListener("input", () => { sideSignature = ""; renderSidebar(sideList); });

function sessionRow(s) {
  const open = () => guarded(null, () => openSession(s.id));
  const status = s.latestStatus || s.status;
  const light = s.waiting ? "needs" : (status === "running" || status === "queued") ? "live"
    : status === "done" ? "done" : (status === "failed" || status === "interrupted") ? "failed" : "";
  const row = el("div", { class: "side-item " + light + (s.id === selectedRoot ? " selected" : ""), role: "button", tabindex: "0" },
    el("span", { class: "dot" }),
    el("div", {},
      el("div", { class: "item-head" }, el("span", { class: "name", title: s.task }, s.task), el("span", { class: "when" }, ago(lastTouched(s)))),
      el("div", { class: "muted small-line" }, s.agent + " · " + s.repos.map((r) => r.fullName.split("/").pop()).join(", ")),
      s.waiting ? el("span", { class: "badge st-attention" }, "needs you") : ""));
  row.addEventListener("click", open);
  row.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
  return row;
}

// The address shows which chat is open, so a refresh or a link comes back to it.
function setHash(id) {
  try { history.replaceState(null, "", id ? "#c=" + encodeURIComponent(id) : location.pathname + location.search); } catch (_) { /* not important */ }
}

$("new-session").addEventListener("click", () => guarded(null, openNew));
$("empty-new").addEventListener("click", () => guarded(null, openNew));
$("new-back").addEventListener("click", () => guarded(null, openSessions));
$("s-back").addEventListener("click", () => guarded(null, openSessions));
document.querySelectorAll(".page-back").forEach((b) => b.addEventListener("click", () => guarded(null, openSessions)));

// ---- new session ----
let selected = []; // { serverId, fullName, cloneUrl, base, defaultBranch }
let serverList = [];
let workspaceList = [];
let lastResults = [];
let forkFrom = ""; // the conversation the New chat form starts from, if any

// prefill (optional): { task, repos, note } for continuing an earlier session.
async function openNew(prefill) {
  stopPolling();
  selectedRoot = null;
  setHash("");
  $("n-submit").disabled = false;
  show("new");
  refreshSidebar();
  $("n-note").hidden = true;
  forkFrom = (prefill && prefill.forkFrom) || "";
  const [servers, agents, workspaces, net] = await Promise.all([
    api("GET", "/api/servers"), api("GET", "/api/agents"), api("GET", "/api/workspaces"), api("GET", "/api/network")]);
  serverList = servers; workspaceList = workspaces; selected = []; lastResults = []; networkInfo = net;
  $("new-form").reset();
  newAttach.clear();
  $("n-prompt").style.height = "";
  const def = net.modes.find((m) => m.id === net.mode);
  $("n-network").replaceChildren(
    el("option", { value: "" }, "Default: " + (def ? def.label : net.mode)),
    ...net.modes.map((m) => el("option", { value: m.id }, m.label)));
  updateNetworkDesc();
  $("n-agent").replaceChildren(...agents.map((a) => el("option", { value: a.id },
    a.label + (a.loggedIn === false ? " (not signed in)" : ""))));
  newAgents = agents;
  newModel.fill(agents.find((a) => a.id === $("n-agent").value), "");
  $("n-server").replaceChildren(...servers.map((s) => el("option", { value: s.id }, s.name)));
  $("n-server").hidden = servers.length < 2; // nothing to choose with one server
  fillWorkspaceSelect();
  $("n-results").replaceChildren(); $("n-results").hidden = true;
  $("n-query").disabled = !servers.length;
  $("n-parent").replaceChildren(el("option", { value: "" }, "None"), ...sideList.map((c) => el("option", { value: c.id }, c.task)));
  $("n-query").placeholder = servers.length ? "Add a repo: search your git servers" : "Add a git server first (Git servers tab)";
  if (prefill) {
    $("n-task").value = prefill.task;
    selected = prefill.repos;
    if (prefill.note) { $("n-note").textContent = prefill.note; $("n-note").hidden = false; }
  }
  renderSelected();
  $("n-suggest").hidden = true;
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
    remove.addEventListener("click", () => { selected.splice(i, 1); renderSelected(); renderResults(); suggestSoon(); });
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

// The model for the first message follows the agent picked for it.
let newAgents = [];
const newModel = modelPicker($("n-model"), $("n-model-name"));
$("n-agent").addEventListener("change", () => newModel.fill(newAgents.find((a) => a.id === $("n-agent").value), ""));

// Files attached to the first message.
const newAttach = attachPicker($("n-files"), $("n-attach"), $("n-prompt"), $("n-previews"));

$("new-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("n-submit"), async () => {
    const s = await api("POST", "/api/sessions", {
      task: $("n-task").value, agent: $("n-agent").value, model: newModel.value(), prompt: $("n-prompt").value,
      network: $("n-network").value, memory: $("n-memory").checked, repos: refsOf(),
      forkFrom, parent: $("n-parent").value, images: newAttach.list(),
    });
    newAttach.clear();
    await openSession(s.id);
  });
});

// ---- repo suggestions for what you are writing ----
// Leave the repos empty and Unire picks them from your message (the repos it names,
// and those usually used with them); here it says which, and offers the near misses.
let suggestTimer = null;
let suggestSeq = 0;
function suggestSoon() {
  clearTimeout(suggestTimer);
  suggestTimer = setTimeout(suggestRepos, 600);
}
$("n-prompt").addEventListener("input", suggestSoon);

// Grow the prompt box with its text, up to a limit; it scrolls after that.
function autoGrowPrompt() {
  const box = $("n-prompt");
  box.style.height = "auto";
  box.style.height = Math.min(Math.max(box.scrollHeight, 132), 360) + "px";
}
$("n-prompt").addEventListener("input", autoGrowPrompt);
$("n-prompt").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
    e.preventDefault();
    $("new-form").requestSubmit();
  }
});

async function suggestRepos() {
  const box = $("n-suggest");
  const text = $("n-prompt").value.trim();
  if (!serverList.length || text.length < 4) { box.hidden = true; return; }
  const seq = ++suggestSeq;
  let res;
  try { res = await api("POST", "/api/repos/suggest", { prompt: text, have: refsOf() }); } catch (_) { return; }
  if (seq !== suggestSeq) return;
  renderSuggest(res.candidates);
}

function renderSuggest(cands) {
  const box = $("n-suggest");
  const fresh = cands.filter((c) => !selected.some((s) => key(s) === key({ serverId: c.serverId, fullName: c.fullName })));
  const autos = fresh.filter((c) => c.auto);
  const rest = fresh.filter((c) => !c.auto);
  const add = (c) => {
    selected.push({ serverId: c.serverId, fullName: c.fullName, cloneUrl: c.cloneUrl, base: "", defaultBranch: c.defaultBranch });
    renderSelected();
    suggestSoon();
  };
  const chip = (c, label) => {
    const b = el("button", { type: "button", class: "chip ghost", title: c.why.join("; ") }, label + c.fullName);
    b.addEventListener("click", () => add(c));
    return b;
  };
  const parts = [];
  if (!selected.length && autos.length) {
    parts.push(el("div", { class: "muted" }, "Unire will use ", el("strong", {}, autos.map((c) => c.fullName).join(", ")),
      " (from your message" + (autos.some((c) => c.why.some((w) => w.startsWith("usually"))) ? " and what you usually use together" : "") +
      "). Choose repos yourself to change that."));
  } else if (selected.length && autos.length) {
    parts.push(el("div", { class: "muted" }, "Usually needed with these:"), el("div", { class: "chips" }, ...autos.map((c) => chip(c, "+ "))));
  }
  if (rest.length) {
    parts.push(el("div", { class: "muted" }, !selected.length && !autos.length ? "Did you mean:" : "Maybe also:"),
      el("div", { class: "chips" }, ...rest.slice(0, 4).map((c) => chip(c, "+ "))));
  }
  box.replaceChildren(...parts);
  box.hidden = !parts.length;
}
