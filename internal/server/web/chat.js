"use strict";

// A conversation: the owner's messages, the agent's answers, and what happened
// in between folded away. Each reply is a new run of the agent that starts from
// the branch the one before pushed; the server chains them. Uses helpers from
// app.js and sessions.js.

// ---- the agent's text, shown as formatted text ----
// Built from DOM nodes and textContent only, so nothing the agent prints is
// ever parsed as HTML. Links open only when they are http(s); anything else
// (such as file:// links into the sandbox) is shown as plain code.
const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(\[[^\]\n]+\]\([^)\s]+\))/g;

function inlineNodes(text) {
  const out = [];
  let last = 0;
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const t = m[0];
    if (m[1]) out.push(el("code", {}, t.slice(1, -1)));
    else if (m[2]) out.push(el("strong", {}, ...inlineNodes(t.slice(2, -2))));
    else {
      const [, label, url] = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(t);
      out.push(/^https?:\/\//i.test(url)
        ? el("a", { href: url, target: "_blank", rel: "noopener noreferrer" }, label)
        : el("code", {}, label.replace(/^`|`$/g, "")));
    }
    last = m.index + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function renderMarkdown(text) {
  const root = document.createDocumentFragment();
  const lines = text.split("\n");
  let para = [];
  let list = null;
  const flush = () => {
    if (para.length) root.append(el("p", {}, ...inlineNodes(para.join("\n"))));
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (/^\s*```/.test(line)) {
      flush(); list = null;
      const code = [];
      for (i++; i < lines.length && !/^\s*```/.test(lines[i]); i++) code.push(lines[i]);
      root.append(el("pre", { class: "code" }, el("code", {}, code.join("\n"))));
      continue;
    }
    const item = /^(\s*)([*-]|\d+\.)\s+(.*)$/.exec(line);
    if (item) {
      flush();
      const ordered = /\d/.test(item[2]);
      if (!list || list.ordered !== ordered) {
        list = { ordered, node: el(ordered ? "ol" : "ul", {}) };
        root.append(list.node);
      }
      list.node.append(el("li", { style: "margin-left:" + Math.floor(item[1].length / 2) * 16 + "px" }, ...inlineNodes(item[3])));
      continue;
    }
    list = null;
    const heading = /^#{1,4}\s+(.*)$/.exec(line);
    if (heading) { flush(); root.append(el("h4", {}, ...inlineNodes(heading[1]))); continue; }
    if (/^-{3,}\s*$/.test(line)) { flush(); root.append(el("hr", {})); continue; }
    if (!line.trim()) { flush(); continue; }
    para.push(line);
  }
  flush();
  return root;
}

// ---- one log line (progress, blocked connections) ----
// A blocked connection gets an Allow button that adds the address to the
// owner's allowed list for future sessions.
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

// ---- the conversation ----

// ---- the agent asks you something ----
// A question is an "ask" event ("<id>\n<question>"); the answer comes back as
// an "answer" event with the same id. The card shows an answer box until then.
function splitEvent(text) {
  const i = text.indexOf("\n");
  return i < 0 ? [text, ""] : [text.slice(0, i), text.slice(i + 1)];
}

function askCard(t, e) {
  const [id, question] = splitEvent(e.text);
  if (t.asks[id]) return;
  const box = el("textarea", { rows: "2", maxlength: "10000", "aria-label": "Your answer", placeholder: "Your answer" });
  const send = el("button", { type: "submit", class: "primary" }, "Send");
  const form = el("form", { class: "ask-form", autocomplete: "off" }, box,
    el("div", { class: "between" },
      el("span", { class: "muted" }, "The agent will see your answer. Do not paste passwords or tokens."), send));
  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const text = box.value.trim();
    if (!text) return;
    guarded(send, () => api("POST", "/api/sessions/" + encodeURIComponent(t.s.id) + "/answer", { id, text }));
  });
  box.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" && !ev.shiftKey && !ev.isComposing) { ev.preventDefault(); form.requestSubmit(); }
  });
  const card = el("div", { class: "bubble agent ask" },
    el("div", { class: "who muted" }, agentLabel(t.s.agent) + " asks"),
    el("div", { class: "said" }, question), form);
  t.asks[id] = { card, form };
  t.askBox.append(card);
}

