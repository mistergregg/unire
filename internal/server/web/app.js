"use strict";

// Shared helpers and the git servers and settings pages. The sessions pages
// are in sessions.js, which loads after this file.

const $ = (id) => document.getElementById(id);

// Build DOM with textContent only, so server data is never parsed as HTML.
function el(tag, attrs, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  for (const k of kids) n.append(k);
  return n;
}

// ---- icons: small line icons. The markup is fixed text written here, never data. ----
const ICONS = {
  agents: '<rect x="4" y="8" width="16" height="11" rx="3"/><path d="M12 8V5M9 13h.01M15 13h.01M9 16.5h6"/><circle cx="12" cy="4" r="1"/>',
  memory: '<ellipse cx="12" cy="6" rx="7" ry="3"/><path d="M5 6v6c0 1.7 3.1 3 7 3s7-1.3 7-3V6M5 12v6c0 1.7 3.1 3 7 3s7-1.3 7-3v-6"/>',
  git: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="9" r="2"/><path d="M6 7v10M18 11c0 4-6 3-12 6"/>',
  settings: '<path d="M4 7h10M18 7h2M4 17h2M10 17h10"/><circle cx="16" cy="7" r="2"/><circle cx="8" cy="17" r="2"/>',
  box: '<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/><path d="M12 12l8-4.5M12 12v9M12 12L4 7.5"/>',
  shield: '<path d="M12 3l8 3v6c0 4.5-3.2 7.8-8 9-4.8-1.2-8-4.5-8-9V6z"/><path d="M9 12l2 2 4-4"/>',
  layers: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>',
  monitor: '<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>',
  key: '<circle cx="8" cy="15" r="3.5"/><path d="M10.5 12.5L20 3M16 7l3 3"/>',
  bell: '<path d="M6 16v-5a6 6 0 0112 0v5l2 2H4z"/><path d="M10 21h4"/>',
  download: '<path d="M12 4v11M7 11l5 5 5-5M5 20h14"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20 20l-4-4"/>',
  zoomIn: '<path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/>',
  zoomOut: '<path d="M4 14h6v6M20 10h-6V4M14 10l-7 7M10 14l-7 7"/>',
  close: '<path d="M18 6L6 18M6 6l12 12"/>',
  share: '<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><line x1="8.59" y1="13.51" x2="15.42" y2="17.49"/><line x1="15.41" y1="6.51" x2="8.59" y2="10.49"/>',
  external: '<path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/>',
};
const iconSvg = (name) => '<svg viewBox="0 0 24 24" aria-hidden="true">' + (ICONS[name] || "") + "</svg>";
function iconTile(name, small) {
  const t = el("span", { class: "icon-tile" + (small ? " sm" : "") });
  t.innerHTML = iconSvg(name);
  return t;
}
document.querySelectorAll("[data-icon]").forEach((n) => { n.innerHTML = iconSvg(n.dataset.icon); });

// ---- model picker: "Default", the agent's known models, or a name typed in ----
// The picker follows an agent: fill(agent, model) offers that agent's models (from
// GET /api/agents) and selects model; an empty model is the agent's own default.
const OTHER_MODEL = "\u0000other";
function modelPicker(select, input) {
  // The picker is narrow, so long names are cut off: the full one shows on hover.
  const sync = () => {
    input.hidden = select.value !== OTHER_MODEL;
    select.title = select.selectedOptions[0] ? select.selectedOptions[0].textContent : "";
  };
  select.addEventListener("change", () => { sync(); if (!input.hidden) input.focus(); });
  return {
    fill(agent, model) {
      const known = (agent && agent.models) || [];
      select.replaceChildren(el("option", { value: "" }, "Default model"),
        ...known.map((m) => el("option", { value: m }, m)),
        el("option", { value: OTHER_MODEL }, "Other…"));
      model = model || "";
      const typed = model !== "" && !known.includes(model);
      select.value = typed ? OTHER_MODEL : model;
      input.value = typed ? model : "";
      sync();
    },
    // The model to send: "" for the agent's default.
    value() { return select.value === OTHER_MODEL ? input.value.trim() : select.value; },
    set disabled(v) { select.disabled = v; input.disabled = v; },
  };
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", "X-Requested-With": "unire" },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  let data = {};
  try { data = await res.json(); } catch (_) { /* empty body */ }
  if (res.status === 401 && path !== "/api/login") { showAuth(true); throw new Error("Please log in again."); }
  if (!res.ok) throw new Error(data.error || "Request failed (" + res.status + ")");
  return data;
}

let bannerTimer;
function banner(msg, isError) {
  const b = $("banner");
  b.textContent = msg;
  b.className = "banner" + (isError ? " error" : "");
  b.hidden = !msg;
  clearTimeout(bannerTimer);
  if (msg && !isError) bannerTimer = setTimeout(() => { b.hidden = true; }, 5000);
}

