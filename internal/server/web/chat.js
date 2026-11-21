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
      if (/^https?:\/\//i.test(url)) {
        out.push(el("a", { href: url, target: "_blank", rel: "noopener noreferrer" }, label));
      } else if (/^file:\/\//i.test(url)) {
        const cleanPath = url.replace(/^file:\/\//i, "").split(/[?#]/)[0];
        const base = cleanPath.split("/").pop();
        const a = el("a", { href: "#", class: "file-link", title: url }, label);
        a.addEventListener("click", (evt) => {
          evt.preventDefault();
          const cards = document.querySelectorAll(".share-card");
          for (const c of cards) {
            const nameEl = c.querySelector(".share-name");
            if (nameEl && (nameEl.textContent === base || c.dataset.filename === base)) {
              c.open = true;
              c.scrollIntoView({ behavior: "smooth", block: "nearest" });
              return;
            }
          }
        });
        out.push(a);
      } else {
        out.push(el("code", {}, label.replace(/^`|`$/g, "")));
      }
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
    // Loopback and metadata addresses cannot be allowed (the server refuses): a server the
    // agent runs in its own sandbox is reached directly, not through the proxy.
    const raw = m ? m[1] : "";
    const addr = raw.startsWith("[") ? raw.replace(/^\[|\](:\d+)?$/g, "") // [::1]:80
      : (raw.match(/:/g) || []).length === 1 ? raw.replace(/:\d+$/, "") : raw; // 127.0.0.1:80, or a bare IPv6
    const never = /^(localhost|127\.|0\.0\.0\.0|::1|169\.254\.)/.test(addr) || addr.endsWith(".localhost");
    if (m && never) {
      line.append(el("span", { class: "muted" }, "(a sandbox's own address: it cannot be allowed, and a tool should reach its own localhost directly)"));
    } else if (m) {
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
    box.disabled = true;
    guarded(send, async () => {
      try {
        await api("POST", "/api/sessions/" + encodeURIComponent(t.s.id) + "/answer", { id, text });
        answerCard(t, { text: id + "\n" + text });
      } catch (err) {
        box.disabled = false;
        throw err;
      }
    });
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
  if (!a || !a.form) return;
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
  const entry = { mode, text, id, status };
  const now = el("button", { type: "button", class: "tiny steer-now-btn", title: "Stop what the agent is doing and give it this now" }, "Send now");
  now.disabled = id.startsWith("pending-");
  now.addEventListener("click", () => guarded(now, async () => {
    await api("POST", "/api/sessions/" + encodeURIComponent(t.s.id) + "/steer/" + encodeURIComponent(entry.id) + "/now");
    now.hidden = true;
  }));
  const thumbs = el("div", { class: "attach-thumbs" }, ...attachThumbs(e.images || t.attached[id] || []));
  entry.thumbs = thumbs;
  const card = el("div", { class: "steer" },
    el("div", { class: "bubble user" }, el("div", { class: "said" }, text), thumbs),
    el("div", { class: "time" }, note + " · ", status, " ", now));
  now.hidden = mode === "now";
  entry.card = card;
  entry.now = now;
  t.steers[id] = entry;
  splitTurn(t, card); // in the timeline where you sent it, not in a pile after the activity
}

function steerState(t, e) {
  const [id, state] = splitEvent(e.text);
  const s = t.steers[id];
  if (!s) return;
  s.status.textContent = STEER_STATES[state] || state;
  s.status.className = "steer-state " + state;
  s.now.hidden = state !== "queued" || s.mode === "now"; // only a message that is still waiting (and was not sent now) can be sent now
}

// Files sent with a message: an "attach" event, "<message>\n<file> <name>\n…".
// <message> is empty for the turn's prompt, or the id of a message sent while the
// agent worked (its "steer" event follows). The files are served by the server.
function attachedCard(t, e) {
  const [target, rest] = splitEvent(e.text);
  const list = rest.split("\n").filter(Boolean).map((line) => {
    const [file, name] = splitEvent(line.replace(" ", "\n"));
    const src = "/api/sessions/" + encodeURIComponent(t.s.id) + "/attachments/" + encodeURIComponent(file);
    return { name: name || file, src, href: src, image: isImageName(file) };
  });
  if (!target) { t.userThumbs.replaceChildren(...attachThumbs(list)); return; }
  t.attached[target] = list;
  const s = t.steers[target];
  if (s) s.thumbs.replaceChildren(...attachThumbs(list));
}

let thread = [];      // one entry per turn: { s, node pieces, text, after }
let agentChoices = [];

const agentLabel = (id) => (agentChoices.find((a) => a.id === id) || { label: id }).label;
const initialOf = (id) => (agentLabel(id) || "?").trim().charAt(0).toUpperCase();

// A turn is a timeline. The agent works (its activity, then maybe some text), you say
// something while it works, and what it does after that comes below your message, not
// above it. Each stretch between your messages is a "segment": its activity, its
// questions, its text and the files it shared. t.* always points at the newest one.
function newSegment(running) {
  const spin = el("span", { class: "spin" });
  const stepText = el("span", { class: "step-text" });
  const dots = el("span", { class: "working-dots" });
  const summary = el("summary", {}, spin, stepText, dots);
  const steps = el("div", { class: "steps" });
  // Open while the agent works, so what it is doing is in view; folded away when it is done.
  const activity = el("details", { class: "activity" }, summary, steps);
  activity.open = running;
  const askBox = el("div", { class: "asks" });
  const shareBox = el("div", { class: "shares" });
  const answer = el("div", { class: "agent-text" });
  const segRoot = el("div", { class: "seg" }, activity, askBox, answer, shareBox);
  return { segRoot, spin, stepText, dots, summary, steps, activity, askBox, shareBox, answer, count: 0, text: "", lastStep: "", shown: "" };
}

// One turn: your message, then the agent's reply with what it is doing above it.
function makeTurn(s) {
  const seg = newSegment(!isFinished(s.status));
  const foot = el("div", { class: "turn-foot muted" });
  const avatar = el("div", { class: "avatar", "aria-hidden": "true" }, initialOf(s.agent));
  const head = el("div", { class: "msg-head" }, el("span", { class: "name" }, agentLabel(s.agent)),
    ...(s.model ? [el("span", { class: "model", title: "Model chosen for this turn" }, s.model)] : []),
    el("span", { class: "time" }, ago(s.startedAt || s.createdAt)));
  const userThumbs = el("div", { class: "attach-thumbs" });
  const user = el("div", { class: "msg user" }, el("div", { class: "bubble user" }, el("div", { class: "said" }, s.prompt), userThumbs), el("div", { class: "time" }, ago(s.createdAt)));
  const body = el("div", { class: "msg-body" }, head, seg.segRoot, foot);
  const root = el("div", { class: "turn" }, user, el("div", { class: "msg agent" }, avatar, body));
  const t = { s, root, body, foot, avatar, userThumbs, ...seg, replies: [], asks: {}, steers: {}, attached: {}, after: 0, shownDone: false };
  renderSummary(t);
  renderAnswer(t);
  return t;
}

// Your message goes in the timeline at the point where you sent it: what the agent did
// so far is closed off above it, and what it does next starts a new block below.
function splitTurn(t, card) {
  // Close the stretch before your message: it shows what it did, not that it is working.
  t.activity.classList.remove("running");
  t.spin.hidden = t.dots.hidden = true;
  t.stepText.textContent = "Activity · " + t.count + " step" + (t.count === 1 ? "" : "s");
  if (!t.needsLook) t.activity.open = false;
  if (t.text) {
    t.answer.replaceChildren(el("div", { class: "md" }, renderMarkdown(t.text)));
    t.replies.push(t.text);
  } else {
    t.answer.remove(); // nothing was said in this stretch: no skeleton, no "printed no reply"
  }
  if (!t.count && !t.text && !t.askBox.children.length && !t.shareBox.children.length) t.segRoot.remove();
  const seg = newSegment(!isFinished(t.s.status));
  t.body.insertBefore(card, t.foot);
  t.body.insertBefore(seg.segRoot, t.foot);
  Object.assign(t, seg);
  t.needsLook = false;
  renderSummary(t);
  renderAnswer(t);
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
    // After your message the agent may have said nothing more: say so only when the
    // whole turn said nothing.
    const state = running ? "skeleton" : t.replies.length ? "quiet" : "none";
    if (t.shown === state) return; // keep the animation running instead of restarting it
    t.shown = state;
    t.answer.replaceChildren(...(running ? [el("div", { class: "skeleton" }, el("i"), el("i"), el("i"))]
      : t.replies.length ? [] : [el("div", { class: "muted" }, "The agent printed no reply.")]));
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
    if (e.kind === "share") { shareCard(t, e); continue; }
    if (e.kind === "attach") { attachedCard(t, e); continue; }
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
  const whole = [...t.replies, t.text].filter(Boolean).join("\n\n"); // every stretch of the reply
  if (whole) {
    const copy = el("button", { type: "button", class: "tiny" }, "Copy reply");
    copy.addEventListener("click", () => copyText(whole, copy));
    bits.push(el("span", { class: "actions" }, copy));
  }
  t.foot.replaceChildren(...bits);
  t.memoryNote = el("span", {});
  t.foot.append(t.memoryNote);
  refreshPulls(); // the turn may have pushed a branch: offer a pull request
  refreshLinks();
  if (t === thread[thread.length - 1]) usageChanged(); // the turn may have reported usage
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
  $("reply-attach").disabled = running && !steerable;
  $("reply-text").placeholder = steerable ? "Tell the agent something while it works…"
    : running ? "The agent is working…" : "Reply to the agent";
  $("reply-send").hidden = running && !steerable;
  $("reply-send").title = steerable ? "Send: the agent reads it at its next step" : "Send";
  $("steer-now").hidden = !steerable;
  $("composer-hint").textContent = steerable
    ? "Enter: for its next step · Ctrl+Enter: send now"
    : "Enter to send · Shift+Enter for a new line";
  $("s-stop").hidden = !running;
  $("s-stop").disabled = !tail.s.id || tail.s.id.startsWith("pending-");
  $("s-delete").hidden = running;
  $("reply-agent").disabled = running;
  replyModel.disabled = running;
  $("s-title").textContent = thread[0].s.task;
  const badge = $("s-status");
  badge.className = "badge st-" + tail.s.status;
  badge.textContent = tail.s.status;
  const repos = thread[0].s.repos.map((r) => r.fullName.split("/").pop());
  $("s-meta").replaceChildren(
    pill(agentLabel(tail.s.agent) + (tail.s.model ? " · " + tail.s.model : "")),
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
  box.style.height = Math.min(Math.max(box.scrollHeight, 44), 320) + "px";
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
  resetSidePanel();
  $("reply-text").value = "";
  replyAttach.clear();
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
  fillReplyModel();
  setComposer();
  // Keep reading the log in the background. Waiting for it here would leave the
  // button that opened this chat disabled until the agent has finished.
  (async () => { for (const t of thread) await watch(t, token); })().catch((e) => banner(e.message, true));
  watchPulls(token);
  syncThread(token);
}

// A turn can be added to the chat you have open from somewhere else: a reply sent from
// your phone, an automatic fix. Look every few seconds and show new turns as they
// appear, so there is no need to reload the page. (The chat list does the same from
// attention.js.) While a message of yours is on its way, wait: it would show twice.
async function syncThread(token) {
  const waiting = () => thread.some((t) => t.s.id.startsWith("pending-"));
  while (pollToken === token && currentView === "session") {
    await new Promise((res) => setTimeout(res, 4000));
    if (pollToken !== token || currentView !== "session" || document.hidden || waiting()) continue;
    try {
      const turns = await api("GET", "/api/sessions/" + encodeURIComponent(selectedRoot) + "/thread");
      if (pollToken !== token || currentView !== "session" || waiting() || !turns.length) continue;
      const known = new Set(thread.map((t) => t.s.id));
      const stick = nearBottom();
      let changed = false;
      for (const s of turns) {
        if (known.has(s.id)) continue;
        const t = makeTurn(s);
        thread.push(t);
        $("chat").append(t.root);
        watch(t, token);
        changed = true;
      }
      if (turns[0].task !== thread[0].s.task) { thread[0].s.task = turns[0].task; changed = true; } // the agent named the chat
      if (changed) {
        setComposer();
        renderPulls();
        if (stick) window.scrollTo(0, document.body.scrollHeight);
      }
    } catch (_) { /* the next look will do */ }
  }
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

// Files attached to the next reply or message.
const replyAttach = attachPicker($("reply-files"), $("reply-attach"), $("reply-text"), $("reply-previews"));

$("reply-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const text = $("reply-text").value.trim();
  const tail = thread[thread.length - 1];
  if (!text || !tail) return;
  if (!isFinished(tail.s.status)) { if (tail.s.canSteer) steer(text, false); return; }
  sendReply(text);
});

// The model for the next message: the last turn's while the agent stays the same,
// the agent's default when another agent is picked.
const replyModel = modelPicker($("reply-model"), $("reply-model-name"));
function fillReplyModel() {
  const tail = thread[thread.length - 1];
  const agent = $("reply-agent").value;
  replyModel.fill(agentChoices.find((a) => a.id === agent), tail && tail.s.agent === agent ? tail.s.model : "");
}
$("reply-agent").addEventListener("change", fillReplyModel);

// Sends a message as the next turn of the conversation and starts following it.
async function sendReply(text) {
  const tail = thread[thread.length - 1];
  const agent = $("reply-agent").value;
  const model = replyModel.value();
  const nowUnix = Math.floor(Date.now() / 1000);
  const optSession = {
    id: "pending-" + Date.now(),
    task: tail.s.task,
    agent,
    model,
    prompt: text,
    status: "queued",
    createdAt: nowUnix,
    startedAt: nowUnix,
    network: tail.s.network,
    repos: tail.s.repos || [],
    branch: tail.s.branch,
  };
  const images = replyAttach.list();
  const t = makeTurn(optSession);
  t.userThumbs.replaceChildren(...attachThumbs(images.map((a) => ({ name: a.name, src: a.data, image: a.image }))));
  thread.push(t);
  $("chat").append(t.root);
  $("reply-text").value = "";
  replyAttach.clear();
  autoGrow();
  setComposer();
  window.scrollTo(0, document.body.scrollHeight);

  const token = pollToken;
  guarded($("reply-send"), async () => {
    try {
      const s = await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/reply", { prompt: text, agent, model, images });
      t.s = s;
      if (pollToken !== token || currentView !== "session") return;
      setComposer();
      renderPulls();
      watch(t, token);
    } catch (err) {
      if (pollToken === token && currentView === "session") {
        t.root.remove();
        const idx = thread.indexOf(t);
        if (idx >= 0) thread.splice(idx, 1);
        $("reply-text").value = text;
        replyAttach.set(images);
        autoGrow();
        setComposer();
        $("reply-text").focus();
      }
      throw err;
    }
  });
}

// Say something to the agent while it works. now=true stops what it is doing and
// carries on from the message. The message appears in the conversation when the
// server logs it (see steerCard).
function steer(text, now) {
  const tail = thread[thread.length - 1];
  const btn = now ? $("steer-now") : $("reply-send");
  const tempId = "pending-steer-" + Date.now();
  const images = replyAttach.list();
  steerCard(tail, { text: tempId + "\n" + (now ? "now" : "next") + "\n" + text, images: images.map((a) => ({ name: a.name, src: a.data, image: a.image })) });
  $("reply-text").value = "";
  replyAttach.clear();
  autoGrow();
  window.scrollTo(0, document.body.scrollHeight);

  const token = pollToken;
  guarded(btn, async () => {
    try {
      const res = await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/steer", { text, now, images });
      if (res && res.id) {
        const pending = tail.steers[tempId];
        if (pending) {
          delete tail.steers[tempId];
          pending.id = res.id;
          pending.now.disabled = false;
          tail.steers[res.id] = pending;
        }
      }
    } catch (err) {
      if (pollToken === token && currentView === "session") {
        const pending = tail.steers[tempId];
        if (pending) {
          pending.card.remove();
          delete tail.steers[tempId];
        }
        $("reply-text").value = text;
        replyAttach.set(images);
        autoGrow();
        $("reply-text").focus();
      }
      throw err;
    }
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
  const tail = thread[thread.length - 1];
  if (!tail || !tail.s.id || tail.s.id.startsWith("pending-")) return;
  await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/stop");
  banner("Stopping…");
}));

$("s-delete").addEventListener("click", () => {
  if (!confirm("Delete this conversation and its logs?")) return;
  guarded($("s-delete"), async () => {
    for (const t of [...thread].reverse()) await api("DELETE", "/api/sessions/" + encodeURIComponent(t.s.id));
    await openSessions();
  });
});

// ---- files the agent shared with you ----
// A "share" event is "<name>\n<text|image>\n<content>" (an image is base64). It
// shows as a card you can expand, copy and download. Text goes through the same
// markdown renderer as the reply, so nothing in it is ever run as HTML.
const SHARE_IMAGES = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp" };

function download(name, blob) {
  const url = URL.createObjectURL(blob);
  const a = el("a", { href: url, download: name });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
}

function shareCard(t, e) {
  const [name, rest] = splitEvent(e.text);
  const [kind, content] = splitEvent(rest);
  const ext = name.split(".").pop().toLowerCase();
  const mime = SHARE_IMAGES[ext];
  const actions = el("span", { class: "actions" });
  const viewSide = el("button", { type: "button", class: "tiny", title: "View this file in the side inspector" }, "View side");
  viewSide.addEventListener("click", () => viewSideFile(name));
  actions.append(viewSide);
  let body;
  if (kind === "image" && mime) {
    const src = "data:" + mime + ";base64," + content;
    const saveImage = () => {
      const bin = atob(content);
      const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
      download(name, new Blob([bytes], { type: mime }));
    };
    const doShare = () => {
      const bin = atob(content);
      const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
      shareImage({ src, name, blob: new Blob([bytes], { type: mime }) });
    };
    body = el("img", { class: "share-img", src, alt: name, title: name + " · Click to view full screen" });
    body.addEventListener("click", () => openImageModal({ src, name, onDownload: saveImage, onShare: doShare }));
    const full = el("button", { type: "button", class: "tiny", title: "View this image full screen" }, "Full screen");
    full.addEventListener("click", () => openImageModal({ src, name, onDownload: saveImage, onShare: doShare }));
    const save = el("button", { type: "button", class: "tiny" }, "Download");
    save.addEventListener("click", saveImage);
    actions.append(full, save);
    if (navigator.share) {
      const share = el("button", { type: "button", class: "tiny", title: "Share this image" }, "Share");
      share.addEventListener("click", doShare);
      actions.append(share);
    }
  } else {
    const isMd = /\.(md|markdown)$/i.test(name);
    body = isMd ? el("div", { class: "md" }, renderMarkdown(content)) : el("pre", { class: "log" }, content);
    const copy = el("button", { type: "button", class: "tiny" }, "Copy");
    copy.addEventListener("click", () => copyText(content, copy));
    const save = el("button", { type: "button", class: "tiny" }, "Download");
    save.addEventListener("click", () => download(name, new Blob([content], { type: "text/plain;charset=utf-8" })));
    actions.append(copy, save);
  }
  const size = kind === "image" ? "" : " · " + Math.max(1, Math.round(content.length / 1024)) + " KB";
  const card = el("details", { class: "share-card" },
    el("summary", {}, el("span", { class: "share-name" }, name), el("span", { class: "muted" }, size)),
    el("div", { class: "share-actions" }, actions),
    el("div", { class: "share-body" }, body));
  card.dataset.filename = name;
  // An agent that shares a file again (after changing it) replaces the card it shared
  // before, so the chat shows the newest copy once.
  const older = [...t.shareBox.children].find((c) => c.dataset.filename === name);
  if (older) older.replaceWith(card);
  else t.shareBox.append(card);

  registerSideFile({ name, kind, content, ext, mime });
}

// ---- Side Panel: Files & Session Info Inspector ----
let sessionFiles = new Map();
let sidePanelOpen = false;
let sideActiveTab = "files";

function initSidePanel() {
  const toggleBtn = $("s-toggle-side");
  const closeBtn = $("side-panel-close");
  const tabFiles = $("side-tab-files");
  const tabPrs = $("side-tab-prs");
  const tabInfo = $("side-tab-info");
  const viewerBack = $("side-viewer-back");

  if (toggleBtn) {
    toggleBtn.addEventListener("click", () => {
      setSidePanelOpen(!sidePanelOpen);
    });
  }
  if (closeBtn) {
    closeBtn.addEventListener("click", () => {
      setSidePanelOpen(false);
    });
  }
  if (tabFiles) {
    tabFiles.addEventListener("click", () => selectSideTab("files"));
  }
  if (tabPrs) {
    tabPrs.addEventListener("click", () => selectSideTab("prs"));
  }
  if (tabInfo) {
    tabInfo.addEventListener("click", () => selectSideTab("info"));
  }
  if (viewerBack) {
    viewerBack.addEventListener("click", () => {
      const viewer = $("side-file-viewer");
      const list = $("side-files-list-view");
      if (viewer) viewer.hidden = true;
      if (list) list.hidden = false;
    });
  }
}

function setSidePanelOpen(open) {
  sidePanelOpen = open;
  const body = document.querySelector(".session-body");
  if (body) {
    body.classList.toggle("side-open", open);
  }
  const toggleBtn = $("s-toggle-side");
  if (toggleBtn) {
    toggleBtn.setAttribute("aria-expanded", String(open));
    toggleBtn.classList.toggle("active", open);
  }
}

function selectSideTab(tab) {
  sideActiveTab = tab;
  const isFiles = tab === "files";
  const isPrs = tab === "prs";
  const isInfo = tab === "info";
  const tabF = $("side-tab-files");
  const tabP = $("side-tab-prs");
  const tabI = $("side-tab-info");
  const paneF = $("side-pane-files");
  const paneP = $("side-pane-prs");
  const paneI = $("side-pane-info");
  if (tabF) {
    tabF.classList.toggle("active", isFiles);
    tabF.setAttribute("aria-selected", String(isFiles));
  }
  if (tabP) {
    tabP.classList.toggle("active", isPrs);
    tabP.setAttribute("aria-selected", String(isPrs));
  }
  if (tabI) {
    tabI.classList.toggle("active", isInfo);
    tabI.setAttribute("aria-selected", String(isInfo));
  }
  if (paneF) paneF.hidden = !isFiles;
  if (paneP) paneP.hidden = !isPrs;
  if (paneI) paneI.hidden = !isInfo;
}

function updateSideBadge() {
  const sideBadge = $("s-side-badge");
  if (!sideBadge) return;
  const filesCount = sessionFiles ? sessionFiles.size : 0;
  const prsCount = (typeof pullsData !== "undefined" && pullsData && pullsData.pulls) ? pullsData.pulls.length : 0;
  const total = filesCount + prsCount;
  sideBadge.textContent = total;
  sideBadge.hidden = total === 0;
}

function resetSidePanel() {
  sessionFiles.clear();
  const fileViewer = $("side-file-viewer");
  const fileListView = $("side-files-list-view");
  if (fileViewer) fileViewer.hidden = true;
  if (fileListView) fileListView.hidden = false;
  const empty = $("side-files-empty");
  if (empty) empty.hidden = false;
  const list = $("side-files-list");
  if (list) {
    list.hidden = true;
    list.replaceChildren();
  }
  const badge = $("side-files-count");
  if (badge) badge.hidden = true;
  const prsBadge = $("side-prs-count");
  if (prsBadge) prsBadge.hidden = true;
  const sideBadge = $("s-side-badge");
  if (sideBadge) sideBadge.hidden = true;
  const prsEmpty = $("side-prs-empty");
  if (prsEmpty) prsEmpty.hidden = false;
  const prsList = $("side-prs-list");
  if (prsList) {
    prsList.hidden = true;
    prsList.replaceChildren();
  }
  selectSideTab("files");
  if (window.innerWidth >= 1100) {
    setSidePanelOpen(true);
  } else {
    setSidePanelOpen(false);
  }
}

function renderSidePRs(data) {
  const prs = (data && data.pulls) || [];
  const proposals = (data && data.proposals) || [];
  const count = prs.length;
  const countBadge = $("side-prs-count");
  if (countBadge) {
    countBadge.textContent = count;
    countBadge.hidden = count === 0;
  }
  updateSideBadge();

  const empty = $("side-prs-empty");
  const list = $("side-prs-list");
  if (!list) return;

  if (count === 0 && proposals.length === 0) {
    if (empty) empty.hidden = false;
    list.hidden = true;
    list.replaceChildren();
    return;
  }

  if (empty) empty.hidden = true;
  list.hidden = false;

  const items = [];
  for (const p of prs) {
    const state = p.state || "open";
    const failedChecks = (p.checks || []).filter((c) => c.state === "failure").length;
    let checksLabel = p.summary === "success" ? "checks passed" :
                      p.summary === "failure" ? (failedChecks + " checks failing") :
                      p.summary === "pending" ? "checks running" : "no checks";
    const commentsCount = (p.comments || []).length;
    const commentsLabel = commentsCount ? (commentsCount + (commentsCount === 1 ? " comment" : " comments")) : "";
    const metaLine = [p.repo, "#" + p.number, p.branch + (p.base ? " → " + p.base : "")].join(" · ");

    const card = el("div", { class: "side-pr-card " + state },
      el("div", { class: "side-pr-head" },
        el("span", { class: "pr-state " + state }, state),
        el("a", { class: "side-pr-title", href: p.url, target: "_blank", rel: "noopener noreferrer" },
          "#" + p.number + " " + p.title)
      ),
      el("div", { class: "side-pr-meta muted" }, metaLine),
      el("div", { class: "side-pr-sub" },
        el("span", { class: "pr-checks " + (p.summary || "none") }, checksLabel),
        commentsLabel ? el("span", { class: "muted" }, " · " + commentsLabel) : ""
      ),
      el("div", { class: "side-pr-actions" },
        el("a", { class: "button ghost tiny", href: p.url, target: "_blank", rel: "noopener noreferrer" }, "Open on git server ↗")
      )
    );
    items.push(card);
  }

  if (proposals.length > 0) {
    items.push(el("div", { class: "side-section-heading" }, "Proposed branches"));
    for (const prop of proposals) {
      const propCard = el("div", { class: "side-pr-card proposal" },
        el("div", { class: "side-pr-head" },
          el("span", { class: "pr-state proposal" }, "ready to open"),
          el("span", { class: "side-pr-title" }, prop.title || prop.branch)
        ),
        el("div", { class: "side-pr-meta muted" }, prop.repo + " · " + prop.branch + (prop.base ? " → " + prop.base : ""))
      );
      items.push(propCard);
    }
  }

  list.replaceChildren(...items);
}

function registerSideFile(file) {
  sessionFiles.set(file.name, file);
  const count = sessionFiles.size;
  const badge = $("side-files-count");
  if (badge) {
    badge.textContent = count;
    badge.hidden = false;
  }
  updateSideBadge();
  const empty = $("side-files-empty");
  if (empty) empty.hidden = true;
  const list = $("side-files-list");
  if (list) {
    list.hidden = false;
    renderSideFilesList();
  }
}

function renderSideFilesList() {
  const list = $("side-files-list");
  if (!list) return;
  const items = [];
  for (const [name, file] of sessionFiles.entries()) {
    const isImg = file.kind === "image";
    const sizeStr = isImg ? "Image preview" : Math.max(1, Math.round(file.content.length / 1024)) + " KB";
    const item = el("div", { class: "side-file-item", role: "button", tabindex: "0" },
      el("div", { class: "side-file-meta" },
        el("span", { class: "side-file-name" }, name),
        el("span", { class: "side-file-sub" }, sizeStr)),
      el("button", { type: "button", class: "ghost tiny" }, "View"));
    item.addEventListener("click", () => viewSideFile(name));
    item.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        viewSideFile(name);
      }
    });
    items.push(item);
  }
  list.replaceChildren(...items);
}

