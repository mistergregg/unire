"use strict";

// Tells you when an agent is waiting for your answer, wherever you are in
// Unire: a count on the Chats tab and in the page title, and a browser
// notification when the tab is in the background. It checks the session list
// every few seconds while Unire is open. Browsers only allow notifications on
// https (or localhost); the count and the title work everywhere.

const attention = { started: false, notified: new Set() };

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
  if (tab) tab.textContent = count ? "Chats (" + count + ")" : "Chats";

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

// ---- Settings: turn notifications on ----
function notificationsText() {
  if (!("Notification" in window)) return "This browser cannot show notifications here (they need https). The tab count and title still work.";
  if (Notification.permission === "granted") return "On. You get a notification when an agent is waiting for you and Unire is in the background.";
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
