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
};
const iconSvg = (name) => '<svg viewBox="0 0 24 24" aria-hidden="true">' + (ICONS[name] || "") + "</svg>";
function iconTile(name, small) {
  const t = el("span", { class: "icon-tile" + (small ? " sm" : "") });
  t.innerHTML = iconSvg(name);
  return t;
}
document.querySelectorAll("[data-icon]").forEach((n) => { n.innerHTML = iconSvg(n.dataset.icon); });

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
const VIEWS = ["auth", "sessions", "new", "session", "agents", "memory", "repomem", "servers", "settings"];
const TAB_OF = { sessions: "sessions", new: "sessions", session: "sessions", agents: "agents", memory: "memory", repomem: "memory", servers: "servers", settings: "settings" };
let currentView = "";
function show(name) {
  currentView = name;
  if (name !== "auth" && typeof startAttention === "function") startAttention();
  for (const v of VIEWS) $("view-" + v).hidden = v !== name;
  const chats = name === "sessions" || name === "new" || name === "session";
  $("workspace").hidden = !chats;
  $("workspace").classList.toggle("chat-open", name === "new" || name === "session"); // on a phone: the chat, not the list
  document.querySelector("main").classList.toggle("wide", chats);
  $("new-session").classList.toggle("selected", name === "new"); // lit while the new-chat screen is open
  $("nav").hidden = $("logout").hidden = name === "auth";
  document.querySelectorAll(".tab").forEach((t) => t.setAttribute("aria-current", String(TAB_OF[name] === t.dataset.view)));
  if (!chats || name === "sessions") window.scrollTo(0, 0);
}

// The chat list sticks just under the header, whatever height the header has
// (it grows when the tabs wrap on a narrow screen).
const topBar = document.querySelector(".top");
function measureTop() { document.documentElement.style.setProperty("--top-h", topBar.offsetHeight + "px"); }
measureTop();
if (window.ResizeObserver) new ResizeObserver(measureTop).observe(topBar);

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

let typeInfos = [];
async function enter() {
  typeInfos = await api("GET", "/api/types");
  $("f-type").replaceChildren(...typeInfos.map((t) => el("option", { value: t.type }, t.label)));
  updateUrlHint();
  const m = /^#c=(.+)$/.exec(location.hash);
  if (m) {
    try { return await openSession(decodeURIComponent(m[1])); } catch (_) { /* it was deleted: show the list */ }
  }
  await openSessions();
}

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
  const open = { sessions: openSessions, agents: openAgents, memory: openMemory, servers: openServers, settings: openSettings }[t.dataset.view];
  guarded(null, open);
}));

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
  const replaceBtn = el("button", {}, "Replace token");
  const removeBtn = el("button", { class: "danger" }, "Remove");

  testBtn.addEventListener("click", () => guarded(testBtn, async () => {
    const r = await api("POST", "/api/servers/" + encodeURIComponent(s.id) + "/test");
    status.className = "ok"; status.textContent = "Connected as " + r.user;
  }));
  prBtn.addEventListener("click", () => guarded(prBtn, async () => {
    prResult.replaceChildren(el("li", { class: "muted" }, "Checking…"));
    try {
      const r = await api("POST", "/api/servers/" + encodeURIComponent(s.id) + "/test-pulls");
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
    prResult);
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

async function openSettings() {
  show("settings");
  fillSandbox(await api("GET", "/api/sandbox"));
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
