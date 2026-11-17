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

const tableCells = (row) => row.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());

function tableBlock(rows) {
  const head = el("tr", {}, ...tableCells(rows[0]).map((c) => el("th", {}, ...inlineNodes(c))));
  const body = rows.slice(1).map((r) => el("tr", {}, ...tableCells(r).map((c) => el("td", {}, ...inlineNodes(c)))));
  return el("div", { class: "tablewrap" }, el("table", {}, el("thead", {}, head), el("tbody", {}, ...body)));
}

function copyText(text, btn) {
  const done = () => { const old = btn.textContent; btn.textContent = "Copied"; setTimeout(() => { btn.textContent = old; }, 1300); };
  if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(done, () => {}); return; }
  const ta = el("textarea", { style: "position:fixed;opacity:0" }, text); // plain http: the older way
  document.body.append(ta); ta.select();
  try { document.execCommand("copy"); done(); } catch (_) { /* not possible here */ }
  ta.remove();
}

function codeBlock(code) {
  const btn = el("button", { type: "button", class: "tiny copy" }, "Copy");
  btn.addEventListener("click", () => copyText(code, btn));
  return el("pre", { class: "code" }, btn, el("code", {}, code));
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
      root.append(codeBlock(code.join("\n")));
      continue;
    }
    // a table: a header row, a row of dashes, then the body rows
    if (/^\s*\|.*\|\s*$/.test(line) && i + 1 < lines.length && /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$/.test(lines[i + 1])) {
      flush(); list = null;
      const rows = [line];
      for (i += 2; i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i]); i++) rows.push(lines[i]);
      i--; // the line after the table is read again by the loop
      root.append(tableBlock(rows));
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

// ---- what you said to the agent while it worked ----
// A "steer" event is "<id>\n<next|now>\n<text>": shown as your message in the
// conversation. A "steerstate" event ("<id>\n<state>") says what became of it.
const STEER_STATES = {
  queued: "waiting for the agent",
  delivered: "the agent has it",
  failed: "not delivered",
  cancelled: "the agent dropped it",
  promoting: "stopping the agent to take it",
};

function steerCard(t, e) {
  const [id, rest] = splitEvent(e.text);
  const [mode, text] = splitEvent(rest);
  if (t.steers[id]) return;
  const status = el("span", { class: "steer-state" }, STEER_STATES.queued);
  const note = mode === "now" ? "sent now: it stopped the agent" : "sent";
  // A message that is still waiting can be made urgent: the agent is stopped and
  // gets it, with anything else that waits, at once.
  const now = el("button", { type: "button", class: "tiny steer-now-btn", title: "Stop what the agent is doing and give it this now" }, "Send now");
  now.addEventListener("click", () => guarded(now, async () => {
    await api("POST", "/api/sessions/" + encodeURIComponent(t.s.id) + "/steer/" + encodeURIComponent(id) + "/now");
    now.hidden = true;
  }));
  const card = el("div", { class: "steer" },
    el("div", { class: "bubble user" }, el("div", { class: "said" }, text)),
    el("div", { class: "time" }, note + " · ", status, " ", now));
  now.hidden = mode === "now";
  t.steers[id] = { card, status, now, mode };
  t.steerBox.append(card);
}

function steerState(t, e) {
  const [id, state] = splitEvent(e.text);
  const s = t.steers[id];
  if (!s) return;
  s.status.textContent = STEER_STATES[state] || state;
  s.status.className = "steer-state " + state;
  s.now.hidden = state !== "queued" || s.mode === "now"; // only a message that is still waiting (and was not sent now) can be sent now
}

let thread = [];      // one entry per turn: { s, node pieces, text, after }
let agentChoices = [];

const agentLabel = (id) => (agentChoices.find((a) => a.id === id) || { label: id }).label;
const initialOf = (id) => (agentLabel(id) || "?").trim().charAt(0).toUpperCase();

// One turn: your message, then the agent's reply with what it is doing above it.
function makeTurn(s) {
  const spin = el("span", { class: "spin" });
  const stepText = el("span", { class: "step-text" });
  const dots = el("span", { class: "working-dots" });
  const summary = el("summary", {}, spin, stepText, dots);
  const steps = el("div", { class: "steps" });
  // Open while the agent works, so what it is doing is in view; folded away when it is done.
  const activity = el("details", { class: "activity" }, summary, steps);
  activity.open = !isFinished(s.status);
  const askBox = el("div", { class: "asks" });
  const steerBox = el("div", { class: "steers" });
  const answer = el("div", { class: "agent-text" });
  const foot = el("div", { class: "turn-foot muted" });
  const avatar = el("div", { class: "avatar", "aria-hidden": "true" }, initialOf(s.agent));
  const head = el("div", { class: "msg-head" }, el("span", { class: "name" }, agentLabel(s.agent)), el("span", { class: "time" }, ago(s.startedAt || s.createdAt)));
  const user = el("div", { class: "msg user" }, el("div", { class: "bubble user" }, el("div", { class: "said" }, s.prompt)), el("div", { class: "time" }, ago(s.createdAt)));
  const body = el("div", { class: "msg-body" }, head, activity, askBox, steerBox, answer, foot);
  const root = el("div", { class: "turn" }, user, el("div", { class: "msg agent" }, avatar, body));
  const t = { s, root, steps, summary, spin, stepText, dots, activity, askBox, steerBox, answer, foot, avatar, asks: {}, steers: {}, text: "", count: 0, after: 0, shownDone: false, shown: "" };
  renderSummary(t);
  renderAnswer(t);
  return t;
}

