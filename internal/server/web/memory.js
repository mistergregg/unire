"use strict";

// Memory pages: the list of repos with memory, a search across session
// summaries, and one repo's brief and summaries. Uses helpers from app.js.

const keyName = (key) => { const i = key.indexOf("/"); return i >= 0 ? key.slice(i + 1) : key; };

async function openMemory() {
  stopPolling();
  show("memory");
  $("mem-results").replaceChildren();
  const list = await api("GET", "/api/memory");
  $("mem-list").replaceChildren(...(list.length ? list.map((r) => {
    const row = el("div", { class: "item click", role: "button", tabindex: "0" },
      el("div", { class: "item-head" },
        el("span", { class: "name" }, r.name),
        el("span", { class: "badge" }, (r.hasBrief ? "brief" : "no brief") + " · " + r.summaries + (r.summaries === 1 ? " summary" : " summaries"))),
      el("div", { class: "muted" }, r.updatedAt ? "brief updated " + ago(r.updatedAt) : "summaries only"));
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
  return el("div", { class: "item" },
    el("div", { class: "item-head" },
      el("span", { class: "name" }, x.task),
      el("span", { class: "badge" }, x.agent + " · " + x.status + " · " + (x.source === "agent" ? "agent summary" : "plain summary"))),
    el("div", { class: "muted" }, ago(x.createdAt) + " · " + x.repos.map(keyName).join(", ")),
    x.snippet ? el("div", { class: "muted" }, x.snippet) : "",
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
