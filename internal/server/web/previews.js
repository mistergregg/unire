"use strict";

// Live previews in the chat inspector side panel: detects previewable projects
// in the session's workspace, lets the owner start/stop/reload them, switch
// Phone/Tablet/Desktop viewport sizes, and inspect container logs.

let previewsData = { available: false, reason: "", previews: [], problems: [] };
let previewsFor = "";       // conversation root id previewsData belongs to
let selectedPreviewKey = ""; // "<repo>/<name>"
let previewDevice = "phone"; // "phone" | "tablet" | "desktop"
let loadedPreviewKey = "";
let loadedPreviewBase = "";
let previewIframeNonce = 0;
let previewPollTimer = null;
let previewLogsText = "";
let previewLogsOpen = false;

try {
  const savedDev = localStorage.getItem("unire_preview_device");
  if (savedDev === "phone" || savedDev === "tablet" || savedDev === "desktop") {
    previewDevice = savedDev;
  }
} catch (_) {}

const previewItemKey = (p) => p.repo + "/" + p.name;
const isBusyState = (st) => st === "downloading" || st === "starting";

const DEVICE_SIZES = {
  phone: { w: 375, h: 667, label: "Phone" },
  tablet: { w: 768, h: 900, label: "Tablet" },
  desktop: { w: 0, h: 520, label: "Desktop" },
};

function initPreviewControls() {
  const tab = $("side-tab-preview");
  if (tab) {
    tab.addEventListener("click", () => selectSideTab("preview"));
  }
  const headBtn = $("s-open-preview");
  if (headBtn) {
    headBtn.addEventListener("click", () => {
      if (sidePanelOpen && sideActiveTab === "preview") {
        setSidePanelOpen(false);
      } else {
        setSidePanelOpen(true);
        selectSideTab("preview");
      }
    });
  }
  if (window.ResizeObserver) {
    const pane = $("side-pane-preview");
    if (pane) {
      new ResizeObserver(() => scalePreviewFrame()).observe(pane);
    }
  }
}

function stopPreviewPolling() {
  if (previewPollTimer) {
    clearTimeout(previewPollTimer);
    previewPollTimer = null;
  }
}

function resetPreviews() {
  stopPreviewPolling();
  previewsData = { available: false, reason: "", previews: [], problems: [] };
  previewsFor = "";
  selectedPreviewKey = "";
  loadedPreviewKey = "";
  loadedPreviewBase = "";
  previewLogsText = "";
  previewLogsOpen = false;
  updatePreviewIndicators();
  const body = $("side-preview-body");
  if (body) body.replaceChildren();
}

function activePreview() {
  const list = previewsData.previews || [];
  if (!list.length) return null;
  const match = list.find((p) => previewItemKey(p) === selectedPreviewKey);
  if (match) return match;
  const running = list.find((p) => p.status && (p.status.state === "ready" || isBusyState(p.status.state)));
  const picked = running || list[0];
  selectedPreviewKey = previewItemKey(picked);
  return picked;
}

function updatePreviewIndicators() {
  const list = previewsData.previews || [];
  const headBtn = $("s-open-preview");
  const headDot = headBtn ? headBtn.querySelector(".preview-head-dot") : null;
  const tabDot = $("side-preview-dot");

  let stateClass = "";
  if (list.some((p) => p.status && p.status.state === "ready")) stateClass = "live";
  else if (list.some((p) => p.status && isBusyState(p.status.state))) stateClass = "busy";
  else if (list.some((p) => p.status && p.status.state === "failed")) stateClass = "bad";

  if (headBtn) {
    headBtn.hidden = !previewsData.available || list.length === 0;
  }
  if (headDot) {
    headDot.className = "preview-head-dot" + (stateClass ? " " + stateClass : "");
  }
  if (tabDot) {
    tabDot.hidden = !stateClass;
    tabDot.className = "preview-tab-dot" + (stateClass ? " " + stateClass : "");
  }
}

async function refreshPreviews() {
  if (currentView !== "session" || typeof thread === "undefined" || !thread.length) return;
  const token = pollToken;
  const root = selectedRoot;
  try {
    const d = await api("GET", "/api/sessions/" + encodeURIComponent(thread[thread.length - 1].s.id) + "/previews");
    if (pollToken !== token || root !== selectedRoot) return;
    previewsData = d || { available: false, reason: "", previews: [], problems: [] };
    previewsFor = root;
    updatePreviewIndicators();
    renderPreviewPane();
    schedulePreviewPoll(token, root);
  } catch (_) { /* preview pane stays optional */ }
}

