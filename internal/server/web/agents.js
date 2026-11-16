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
    return el("div", { class: "item" },
      el("div", { class: "item-head" }, el("span", { class: "name" }, a.label), stateBadge(a)),
      el("div", { class: "muted" }, a.description),
      el("div", { class: "item-actions" }, signIn),
      el("details", { class: "spaced" },
        el("summary", {}, "Troubleshoot"),
        el("div", { class: "item-actions" }, inspect, last, reset),
        out));
  }));
}

async function recheckAgents() {
  renderAgents(await api("POST", "/api/agents/recheck"));
}

// ---- the sign-in terminal ----
function closeTerminal() {
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
  ws.onmessage = (e) => term.write(new Uint8Array(e.data));
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