// The one line that says what the agent is doing now, or how much it did.
function renderSummary(t) {
  const running = !isFinished(t.s.status);
  t.activity.classList.toggle("running", running);
  t.avatar.classList.toggle("live", running);
  t.spin.hidden = t.dots.hidden = !running;
  t.stepText.textContent = running ? (t.lastStep || "Working") : "Activity · " + t.count + " step" + (t.count === 1 ? "" : "s");
}

function renderAnswer(t) {
  const running = !isFinished(t.s.status);
  if (!t.text) {
    const state = running ? "skeleton" : "none";
    if (t.shown === state) return; // keep the animation running instead of restarting it
    t.shown = state;
    t.answer.replaceChildren(running
      ? el("div", { class: "skeleton" }, el("i"), el("i"), el("i"))
      : el("div", { class: "muted" }, "The agent printed no reply."));
    return;
  }
  t.shown = "text";
  t.answer.replaceChildren(el("div", { class: "md" }, renderMarkdown(t.text)));
}

function addEvents(t, events) {
  let textChanged = false;
  for (const e of events) {
    if (e.kind === "out") { t.text += (t.text ? "\n" : "") + e.text; textChanged = true; continue; }
    if (e.kind === "ask") { askCard(t, e); continue; }
    if (e.kind === "answer") { answerCard(t, e); continue; }
    if (e.kind === "steer") { steerCard(t, e); continue; }
    if (e.kind === "steerstate") { steerState(t, e); continue; }
    if (e.kind === "prtitle" || e.kind === "prbody") continue; // what the agent suggests for a pull request, used when you are asked
    t.steps.append(logLine(e));
    t.count++;
    if (e.kind === "log") t.lastStep = e.text;
    if (e.kind === "blocked") { t.activity.open = true; t.needsLook = true; } // something needs the owner's attention
  }
  if (events.length) t.steps.scrollTop = t.steps.scrollHeight;
  renderSummary(t);
  if (textChanged) renderAnswer(t);
}

