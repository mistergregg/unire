"use strict";

// The Health page: what Unire depends on, whether each is working, and what to do when
// it is not. Uses helpers from app.js.

const HEALTH_LABEL = { ok: "OK", warn: "Look at this", bad: "Broken", unknown: "Unknown" };

async function openHealth() {
  stopPolling();
  show("health");
  $("health-summary").textContent = "Checking… (the agents and git servers are asked too, which can take a few seconds)";
  $("health-list").replaceChildren();
  let res;
  try { res = await api("GET", "/api/health"); } catch (e) { $("health-summary").textContent = e.message; return; }
  const bad = res.checks.filter((c) => c.status === "bad").length;
  const warn = res.checks.filter((c) => c.status === "warn" || c.status === "unknown").length;
  $("health-summary").textContent = bad ? bad + " problem" + (bad === 1 ? "" : "s") + " to fix" + (warn ? ", " + warn + " to look at" : "")
    : warn ? warn + " thing" + (warn === 1 ? "" : "s") + " to look at, nothing broken" : "Everything looks fine.";
  $("health-summary").className = bad ? "err" : "muted";
  // What needs attention comes first.
  const rank = { bad: 0, warn: 1, unknown: 2, ok: 3 };
  const sorted = [...res.checks].sort((a, b) => rank[a.status] - rank[b.status]);
  $("health-list").replaceChildren(...sorted.map((c) =>
    el("div", { class: "health-row " + c.status },
      el("span", { class: "dot", title: HEALTH_LABEL[c.status] }),
      el("div", { class: "health-body" },
        el("div", { class: "health-name" }, c.name, el("span", { class: "muted small" }, "  " + HEALTH_LABEL[c.status])),
        el("div", { class: "health-summary" }, c.summary),
        c.fix ? el("div", { class: "health-fix" }, c.fix) : ""))));
}

$("health-refresh").addEventListener("click", () => guarded($("health-refresh"), openHealth));