async function pollSelectedPreview(token, root) {
  if (pollToken !== token || root !== selectedRoot || currentView !== "session" || !thread.length) return;
  const p = activePreview();
  if (!p || !p.status) return;
  try {
    const st = await api("GET", "/api/sessions/" + encodeURIComponent(thread[thread.length - 1].s.id) +
      "/previews/" + encodeURIComponent(p.repo) + "/" + encodeURIComponent(p.name));
    if (pollToken !== token || root !== selectedRoot) return;
    p.status = st;
    updatePreviewIndicators();
    renderPreviewPane();
    schedulePreviewPoll(token, root);
  } catch (_) {
    schedulePreviewPoll(token, root);
  }
}

function schedulePreviewPoll(token, root) {
  stopPreviewPolling();
  const p = activePreview();
  if (!p || !p.status) return;
  const st = p.status.state;
  const delay = isBusyState(st) ? 1500 : st === "ready" ? 5000 : 0;
  if (!delay) return;
  previewPollTimer = setTimeout(() => pollSelectedPreview(token, root), delay);
}

function watchPreviews(token) {
  refreshPreviews();
  if (token !== pollToken) return;
}

function onPreviewTabOpen() {
  if (previewsFor !== selectedRoot) {
    refreshPreviews();
  } else {
    scalePreviewFrame();
  }
}

function previewStatusBadge(state) {
  if (state === "ready") return el("span", { class: "badge st-done" }, "live");
  if (state === "downloading" || state === "starting") return el("span", { class: "badge st-running" }, state);
  if (state === "failed") return el("span", { class: "badge st-failed" }, "failed");
  if (state === "stopped") return el("span", { class: "badge" }, "stopped");
  return el("span", { class: "badge" }, "not started");
}

function stripToken(url) {
  if (!url) return "";
  return url.split("?")[0];
}

function isMixedContentBlocked(url) {
  return location.protocol === "https:" && /^http:\/\//i.test(url || "");
}

function scalePreviewFrame() {
  const stage = document.querySelector(".preview-stage");
  const wrap = document.querySelector(".preview-frame-wrap");
  const iframe = document.querySelector(".preview-frame");
  if (!stage || !wrap || !iframe) return;

  const spec = DEVICE_SIZES[previewDevice] || DEVICE_SIZES.phone;
  if (previewDevice === "desktop" || !spec.w) {
    stage.classList.add("desktop");
    wrap.style.width = "100%";
    wrap.style.height = spec.h + "px";
    iframe.style.width = "100%";
    iframe.style.height = spec.h + "px";
    iframe.style.transform = "";
    return;
  }

  stage.classList.remove("desktop");
  const avail = Math.max(180, stage.clientWidth - 18);
  const scale = Math.min(1, avail / spec.w);
  wrap.style.width = Math.round(spec.w * scale) + "px";
  wrap.style.height = Math.round(spec.h * scale) + "px";
  iframe.style.width = spec.w + "px";
  iframe.style.height = spec.h + "px";
  iframe.style.transform = scale < 1 ? "scale(" + scale.toFixed(4) + ")" : "";
}

async function startSelectedPreview(p, btn) {
  if (!thread.length) return;
  const tailId = thread[thread.length - 1].s.id;
  const token = pollToken;
  const root = selectedRoot;
  previewLogsText = "";
  await guarded(btn, async () => {
    const st = await api("POST", "/api/sessions/" + encodeURIComponent(tailId) +
      "/previews/" + encodeURIComponent(p.repo) + "/" + encodeURIComponent(p.name) + "/start");
    if (pollToken !== token || root !== selectedRoot) return;
    p.status = st;
    loadedPreviewKey = "";
    loadedPreviewBase = "";
    updatePreviewIndicators();
    renderPreviewPane();
    schedulePreviewPoll(token, root);
  });
}

async function stopSelectedPreview(p, btn) {
  if (!thread.length) return;
  const tailId = thread[thread.length - 1].s.id;
  const token = pollToken;
  const root = selectedRoot;
  await guarded(btn, async () => {
    const st = await api("POST", "/api/sessions/" + encodeURIComponent(tailId) +
      "/previews/" + encodeURIComponent(p.repo) + "/" + encodeURIComponent(p.name) + "/stop");
    if (pollToken !== token || root !== selectedRoot) return;
    p.status = st;
    loadedPreviewKey = "";
    loadedPreviewBase = "";
    stopPreviewPolling();
    updatePreviewIndicators();
    renderPreviewPane();
  });
}

