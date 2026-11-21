"use strict";

// How much of each agent's usage limits is left, in a slim strip on the chat
// pages (above the message box, and on the new-chat screen). The readings come
// from GET /api/usage: polled every minute while a chat page is open, and right
// after a turn ends (usageChanged). Details show on hover or tap.

const USAGE_POLL_MS = 60 * 1000;
let usageData = null;
let usageOpen = ""; // the agent whose details were opened with a tap

// "2 h 10 min", "3 d 4 h", "less than a minute"
function spanText(secs) {
  const m = Math.floor(Math.max(0, secs) / 60);
  if (m < 1) return "less than a minute";
  if (m < 60) return m + " min";
  const h = Math.floor(m / 60);
  if (h < 24) return h + " h" + (m % 60 ? " " + (m % 60) + " min" : "");
  const d = Math.floor(h / 24);
  return d + " d" + (h % 24 ? " " + (h % 24) + " h" : "");
}

// plenty, getting low, almost out
function usageLevel(left) {
  if (left < 20) return "out";
  if (left < 50) return "low";
  return "ok";
}

// "5-hour" -> "5h" for the narrow strip
const shortWindow = (name) => name.replace(/^(\d+)-(hour|day|min)$/, (_, n, u) => n + u[0]);

function shortAgo(unix) {
  if (!unix) return "never";
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (s < 90) return "just now";
  if (s < 5400) return Math.round(s / 60) + "m ago";
  if (s < 129600) return Math.round(s / 3600) + "h ago";
  return Math.round(s / 86400) + "d ago";
}

function usageWindowChip(w) {
  const left = Math.round(100 - w.usedPercent);
  const bar = el("span", { class: "usage-bar" }, el("span", {}));
  bar.firstChild.style.width = Math.max(0, Math.min(100, left)) + "%";
  const winName = shortWindow(w.name || "");
  return el("span", { class: "usage-win " + usageLevel(left), title: (w.name || winName) + ": " + left + "% left" },
    el("span", { class: "usage-wname" }, winName),
    bar,
    el("span", { class: "usage-left" }, left + "%", el("span", { class: "usage-word" }, " left")));
}

// Some agents keep one set of limits per group of models (Antigravity: "Gemini
// Models", "Claude and GPT models"). The strip shows each group like an agent:
// "Gemini", "Claude/GPT".
const groupLabel = (g) => g.replace(/\s+models$/i, "").replace(/\s+and\s+/gi, "/");

// The reading as the items to show: one per group when the windows have groups
// (each keeps the agent's id, label and notes), otherwise the reading itself.
function usageItems(u) {
  if (!u.reported || !u.windows || !u.windows.some((w) => w.group)) return [u];
  const groups = [];
  for (const w of u.windows) {
    let g = groups.find((x) => x.group === (w.group || ""));
    if (!g) groups.push((g = { ...u, group: w.group || "", key: u.agentId + "/" + (w.group || ""), agentLabel: u.label || u.agentId, label: groupLabel(w.group || u.label || u.agentId), models: w.models || "", windows: [] }));
    g.windows.push(w);
  }
  return groups;
}

function usageDetails(u) {
  const now = Date.now() / 1000;
  const head = u.group ? u.agentLabel + " · " + u.group : u.label || u.agentId;
  const rows = [el("div", { class: "usage-pop-head" }, head, u.plan ? el("span", { class: "muted" }, " · " + u.plan + " plan") : "")];
  if (u.models) rows.push(el("div", { class: "muted" }, "Models: " + u.models));
  if (!u.reported) {
    rows.push(el("div", {}, "Usage is not reported by " + (u.label || u.agentId) + ", so Unire cannot show how much is left."));
    return rows;
  }
  if (!u.windows || !u.windows.length) {
    rows.push(el("div", {}, "No reading yet. " + (u.agentId === "claude"
      ? "Claude reports its usage during a turn."
      : "It is read during a turn, or asked for in the background.")));
  }
  for (const w of u.windows || []) {
    const left = Math.round(100 - w.usedPercent);
    let reset = "";
    if (w.resetsAt) reset = w.resetsAt > now ? "resets in " + spanText(w.resetsAt - now) + (u.group ? " (" + new Date(w.resetsAt * 1000).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }) + ")" : "") : "has reset since this reading";
    rows.push(el("div", { class: "usage-pop-row" },
      el("b", {}, w.name + ": " + left + "% left"),
      el("span", { class: "muted" }, " · " + Math.round(w.usedPercent) + "% used" + (reset ? " · " + reset : ""))));
  }
  if (u.status && u.status !== "allowed") rows.push(el("div", {}, "Status: " + u.status));
  if (u.updatedAt) rows.push(el("div", { class: "muted" }, "As of " + ago(u.updatedAt) + (u.stale ? " (out of date)" : "")));
  if (u.note) rows.push(el("div", { class: "muted" }, "Last try to read it: " + u.note));
  return rows;
}

let usageHover = "";
let hoverTimer = null;