// Runs an action with its button disabled. The banner is cleared first, not
// afterwards, so a success message the action sets stays visible.
async function guarded(button, fn) {
  if (button) button.disabled = true;
  banner("");
  try { await fn(); } catch (e) { banner(e.message, true); } finally { if (button) button.disabled = false; }
}

function ago(unix) {
  if (!unix) return "never";
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (s < 90) return "just now";
  if (s < 5400) return Math.round(s / 60) + " min ago";
  if (s < 129600) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " days ago";
}

// ---- views ----
const VIEWS = ["auth", "sessions", "new", "session", "prs", "agents", "memory", "repomem", "servers", "settings", "health"];
const TAB_OF = { sessions: "sessions", new: "sessions", session: "sessions", prs: "prs", agents: "agents", memory: "memory", repomem: "memory", servers: "servers", settings: "settings", health: "health" };
let currentView = "";
function setViewHash(hash) {
  const target = hash ? "#" + hash : location.pathname + location.search;
  if ((hash ? "#" + hash : "") === location.hash) return;
  try { history.replaceState(null, "", target); } catch (_) {}
}
function show(name) {
  currentView = name;
  if (name !== "auth" && typeof startAttention === "function") startAttention();
  if (TAB_OF[name] !== "sessions" && typeof clearSelectedSession === "function") clearSelectedSession();
  for (const v of VIEWS) $("view-" + v).hidden = v !== name;
  $("workspace").hidden = name === "auth";
  $("workspace").classList.toggle("chat-open", name !== "sessions"); // on a phone: the active view in pane, not the sidebar
  $("workspace").classList.toggle("in-session", name === "session");
  document.querySelector("main").classList.toggle("wide", true);
  $("new-session").classList.toggle("selected", name === "new"); // lit while the new-chat screen is open
  $("nav").hidden = $("logout").hidden = name === "auth";
  document.querySelectorAll(".tab").forEach((t) => t.setAttribute("aria-current", String(TAB_OF[name] === t.dataset.view)));
  if (name === "sessions") setViewHash("");
  else if (name !== "auth" && name !== "session" && name !== "repomem") setViewHash(name);
  if (name === "sessions" || name === "auth") window.scrollTo(0, 0);
  if ((name === "new" || name === "session") && typeof refreshUsage === "function") refreshUsage();
}

// Top header height measurement (if a top bar is present; 0px in sidebar layout).
const topBar = document.querySelector(".top");
function measureTop() {
  const h = topBar ? topBar.offsetHeight : 0;
  document.documentElement.style.setProperty("--top-h", h + "px");
}
measureTop();
if (topBar && window.ResizeObserver) new ResizeObserver(measureTop).observe(topBar);

let setupMode = false;
function showAuth(needsLogin) {
  show("auth");
  $("auth-password").value = "";
  $("auth-title").textContent = setupMode ? "Set an admin password" : "Log in";
  $("auth-hint").textContent = setupMode
    ? "First run. Use at least 10 characters. This protects the whole dashboard."
    : needsLogin ? "Enter the admin password." : "";
  $("auth-submit").textContent = setupMode ? "Create password" : "Log in";
  $("auth-password").autocomplete = setupMode ? "new-password" : "current-password";
}

async function init() {
  const st = await api("GET", "/api/status");
  setupMode = !st.setupDone;
  if (!st.loggedIn) return showAuth(true);
  await enter();
}

async function navigateHash() {
  const h = location.hash;
  const cm = /^#c=(.+)$/.exec(h);
  if (cm) {
    try { return await openSession(decodeURIComponent(cm[1])); } catch (_) { /* deleted: fall through */ }
  }
  const rm = /^#repomem=(.+)$/.exec(h);
  if (rm) {
    try { return await openRepoMemory(decodeURIComponent(rm[1])); } catch (_) {}
  }
  const byHash = {
    "#new": openNew, "#prs": openPullsOverview, "#agents": openAgents,
    "#memory": openMemory, "#servers": openServers, "#settings": openSettings, "#health": openHealth,
  };
  if (byHash[h]) return await byHash[h]();
  await openSessions();
}

let typeInfos = [];
async function enter() {
  typeInfos = await api("GET", "/api/types");
  $("f-type").replaceChildren(...typeInfos.map((t) => el("option", { value: t.type }, t.label)));
  updateUrlHint();
  await navigateHash();
}
window.addEventListener("hashchange", () => {
  if (currentView && currentView !== "auth") guarded(null, navigateHash);
});

