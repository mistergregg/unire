"use strict";

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

async function guarded(button, fn) {
  if (button) button.disabled = true;
  try { await fn(); banner(""); } catch (e) { banner(e.message, true); } finally { if (button) button.disabled = false; }
}

// ---- views ----
const VIEWS = ["auth", "servers", "repos", "settings"];
function show(name) {
  for (const v of VIEWS) $("view-" + v).hidden = v !== name;
  $("nav").hidden = $("logout").hidden = name === "auth";
  document.querySelectorAll(".tab").forEach((t) => t.setAttribute("aria-current", String(t.dataset.view === name)));
}

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

async function enter() {
  typeInfos = await api("GET", "/api/types");
  const sel = $("f-type");
  sel.replaceChildren(...typeInfos.map((t) => el("option", { value: t.type }, t.label)));
  updateUrlHint();
  await openServers();
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
  const open = { servers: openServers, repos: openRepos, settings: openSettings }[t.dataset.view];
  guarded(null, open);
}));

// ---- git servers ----
// The form is built from each provider's own description (label, URL hint,
// token help), so a new provider needs no UI change.
let typeInfos = [];
function updateUrlHint() {
  const info = typeInfos.find((t) => t.type === $("f-type").value) || {};
  $("f-url-hint").textContent = info.baseUrlHint ? "(" + info.baseUrlHint + ")" : "";
  $("f-url").required = !!info.baseUrlRequired;
  $("f-token-help").textContent = info.tokenHelp || "";
}
$("f-type").addEventListener("change", updateUrlHint);

let servers = [];
async function openServers() {
  show("servers");
  servers = await api("GET", "/api/servers");
  const list = $("server-list");
  if (!servers.length) {
    list.replaceChildren(el("p", { class: "muted" }, "No git servers yet. Add one below."));
    return;
  }
  list.replaceChildren(...servers.map(serverCard));
}

function serverCard(s) {
  const tokenInput = el("input", { type: "password", placeholder: "New token", autocomplete: "new-password", spellcheck: "false" });
  const status = el("span", { class: "muted" });
  const testBtn = el("button", {}, "Test");
  const replaceBtn = el("button", {}, "Replace token");
  const removeBtn = el("button", { class: "danger" }, "Remove");

  testBtn.addEventListener("click", () => guarded(testBtn, async () => {
    const r = await api("POST", "/api/servers/" + encodeURIComponent(s.id) + "/test");
    status.className = "ok"; status.textContent = "Connected as " + r.user;
  }).then(() => {}));
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

  return el("div", { class: "item" },
    el("div", { class: "item-head" },
      el("span", { class: "name" }, s.name),
      el("span", { class: "badge" }, s.type + (s.tokenSet ? " · token saved" : " · no token"))),
    el("div", { class: "muted" }, el("code", {}, s.baseUrl || (s.type === "github" ? "github.com" : ""))),
    el("div", { class: "item-actions" }, testBtn, tokenInput, replaceBtn, removeBtn),
    el("div", { class: "item-actions" }, status));
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

// ---- repos ----
async function openRepos() {
  show("repos");
  servers = await api("GET", "/api/servers");
  const sel = $("r-server");
  sel.replaceChildren(...servers.map((s) => el("option", { value: s.id }, s.name)));
  $("repo-list").replaceChildren(servers.length ? "" : el("p", { class: "muted" }, "Add a git server first."));
}

$("repo-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const id = $("r-server").value;
  if (!id) return banner("Add a git server first.", true);
  guarded(e.submitter, async () => {
    const q = encodeURIComponent($("r-query").value);
    const repos = await api("GET", "/api/servers/" + encodeURIComponent(id) + "/repos?q=" + q);
    $("repo-list").replaceChildren(...(repos.length ? repos.map(repoCard) : [el("p", { class: "muted" }, "No matching repos.")]));
  });
});

function repoCard(r) {
  return el("div", { class: "item" },
    el("div", { class: "item-head" },
      el("span", { class: "name" }, r.fullName),
      el("span", { class: "badge" }, (r.private ? "private" : "public") + " · " + (r.defaultBranch || "?"))),
    r.description ? el("div", { class: "muted" }, r.description) : "",
    el("div", {}, el("code", {}, r.cloneUrl)));
}

// ---- settings: devices, password ----
function describeAgent(ua) {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Windows/.test(ua) ? "Windows" : /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Mac OS/.test(ua) ? "macOS" : /Linux/.test(ua) ? "Linux" : "";
  return ua ? browser + (os ? " on " + os : "") : "Unknown device";
}

function ago(unix) {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (s < 90) return "just now";
  if (s < 5400) return Math.round(s / 60) + " min ago";
  if (s < 129600) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " days ago";
}

async function openSettings() {
  show("settings");
  const logins = await api("GET", "/api/logins");
  $("login-list").replaceChildren(...logins.map((l) => {
    const btn = el("button", {}, l.current ? "Log out here" : "Log out");
    btn.addEventListener("click", () => guarded(btn, async () => {
      await api("DELETE", "/api/logins/" + encodeURIComponent(l.id));
      if (l.current) { showAuth(true); return; }
      await openSettings();
    }));
    return el("div", { class: "item" },
      el("div", { class: "item-head" },
        el("span", { class: "name" }, describeAgent(l.userAgent)),
        l.current ? el("span", { class: "badge" }, "this device") : ""),
      el("div", { class: "muted" }, (l.ip || "unknown address") + " · active " + ago(l.lastSeen) + " · signed in " + ago(l.createdAt)),
      el("div", { class: "item-actions" }, btn));
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
    await openSettings();
    banner("Password changed. Other devices were logged out.");
  });
});

init().catch((e) => banner(e.message, true));
