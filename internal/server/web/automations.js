"use strict";

// The Automations page: tasks that run by themselves on a schedule. Each automation
// has its own chat (every run is a turn in it) and a short history of its runs and
// skips. Uses helpers from app.js and openSession from chat.js.

let autoAgents = [];
let autoServers = [];
let autoSelected = []; // repos chosen in the form
let autoResults = [];
let autoEditing = "";
// The owner's time zone: a daily time is kept with it, so "09:00" is 9 in the morning where they are.
const BROWSER_TZ = (() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (_) { return ""; } })(); // id of the automation in the form, "" for a new one
const autoModel = modelPicker($("a-model"), $("a-model-name"));

async function openAutomations() {
  stopPolling();
  show("automations");
  const [data, agents, servers] = await Promise.all([
    api("GET", "/api/automations"), api("GET", "/api/agents"), api("GET", "/api/servers")]);
  autoAgents = agents;
  autoServers = servers;
  $("auto-clock").textContent = BROWSER_TZ
    ? "Daily times are in your time zone (" + BROWSER_TZ + "), whatever the server's clock is set to."
    : "Your browser did not say its time zone, so daily times are the server's (" + data.timeZone + ", where it is " + data.serverClock + " now).";
  renderAutomations(data.automations);
  if (!autoEditing && !$("a-prompt").value.trim()) resetAutomationForm();
}

function runsLine(a) {
  const parts = [a.schedule];
  if (a.maxPerDay) parts.push(a.runsToday + " of " + a.maxPerDay + " runs today");
  else parts.push(a.runsToday + " run" + (a.runsToday === 1 ? "" : "s") + " today");
  if (a.maxUsage) parts.push("skips at " + a.maxUsage + "% usage");
  parts.push(a.mayMerge ? "may merge" : "does not merge");
  return parts.join(" · ");
}