// ---- auth ----
$("auth-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("auth-submit"), async () => {
    await api("POST", setupMode ? "/api/setup" : "/api/login", { password: $("auth-password").value });
    setupMode = false;
    await enter();
  });
});
$("logout").addEventListener("click", () => guarded($("logout"), async () => {
  await api("POST", "/api/logout");
  showAuth(true);
}));
document.querySelectorAll(".tab").forEach((t) => t.addEventListener("click", () => {
  banner("");
  const open = { sessions: openSessions, prs: openPullsOverview, agents: openAgents, memory: openMemory, servers: openServers, settings: openSettings, health: openHealth }[t.dataset.view];
  guarded(null, open);
}));

// Keyboard shortcut: "/" (when not in a field) or Cmd/Ctrl+K focuses chat search.
document.addEventListener("keydown", (e) => {
  if (currentView === "auth") return;
  const search = $("side-search");
  if (!search) return;
  const isK = (e.key === "k" || e.key === "K") && (e.metaKey || e.ctrlKey);
  const tag = document.activeElement && document.activeElement.tagName;
  const inField = tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || (document.activeElement && document.activeElement.isContentEditable);
  if (isK || (e.key === "/" && !inField && !e.metaKey && !e.ctrlKey && !e.altKey)) {
    e.preventDefault();
    if (typeof setLeftSidebarCollapsed === "function") setLeftSidebarCollapsed(false);
    search.focus();
    search.select();
  } else if (e.key === "Escape" && document.activeElement === search) {
    if (search.value) { search.value = ""; search.dispatchEvent(new Event("input")); }
    search.blur();
  }
});

// ---- git servers ----
// The form is built from each provider's own description (label, URL hint,
// token help), so a new provider needs no UI change.
function updateUrlHint() {
  const info = typeInfos.find((t) => t.type === $("f-type").value) || {};
  $("f-url-hint").textContent = info.baseUrlHint ? "(" + info.baseUrlHint + ")" : "";
  $("f-url").required = !!info.baseUrlRequired;
  $("f-token-help").textContent = info.tokenHelp || "";
}
$("f-type").addEventListener("change", updateUrlHint);

async function openServers() {
  show("servers");
  const servers = await api("GET", "/api/servers");
  const list = $("server-list");
  if (!servers.length) {
    list.replaceChildren(el("p", { class: "muted" }, "No git servers yet. Add one below to start working on your repos."));
    return;
  }
  list.replaceChildren(...servers.map(serverCard));
}

function serverCard(s) {
  const tokenInput = el("input", { type: "password", placeholder: "New token", autocomplete: "new-password", spellcheck: "false" });
  const status = el("span", { class: "muted" });
  const testBtn = el("button", {}, "Test");
  const prBtn = el("button", { title: "Check that the token can open and follow pull requests (changes nothing)" }, "Check pull requests");
  const prResult = el("ul", { class: "pr-perms" });
  const prRepo = el("input", { type: "text", placeholder: "Check one repo: owner/name (optional)", spellcheck: "false", "aria-label": "Repo to check pull request access on" });
  const replaceBtn = el("button", {}, "Replace token");
  const removeBtn = el("button", { class: "danger" }, "Remove");

  testBtn.addEventListener("click", () => guarded(testBtn, async () => {
    const r = await api("POST", "/api/servers/" + encodeURIComponent(s.id) + "/test");
    status.className = "ok"; status.textContent = "Connected as " + r.user;
  }));
  prBtn.addEventListener("click", () => guarded(prBtn, async () => {
    prResult.replaceChildren(el("li", { class: "muted" }, "Checking…"));
    try {
      const r = await api("POST", "/api/servers/" + encodeURIComponent(s.id) + "/test-pulls", { repo: prRepo.value.trim() });
      prResult.replaceChildren(el("li", { class: "muted" }, "Checked on " + r.repo),
        ...r.capabilities.map((c) => el("li", { class: c.ok ? "ok" : "bad" }, (c.ok ? "✓ " : "✗ ") + c.name + (c.note ? " (" + c.note + ")" : ""))));
    } catch (e) { prResult.replaceChildren(); throw e; }
  }));
  replaceBtn.addEventListener("click", () => guarded(replaceBtn, async () => {
    if (!tokenInput.value) throw new Error("Enter the new token first.");
    const r = await api("PUT", "/api/servers/" + encodeURIComponent(s.id) + "/token", { token: tokenInput.value });
    tokenInput.value = "";
    status.className = "ok"; status.textContent = "Token replaced, connected as " + r.user;
  }));
  removeBtn.addEventListener("click", () => {
    if (!confirm("Remove " + s.name + " and delete its saved token?")) return;
    guarded(removeBtn, async () => { await api("DELETE", "/api/servers/" + encodeURIComponent(s.id)); await openServers(); });
  });

  return el("div", { class: "item tile" },
    el("div", { class: "tile-head" },
      el("span", { class: "avatar-wrap" },
        el("span", { class: "avatar lg" }, (s.type || "?").charAt(0).toUpperCase()),
        el("span", { class: "state " + (s.tokenSet ? "ok" : "bad"), title: s.tokenSet ? "Token saved" : "No token" })),
      el("div", { class: "tile-title" },
        el("span", { class: "name" }, s.name),
        el("div", { class: "tile-sub" }, s.baseUrl || (s.type === "github" ? "github.com" : ""))),
      el("span", { class: "badge" }, s.type)),
    el("div", { class: "tile-body" }, s.tokenSet ? "Token saved and encrypted." : "No token saved yet."),
    el("div", { class: "item-actions" }, tokenInput, replaceBtn),
    el("div", { class: "item-actions" }, testBtn, prBtn, removeBtn, status),
    prRepo, prResult);
}