async function fetchPreviewLogs(p, outEl, btn) {
  if (!thread.length) return;
  const tailId = thread[thread.length - 1].s.id;
  const run = async () => {
    const res = await fetch("/api/sessions/" + encodeURIComponent(tailId) +
      "/previews/" + encodeURIComponent(p.repo) + "/" + encodeURIComponent(p.name) + "/logs", {
      headers: { "X-Requested-With": "unire" },
      credentials: "same-origin",
    });
    if (!res.ok) throw new Error("Could not load logs (" + res.status + ")");
    previewLogsText = await res.text();
    outEl.textContent = previewLogsText || "(no output yet)";
  };
  if (btn) await guarded(btn, run);
  else try { await run(); } catch (e) { outEl.textContent = e.message; }
}

function renderPreviewPane() {
  const container = $("side-preview-body");
  if (!container) return;

  if (!previewsData.available) {
    const checkBtn = el("button", { type: "button", class: "compact" }, "Check again");
    checkBtn.addEventListener("click", () => guarded(checkBtn, refreshPreviews));
    container.replaceChildren(
      el("div", { class: "side-empty-msg" },
        el("p", {}, "Live previews are unavailable"),
        el("span", { class: "muted" }, previewsData.reason || "Set UNIRE_PREVIEW_PORTS (for example 9100-9199) and publish those ports on the Unire container."),
        el("div", { class: "item-actions" }, checkBtn)
      )
    );
    return;
  }

  const parts = [];
  const problems = previewsData.problems || [];
  for (const prob of problems) {
    parts.push(el("div", { class: "preview-notice err" }, prob.repo + ": " + prob.error));
  }

  const list = previewsData.previews || [];
  if (!list.length) {
    const checkBtn = el("button", { type: "button", class: "compact" }, "Check again");
    checkBtn.addEventListener("click", () => guarded(checkBtn, refreshPreviews));
    parts.push(
      el("div", { class: "side-empty-msg" },
        el("p", {}, "No previewable apps detected"),
        el("span", { class: "muted" }, "Unire looks for a Flutter app (pubspec.yaml), a static site (index.html), or unire.preview.json in the workspace."),
        el("div", { class: "item-actions" }, checkBtn)
      )
    );
    container.replaceChildren(...parts);
    return;
  }

  if (list.length > 1) {
    const picker = el("div", { class: "preview-picker" }, ...list.map((item) => {
      const k = previewItemKey(item);
      const label = item.repo + (item.name && item.name !== "app" ? " / " + item.name : "");
      const btn = el("button", {
        type: "button",
        class: "preview-choice" + (k === selectedPreviewKey ? " active" : ""),
      }, label);
      btn.addEventListener("click", () => {
        if (selectedPreviewKey === k) return;
        selectedPreviewKey = k;
        loadedPreviewKey = "";
        loadedPreviewBase = "";
        previewLogsText = "";
        renderPreviewPane();
        schedulePreviewPoll(pollToken, selectedRoot);
      });
      return btn;
    }));
    parts.push(picker);
  }

  const p = activePreview();
  const st = p.status || null;
  const state = st ? st.state : "";
  const isReady = state === "ready" && st && st.url;
  const isBusy = isBusyState(state);

  const metaRow = el("div", { class: "preview-meta-row" },
    el("div", { class: "preview-title-group" },
      el("span", { class: "preview-name" }, p.repo + (p.dir && p.dir !== "." ? "/" + p.dir : "")),
      el("span", { class: "preview-recipe" }, p.recipe)
    ),
    previewStatusBadge(state)
  );

  const actions = el("div", { class: "preview-actions" });
  if (!state || state === "stopped" || state === "failed") {
    const startBtn = el("button", { type: "button", class: "primary compact" }, state === "failed" ? "Retry preview" : "Start preview");
    startBtn.addEventListener("click", () => startSelectedPreview(p, startBtn));
    actions.append(startBtn);
  }
  if (isReady) {
    const reloadBtn = el("button", { type: "button", class: "compact" }, "Reload");
    reloadBtn.addEventListener("click", () => {
      const iframe = container.querySelector(".preview-frame");
      if (iframe && st.url) {
        previewIframeNonce++;
        iframe.src = st.url + (st.url.includes("?") ? "&" : "?") + "_r=" + previewIframeNonce;
      }
    });
    actions.append(reloadBtn);

    const openLink = el("a", {
      class: "button compact",
      href: st.url,
      target: "_blank",
      rel: "noopener noreferrer",
      title: "Open preview in a new browser tab",
    }, "Open");
    const ext = el("span", { class: "btn-icon", "aria-hidden": "true" });
    ext.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>';
    openLink.append(ext);
    actions.append(openLink);
  }
  if (isReady || isBusy) {
    const stopBtn = el("button", { type: "button", class: "compact danger" }, "Stop");
    stopBtn.addEventListener("click", () => stopSelectedPreview(p, stopBtn));
    actions.append(stopBtn);
  }

  const controls = el("div", { class: "preview-controls" }, actions);
  if (isReady && !isMixedContentBlocked(st.url)) {
    const devBar = el("div", { class: "preview-devices", role: "group", "aria-label": "Preview size" },
      ...Object.entries(DEVICE_SIZES).map(([key, spec]) => {
        const b = el("button", {
          type: "button",
          class: "preview-dev-btn" + (previewDevice === key ? " active" : ""),
          "aria-pressed": String(previewDevice === key),
        }, spec.label);
        b.addEventListener("click", () => {
          previewDevice = key;
          try { localStorage.setItem("unire_preview_device", key); } catch (_) {}
          for (const sibling of devBar.querySelectorAll(".preview-dev-btn")) {
            sibling.classList.remove("active");
            sibling.setAttribute("aria-pressed", "false");
          }
          b.classList.add("active");
          b.setAttribute("aria-pressed", "true");
          scalePreviewFrame();
        });
        return b;
      })
    );
    controls.append(devBar);
  }

  const cardKids = [metaRow, controls];

  const tail = (typeof thread !== "undefined" && thread.length) ? thread[thread.length - 1] : null;
  if (tail && tail.s && tail.s.network === "agent") {
    cardKids.push(el("div", { class: "preview-notice warn" },
      "This chat uses Agent-only network mode, so the preview cannot download packages."));
  }

  if (isBusy) {
    cardKids.push(el("div", { class: "preview-progress" },
      el("span", { class: "spin", "aria-hidden": "true" }),
      el("span", { class: "preview-progress-msg" }, (st && st.message) || (state === "downloading" ? "Downloading preview image…" : "Starting preview…"))
    ));
  } else if (state === "failed") {
    cardKids.push(el("div", { class: "preview-notice err" }, (st && st.message) || "Preview failed to start."));
  } else if (!isReady) {
    cardKids.push(el("div", { class: "preview-notice" },
      state === "stopped" ? "Preview is stopped. Start it to run the app." : "Start the preview to run the app in its own container."));
  }

  if (isReady) {
    if (isMixedContentBlocked(st.url)) {
      cardKids.push(el("div", { class: "preview-notice warn" },
        "Dashboard is served over HTTPS, so the browser blocks embedding an HTTP preview here. Use Open above to view it in a new tab."));
    } else {
      const k = previewItemKey(p);
      const base = stripToken(st.url);
      let existingIframe = container.querySelector(".preview-frame");
      let stage;
      if (existingIframe && loadedPreviewKey === k && loadedPreviewBase === base) {
        stage = existingIframe.closest(".preview-stage");
      }
      if (!stage) {
        const iframe = el("iframe", {
          class: "preview-frame",
          title: p.repo + " preview",
          src: st.url,
          sandbox: "allow-scripts allow-same-origin allow-forms allow-popups allow-modals",
        });
        loadedPreviewKey = k;
        loadedPreviewBase = base;
        stage = el("div", { class: "preview-stage" },
          el("div", { class: "preview-frame-wrap" }, iframe)
        );
      }
      cardKids.push(stage);
      if (p.recipe === "flutter") {
        cardKids.push(el("div", { class: "preview-foot" },
          "Flutter web build in a browser frame · Reload after agent changes."));
      }
    }
  }

  if (st) {
    const logOut = el("pre", { class: "log" }, (st.logs || previewLogsText) || "Open to load preview logs…");
    const refreshLogsBtn = el("button", { type: "button", class: "tiny" }, "Refresh logs");
    refreshLogsBtn.addEventListener("click", () => fetchPreviewLogs(p, logOut, refreshLogsBtn));
    const logsDetails = el("details", { class: "preview-logs" },
      el("summary", {}, "Preview logs"),
      el("div", { class: "preview-logs-bar" }, refreshLogsBtn),
      logOut
    );
    if (state === "failed" || previewLogsOpen) {
      logsDetails.open = true;
    }
    logsDetails.addEventListener("toggle", () => {
      previewLogsOpen = logsDetails.open;
      if (logsDetails.open && !st.logs) {
        fetchPreviewLogs(p, logOut);
      }
    });
    cardKids.push(logsDetails);
  }

  parts.push(el("div", { class: "preview-card" }, ...cardKids));
  container.replaceChildren(...parts);
  if (isReady && !isMixedContentBlocked(st.url)) {
    requestAnimationFrame(scalePreviewFrame);
  }
}

initPreviewControls();