function viewSideFile(name) {
  const file = sessionFiles.get(name);
  if (!file) return;
  setSidePanelOpen(true);
  selectSideTab("files");
  const fileListView = $("side-files-list-view");
  if (fileListView) fileListView.hidden = true;
  const viewer = $("side-file-viewer");
  if (!viewer) return;
  viewer.hidden = false;
  const viewerTitle = $("side-viewer-title");
  if (viewerTitle) {
    viewerTitle.textContent = name;
    viewerTitle.title = name;
  }

  const actions = $("side-viewer-actions");
  actions.replaceChildren();

  const isImg = file.kind === "image" && file.mime;
  let body;
  if (isImg) {
    const src = "data:" + file.mime + ";base64," + file.content;
    const saveImage = () => {
      const bin = atob(file.content);
      const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
      download(name, new Blob([bytes], { type: file.mime }));
    };
    const doShare = () => {
      const bin = atob(file.content);
      const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
      shareImage({ src, name, blob: new Blob([bytes], { type: file.mime }) });
    };
    body = el("img", { class: "share-img", src, alt: name, title: name + " · Click to view full screen" });
    body.addEventListener("click", () => openImageModal({ src, name, onDownload: saveImage, onShare: doShare }));
    const full = el("button", { type: "button", class: "tiny", title: "View this image full screen" }, "Full screen");
    full.addEventListener("click", () => openImageModal({ src, name, onDownload: saveImage, onShare: doShare }));
    const save = el("button", { type: "button", class: "tiny" }, "Download");
    save.addEventListener("click", saveImage);
    actions.append(full, save);
    if (navigator.share) {
      const share = el("button", { type: "button", class: "tiny", title: "Share this image" }, "Share");
      share.addEventListener("click", doShare);
      actions.append(share);
    }
  } else {
    const isMd = /\.(md|markdown)$/i.test(name);
    body = isMd ? el("div", { class: "md" }, renderMarkdown(file.content)) : el("pre", { class: "log" }, file.content);
    const copy = el("button", { type: "button", class: "tiny" }, "Copy");
    copy.addEventListener("click", () => copyText(file.content, copy));
    const save = el("button", { type: "button", class: "tiny" }, "Download");
    save.addEventListener("click", () => download(name, new Blob([file.content], { type: "text/plain;charset=utf-8" })));
    actions.append(copy, save);
  }
  $("side-viewer-body").replaceChildren(body);
}

initSidePanel();