$("add-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("add-submit"), async () => {
    const r = await api("POST", "/api/servers", {
      name: $("f-name").value, type: $("f-type").value, baseUrl: $("f-url").value,
      token: $("f-token").value, insecureSkipVerify: $("f-insecure").checked,
    });
    $("add-form").reset();
    updateUrlHint();
    await openServers();
    banner("Saved. Connected as " + r.user + ".");
  });
});

// ---- settings: sandbox, workspaces, devices, password ----
function describeAgent(ua) {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Windows/.test(ua) ? "Windows" : /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Mac OS/.test(ua) ? "macOS" : /Linux/.test(ua) ? "Linux" : "";
  return ua ? browser + (os ? " on " + os : "") : "Unknown device";
}

function fillSandbox(r) {
  $("sb-image").value = r.sandbox.image;
  $("sb-volume").value = r.sandbox.credsVolume;
  $("sb-memory").value = r.sandbox.memory;
  $("sb-cpus").value = r.sandbox.cpus;
}

// Network settings. The mode list and descriptions come from the server.
let networkInfo = null;
function modeText(id) { const m = networkInfo.modes.find((x) => x.id === id); return m ? m.description : ""; }

function fillNetwork(info) {
  networkInfo = info;
  const p = info.protection;
  $("net-status").textContent = p.available
    ? "Network protection is active: sandboxes have no direct route out and go through Unire's filter."
    : "Network protection is NOT available: " + p.reason + ". Sessions in Internet only or Agent only mode will not start until it is; Full access still works.";
  $("net-status").className = p.available ? "muted" : "err";
  $("net-mode").replaceChildren(...info.modes.map((m) => el("option", { value: m.id }, m.label)));
  $("net-mode").value = info.mode;
  $("net-desc").textContent = modeText(info.mode);
  $("net-allow").value = info.allow.join("\n");
}

$("net-mode").addEventListener("change", () => { $("net-desc").textContent = modeText($("net-mode").value); });
$("net-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("net-submit"), async () => {
    fillNetwork(await api("PUT", "/api/network", { mode: $("net-mode").value, allow: $("net-allow").value.split("\n") }));
    banner("Network settings saved. They apply to new sessions.");
  });
});

function syncThemePicker() {
  const picker = $("theme-picker");
  if (!picker || typeof getUnireTheme !== "function") return;
  const cur = getUnireTheme();
  picker.querySelectorAll("[data-theme-choice]").forEach((btn) => {
    const active = btn.dataset.themeChoice === cur;
    btn.classList.toggle("active", active);
    btn.setAttribute("aria-checked", String(active));
  });
}
if ($("theme-picker")) {
  $("theme-picker").addEventListener("click", (e) => {
    const btn = e.target.closest("[data-theme-choice]");
    if (!btn || typeof setUnireTheme !== "function") return;
    setUnireTheme(btn.dataset.themeChoice);
    syncThemePicker();
  });
}

async function openSettings() {
  show("settings");
  syncThemePicker();
  fillSandbox(await api("GET", "/api/sandbox"));
  fillLimits(await api("GET", "/api/limits"));
  $("pr-auto").checked = (await api("GET", "/api/pulls/settings")).autoOpen;
  fillNetwork(await api("GET", "/api/network"));
  await renderWorkspaces();
  await renderLogins();
}

$("sb-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("sb-submit"), async () => {
    fillSandbox(await api("PUT", "/api/sandbox", {
      image: $("sb-image").value, credsVolume: $("sb-volume").value,
      memory: $("sb-memory").value, cpus: $("sb-cpus").value,
    }));
    banner("Sandbox settings saved. They apply to new sessions.");
  });
});

async function renderWorkspaces() {
  const list = await api("GET", "/api/workspaces");
  $("ws-list").replaceChildren(...(list.length ? list.map((w) => {
    const btn = el("button", { class: "danger" }, "Delete");
    btn.addEventListener("click", () => {
      if (!confirm("Delete the workspace " + w.name + "?")) return;
      guarded(btn, async () => { await api("DELETE", "/api/workspaces/" + encodeURIComponent(w.id)); await renderWorkspaces(); });
    });
    return el("div", { class: "row-item" },
      el("div", { class: "grow" },
        el("span", { class: "name" }, w.name),
        el("div", { class: "tile-sub" }, w.repos.map((r) => r.fullName).join(", "))),
      el("span", { class: "badge" }, w.repos.length + (w.repos.length === 1 ? " repo" : " repos")),
      btn);
  }) : [el("p", { class: "muted small" }, "None yet. Save one from the New chat screen.")]));
}

