"use strict";

// Pull requests of the open conversation: Unire asks whether to open one for a
// branch the agent pushed, then follows it (its state, checks and comments) and
// can hand a failing check or a review comment back to the agent as a reply.
// Uses the helpers from app.js and chat.js.

let pullsData = { proposals: [], pulls: [] };
let pullsFor = ""; // the conversation pullsData belongs to
const openPulls = new Set(); // pull requests whose details are open, by key
const proposalEls = new Map(); // a proposal card is kept between refreshes, so what you typed in it stays

// "Not now" is remembered in this browser, per conversation and branch.
const DISMISSED = "unire.pr.dismissed";
function dismissed() {
  try { return new Set(JSON.parse(localStorage.getItem(DISMISSED) || "[]")); } catch (_) { return new Set(); }
}
function dismiss(key) {
  const s = dismissed();
  s.add(key);
  try { localStorage.setItem(DISMISSED, JSON.stringify([...s].slice(-200))); } catch (_) { /* not remembered */ }
}
const proposalKey = (p) => [selectedRoot, p.serverId, p.repo, p.branch].join("/");
const pullKey = (p) => [p.serverId, p.repo, p.number].join("/");

function resetPulls() {
  pullsData = { proposals: [], pulls: [] };
  pullsFor = "";
  proposalEls.clear();
  $("pulls").replaceChildren();
}

async function refreshPulls() {
  if (currentView !== "session" || typeof thread === "undefined" || !thread.length) return;
  const token = pollToken;
  const root = selectedRoot;
  try {
    const d = await api("GET", "/api/sessions/" + encodeURIComponent(thread[thread.length - 1].s.id) + "/pulls");
    if (pollToken !== token || root !== selectedRoot) return;
    pullsData = d;
    pullsFor = root;
    renderPulls();
  } catch (_) { /* the chat works without it */ }
}

// Looks again every half minute while the chat is open: checks finish and
// reviewers comment without anything happening here.
async function watchPulls(token) {
  while (pollToken === token && currentView === "session") {
    await refreshPulls();
    await new Promise((res) => setTimeout(res, 30000));
  }
}

const CHECK_TEXT = { success: "checks passed", failure: "checks failing", pending: "checks running", none: "no checks" };

function summaryLine(p) {
  const failed = p.checks.filter((c) => c.state === "failure").length;
  const bits = [CHECK_TEXT[p.summary] || ""];
  if (p.summary === "failure" && failed) bits[0] = failed + " of " + p.checks.length + " checks failing";
  if (p.comments.length) bits.push(p.comments.length + (p.comments.length === 1 ? " comment" : " comments"));
  return bits.filter(Boolean).join(" · ");
}

// What the agent is told when you hand it a problem: the facts, and to push to the
// same branch so the pull request updates.
function failingChecksMessage(p) {
  const list = p.checks.filter((c) => c.state === "failure").map((c) => "- " + c.name + (c.url ? " (" + c.url + ")" : "")).join("\n");
  return "The checks on the pull request for " + p.repo + " (#" + p.number + ") are failing:\n" + list +
    "\n\nFind out why (you can run `unire pr-status`), fix what is wrong, and push to the same branch so the pull request updates.";
}

function commentsMessage(p) {
  const list = p.comments.slice(-10).map((c) => {
    const where = c.path ? " on " + c.path + (c.line ? ":" + c.line : "") : "";
    const body = c.body.length > 800 ? c.body.slice(0, 800) + "…" : c.body;
    return "- " + c.author + where + ": " + body;
  }).join("\n");
  return "People commented on the pull request for " + p.repo + " (#" + p.number + "):\n" + list +
    "\n\nAddress what needs a change on the same branch, push, and say what you did with `unire pr-reply`.";
}

function pullCard(p) {
  const key = pullKey(p);
  const state = el("span", { class: "pr-state " + (p.state || "unknown") }, p.state || "unknown");
  const checks = el("span", { class: "pr-checks " + p.summary }, summaryLine(p));
  const title = el("a", { class: "pr-title", href: p.url, target: "_blank", rel: "noopener noreferrer" }, "#" + p.number + " " + p.title);
  const head = el("div", { class: "pr-head" }, title, state);
  const details = el("details", { class: "pr-details" });
  details.open = openPulls.has(key);
  details.addEventListener("toggle", () => { if (details.open) openPulls.add(key); else openPulls.delete(key); });
  details.append(el("summary", {}, el("span", { class: "muted" }, p.repo + " · " + p.branch + (p.base ? " → " + p.base : "") + " · "), checks));
  if (p.error) details.append(el("p", { class: "err" }, "Could not read it from the server: " + p.error));
  if (p.checks.length) {
    details.append(el("ul", { class: "pr-list" }, ...p.checks.map((c) =>
      el("li", { class: "pr-check " + c.state }, el("span", { class: "dot" }),
        c.url ? el("a", { href: c.url, target: "_blank", rel: "noopener noreferrer" }, c.name) : c.name,
        el("span", { class: "muted" }, " " + c.state)))));
  }
  if (p.comments.length) {
    details.append(el("div", { class: "pr-comments" }, ...p.comments.map((c) =>
      el("div", { class: "pr-comment" },
        el("div", { class: "muted" }, c.author + (c.path ? " on " + c.path + (c.line ? ":" + c.line : "") : "")),
        el("div", { class: "said" }, c.body)))));
  }
  // Handing a problem to the agent only makes sense once it is free to take it.
  const tail = thread[thread.length - 1];
  const free = tail && isFinished(tail.s.status);
  const actions = el("div", { class: "pr-actions" });
  if (p.state === "open") {
    if (p.summary === "failure") actions.append(handButton("Ask the agent to fix the checks", failingChecksMessage(p), free));
    if (p.comments.length) actions.append(handButton("Ask the agent to address the comments", commentsMessage(p), free));
  }
  // Closing or reopening is done on the git server, not by the agent. Nothing is merged.
  actions.append(anotherButton(p, details));
  if (p.state === "open") actions.append(stateButton(p, false, "Close pull request"));
  else if (p.state === "closed") actions.append(stateButton(p, true, "Reopen pull request"));
  if (actions.children.length) details.append(actions);
  return el("div", { class: "pr-card" }, head, details);
}