function whenText(unix) {
  if (!unix) return "";
  const d = new Date(unix * 1000);
  const today = new Date().toDateString() === d.toDateString();
  return (today ? "today " : d.toLocaleDateString() + " ") + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function renderAutomations(list) {
  const box = $("auto-list");
  if (!list.length) {
    box.replaceChildren(el("p", { class: "muted" }, "No automations yet. Make one below: say what it should do, which repos, and how often."));
    return;
  }
  box.replaceChildren(...list.map(automationCard));
}

function automationCard(a) {
  const agent = autoAgents.find((x) => x.id === a.agent);
  const state = !a.enabled ? el("span", { class: "badge" }, "paused")
    : a.running ? el("span", { class: "badge st-running" }, "running")
    : el("span", { class: "badge st-done" }, "on");
  const next = a.enabled && a.nextRun ? "Next run " + whenText(a.nextRun) : a.enabled ? "Not scheduled" : "Paused";

  const runBtn = el("button", { type: "button", title: "Start a run now (the schedule stays the same)" }, "Run now");
  runBtn.disabled = a.running;
  runBtn.addEventListener("click", () => guarded(runBtn, async () => {
    const r = await api("POST", "/api/automations/" + encodeURIComponent(a.id) + "/run");
    banner("Started. It is in the automation's chat.");
    await openAutomations();
    if (r.sessionId) refreshSidebar();
  }));
  const toggle = el("button", { type: "button" }, a.enabled ? "Pause" : "Turn on");
  toggle.addEventListener("click", () => guarded(toggle, async () => {
    await api("POST", "/api/automations/" + encodeURIComponent(a.id) + "/enabled", { on: !a.enabled });
    await openAutomations();
  }));
  const edit = el("button", { type: "button", class: "ghost" }, "Edit");
  edit.addEventListener("click", () => editAutomation(a));
  const del = el("button", { type: "button", class: "danger" }, "Delete");
  del.addEventListener("click", () => {
    if (!confirm("Delete the automation " + a.name + "? Its chat stays.")) return;
    guarded(del, async () => {
      await api("DELETE", "/api/automations/" + encodeURIComponent(a.id));
      if (autoEditing === a.id) resetAutomationForm();
      await openAutomations();
    });
  });
  const actions = [runBtn, toggle, edit, del];
  if (a.chatId) {
    const chat = el("button", { type: "button", class: "ghost" }, "Open chat");
    chat.addEventListener("click", () => guarded(chat, () => openSession(a.chatId)));
    actions.unshift(chat);
  }

  const runs = el("details", { class: "auto-runs" },
    el("summary", {}, a.runs.length ? "Last runs (" + a.runs.length + ")" : "No runs yet"),
    el("ul", { class: "auto-run-list" }, ...a.runs.map((r) => {
      const li = el("li", { class: r.sessionId ? "" : "skipped" },
        el("span", { class: "muted" }, whenText(r.at)), " ",
        r.status ? statusBadge(r.status) : el("span", { class: "badge" }, "skipped"), " ", r.note);
      if (r.sessionId) {
        li.classList.add("link");
        li.addEventListener("click", () => guarded(null, () => openSession(r.sessionId)));
      }
      return li;
    })));
  // A run that could not start needs the owner: show the history open.
  if (a.runs.length && a.runs[0].note.startsWith("could not start")) runs.open = true;

  return el("div", { class: "item auto-item" },
    el("div", { class: "tile-head" },
      iconTile("clock", true),
      el("div", { class: "tile-title" },
        el("span", { class: "name" }, a.name),
        el("div", { class: "tile-sub" }, runsLine(a))),
      state),
    el("div", { class: "tile-body" },
      el("div", { class: "auto-prompt" }, a.prompt),
      el("div", { class: "muted small" },
        (agent ? agent.label : a.agent) + (a.model ? " · " + a.model : "") + " · " +
        (a.repos.length ? a.repos.map((r) => r.fullName).join(", ") : "no repos") + " · " + next)),
    el("div", { class: "item-actions" }, ...actions),
    runs);
}

// ---- the form ----

function fillAutoAgents(agentId, model) {
  $("a-agent").replaceChildren(...autoAgents.map((a) => el("option", { value: a.id },
    a.label + (a.loggedIn === false ? " (not signed in)" : ""))));
  if (agentId && autoAgents.some((a) => a.id === agentId)) $("a-agent").value = agentId;
  autoModel.fill(autoAgents.find((a) => a.id === $("a-agent").value), model || "");
}
$("a-agent").addEventListener("change", () => autoModel.fill(autoAgents.find((a) => a.id === $("a-agent").value), ""));

function syncKind() {
  const every = $("a-kind").value === "every";
  $("a-every-wrap").hidden = !every;
  $("a-daily-wrap").hidden = every;
}
$("a-kind").addEventListener("change", syncKind);

function resetAutomationForm() {
  autoEditing = "";
  $("auto-form").reset();
  autoSelected = [];
  autoResults = [];
  $("a-results").hidden = true;
  $("auto-form-title").textContent = "New automation";
  $("a-submit").textContent = "Save automation";
  $("a-cancel").hidden = true;
  fillAutoAgents("", "");
  fillAutoServers();
  syncKind();
  renderAutoSelected();
}

function editAutomation(a) {
  resetAutomationForm();
  autoEditing = a.id;
  $("auto-form-title").textContent = "Edit " + a.name;
  $("a-submit").textContent = "Save changes";
  $("a-cancel").hidden = false;
  fillAutomationForm(a);
}

// Opens the page with an agent's proposal in the form, as a new automation to check and save.
async function editProposal(p) {
  autoEditing = "";
  await openAutomations();
  resetAutomationForm();
  $("auto-form-title").textContent = "New automation (proposed in a chat)";
  $("a-cancel").hidden = false;
  fillAutomationForm({ ...p, mayMerge: false, enabled: true });
}

function fillAutomationForm(a) {
  $("a-name").value = a.name;
  $("a-prompt").value = a.prompt;
  fillAutoAgents(a.agent, a.model);
  $("a-kind").value = a.everyMinutes ? "every" : "daily";
  if (a.everyMinutes) $("a-every").value = a.everyMinutes;
  if (a.dailyAt) $("a-daily").value = a.dailyAt;
  $("a-maxday").value = a.maxPerDay || 0;
  $("a-maxusage").value = a.maxUsage || 0;
  $("a-merge").checked = !!a.mayMerge;
  $("a-enabled").checked = a.enabled !== false;
  autoSelected = (a.repos || []).map((r) => ({ ...r }));
  syncKind();
  renderAutoSelected();
  $("auto-form").scrollIntoView({ behavior: "smooth", block: "start" });
}
$("a-cancel").addEventListener("click", resetAutomationForm);

function fillAutoServers() {
  $("a-server").replaceChildren(...autoServers.map((s) => el("option", { value: s.id }, s.name)));
  $("a-server").hidden = autoServers.length < 2;
  $("a-query").disabled = !autoServers.length;
  $("a-query").placeholder = autoServers.length ? "Add a repo: search your git servers" : "Add a git server first (Git servers page)";
}

const autoKey = (r) => r.serverId + "/" + r.fullName;

function renderAutoSelected() {
  $("a-selected").hidden = !autoSelected.length;
  $("a-selected").replaceChildren(...autoSelected.map((r, i) => {
    const remove = el("button", { type: "button", class: "ghost", title: "Remove", "aria-label": "Remove " + r.fullName }, "×");
    remove.addEventListener("click", () => { autoSelected.splice(i, 1); renderAutoSelected(); renderAutoResults(); });
    return el("span", { class: "chip" }, el("span", { class: "name" }, r.fullName), remove);
  }));
}

let autoResultsOpen = false;
function renderAutoResults() {
  const box = $("a-results");
  box.replaceChildren(...autoResults.map((r) => {
    const already = autoSelected.some((s) => autoKey(s) === autoKey(r));
    const row = el("button", { type: "button", class: "result" },
      el("span", { class: "name" }, r.fullName),
      el("span", { class: "muted" }, (already ? "added" : (r.private ? "private" : "public")) + (r.description ? " · " + r.description : "")));
    row.disabled = already;
    row.addEventListener("click", () => {
      autoSelected.push({ serverId: r.serverId, fullName: r.fullName, cloneUrl: r.cloneUrl, base: "" });
      renderAutoSelected(); renderAutoResults();
    });
    return row;
  }));
  box.hidden = !autoResults.length || !autoResultsOpen;
}
$("a-results").addEventListener("mousedown", (e) => e.preventDefault());

async function searchAutoRepos() {
  const id = $("a-server").value;
  if (!id) return;
  const repos = await api("GET", "/api/servers/" + encodeURIComponent(id) + "/repos?q=" + encodeURIComponent($("a-query").value));
  autoResults = repos.map((r) => ({ ...r, serverId: id }));
  renderAutoResults();
}
let autoSearchTimer = null;
const autoSearchSoon = (ms) => {
  clearTimeout(autoSearchTimer);
  autoSearchTimer = setTimeout(() => searchAutoRepos().catch((e) => banner(e.message, true)), ms);
};
$("a-query").addEventListener("input", () => autoSearchSoon(300));
$("a-query").addEventListener("focus", () => { autoResultsOpen = true; renderAutoResults(); if (!autoResults.length) autoSearchSoon(0); });
$("a-query").addEventListener("blur", () => { autoResultsOpen = false; $("a-results").hidden = true; });
$("a-query").addEventListener("keydown", (e) => { if (e.key === "Enter") e.preventDefault(); });
$("a-server").addEventListener("change", () => autoSearchSoon(0));

$("auto-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("a-submit"), async () => {
    const every = $("a-kind").value === "every";
    const body = {
      name: $("a-name").value, prompt: $("a-prompt").value, agent: $("a-agent").value, model: autoModel.value(),
      repos: autoSelected.map((r) => ({ serverId: r.serverId, fullName: r.fullName, cloneUrl: r.cloneUrl, base: r.base || "" })),
      everyMinutes: every ? parseInt($("a-every").value, 10) || 0 : 0,
      dailyAt: every ? "" : $("a-daily").value,
      maxPerDay: parseInt($("a-maxday").value, 10) || 0,
      maxUsage: parseInt($("a-maxusage").value, 10) || 0,
      mayMerge: $("a-merge").checked, enabled: $("a-enabled").checked, timeZone: BROWSER_TZ,
    };
    if (every && body.everyMinutes < 5) throw new Error("Run it every 5 minutes or more.");
    if (autoEditing) await api("PUT", "/api/automations/" + encodeURIComponent(autoEditing), body);
    else await api("POST", "/api/automations", body);
    const was = autoEditing;
    resetAutomationForm();
    await openAutomations();
    banner(was ? "Saved." : "Saved. It runs on its schedule, or press Run now to try it.");
  });
});
$("auto-refresh").addEventListener("click", () => guarded($("auto-refresh"), openAutomations));