async function renderLogins() {
  const logins = await api("GET", "/api/logins");
  $("login-list").replaceChildren(...logins.map((l) => {
    const btn = el("button", {}, l.current ? "Log out here" : "Log out");
    btn.addEventListener("click", () => guarded(btn, async () => {
      await api("DELETE", "/api/logins/" + encodeURIComponent(l.id));
      if (l.current) { showAuth(true); return; }
      await renderLogins();
    }));
    return el("div", { class: "row-item" },
      iconTile("monitor", true),
      el("div", { class: "grow" },
        el("span", { class: "name" }, describeAgent(l.userAgent)),
        el("div", { class: "tile-sub" }, (l.ip || "unknown address") + " · active " + ago(l.lastSeen) + " · signed in " + ago(l.createdAt))),
      l.current ? el("span", { class: "badge st-done" }, "this device") : "",
      btn);
  }));
}

$("revoke-all").addEventListener("click", () => {
  if (!confirm("Log out of every device, including this one?")) return;
  guarded($("revoke-all"), async () => { await api("POST", "/api/logins/revoke-all"); showAuth(true); });
});

$("pw-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded($("pw-submit"), async () => {
    await api("POST", "/api/password", { current: $("pw-current").value, new: $("pw-new").value });
    $("pw-form").reset();
    await renderLogins();
    banner("Password changed. Other devices were logged out.");
  });
});

// Start after every script has loaded (sessions.js defines openSessions).
window.addEventListener("load", () => init().catch((e) => banner(e.message, true)));

// ---- limits: how long a run may take, how long things are kept ----
function fillLimits(l) {
  $("lim-run").value = l.maxRunMinutes;
  $("lim-logs").value = l.keepLogsDays;
  $("lim-ws").value = l.keepWorkspaceDays;
}

const limForm = $("lim-form");
if (limForm) {
  limForm.addEventListener("submit", (e) => {
    e.preventDefault();
    guarded($("lim-submit"), async () => {
      fillLimits(await api("PUT", "/api/limits", {
        maxRunMinutes: Number($("lim-run").value) || 0,
        keepLogsDays: Number($("lim-logs").value) || 0,
        keepWorkspaceDays: Number($("lim-ws").value) || 0,
      }));
      banner("Limits saved. The run limit applies to runs that start from now on.");
    });
  });
}

// ---- pull requests: ask first, or open them when the agent is done ----
const prAuto = $("pr-auto");
if (prAuto) {
  prAuto.addEventListener("change", () => guarded(null, async () => {
    try {
      prAuto.checked = (await api("PUT", "/api/pulls/settings", { autoOpen: prAuto.checked })).autoOpen;
      banner(prAuto.checked ? "Pull requests are opened when the agent is done." : "Unire asks you before it opens a pull request.");
    } catch (e) { prAuto.checked = !prAuto.checked; throw e; }
  }));
}

// ---- file download helpers ----
function downloadUrl(name, url) {
  const a = el("a", { href: url, download: name || "file" });
  document.body.append(a);
  a.click();
  a.remove();
}

function downloadBlob(name, blob) {
  const url = URL.createObjectURL(blob);
  downloadUrl(name, url);
  setTimeout(() => URL.revokeObjectURL(url), 10000);
}

// ---- image share helper (Web Share API) ----
async function shareImage({ src, name, blob, href, onShare }) {
  if (typeof onShare === "function") {
    try {
      await onShare();
      return;
    } catch (_) {}
  }
  if (!navigator.share) {
    banner("Sharing is not supported on this browser.", true);
    return;
  }
  const displayName = name || "image.png";
  try {
    let fileBlob = blob;
    if (!fileBlob && src) {
      if (src.startsWith("data:")) {
        const parts = src.split(",");
        const mimeMatch = parts[0].match(/:(.*?);/);
        const mime = mimeMatch ? mimeMatch[1] : "image/png";
        const bstr = atob(parts[1]);
        const n = bstr.length;
        const u8arr = new Uint8Array(n);
        for (let i = 0; i < n; i++) u8arr[i] = bstr.charCodeAt(i);
        fileBlob = new Blob([u8arr], { type: mime });
      } else {
        const res = await fetch(src);
        fileBlob = await res.blob();
      }
    }
    const ext = displayName.split(".").pop().toLowerCase();
    const type = (fileBlob && fileBlob.type) ? fileBlob.type : (ext === "jpg" || ext === "jpeg" ? "image/jpeg" : "image/png");
    if (fileBlob) {
      const file = new File([fileBlob], displayName, { type });
      if (navigator.canShare && navigator.canShare({ files: [file] })) {
        await navigator.share({
          title: displayName,
          files: [file],
        });
        return;
      }
    }
    const shareUrl = href || (src && !src.startsWith("data:") ? src : window.location.href);
    await navigator.share({
      title: displayName,
      url: shareUrl,
    });
  } catch (err) {
    if (err && err.name !== "AbortError") {
      banner("Could not share image: " + err.message, true);
    }
  }
}