// After a turn ends: what it pushed, why it failed, and what it saved to memory.
async function finishTurn(t) {
  if (t.shownDone) return;
  t.shownDone = true;
  closeOpenAsks(t);
  if (!t.needsLook) t.activity.open = false;
  try { t.s = await api("GET", "/api/sessions/" + encodeURIComponent(t.s.id)); } catch (_) { /* keep what we have */ }
  renderSummary(t);
  renderAnswer(t);
  const bits = [];
  if (t.s.status === "failed" || t.s.status === "interrupted" || t.s.status === "stopped") {
    bits.push(el("span", { class: t.s.status === "failed" ? "err" : "" }, t.s.status + (t.s.error ? ": " + t.s.error : "")));
  }
  if (t.s.pushed && t.s.pushed.length) {
    bits.push(el("span", {}, "Pushed ", el("code", {}, t.s.branch), " to " + t.s.pushed.join(", ")));
  } else if (t.s.status === "done") {
    bits.push(el("span", {}, "No changes were pushed."));
  }
  if (t.text) {
    const copy = el("button", { type: "button", class: "tiny" }, "Copy reply");
    copy.addEventListener("click", () => copyText(t.text, copy));
    bits.push(el("span", { class: "actions" }, copy));
  }
  t.foot.replaceChildren(...bits);
  t.memoryNote = el("span", {});
  t.foot.append(t.memoryNote);
  refreshPulls(); // the turn may have pushed a branch: offer a pull request
  refreshLinks();
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

const pill = (text, title) => el("span", { class: "pill", title: title || "" }, text);

function setComposer() {
  const tail = thread[thread.length - 1];
  const running = !isFinished(tail.s.status);
  const steerable = running && tail.s.canSteer; // it takes messages while it works
  $("reply-text").disabled = running && !steerable;
  $("reply-text").placeholder = steerable ? "Tell the agent something while it works…"
    : running ? "The agent is working…" : "Reply to the agent";
  $("reply-send").hidden = running && !steerable;
  $("reply-send").title = steerable ? "Send: the agent reads it at its next step" : "Send";
  $("steer-now").hidden = !steerable;
  $("composer-hint").textContent = steerable
    ? "Enter: for its next step · Ctrl+Enter: send now"
    : "Enter to send · Shift+Enter for a new line";
  $("s-stop").hidden = !running;
  $("s-delete").hidden = running;
  $("reply-agent").disabled = running;
  $("s-title").textContent = thread[0].s.task;
  const badge = $("s-status");
  badge.className = "badge st-" + tail.s.status;
  badge.textContent = tail.s.status;
  const repos = thread[0].s.repos.map((r) => r.fullName.split("/").pop());
  $("s-meta").replaceChildren(
    pill(agentLabel(tail.s.agent)),
    pill("network: " + tail.s.network),
    ...repos.slice(0, 4).map((r) => pill(r)),
    ...(repos.length > 4 ? [pill("+" + (repos.length - 4) + " more", repos.slice(4).join(", "))] : []),
    pill("started " + ago(thread[0].s.startedAt || thread[0].s.createdAt)));
  $("s-error").hidden = true;
}

// The message box grows with what you type, up to a limit.
function autoGrow() {
  const box = $("reply-text");
  box.style.height = "auto";
  box.style.height = Math.min(box.scrollHeight, 200) + "px";
}
$("reply-text").addEventListener("input", autoGrow);

const nearBottom = () => window.innerHeight + window.scrollY >= document.body.scrollHeight - 120;

async function openSession(id) {
  stopPolling();
  show("session");
  $("chat").replaceChildren();
  resetPulls();
  linksData = null;
  $("s-links").replaceChildren();
  $("reply-text").value = "";
  autoGrow();
  const token = (pollToken = {});
  const [turns, agents] = await Promise.all([api("GET", "/api/sessions/" + encodeURIComponent(id) + "/thread"), api("GET", "/api/agents")]);
  if (pollToken !== token) return;
  agentChoices = agents;
  thread = turns.map(makeTurn);
  selectedRoot = thread[0].s.id;
  setHash(selectedRoot);
  refreshSidebar();
  thread.forEach((t) => $("chat").append(t.root));
  const tail = thread[thread.length - 1];
  $("reply-agent").replaceChildren(...agents.map((a) => el("option", { value: a.id },
    a.label + (a.loggedIn === false ? " (not signed in)" : ""))));
  $("reply-agent").value = tail.s.agent;
  setComposer();
  // Keep reading the log in the background. Waiting for it here would leave the
  // button that opened this chat disabled until the agent has finished.
  (async () => { for (const t of thread) await watch(t, token); })().catch((e) => banner(e.message, true));
  watchPulls(token);
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
      if (!t.seen) { // the first look at a turn: open a chat at its newest message
        t.seen = true;
        if (t === thread[thread.length - 1]) window.scrollTo(0, document.body.scrollHeight);
      }
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
    // Each loop waits on its own timer: a shared one let a second loop (a reply
    // sent while the last turn was still saving its memory) cancel this one's wait,
    // so the page stopped updating until it was reopened.
    await new Promise((res) => setTimeout(res, more ? 0 : 1000));
  }
}

$("reply-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const text = $("reply-text").value.trim();
  const tail = thread[thread.length - 1];
  if (!text || !tail) return;
  if (!isFinished(tail.s.status)) { if (tail.s.canSteer) steer(text, false); return; }
  guarded($("reply-send"), async () => {
    await sendReply(text);
    $("reply-text").value = "";
    autoGrow();
  });
});

// Sends a message as the next turn of the conversation and starts following it.
async function sendReply(text) {
  const tail = thread[thread.length - 1];
  const s = await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/reply", { prompt: text, agent: $("reply-agent").value });
  const t = makeTurn(s);
  thread.push(t);
  $("chat").append(t.root);
  setComposer();
  renderPulls();
  window.scrollTo(0, document.body.scrollHeight);
  watch(t, pollToken);
}

// Say something to the agent while it works. now=true stops what it is doing and
// carries on from the message. The message appears in the conversation when the
// server logs it (see steerCard).
function steer(text, now) {
  const tail = thread[thread.length - 1];
  const btn = now ? $("steer-now") : $("reply-send");
  guarded(btn, async () => {
    await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/steer", { text, now });
    $("reply-text").value = "";
    autoGrow();
  });
}

// Enter sends; Shift+Enter makes a new line; Ctrl+Enter (or the Send now button)
// steers a working agent right away.
$("reply-text").addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
  e.preventDefault();
  const tail = thread[thread.length - 1];
  const text = $("reply-text").value.trim();
  if ((e.ctrlKey || e.metaKey) && text && tail && !isFinished(tail.s.status) && tail.s.canSteer) steer(text, true);
  else $("reply-form").requestSubmit();
});
$("steer-now").addEventListener("click", () => {
  const text = $("reply-text").value.trim();
  if (text) steer(text, true);
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