function handButton(label, message, free) {
  const b = el("button", { type: "button", class: "small", title: free ? "" : "Available when the agent is not working" }, label);
  b.disabled = !free;
  b.addEventListener("click", () => guarded(b, async () => { await sendReply(message); }));
  return b;
}

function proposalCard(p) {
  const title = el("input", { type: "text", value: p.title, maxlength: "250", "aria-label": "Pull request title" });
  const body = el("textarea", { rows: "6", maxlength: "20000", "aria-label": "Pull request description" });
  body.value = p.body;
  const base = el("input", { type: "text", value: p.base || "", maxlength: "250", placeholder: "the repo's default branch", spellcheck: "false", "aria-label": "Branch the pull request goes into" });
  const more = el("details", { class: "pr-more" }, el("summary", {}, "Edit the description"), body);
  const open = el("button", { type: "button", class: "primary" }, "Open pull request");
  const later = el("button", { type: "button", class: "ghost" }, "Not now");
  open.addEventListener("click", () => guarded(open, async () => {
    const tail = thread[thread.length - 1];
    try {
      await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/pulls",
        { serverId: p.serverId, repo: p.repo, branch: p.branch, base: base.value.trim(), title: title.value, body: body.value });
    } catch (e) {
      // A refused token is the usual reason: say where to check it.
      if (/token|permission|rejected|not found/i.test(e.message)) {
        throw new Error(e.message + ". Check what the token may do on the Git servers page (Check pull requests).");
      }
      throw e;
    }
    await refreshPulls();
  }));
  later.addEventListener("click", () => { dismiss(proposalKey(p)); renderPulls(); });
  return el("div", { class: "pr-card propose" },
    el("div", { class: "pr-ask" }, "Open a pull request for ", el("strong", {}, p.repo), "? ",
      el("span", { class: "muted" }, "Branch "), el("code", {}, p.branch), el("span", { class: "muted" }, " into " + (p.base || "its default branch") + ".")),
    title, el("label", { class: "pr-base muted" }, "Into branch", base), more, el("div", { class: "pr-actions" }, open, later));
}

// A branch you said "not now" to stays one click away.
function laterButton(p) {
  const b = el("button", { type: "button", class: "ghost small" }, "Open a pull request for " + p.repo + "…");
  b.addEventListener("click", () => {
    const s = dismissed();
    s.delete(proposalKey(p));
    try { localStorage.setItem(DISMISSED, JSON.stringify([...s])); } catch (_) { /* not remembered */ }
    renderPulls();
  });
  return b;
}

function renderPulls() {
  const box = $("pulls");
  if (!box || pullsFor !== selectedRoot) return;
  const hide = dismissed();
  const tail = thread && thread[thread.length - 1];
  const idle = tail && isFinished(tail.s.status);
  const cards = pullsData.pulls.map(pullCard);
  const later = [];
  for (const p of pullsData.proposals) {
    if (!idle) continue; // ask once the agent has stopped, not while it works
    if (hide.has(proposalKey(p))) later.push(laterButton(p));
    else {
      const key = proposalKey(p);
      if (!proposalEls.has(key)) proposalEls.set(key, proposalCard(p));
      cards.push(proposalEls.get(key));
    }
  }
  if (later.length) cards.push(el("div", { class: "pr-later" }, ...later));
  box.replaceChildren(...cards);
}

function stateButton(p, open, label) {
  const b = el("button", { type: "button", class: "small" + (open ? "" : " danger"), title: open ? "Open it again" : "Close it without merging" }, label);
  b.addEventListener("click", () => {
    if (!open && !confirm("Close #" + p.number + " on " + p.repo + "? Nothing is merged or deleted, and you can reopen it.")) return;
    guarded(b, async () => {
      const tail = thread[thread.length - 1];
      await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/pulls/state",
        { serverId: p.serverId, repo: p.repo, number: p.number, open });
      await refreshPulls();
    });
  });
  return b;
}

// The same branch can go into more than one branch (a release branch, say): this
// opens another pull request for it with a different target.
function anotherButton(p, details) {
  const b = el("button", { type: "button", class: "small", title: "Open a pull request from this branch into another branch" }, "Open another into…");
  b.addEventListener("click", () => {
    const base = el("input", { type: "text", maxlength: "250", placeholder: "branch, for example release/1.2", spellcheck: "false", "aria-label": "Branch to go into" });
    const go = el("button", { type: "submit", class: "primary small" }, "Open");
    const form = el("form", { class: "pr-another", autocomplete: "off" }, base, go);
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      const target = base.value.trim();
      if (!target) return;
      guarded(go, async () => {
        const tail = thread[thread.length - 1];
        await api("POST", "/api/sessions/" + encodeURIComponent(tail.s.id) + "/pulls",
          { serverId: p.serverId, repo: p.repo, branch: p.branch, base: target, title: p.title + " (into " + target + ")" });
        await refreshPulls();
      });
    });
    b.replaceWith(form);
    base.focus();
  });
  return b;
}
