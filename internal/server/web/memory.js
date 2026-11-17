"use strict";

// Memory pages: the list of repos with memory, a search across session
// summaries, and one repo's brief and summaries. Uses helpers from app.js.

const keyName = (key) => { const i = key.indexOf("/"); return i >= 0 ? key.slice(i + 1) : key; };

async function openMemory() {
  stopPolling();
  show("memory");
  $("mem-results").replaceChildren();
  renderMemoryStats();
  const list = await api("GET", "/api/memory");
  $("mem-list").replaceChildren(...(list.length ? list.map((r) => {
    const row = el("div", { class: "item tile click", role: "button", tabindex: "0" },
      el("div", { class: "tile-head" },
        iconTile("memory", true),
        el("div", { class: "tile-title" },
          el("span", { class: "name" }, r.name),
          el("div", { class: "tile-sub" }, r.updatedAt ? "brief updated " + ago(r.updatedAt) : "summaries only")),
        el("span", { class: "badge" + (r.hasBrief ? " st-done" : "") }, r.hasBrief ? "brief" : "no brief")),
      el("div", { class: "tile-body" }, r.summaries + (r.summaries === 1 ? " session summary" : " session summaries")));
    const open = () => guarded(null, () => openRepoMemory(r.key));
    row.addEventListener("click", open);
    row.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
    return row;
  }) : [el("p", { class: "muted" }, "Nothing remembered yet. Memory builds up as sessions finish.")]));
}

$("rm-back").addEventListener("click", () => guarded(null, openMemory));

// ---- search ----
function summaryItem(x, onDeleted) {
  const body = el("pre", { class: "log", hidden: "" });
  const btn = el("button", { type: "button" }, "Read");
  btn.addEventListener("click", () => guarded(btn, async () => {
    if (!body.hidden) { body.hidden = true; btn.textContent = "Read"; return; }
    if (!body.textContent) body.textContent = (await api("GET", "/api/memory/summaries/" + encodeURIComponent(x.sessionId))).summary;
    body.hidden = false; btn.textContent = "Hide";
  }));
  const del = el("button", { type: "button", class: "danger" }, "Delete");
  del.addEventListener("click", () => {
    if (!confirm("Delete this summary? Agents will no longer see it.")) return;
    guarded(del, async () => { await api("DELETE", "/api/memory/summaries/" + encodeURIComponent(x.sessionId)); onDeleted(); });
  });
  return el("div", { class: "item tile" },
    el("div", { class: "tile-head" },
      el("div", { class: "tile-title" },
        el("span", { class: "name" }, x.task),
        el("div", { class: "tile-sub" }, ago(x.createdAt) + " · " + x.repos.map(keyName).join(", "))),
      el("span", { class: "badge st-" + (x.status === "done" ? "done" : "failed") }, x.agent + " · " + x.status),
      el("span", { class: "badge" }, x.source === "agent" ? "agent summary" : "plain summary")),
    x.snippet ? el("div", { class: "tile-body" }, x.snippet) : "",
    el("div", { class: "item-actions" }, btn, del),
    body);
}

$("mem-search-form").addEventListener("submit", (e) => {
  e.preventDefault();
  guarded(e.submitter, async () => {
    const q = $("mem-query").value.trim();
    const res = await api("GET", "/api/memory/search?q=" + encodeURIComponent(q));
    $("mem-results").replaceChildren(...(res.length
      ? res.map((x) => summaryItem(x, () => $("mem-search-form").requestSubmit()))
      : [el("p", { class: "muted" }, "No summaries match.")]));
  });
});

// ---- one repo ----
let repoKeyOpen = null;