function updateUsagePopups() {
  const activeId = usageOpen || usageHover;
  let activeItem = null;
  if (activeId && usageData) {
    const sorted = [...usageData].sort((a, b) => Number(b.reported) - Number(a.reported));
    const items = sorted.flatMap((u) => usageItems(u));
    items.sort((a, b) => Number(!!a.group) - Number(!!b.group));
    activeItem = items.find((it) => (it.key || it.agentId) === activeId) || null;
  }

  for (const strip of document.querySelectorAll(".usage-strip")) {
    const pop = strip.querySelector(".usage-pop");
    const track = strip.querySelector(".usage-track");
    if (!pop || !track) continue;

    let activeChip = null;
    for (const chip of track.querySelectorAll(".usage-agent")) {
      const id = chip.dataset.usageId;
      const isActive = Boolean(activeItem && id === activeId);
      chip.classList.toggle("active", isActive);
      chip.setAttribute("aria-expanded", String(usageOpen === id));
      if (isActive) activeChip = chip;
    }

    if (activeItem && activeChip) {
      pop.replaceChildren(...usageDetails(activeItem));
      pop.classList.add("open");
      const maxLeft = Math.max(0, strip.clientWidth - pop.offsetWidth);
      const pos = Math.max(0, Math.min(activeChip.offsetLeft - track.scrollLeft, maxLeft));
      pop.style.left = pos + "px";
    } else {
      pop.classList.remove("open");
      pop.replaceChildren();
    }
  }
}

function renderUsage() {
  for (const strip of document.querySelectorAll(".usage-strip")) {
    if (!usageData) { strip.hidden = true; continue; }
    strip.hidden = false;
    // Agents that report their usage first; the order among them is the server's.
    const sorted = [...usageData].sort((a, b) => Number(b.reported) - Number(a.reported));
    const items = sorted.flatMap((u) => usageItems(u));
    // Keep single-agent chips first; grouped chips follow.
    items.sort((a, b) => Number(!!a.group) - Number(!!b.group));

    const pop = el("div", { class: "usage-pop", role: "tooltip" });
    pop.addEventListener("mouseenter", () => {
      clearTimeout(hoverTimer);
    });
    pop.addEventListener("mouseleave", () => {
      hoverTimer = setTimeout(() => {
        usageHover = "";
        updateUsagePopups();
      }, 100);
    });
    pop.addEventListener("click", (e) => { e.stopPropagation(); });

    const track = el("div", { class: "usage-track" });
    track.addEventListener("scroll", () => {
      if (usageOpen || usageHover) updateUsagePopups();
    }, { passive: true });

    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      const id = it.key || it.agentId;
      const name = it.label || it.agentId;
      const body = [el("span", { class: "usage-name" }, el("span", { class: "long" }, name), el("span", { class: "short" }, name.split(" ")[0]))];
      let cls = "usage-agent";
      if (!it.reported) {
        cls += " none";
        body.push(el("span", { class: "usage-none" }, el("span", { class: "long" }, "usage not reported by the CLI"), el("span", { class: "short" }, "not reported")));
      } else if (!it.windows || !it.windows.length) {
        cls += " none";
        body.push(el("span", { class: "usage-none" }, "no reading yet"));
      } else {
        body.push(...it.windows.map(usageWindowChip));
        if (it.stale) cls += " stale";
        if (it.stale && (!it.group || i === items.length - 1)) {
          body.push(el("span", { class: "usage-age" }, el("span", { class: "long" }, shortAgo(it.updatedAt)), el("span", { class: "short" }, "old")));
        }
      }
      const chip = el("button", {
        type: "button",
        class: cls,
        "aria-expanded": "false",
        title: name + " usage details"
      }, ...body);
      chip.dataset.usageId = id;

      chip.addEventListener("click", (e) => {
        e.stopPropagation();
        clearTimeout(hoverTimer);
        usageOpen = usageOpen === id ? "" : id;
        usageHover = "";
        updateUsagePopups();
      });
      chip.addEventListener("mouseenter", () => {
        clearTimeout(hoverTimer);
        usageHover = id;
        updateUsagePopups();
      });
      chip.addEventListener("mouseleave", () => {
        hoverTimer = setTimeout(() => {
          if (usageHover === id) {
            usageHover = "";
            updateUsagePopups();
          }
        }, 100);
      });

      track.append(chip);
    }

    strip.replaceChildren(track, pop);
  }
  updateUsagePopups();
}

let usageBusy = false;
async function refreshUsage() {
  if (usageBusy) return;
  usageBusy = true;
  try { usageData = await api("GET", "/api/usage"); renderUsage(); } catch (_) { /* keep the last readings */ }
  usageBusy = false;
}

// A turn has ended: it may have reported new readings.
function usageChanged() { refreshUsage(); }

const onChatPage = () => currentView === "session" || currentView === "new";
setInterval(() => { if (onChatPage() && !document.hidden) refreshUsage(); }, USAGE_POLL_MS);
document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    if (usageHover) { usageHover = ""; updateUsagePopups(); }
  } else if (onChatPage()) {
    refreshUsage();
  }
});
document.addEventListener("click", () => {
  if (usageOpen || usageHover) {
    usageOpen = "";
    usageHover = "";
    updateUsagePopups();
  }
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && (usageOpen || usageHover)) {
    usageOpen = "";
    usageHover = "";
    updateUsagePopups();
  }
});
window.addEventListener("blur", () => {
  if (usageHover) { usageHover = ""; updateUsagePopups(); }
});
document.addEventListener("mouseleave", () => {
  if (usageHover) { usageHover = ""; updateUsagePopups(); }
});
window.addEventListener("resize", () => {
  if (usageOpen || usageHover) updateUsagePopups();
});