// ---- full-screen image viewer modal ----
let lastActiveElement = null;
let imageModalZoomed = false;
let activeImageDownload = null;
let activeImageShare = null;

function updateImageModalZoomBtn() {
  const btn = $("image-modal-zoom");
  if (!btn) return;
  btn.title = imageModalZoomed ? "Fit to screen (Z)" : "Actual size (Z)";
  btn.setAttribute("aria-label", imageModalZoomed ? "Fit to screen" : "Actual size");
  btn.innerHTML = iconSvg(imageModalZoomed ? "zoomOut" : "zoomIn");
}

function toggleImageModalZoom() {
  const viewport = $("image-modal-viewport");
  if (!viewport) return;
  imageModalZoomed = !imageModalZoomed;
  viewport.classList.toggle("is-zoomed", imageModalZoomed);
  updateImageModalZoomBtn();
}

function handleImageModalKey(e) {
  if (e.key === "Escape") {
    e.preventDefault();
    closeImageModal();
  } else if ((e.key === "z" || e.key === "Z") && !e.ctrlKey && !e.metaKey && !e.altKey) {
    e.preventDefault();
    toggleImageModalZoom();
  } else if ((e.key === "s" || e.key === "S") && !e.ctrlKey && !e.metaKey && !e.altKey) {
    if (typeof activeImageShare === "function") {
      e.preventDefault();
      activeImageShare();
    }
  }
}

function openImageModal({ src, name, onDownload, href, onShare }) {
  const modal = $("image-modal");
  if (!modal) return;
  const img = $("image-modal-img");
  const title = $("image-modal-title");
  const dlBtn = $("image-modal-download");
  const shareBtn = $("image-modal-share");
  const viewport = $("image-modal-viewport");

  lastActiveElement = document.activeElement;
  modal.hidden = false;
  document.body.classList.add("modal-open");

  const displayName = name || "Image";
  if (title) {
    title.textContent = displayName;
    title.title = displayName;
  }

  imageModalZoomed = false;
  if (viewport) {
    viewport.classList.remove("is-zoomed");
    viewport.scrollTop = 0;
    viewport.scrollLeft = 0;
  }
  updateImageModalZoomBtn();

  if (img) {
    img.src = src;
    img.alt = displayName;
  }

  activeImageDownload = onDownload || (() => downloadUrl(displayName, href || src));
  if (dlBtn) {
    dlBtn.onclick = () => {
      if (typeof activeImageDownload === "function") activeImageDownload();
    };
  }

  activeImageShare = onShare || (() => shareImage({ src, name: displayName, href }));
  if (shareBtn) {
    shareBtn.hidden = !navigator.share;
    shareBtn.onclick = () => {
      if (typeof activeImageShare === "function") activeImageShare();
    };
  }

  const closeBtn = $("image-modal-close");
  if (closeBtn) closeBtn.focus();

  document.removeEventListener("keydown", handleImageModalKey);
  document.addEventListener("keydown", handleImageModalKey);
}

function closeImageModal() {
  const modal = $("image-modal");
  if (!modal || modal.hidden) return;
  modal.hidden = true;
  document.body.classList.remove("modal-open");
  const img = $("image-modal-img");
  if (img) img.src = "";
  activeImageDownload = null;
  activeImageShare = null;
  const shareBtn = $("image-modal-share");
  if (shareBtn) shareBtn.onclick = null;
  document.removeEventListener("keydown", handleImageModalKey);
  if (lastActiveElement && typeof lastActiveElement.focus === "function") {
    try { lastActiveElement.focus(); } catch (_) {}
  }
}