function fillRepoMemory(r) {
  repoKeyOpen = r.key;
  $("rm-title").textContent = keyName(r.key);
  $("rm-brief").value = r.brief;
  $("rm-brief").maxLength = r.limit;
  $("rm-meta").textContent = r.hasBrief
    ? "Last changed " + ago(r.updatedAt) + (r.bySession ? " by a session" : " by you") + ". Agents are given this at the start of every session on this repo."
    : "No brief yet. One is written when a session on this repo finishes, or you can write your own.";
  $("rm-restore").hidden = !r.hasPrevious;
  $("rm-clear").hidden = !r.hasBrief;
  updateCount();
  $("rm-summaries").replaceChildren(...(r.summaries.length
    ? r.summaries.map((x) => summaryItem(x, () => guarded(null, () => openRepoMemory(r.key))))
    : [el("p", { class: "muted" }, "No session summaries for this repo.")]));
}

function updateCount() {
  const n = $("rm-brief").value.length, max = $("rm-brief").maxLength;
  $("rm-count").textContent = n + " of " + max + " characters. Secrets such as tokens and passwords are removed when you save.";
}
$("rm-brief").addEventListener("input", updateCount);

async function openRepoMemory(key) {
  stopPolling();
  show("repomem");
  fillRepoMemory(await api("GET", "/api/memory/repo?key=" + encodeURIComponent(key)));
}

$("rm-save").addEventListener("click", () => guarded($("rm-save"), async () => {
  fillRepoMemory(await api("PUT", "/api/memory/repo", { key: repoKeyOpen, brief: $("rm-brief").value }));
  banner("Brief saved.");
}));
$("rm-restore").addEventListener("click", () => guarded($("rm-restore"), async () => {
  await api("POST", "/api/memory/repo/restore", { key: repoKeyOpen });
  await openRepoMemory(repoKeyOpen);
  banner("Restored the previous version.");
}));
$("rm-clear").addEventListener("click", () => {
  if (!confirm("Clear this brief? Session summaries stay.")) return;
  guarded($("rm-clear"), async () => {
    await api("DELETE", "/api/memory/repo?key=" + encodeURIComponent(repoKeyOpen));
    await openRepoMemory(repoKeyOpen);
    banner("Brief cleared.");
  });
});

// How runs with memory compare with runs without it, over the last 90 days. The groups
// are not matched (you chose which chats had memory off), so this is a hint, not proof.
async function renderMemoryStats() {
  const box = $("mem-stats");
  let res;
  try { res = await api("GET", "/api/memory/stats"); } catch (_) { box.replaceChildren(); return; }
  const on = res.groups.find((g) => g.memory) || { runs: 0 };
  const off = res.groups.find((g) => !g.memory) || { runs: 0 };
  const pct = (n, d) => (d ? Math.round((100 * n) / d) + "%" : "–");
  const num = (x, unit) => (x ? Math.round(x * 10) / 10 + unit : "–");
  const row = (label, a, b) => el("tr", {}, el("td", {}, label), el("td", {}, a), el("td", {}, b));
  const table = el("table", { class: "stats" },
    el("thead", {}, el("tr", {}, el("th", {}), el("th", {}, "Memory on"), el("th", {}, "Memory off"))),
    el("tbody", {},
      row("Runs finished", String(on.runs), String(off.runs)),
      row("Failed", pct(on.failed, on.runs), pct(off.failed, off.runs)),
      row("Stopped by you", pct(on.stopped, on.runs), pct(off.stopped, off.runs)),
      row("Average run time", num(on.avgMinutes, " min"), num(off.avgMinutes, " min")),
      row("Memory given at the start", num(on.avgInjectedChars / 4, " tokens"), "–"),
      row("Time saving memory", num(on.avgSaveSeconds, " s"), "–"),
      row("Pull requests opened", String(on.prs), String(off.prs)),
      row("…of those merged", pct(on.merged, on.prs), pct(off.merged, off.prs)),
      row("…closed without merging", pct(on.closed, on.prs), pct(off.closed, off.prs))));
  const few = Math.min(on.runs, off.runs) < 20;
  box.replaceChildren(table, el("p", { class: "muted small" },
    "The last " + res.days + " days. " + (few
      ? "Too few runs with memory off to compare (turn memory off for some chats, under Options on the New chat page, to see a difference). "
      : "") +
    "The groups are not matched, so treat differences as a hint. Pull request states are as last seen when you opened the chat."));
}