function answerCard(t, e) {
  const [id, text] = splitEvent(e.text);
  const a = t.asks[id];
  if (!a) return;
  a.form.replaceWith(el("div", { class: "muted said" }, "You answered: " + text));
  a.form = null;
}

// A question still open when the turn ends will never be answered.
function closeOpenAsks(t) {
  for (const a of Object.values(t.asks)) {
    if (a.form) { a.form.replaceWith(el("div", { class: "muted" }, "No answer was given.")); a.form = null; }
  }
}

let thread = [];      // one entry per turn: { s, node pieces, text, after }
let agentChoices = [];

function makeTurn(s) {
  const steps = el("div", { class: "steps" });
  const summary = el("summary", {}, "Activity");
  const activity = el("details", { class: "activity" }, summary, steps);
  const askBox = el("div", { class: "asks" });
  const answer = el("div", { class: "bubble agent" });
  const foot = el("div", { class: "turn-foot muted" });
  const root = el("div", { class: "turn" },
    el("div", { class: "bubble user" }, el("div", { class: "who muted" }, "You"), el("div", { class: "said" }, s.prompt)),
    activity, askBox, answer, foot);
  const t = { s, root, steps, summary, activity, askBox, answer, foot, asks: {}, text: "", count: 0, after: 0, shownDone: false };
  renderAnswer(t);
  return t;
}

function renderAnswer(t) {
  const running = !isFinished(t.s.status);
  if (!t.text) {
    t.answer.replaceChildren(el("div", { class: "muted" }, running ? "Working…" : "The agent printed no reply."));
    return;
  }
  t.answer.replaceChildren(el("div", { class: "who muted" }, agentLabel(t.s.agent)), el("div", { class: "md" }, renderMarkdown(t.text)));
}

const agentLabel = (id) => (agentChoices.find((a) => a.id === id) || { label: id }).label;

function addEvents(t, events) {
  let textChanged = false;
  for (const e of events) {
    if (e.kind === "out") { t.text += (t.text ? "\n" : "") + e.text; textChanged = true; continue; }
    if (e.kind === "ask") { askCard(t, e); continue; }
    if (e.kind === "answer") { answerCard(t, e); continue; }
    t.steps.append(logLine(e));
    t.count++;
    if (e.kind === "blocked") t.activity.open = true; // something needs the owner's attention
  }
  t.summary.textContent = "Activity (" + t.count + " step" + (t.count === 1 ? "" : "s") + ")";
  if (textChanged) renderAnswer(t);
}

// After a turn ends: what it pushed, why it failed, and what it saved to memory.
async function finishTurn(t) {
  if (t.shownDone) return;
  t.shownDone = true;
  closeOpenAsks(t);
  try { t.s = await api("GET", "/api/sessions/" + encodeURIComponent(t.s.id)); } catch (_) { /* keep what we have */ }
  renderAnswer(t);
  const bits = [];
  if (t.s.status === "failed" || t.s.status === "interrupted" || t.s.status === "stopped") {
    bits.push(el("div", { class: t.s.status === "failed" ? "err" : "" }, t.s.status + (t.s.error ? ": " + t.s.error : "")));
  }
  if (t.s.pushed && t.s.pushed.length) {
    bits.push(el("div", {}, "Pushed ", el("code", {}, t.s.branch), " to " + t.s.pushed.join(", ")));
  } else if (t.s.status === "done") {
    bits.push(el("div", {}, "No changes were pushed."));
  }
  t.foot.replaceChildren(...bits);
  t.memoryNote = el("div", {});
  t.foot.append(t.memoryNote);
}

// Saving to memory goes on after the turn is done. Show that, then the summary.
async function showMemory(t, saving) {
  if (t.memoryShown) return;
  if (saving) { t.memoryNote.textContent = "Saving to memory…"; return; }
  t.memoryShown = true;
  t.memoryNote.textContent = "";
  try {
    const m = await api("GET", "/api/memory/summaries/" + encodeURIComponent(t.s.id));
    t.memoryNote.append(el("details", {}, el("summary", {}, "Saved to memory"), el("pre", { class: "log" }, m.summary)));
  } catch (_) { /* memory was off for this turn, or nothing was saved */ }
}

