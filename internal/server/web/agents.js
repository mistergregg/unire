"use strict";

// The Agents page: each agent's sign-in state, and a terminal in the browser
// that runs the agent's own sign-in command. Uses helpers from app.js and the
// vendored xterm.js (vendor/).

let termSession = null; // { term, ws, onResize }

async function openAgents() {
  stopPolling();
  show("agents");
  closeTerminal();
  renderAgents(await api("GET", "/api/agents"));
}

function stateBadge(a) {
  if (a.loggedIn === true) return el("span", { class: "badge st-done" }, "signed in");
  if (a.loggedIn === false) return el("span", { class: "badge st-failed" }, "not signed in");
  return el("span", { class: "badge" }, "sign-in unknown");
}

// How many models the agent offers, when that list was last checked, and what
// went wrong if a check failed.
function modelsLine(a) {
  const parts = [];
  const n = (a.models || []).length;
  parts.push(n ? n + " model" + (n === 1 ? "" : "s") + " known" : "no model list yet");
  if (a.modelsUpdatedAt) parts.push("checked " + ago(a.modelsUpdatedAt));
  const line = el("div", { class: "models-note" }, el("span", {}, parts.join(" · ")));
  if (a.modelsNote) line.append(el("span", { class: "badge st-failed" }, a.modelsNote));
  return line;
}

// The model the agent uses when a session does not name one. Saving an empty
// value puts it back to whatever the CLI itself picks.
function defaultModelRow(a) {
  const select = el("select", {});
  const typed = el("input", { type: "text", placeholder: "model name", hidden: "" });
  const picker = modelPicker(select, typed, () => "The CLI's own default");
  picker.fill(a, a.defaultModel || "");
  const save = el("button", { type: "button", class: "ghost" }, "Save");
  save.addEventListener("click", () => guarded(save, async () => {
    const r = await api("PUT", "/api/agents/" + encodeURIComponent(a.id) + "/model", { model: picker.value() });
    a.defaultModel = r.model;
    banner(r.model
      ? "New " + a.label + " sessions use " + r.model + " unless they name a model."
      : a.label + " is back to the model its CLI picks.");
  }));
  return el("div", { class: "model-row" },
    el("span", { class: "label" }, "Default model"), select, typed, save);
}

function renderAgents(list) {
  $("agent-list").replaceChildren(...list.map((a) => {
    const signIn = el("button", { class: a.loggedIn ? "" : "primary" }, a.loggedIn ? "Sign in again" : "Sign in");
    signIn.addEventListener("click", () => guarded(signIn, async () => openTerminal(a)));
    // Troubleshooting, tucked away: see what the agent has saved (no secrets), or start clean.
    const out = el("pre", { class: "log", hidden: "" });
    const inspect = el("button", { type: "button", class: "ghost" }, "Saved state");
    inspect.addEventListener("click", () => guarded(inspect, async () => {
      const r = await api("POST", "/api/agents/" + encodeURIComponent(a.id) + "/inspect");
      out.textContent = r.output || "(nothing printed)";
      out.hidden = false;
    }));
    const last = el("button", { type: "button", class: "ghost" }, "Last sign-in output");
    last.addEventListener("click", () => guarded(last, async () => {
      try {
        const r = await api("GET", "/api/agents/" + encodeURIComponent(a.id) + "/last-signin");
        out.textContent = "What the last sign-in terminal showed (links, codes and email addresses removed):\n\n" + (r.output || "(nothing)");
      } catch (e) {
        out.textContent = e.message;
      }
      out.hidden = false;
    }));
    const reset = el("button", { type: "button", class: "ghost danger" }, "Reset sign-in");
    reset.addEventListener("click", () => {
      if (!confirm("Reset " + a.label + "?\n\n" + a.resetHelp + "\n\nYou will need to sign in again.")) return;
      guarded(reset, async () => {
        const r = await api("POST", "/api/agents/" + encodeURIComponent(a.id) + "/reset");
        renderAgents(await api("POST", "/api/agents/recheck"));
        banner(r.output || "Reset.");
      });
    });
    const state = a.loggedIn === true ? "ok" : a.loggedIn === false ? "bad" : "";
    return el("div", { class: "item tile" },
      el("div", { class: "tile-head" },
        el("span", { class: "avatar-wrap" },
          el("span", { class: "avatar lg" }, a.label.charAt(0).toUpperCase()),
          el("span", { class: "state " + state })),
        el("div", { class: "tile-title" }, el("span", { class: "name" }, a.label)),
        stateBadge(a)),
      el("div", { class: "tile-body" }, a.description),
      defaultModelRow(a),
      modelsLine(a),
      el("div", { class: "item-actions" }, signIn),
      el("details", {},
        el("summary", {}, "Troubleshoot"),
        el("div", { class: "item-actions" }, inspect, last, reset),
        out));
  }));
}