function initImageModal() {
  const modal = $("image-modal");
  if (!modal) return;
  modal.addEventListener("click", (e) => {
    if (!e.target.closest("#image-modal-img") && !e.target.closest(".image-modal-bar")) {
      closeImageModal();
    }
  });
  const closeBtn = $("image-modal-close");
  if (closeBtn) closeBtn.addEventListener("click", closeImageModal);
  const zoomBtn = $("image-modal-zoom");
  if (zoomBtn) zoomBtn.addEventListener("click", toggleImageModalZoom);
  const img = $("image-modal-img");
  if (img) img.addEventListener("click", toggleImageModalZoom);

  // Delegated click handler: clicking any image in chat or files side panel opens full screen
  document.addEventListener("click", (e) => {
    const imgEl = e.target.closest("img");
    if (!imgEl || imgEl.closest("#image-modal")) return;
    if (imgEl.classList.contains("share-img") || imgEl.closest(".side-viewer-body, .share-body, .attach-thumbs, .attach-preview")) {
      e.preventDefault();
      const src = imgEl.src;
      const rawName = imgEl.alt || imgEl.title || "Image";
      const name = rawName.replace(/\s*[·—]\s*Click to view full screen$/, "");
      if (src) {
        openImageModal({ src, name });
      }
    }
  });
}

initImageModal();

// ---- Resizable and Collapsible Sides ----
const DEFAULT_LEFT_WIDTH = 270;
const MIN_LEFT_WIDTH = 180;
const MAX_LEFT_WIDTH = 550;
const SNAP_LEFT_ZONE = 16;
const COLLAPSE_LEFT_THRESHOLD = 110;

let resizeTooltipEl = null;
function getResizeTooltip() {
  if (!resizeTooltipEl) {
    resizeTooltipEl = el("div", { id: "resize-tooltip", class: "resize-tooltip", hidden: "" });
    document.body.appendChild(resizeTooltipEl);
  }
  return resizeTooltipEl;
}

window.showResizeTooltip = function(width, isLocked, defaultWidth, x, y, customText) {
  const tip = getResizeTooltip();
  tip.hidden = false;
  tip.classList.toggle("snapped-lock", !!isLocked);
  if (customText) {
    tip.textContent = customText;
  } else if (isLocked) {
    tip.innerHTML = '<svg class="lock-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg> <span>Default (' + defaultWidth + 'px)</span>';
  } else {
    tip.textContent = width + "px";
  }
  tip.style.left = Math.max(10, Math.min(window.innerWidth - 140, x)) + "px";
  tip.style.top = Math.max(10, y - 36) + "px";
};

window.hideResizeTooltip = function() {
  const tip = getResizeTooltip();
  tip.hidden = true;
};

window.setLeftSidebarCollapsed = function(collapsed) {
  const ws = $("workspace");
  if (!ws) return;
  ws.classList.toggle("side-collapsed", collapsed);
  try { localStorage.setItem("unire_side_left_collapsed", String(collapsed)); } catch (_) {}
  const collapseBtn = $("side-collapse-btn");
  if (collapseBtn) collapseBtn.setAttribute("aria-expanded", String(!collapsed));
  const expandBtn = $("s-expand-side");
  if (expandBtn) expandBtn.setAttribute("aria-expanded", String(!collapsed));
  const wsExpandBtn = $("workspace-expand-btn");
  if (wsExpandBtn) wsExpandBtn.setAttribute("aria-expanded", String(!collapsed));
};