function setComposer() {
  const tail = thread[thread.length - 1];
  const running = !isFinished(tail.s.status);
  $("reply-text").disabled = running;
  $("reply-text").placeholder = running ? "The agent is working…" : "Reply to the agent";
  $("reply-send").hidden = running;
  $("s-stop").hidden = !running;
  $("s-delete").hidden = running;
  $("reply-agent").disabled = running;
  $("s-title").textContent = thread[0].s.task;
  const badge = $("s-status");
  badge.className = "badge st-" + tail.s.status;
  badge.textContent = tail.s.status;
  $("s-meta").textContent = tail.s.agent + " · network: " + tail.s.network + " · started " + ago(thread[0].s.startedAt || thread[0].s.createdAt);
  $("s-error").hidden = true;
}

const nearBottom = () => window.innerHeight + window.scrollY >= document.body.scrollHeight - 120;

async function openSession(id) {
  stopPolling();
  show("session");
  $("chat").replaceChildren();
  $("reply-text").value = "";
  const token = (pollToken = {});
  const [turns, agents] = await Promise.all([api("GET", "/api/sessions/" + encodeURIComponent(id) + "/thread"), api("GET", "/api/agents")]);
  if (pollToken !== token) return;
  agentChoices = agents;
  thread = turns.map(makeTurn);
  thread.forEach((t) => $("chat").append(t.root));
  const tail = thread[thread.length - 1];
  $("reply-agent").replaceChildren(...agents.map((a) => el("option", { value: a.id },
    a.label + (a.loggedIn === false ? " (not signed in)" : ""))));
  $("reply-agent").value = tail.s.agent;
  setComposer();
  for (const t of thread) await watch(t, token);
}

// Reads a turn's log until it ends. Finished turns are read once; the running
// one is polled.
async function watch(t, token) {
  for (;;) {
    if (pollToken !== token || currentView !== "session") return;
    let more = false;
    try {
      const r = await api("GET", "/api/sessions/" + encodeURIComponent(t.s.id) + "/logs?after=" + t.after);
      if (pollToken !== token) return;
      const stick = nearBottom();
      addEvents(t, r.events);
      if (r.events.length) t.after = r.events[r.events.length - 1].id;
      more = r.events.length === 500;
      t.s.status = r.status; t.s.error = r.error;
      if (t === thread[thread.length - 1]) setComposer();
      if (stick && r.events.length) window.scrollTo(0, document.body.scrollHeight);
      if (r.finished && !more) {
        await finishTurn(t);
        if (t === thread[thread.length - 1]) setComposer(); // the owner can reply now
        await showMemory(t, r.saving);
        if (!r.saving) return;
        // else keep reading: the memory step is still writing to the log
      }
    } catch (e) { banner(e.message, true); }
    await new Promise((res) => schedule(res, more ? 0 : 1000));
  }
}

$("reply-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const text = $("reply-text").value.trim();
  const tail = thread[thread.length - 1];
  if (!text || !tail || !isFinished(tail.s.status)) return;
  guarded($("reply-send"), async () => {
    const s = await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/reply", { prompt: text, agent: $("reply-agent").value });
    $("reply-text").value = "";
    const t = makeTurn(s);
    thread.push(t);
    $("chat").append(t.root);
    setComposer();
    window.scrollTo(0, document.body.scrollHeight);
    watch(t, pollToken);
  });
});

// Enter sends; Shift+Enter makes a new line.
$("reply-text").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); $("reply-form").requestSubmit(); }
});

$("s-stop").addEventListener("click", () => guarded($("s-stop"), async () => {
  await api("POST", "/api/sessions/" + encodeURIComponent(thread[thread.length - 1].s.id) + "/stop");
  banner("Stopping…");
}));

$("s-delete").addEventListener("click", () => {
  if (!confirm("Delete this conversation and its logs?")) return;
  guarded($("s-delete"), async () => {
    for (const t of [...thread].reverse()) await api("DELETE", "/api/sessions/" + encodeURIComponent(t.s.id));
    await openSessions();
  });
});