async function recheckAgents() {
  renderAgents(await api("POST", "/api/agents/recheck"));
}

// Ask the CLIs what models they offer now. A probe can take most of a minute and
// runs in the background, so the page picks the lists up as they land instead of
// making anyone wait for them.
async function refreshModels() {
  renderAgents(await api("POST", "/api/agents/models/refresh"));
  banner("Asking each signed-in CLI which models it offers; this can take a moment.");
  pollModels().catch(() => {});
}

async function pollModels() {
  for (let i = 0; i < 10; i++) {
    await new Promise((r) => setTimeout(r, 6000));
    if (currentView !== "agents" || termSession) return;
    renderAgents(await api("GET", "/api/agents"));
  }
}

// ---- the sign-in terminal ----

// The agent prints a sign-in link, often wrapped over several lines. Find the
// newest one on screen and offer it as a button, so nobody has to copy it.
let scanTimer = null;
function scheduleLinkScan() {
  clearTimeout(scanTimer);
  scanTimer = setTimeout(() => {
    if (!termSession) return;
    const buf = termSession.term.buffer.active;
    let text = "";
    for (let i = 0; i < buf.length; i++) {
      const line = buf.getLine(i);
      if (line) text += (line.isWrapped ? "" : "\n") + line.translateToString(true);
    }
    const found = text.match(/https:\/\/[^\s"'<>│┃]+/g);
    const link = found && found[found.length - 1];
    $("term-link").hidden = !link;
    if (link) $("term-open").href = link;
  }, 300);
}

$("term-code-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const code = $("term-code").value.trim();
  if (!code || !termSession || termSession.ws.readyState !== WebSocket.OPEN) return;
  termSession.term.paste(code); // as if pasted into the terminal
  const { ws } = termSession;
  setTimeout(() => { if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode("\r")); }, 200);
  $("term-code").value = "";
  termSession.term.focus();
});

function closeTerminal() {
  clearTimeout(scanTimer);
  $("term-link").hidden = true;
  $("term-code").value = "";
  if (!termSession) { $("term-card").hidden = true; return; }
  const { term, ws, onResize } = termSession;
  termSession = null;
  window.removeEventListener("resize", onResize);
  try { ws.onclose = null; ws.close(); } catch (_) { /* already closed */ }
  term.dispose();
  $("term").replaceChildren();
  $("term-card").hidden = true;
}

async function openTerminal(a) {
  const replacing = !!termSession;
  closeTerminal();
  // Give the server a moment to release the previous terminal.
  if (replacing) await new Promise((r) => setTimeout(r, 600));
  if (typeof Terminal === "undefined" || typeof FitAddon === "undefined") throw new Error("The terminal library did not load. Reload the page.");
  $("term-title").textContent = "Sign in to " + a.label;
  $("term-help").textContent = a.loginHelp;
  $("term-card").hidden = false;

  const term = new Terminal({ cursorBlink: true, fontSize: 14, scrollback: 2000, theme: { background: "#0b0e13", foreground: "#e7eaee" } });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  // Make the sign-in links clickable, so a long link that wraps over several
  // lines never has to be copied by hand.
  if (typeof WebLinksAddon !== "undefined") term.loadAddon(new WebLinksAddon.WebLinksAddon());
  term.open($("term"));
  fit.fit();

  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(proto + "://" + location.host + "/api/agents/" + encodeURIComponent(a.id) + "/login?cols=" + term.cols + "&rows=" + term.rows);
  ws.binaryType = "arraybuffer";
  const onResize = () => fit.fit();
  window.addEventListener("resize", onResize);
  termSession = { term, ws, onResize };

  ws.onopen = () => term.focus();
  ws.onmessage = (e) => { term.write(new Uint8Array(e.data)); scheduleLinkScan(); };
  ws.onerror = () => banner("Could not open the sign-in terminal. Another one may already be open, or Docker is not reachable.", true);
  ws.onclose = (e) => {
    if (termSession && termSession.ws === ws) {
      term.write("\r\n\x1b[2m[finished: " + (e.reason || "the connection closed") + "]\x1b[0m\r\n");
      recheckAgents().catch(() => {});
    }
  };
  term.onData((d) => { if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d)); });
  term.onResize(({ cols, rows }) => { if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows })); });
}

$("term-close").addEventListener("click", () => {
  closeTerminal();
  guarded(null, recheckAgents);
});

$("agents-refresh-models").addEventListener("click", (e) => guarded(e.currentTarget, refreshModels));
