"use strict";

// Tells you when an agent is waiting for your answer, wherever you are in
// Unire: a count on the Chats tab and in the page title, and a browser
// notification when the tab is in the background. It checks the session list
// every few seconds while Unire is open. Browsers only allow notifications on
// https (or localhost); the count and the title work everywhere.

const attention = { started: false, notified: new Set(), knownPRs: new Set(), prsInitialized: false,
  autoTurns: null, autoStarts: null, ticks: 0 };

function playPRChime() {
  try {
    const AudioCtx = window.AudioContext || window.webkitAudioContext;
    if (!AudioCtx) return;
    const ctx = new AudioCtx();
    const now = ctx.currentTime;
    // Pleasant two-tone bell chime: D5 (587.33Hz) -> A5 (880Hz)
    const notes = [587.33, 880];
    notes.forEach((freq, idx) => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = "sine";
      osc.frequency.setValueAtTime(freq, now + idx * 0.09);
      gain.gain.setValueAtTime(0.12, now + idx * 0.09);
      gain.gain.exponentialRampToValueAtTime(0.001, now + idx * 0.09 + 0.35);
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.start(now + idx * 0.09);
      osc.stop(now + idx * 0.09 + 0.36);
    });
  } catch (_) { /* audio disabled or not allowed yet */ }
}

function startAttention() {
  if (attention.started) return;
  attention.started = true;
  checkAttention();
  setInterval(checkAttention, 5000);
}

async function checkAttention() {
  if (currentView === "auth") return;
  let list;
  try { list = await api("GET", "/api/sessions"); } catch (_) { return; }
  if (typeof renderSidebar === "function" && !$("workspace").hidden) renderSidebar(list);
  const waiting = list.filter((s) => s.waiting > 0);
  const count = waiting.reduce((n, s) => n + s.waiting, 0);

  document.title = count ? "(" + count + ") Unire: needs you" : "Unire";
  const tab = document.querySelector('.tab[data-view="sessions"]');
  // Only the label changes: replacing the tab's text would also remove its icon.
  const label = tab && (tab.querySelector("span") || tab);
  if (label) label.textContent = count ? "Chats (" + count + ")" : "Chats";

  const now = new Set();
  for (const s of waiting) {
    const tail = s.latest || s.id;
    const key = tail + "/" + s.waiting;
    now.add(key);
    if (attention.notified.has(key)) continue;
    const looking = currentView === "session" && typeof thread !== "undefined" && thread.length && thread[thread.length - 1].s.id === tail;
    if (!looking) tellOwner(s, tail);
  }
  attention.notified = now; // a later question in the same session is new again

  // Track pull requests and alert when a new one is opened
  const currentPRs = new Set();
  for (const s of list) {
    for (const p of (s.pulls || [])) {
      const prKey = s.id + "/" + p.serverId + "/" + p.repo + "/" + p.number;
      currentPRs.add(prKey);
      if (attention.prsInitialized && !attention.knownPRs.has(prKey)) {
        tellOwnerNewPR(s, p);
      }
    }
  }
  attention.knownPRs = currentPRs;
  attention.prsInitialized = true;

  checkAutomationTurns(list);
  if (attention.ticks++ % 6 === 0) checkAutomationStarts(); // every 30 seconds
}

// An automation's run that fails is worth hearing about: nobody was watching it.
// Only changes seen while Unire is open count, so old failures are not announced.
function checkAutomationTurns(list) {
  const seen = new Map();
  for (const s of list) {
    if (!s.automation) continue;
    const turn = s.latest || s.id;
    const status = s.latestStatus || s.status;
    seen.set(turn, status);
    const failed = status === "failed" || status === "interrupted";
    if (failed && attention.autoTurns && attention.autoTurns.get(turn) !== status) {
      tellAutomationProblem(s.automation, "the run " + status + ".", () => openSession(s.id), turn);
    }
  }
  attention.autoTurns = seen;
}

// A run that could not start at all (the agent signed out, Docker down) has no chat
// turn, so the automations' own history is checked for it.
async function checkAutomationStarts() {
  let data;
  try { data = await api("GET", "/api/automations"); } catch (_) { return; }
  const seen = new Map();
  for (const a of data.automations || []) {
    const r = a.runs && a.runs[0];
    if (!r) continue;
    seen.set(a.id, r.at);
    const isNew = attention.autoStarts && attention.autoStarts.get(a.id) !== r.at;
    if (isNew && !r.sessionId && r.note.startsWith("could not start")) {
      tellAutomationProblem(a.name, r.note, () => openAutomations(), a.id + "/" + r.at);
    }
  }
  attention.autoStarts = seen;
}

function tellAutomationProblem(name, what, open, tag) {
  const text = "Automation " + name + ": " + what;
  if (document.hidden && "Notification" in window && Notification.permission === "granted") {
    const n = new Notification("Unire: automation problem", { body: text, tag: "auto/" + tag });
    n.onclick = () => { window.focus(); guarded(null, open); n.close(); };
  } else if (!document.hidden) {
    banner(text, true);
  }
}

function tellOwner(s, tail) {
  const text = s.task + " is waiting for your answer.";
  if (document.hidden && "Notification" in window && Notification.permission === "granted") {
    const n = new Notification("Unire", { body: text, tag: tail });
    n.onclick = () => { window.focus(); guarded(null, () => openSession(s.id)); n.close(); };
  } else if (!document.hidden) {
    banner(text);
  }
}

function tellOwnerNewPR(s, p) {
  playPRChime();
  const text = "Pull request #" + p.number + " opened in " + p.repo + " for “" + s.task + "”";
  if (document.hidden && "Notification" in window && Notification.permission === "granted") {
    const n = new Notification("Unire: Pull request #" + p.number + " opened", {
      body: p.repo + ": " + p.title,
      tag: s.id + "/" + p.number
    });
    n.onclick = () => {
      window.focus();
      guarded(null, async () => {
        await openSession(s.id);
        if (typeof setSidePanelOpen === "function") setSidePanelOpen(true);
        if (typeof selectSideTab === "function") selectSideTab("prs");
      });
      n.close();
    };
  } else {
    banner(text);
  }
}

// ---- Settings: turn notifications on ----
function notificationsText() {
  if (!("Notification" in window)) return "This browser cannot show notifications here (they need https). The tab count and title still work.";
  if (Notification.permission === "granted") return "On. You get a notification when an agent is waiting for you, or an automation's run fails, and Unire is in the background.";
  if (Notification.permission === "denied") return "Blocked in the browser's settings for this site.";
  return "Off. Turn them on to hear about questions while you are in another tab.";
}

function showNotificationsState() {
  $("notif-status").textContent = notificationsText();
  $("notif-enable").hidden = !("Notification" in window) || Notification.permission !== "default";
}

$("notif-enable").addEventListener("click", () => guarded($("notif-enable"), async () => {
  await Notification.requestPermission();
  showNotificationsState();
}));
showNotificationsState();