function initLeftSidebar() {
  const ws = $("workspace");
  const resizer = $("side-resizer");
  const collapseBtn = $("side-collapse-btn");
  const expandBtn = $("s-expand-side");
  const wsExpandBtn = $("workspace-expand-btn");
  if (!ws) return;

  // Restore saved width
  let savedWidth = DEFAULT_LEFT_WIDTH;
  try {
    const w = parseInt(localStorage.getItem("unire_side_left_w"), 10);
    if (!isNaN(w) && w >= MIN_LEFT_WIDTH && w <= MAX_LEFT_WIDTH) {
      savedWidth = w;
    }
  } catch (_) {}
  ws.style.setProperty("--side-left-w", savedWidth + "px");

  // Restore collapsed state
  try {
    if (localStorage.getItem("unire_side_left_collapsed") === "true") {
      setLeftSidebarCollapsed(true);
    }
  } catch (_) {}

  if (collapseBtn) {
    collapseBtn.addEventListener("click", () => setLeftSidebarCollapsed(true));
  }
  if (expandBtn) {
    expandBtn.addEventListener("click", () => setLeftSidebarCollapsed(false));
  }
  if (wsExpandBtn) {
    wsExpandBtn.addEventListener("click", () => setLeftSidebarCollapsed(false));
  }

  if (resizer) {
    let dragging = false;

    resizer.addEventListener("pointerdown", (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      dragging = true;
      if (resizer.setPointerCapture && e.pointerId) {
        try { resizer.setPointerCapture(e.pointerId); } catch (_) {}
      }
      ws.classList.add("resizing");
      resizer.classList.add("dragging");

      const onPointerMove = (ev) => {
        if (!dragging) return;
        const rawWidth = ev.clientX;

        if (rawWidth < COLLAPSE_LEFT_THRESHOLD) {
          showResizeTooltip(0, false, DEFAULT_LEFT_WIDTH, ev.clientX + 14, ev.clientY, "Minimize to side");
          resizer.classList.remove("snapped-lock");
          return;
        }

        let width = rawWidth;
        let isLocked = false;
        if (Math.abs(rawWidth - DEFAULT_LEFT_WIDTH) <= SNAP_LEFT_ZONE) {
          width = DEFAULT_LEFT_WIDTH;
          isLocked = true;
        } else {
          const maxW = Math.min(MAX_LEFT_WIDTH, window.innerWidth * 0.45);
          width = Math.max(MIN_LEFT_WIDTH, Math.min(maxW, rawWidth));
        }

        resizer.classList.toggle("snapped-lock", isLocked);
        showResizeTooltip(Math.round(width), isLocked, DEFAULT_LEFT_WIDTH, ev.clientX + 14, ev.clientY);
        ws.style.setProperty("--side-left-w", Math.round(width) + "px");
        if (ws.classList.contains("side-collapsed")) {
          ws.classList.remove("side-collapsed");
        }
      };

      const onPointerUp = (ev) => {
        if (!dragging) return;
        dragging = false;
        if (resizer.releasePointerCapture && ev.pointerId) {
          try { resizer.releasePointerCapture(ev.pointerId); } catch (_) {}
        }
        ws.classList.remove("resizing");
        resizer.classList.remove("dragging", "snapped-lock");
        hideResizeTooltip();
        document.removeEventListener("pointermove", onPointerMove);
        window.removeEventListener("pointermove", onPointerMove);
        document.removeEventListener("pointerup", onPointerUp);
        window.removeEventListener("pointerup", onPointerUp);
        window.removeEventListener("pointercancel", onPointerUp);

        const rawWidth = ev.clientX;
        if (rawWidth < COLLAPSE_LEFT_THRESHOLD) {
          setLeftSidebarCollapsed(true);
        } else {
          let finalWidth = rawWidth;
          if (Math.abs(rawWidth - DEFAULT_LEFT_WIDTH) <= SNAP_LEFT_ZONE) {
            finalWidth = DEFAULT_LEFT_WIDTH;
          } else {
            const maxW = Math.min(MAX_LEFT_WIDTH, window.innerWidth * 0.45);
            finalWidth = Math.max(MIN_LEFT_WIDTH, Math.min(maxW, rawWidth));
          }
          finalWidth = Math.round(finalWidth);
          ws.style.setProperty("--side-left-w", finalWidth + "px");
          setLeftSidebarCollapsed(false);
          try { localStorage.setItem("unire_side_left_w", String(finalWidth)); } catch (_) {}
        }
      };

      window.addEventListener("pointermove", onPointerMove);
      window.addEventListener("pointerup", onPointerUp);
      window.addEventListener("pointercancel", onPointerUp);
    });

    resizer.addEventListener("dblclick", () => {
      ws.style.setProperty("--side-left-w", DEFAULT_LEFT_WIDTH + "px");
      setLeftSidebarCollapsed(false);
      try { localStorage.setItem("unire_side_left_w", String(DEFAULT_LEFT_WIDTH)); } catch (_) {}
      resizer.classList.add("snapped-lock");
      setTimeout(() => resizer.classList.remove("snapped-lock"), 450);
    });

    resizer.addEventListener("keydown", (e) => {
      let currentW = DEFAULT_LEFT_WIDTH;
      try {
        const val = parseInt(ws.style.getPropertyValue("--side-left-w"), 10);
        if (!isNaN(val)) currentW = val;
      } catch (_) {}

      if (e.key === "ArrowLeft") {
        e.preventDefault();
        const next = currentW - 15;
        if (next < COLLAPSE_LEFT_THRESHOLD) {
          setLeftSidebarCollapsed(true);
        } else {
          const w = Math.max(MIN_LEFT_WIDTH, next);
          ws.style.setProperty("--side-left-w", w + "px");
          try { localStorage.setItem("unire_side_left_w", String(w)); } catch (_) {}
        }
      } else if (e.key === "ArrowRight") {
        e.preventDefault();
        setLeftSidebarCollapsed(false);
        const maxW = Math.min(MAX_LEFT_WIDTH, window.innerWidth * 0.45);
        const next = Math.min(maxW, currentW + 15);
        ws.style.setProperty("--side-left-w", next + "px");
        try { localStorage.setItem("unire_side_left_w", String(next)); } catch (_) {}
      } else if (e.key === "Home" || e.key === "Enter") {
        e.preventDefault();
        ws.style.setProperty("--side-left-w", DEFAULT_LEFT_WIDTH + "px");
        setLeftSidebarCollapsed(false);
        try { localStorage.setItem("unire_side_left_w", String(DEFAULT_LEFT_WIDTH)); } catch (_) {}
        resizer.classList.add("snapped-lock");
        setTimeout(() => resizer.classList.remove("snapped-lock"), 450);
      }
    });
  }
}

initLeftSidebar();
