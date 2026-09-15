// Prowl Gateway dashboard. Vanilla ES module, no dependencies.
// Talks to the local gateway JSON API and renders a provider directory,
// live health, and usage.

// ---- Auth ------------------------------------------------------------------
// The page holds no credential. It was authorised on load by an httpOnly
// session cookie the server set, and the browser attaches that cookie to
// every same-origin request below. Keeping the token out of JavaScript means
// nothing on the page can leak it.

// ---- API layer -------------------------------------------------------------
async function apiRequest(method, path, body) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  try {
    data = await res.json();
  } catch (_) {
    data = null;
  }
  if (!res.ok) {
    const msg = (data && data.error) || "HTTP " + res.status;
    const err = new Error(msg);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

const api = {
  state: () => apiRequest("GET", "/api/state"),
  usage: () => apiRequest("GET", "/api/usage"),
  saveKey: (id, apiKey, vars) =>
    apiRequest("POST", "/api/providers/" + encodeURIComponent(id) + "/key", {
      api_key: apiKey,
      vars: vars || {},
    }),
  removeKey: (id) =>
    apiRequest("DELETE", "/api/providers/" + encodeURIComponent(id) + "/key"),
  setEnabled: (id, enabled) =>
    apiRequest(
      "POST",
      "/api/providers/" + encodeURIComponent(id) + "/enabled",
      { enabled },
    ),
  setStrategy: (strategy) => apiRequest("POST", "/api/settings", { strategy }),
  test: (id) =>
    apiRequest("POST", "/api/providers/" + encodeURIComponent(id) + "/test"),
};

// ---- Formatting helpers ----------------------------------------------------
const nf = new Intl.NumberFormat();

function fmtNum(n) {
  return nf.format(Math.round(Number(n) || 0));
}

function fmtCompact(n) {
  n = Number(n) || 0;
  if (Math.abs(n) >= 1e9) return (n / 1e9).toFixed(1) + "B";
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (Math.abs(n) >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return fmtNum(n);
}

function fmtCost(n) {
  n = Number(n) || 0;
  if (n === 0) return "$0.00";
  if (n < 0.01) return "<$0.01";
  return "$" + n.toFixed(n < 1 ? 3 : 2);
}

function fmtMs(ms) {
  ms = Number(ms) || 0;
  if (ms <= 0) return "\u2014";
  if (ms >= 1000) return (ms / 1000).toFixed(2) + "s";
  return Math.round(ms) + "ms";
}

function fmtContext(n) {
  n = Number(n) || 0;
  if (n <= 0) return "\u2014";
  if (n >= 1000) return Math.round(n / 1000) + "K";
  return String(n);
}

function fmtClock(iso) {
  if (!iso) return "\u2014";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "\u2014";
  return d.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function fmtRemaining(iso) {
  if (!iso) return "";
  const end = new Date(iso).getTime();
  if (isNaN(end)) return "";
  const secs = Math.max(0, Math.round((end - Date.now()) / 1000));
  if (secs >= 60) return Math.floor(secs / 60) + "m " + (secs % 60) + "s";
  return secs + "s";
}

function titleCase(s) {
  return String(s || "")
    .split("-")
    .map((w) => (w ? w[0].toUpperCase() + w.slice(1) : w))
    .join(" ");
}

// Escape text for safe insertion into HTML.
function esc(s) {
  return String(s == null ? "" : s).replace(
    /[&<>"']/g,
    (c) =>
      ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
      })[c],
  );
}

// Escape a value for use in a CSS attribute selector.
function cssEsc(s) {
  if (window.CSS && CSS.escape) return CSS.escape(s);
  return String(s).replace(/["\\\]]/g, "\\$&");
}

// ---- Toasts ----------------------------------------------------------------
const toastStack = document.getElementById("toast-stack");

function toast(kind, title, msg) {
  const el = document.createElement("div");
  el.className = "toast " + kind;
  el.setAttribute("role", "status");
  const icon = kind === "ok" ? "\u2713" : kind === "err" ? "\u2715" : "\u2139";
  el.innerHTML =
    '<span class="toast-icon" aria-hidden="true">' +
    icon +
    "</span>" +
    '<div class="toast-body"><div class="toast-title">' +
    esc(title) +
    "</div>" +
    (msg ? '<div class="toast-msg">' + esc(msg) + "</div>" : "") +
    "</div>";
  toastStack.appendChild(el);
  const remove = () => {
    el.classList.add("leaving");
    setTimeout(() => el.remove(), 200);
  };
  const timer = setTimeout(remove, 4200);
  el.addEventListener("click", () => {
    clearTimeout(timer);
    remove();
  });
}

// ---- Shared state ----------------------------------------------------------
let lastState = null;
let lastUsage = null;
let activeView = "providers";
let pollTimer = null;
let countdownTimer = null;
let capsBuilt = false;

const expanded = new Set(); // provider ids whose detail row is open
const testResults = {}; // provider id -> { status, model, latencyMs, error }
const filterState = new Set(); // active filter tokens

// Turning one of these on clears its opposite so filters never contradict.
const EXCLUSIVE = {
  "access:free": "access:paid",
  "access:paid": "access:free",
  "key:configured": "key:empty",
  "key:empty": "key:configured",
};

const CAP_LABEL = {
  text: "Text",
  reasoning: "Reasoning",
  vision: "Vision",
  image: "Image",
  audio: "Audio",
  video: "Video",
  embedding: "Embeddings",
  pdf: "PDF",
  rerank: "Rerank",
  code: "Code",
};
const CAP_FILTER_ORDER = [
  "text",
  "reasoning",
  "vision",
  "image",
  "audio",
  "video",
  "embedding",
  "pdf",
  "rerank",
];
const CAP_ROW_ORDER = [
  "reasoning",
  "vision",
  "image",
  "audio",
  "video",
  "text",
  "pdf",
  "embedding",
  "rerank",
];

function capLabel(m) {
  return CAP_LABEL[m] || titleCase(m);
}

// ---- Element refs ----------------------------------------------------------
const tabs = Array.from(document.querySelectorAll(".tab"));
const panels = Array.from(document.querySelectorAll(".panel"));
const providerSearch = document.getElementById("provider-search");
const providerBody = document.getElementById("provider-body");
const providerEmpty = document.getElementById("provider-empty");
const providerCount = document.getElementById("provider-count");
const providerFilters = document.getElementById("provider-filters");
const capChips = document.getElementById("cap-chips");
const filterClear = document.getElementById("filter-clear");
const strategyPicker = document.getElementById("strategy-picker");
const providerTableWrap = document
  .getElementById("provider-table")
  .closest(".table-wrap");

// ---- Tab navigation --------------------------------------------------------
function showView(view) {
  activeView = view;
  tabs.forEach((t) =>
    t.setAttribute("aria-selected", String(t.dataset.view === view)),
  );
  panels.forEach((p) => {
    p.hidden = p.dataset.panel !== view;
  });
  if (location.hash.slice(1) !== view) {
    history.replaceState(null, "", "#" + view);
  }
  startPolling();
  if (view === "health") refreshState();
  else if (view === "usage") refreshUsage();
}

tabs.forEach((tab, idx) => {
  tab.addEventListener("click", () => showView(tab.dataset.view));
  tab.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    e.preventDefault();
    const dir = e.key === "ArrowRight" ? 1 : -1;
    const next = tabs[(idx + dir + tabs.length) % tabs.length];
    next.focus();
    showView(next.dataset.view);
  });
});

// ---- Polling ---------------------------------------------------------------
function startPolling() {
  clearInterval(pollTimer);
  pollTimer = null;
  document
    .getElementById("health-poll")
    .classList.toggle("live", activeView === "health");
  document
    .getElementById("usage-poll")
    .classList.toggle("live", activeView === "usage");
  if (activeView === "health") {
    pollTimer = setInterval(() => {
      if (!document.hidden) refreshState();
    }, 5000);
  } else if (activeView === "usage") {
    pollTimer = setInterval(() => {
      if (!document.hidden) refreshUsage();
    }, 5000);
  }
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) {
    if (activeView === "health") refreshState();
    if (activeView === "usage") refreshUsage();
  }
});

// Cooldown countdowns tick every second while on the health tab.
countdownTimer = setInterval(() => {
  if (activeView !== "health") return;
  document.querySelectorAll("[data-cooldown-ends]").forEach((el) => {
    const left = fmtRemaining(el.dataset.cooldownEnds);
    const span = el.querySelector(".cool-left");
    if (span) span.textContent = left ? " \u00b7 " + left : "";
  });
}, 1000);

// ---- Refreshers ------------------------------------------------------------
async function refreshState() {
  try {
    const state = await api.state();
    lastState = state;
    renderTopbar(state);
    renderStats();
    renderStrategy(state);
    renderProviders();
    if (activeView === "health") renderHealth(state);
  } catch (e) {
    toast("err", "Could not load gateway state", e.message);
  }
}

async function refreshUsage() {
  try {
    lastUsage = await api.usage();
    renderStats();
    renderUsage(lastUsage);
  } catch (e) {
    toast("err", "Could not load usage", e.message);
  }
}

// ---- Topbar, stats, strategy ----------------------------------------------
function renderTopbar(state) {
  document.getElementById("base-url").textContent = state.base_url || "\u2014";
}

function renderStats() {
  const providers = (lastState && lastState.providers) || [];
  const freeModels = providers.reduce(
    (a, p) => a + (Number(p.free_models) || 0),
    0,
  );
  const configured = providers.filter((p) => p.configured).length;
  const t = (lastUsage && lastUsage.totals) || {};
  const set = (id, v) => {
    document.getElementById(id).textContent = v;
  };
  set("stat-providers", fmtNum(providers.length));
  set("stat-free", fmtCompact(freeModels));
  set("stat-configured", fmtNum(configured));
  set("stat-requests", fmtCompact(t.requests || 0));
  set("stat-spend", fmtCost(t.cost_usd || 0));
}

const strategyDesc = {
  cost: "Cheapest capable provider first.",
  latency: "Fastest responding provider first.",
  headroom: "Most remaining rate-limit budget first.",
  priority: "Your configured provider order.",
};

function renderStrategy(state) {
  const strategies =
    state.strategies && state.strategies.length
      ? state.strategies
      : ["cost", "latency", "headroom", "priority"];
  const current = state.strategy;
  strategyPicker.innerHTML = "";
  strategies.forEach((s) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "seg-opt";
    btn.setAttribute("role", "radio");
    btn.setAttribute("aria-checked", String(s === current));
    btn.title = strategyDesc[s] || "";
    btn.textContent = s;
    btn.addEventListener("click", () => selectStrategy(s));
    strategyPicker.appendChild(btn);
  });
}

async function selectStrategy(s) {
  if (!lastState || s === lastState.strategy) return;
  const prev = lastState.strategy;
  lastState.strategy = s;
  renderStrategy(lastState);
  try {
    await api.setStrategy(s);
    toast("ok", "Routing strategy set", titleCase(s));
  } catch (e) {
    lastState.strategy = prev;
    renderStrategy(lastState);
    toast("err", "Could not change strategy", e.message);
  }
}

// Base-URL copy button.
document.getElementById("copy-base").addEventListener("click", async () => {
  const btn = document.getElementById("copy-base");
  const url = document.getElementById("base-url").textContent.trim();
  if (!url || url === "\u2014") return;
  const ok = await copyText(url);
  if (ok) {
    btn.classList.add("copied");
    btn.querySelector(".copy-text").textContent = "Copied";
    setTimeout(() => {
      btn.classList.remove("copied");
      btn.querySelector(".copy-text").textContent = "Copy";
    }, 1600);
    toast("ok", "Base URL copied", url);
  } else {
    toast("err", "Copy failed", "Select and copy the URL manually.");
  }
});

async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch (_) {
    /* fall through to legacy path */
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch (_) {
    return false;
  }
}

// ---- Providers: filtering --------------------------------------------------
function isFree(p) {
  return (Number(p.free_models) || 0) > 0 || p.tier === "permanent-free";
}

function matchToken(p, token) {
  switch (token) {
    case "access:free":
      return isFree(p);
    case "access:paid":
      return !isFree(p);
    case "access:nocard":
      return p.friction !== "card";
    case "access:nosignup":
      return p.friction === "none";
    case "key:configured":
      return !!p.configured;
    case "key:empty":
      return !p.configured;
    default:
      if (token.startsWith("cap:")) {
        const want = capLabel(token.slice(4)).toLowerCase();
        return (p.modalities || []).some(
          (m) => capLabel(m).toLowerCase() === want,
        );
      }
      return true;
  }
}

function providerMatches(p, q) {
  for (const t of filterState) {
    if (!matchToken(p, t)) return false;
  }
  if (!q) return true;
  const hay = [
    p.name,
    p.id,
    p.env,
    p.tier,
    p.signup,
    ...(p.modalities || []),
    ...(p.models || []).flatMap((m) => [m.id, m.name]),
  ]
    .join(" ")
    .toLowerCase();
  return hay.includes(q);
}

function buildCapChips(state) {
  const mods = new Set();
  (state.providers || []).forEach((p) =>
    (p.modalities || []).forEach((m) => mods.add(m)),
  );
  const ordered = [
    ...CAP_FILTER_ORDER.filter((m) => mods.has(m)),
    ...[...mods].filter((m) => !CAP_FILTER_ORDER.includes(m)).sort(),
  ];
  const seen = new Set();
  capChips.innerHTML = ordered
    .map((m) => {
      const label = capLabel(m);
      const k = label.toLowerCase();
      if (seen.has(k)) return "";
      seen.add(k);
      return (
        '<button class="chip" type="button" data-filter="cap:' +
        esc(m) +
        '" aria-pressed="false">' +
        esc(label) +
        "</button>"
      );
    })
    .join("");
  capsBuilt = true;
}

function toggleFilter(token) {
  if (filterState.has(token)) {
    filterState.delete(token);
  } else {
    filterState.add(token);
    const ex = EXCLUSIVE[token];
    if (ex) filterState.delete(ex);
  }
  syncFilterChips();
  renderProviders();
}

function clearFilters() {
  filterState.clear();
  syncFilterChips();
  renderProviders();
}

function syncFilterChips() {
  providerFilters.querySelectorAll(".chip[data-filter]").forEach((chip) => {
    chip.setAttribute(
      "aria-pressed",
      String(filterState.has(chip.dataset.filter)),
    );
  });
  filterClear.hidden = filterState.size === 0;
}

providerSearch.addEventListener("input", renderProviders);
providerFilters.addEventListener("click", (e) => {
  const chip = e.target.closest(".chip[data-filter]");
  if (chip) toggleFilter(chip.dataset.filter);
});
filterClear.addEventListener("click", clearFilters);
providerEmpty.addEventListener("click", (e) => {
  if (e.target.closest('[data-action="clear"]')) clearFilters();
  else if (e.target.closest('[data-action="retry"]')) boot();
});

// Convenience: press "/" to jump to search while on the providers view.
document.addEventListener("keydown", (e) => {
  if (e.key !== "/" || activeView !== "providers") return;
  const t = e.target;
  const tag = (t && t.tagName) || "";
  if (tag === "INPUT" || tag === "TEXTAREA" || (t && t.isContentEditable))
    return;
  e.preventDefault();
  providerSearch.focus();
});

// ---- Providers: rendering --------------------------------------------------
function captureDrafts() {
  const d = {};
  providerBody.querySelectorAll("[data-draft]").forEach((i) => {
    d[i.dataset.draft] = i.value;
  });
  return d;
}

function renderProviders() {
  if (!lastState) return;
  const q = providerSearch.value.trim().toLowerCase();
  const all = lastState.providers || [];
  const filtered = all.filter((p) => providerMatches(p, q));
  providerCount.textContent = all.length
    ? filtered.length + " of " + all.length
    : "";

  if (!all.length) {
    providerTableWrap.hidden = true;
    providerEmpty.hidden = false;
    providerEmpty.innerHTML = emptyBlock(
      "\uD83D\uDD0C",
      "No providers found",
      "The gateway returned an empty provider list.",
    );
    return;
  }
  if (!filtered.length) {
    providerTableWrap.hidden = true;
    providerEmpty.hidden = false;
    providerEmpty.innerHTML = emptyBlock(
      "\uD83D\uDD0D",
      "No providers match",
      "Adjust your search or clear the filters above.",
      '<button class="btn btn-ghost btn-sm empty-cta" type="button" data-action="clear">Clear filters</button>',
    );
    return;
  }

  const drafts = captureDrafts();
  providerTableWrap.hidden = false;
  providerEmpty.hidden = true;
  providerBody.innerHTML = filtered.map((p) => providerRow(p, drafts)).join("");
}

function renderLoading() {
  providerTableWrap.hidden = false;
  providerEmpty.hidden = true;
  const cell = (w, num) =>
    "<td" +
    (num ? ' class="num"' : "") +
    '><div class="skel" style="width:' +
    w +
    (num ? ";margin-left:auto" : "") +
    '"></div></td>';
  let rows = "";
  for (let i = 0; i < 8; i++) {
    rows +=
      '<tr class="skel-row"><td></td>' +
      cell("55%") +
      cell("2ch", true) +
      cell("3ch", true) +
      cell("70%") +
      cell("60%") +
      cell("50%") +
      cell("40%") +
      "</tr>";
  }
  providerBody.innerHTML = rows;
}

function renderProvidersError(msg) {
  providerTableWrap.hidden = true;
  providerEmpty.hidden = false;
  providerEmpty.innerHTML = emptyBlock(
    "\u26A0\uFE0F",
    "Cannot reach the gateway",
    msg || "The gateway did not respond.",
    '<button class="btn btn-primary btn-sm empty-cta" type="button" data-action="retry">Retry</button>',
  );
}

function emptyBlock(emoji, title, body, cta) {
  return (
    '<span class="empty-emoji" aria-hidden="true">' +
    emoji +
    "</span>" +
    '<div class="empty-title">' +
    esc(title) +
    "</div>" +
    "<div>" +
    esc(body) +
    "</div>" +
    (cta || "")
  );
}

function providerRow(p, drafts) {
  const open = expanded.has(p.id);
  const cls = ["prow"];
  if (p.configured) cls.push("configured");
  if (p.configured && p.enabled === false) cls.push("disabled");

  const tags = [
    isFree(p)
      ? '<span class="tag tag-free">Free</span>'
      : '<span class="tag tag-paid">Paid</span>',
  ];
  if (p.configured && p.enabled === false)
    tags.push('<span class="tag tag-off">Disabled</span>');
  if (!p.routable) tags.push('<span class="tag tag-block">Not routable</span>');

  const summary =
    '<tr class="' +
    cls.join(" ") +
    '" data-pid="' +
    esc(p.id) +
    '">' +
    '<td class="col-exp"><button class="exp" type="button" data-expand="' +
    esc(p.id) +
    '" aria-expanded="' +
    open +
    '" aria-controls="detail-' +
    esc(p.id) +
    '" aria-label="Toggle details for ' +
    esc(p.name) +
    '"><span class="chev" aria-hidden="true">\u25B8</span></button></td>' +
    "<td>" +
    '<div class="prov"><span class="prov-name">' +
    esc(p.name) +
    '</span><span class="prov-id mono">' +
    esc(p.id) +
    "</span></div>" +
    '<div class="row-tags">' +
    tags.join("") +
    "</div></td>" +
    '<td class="num mono">' +
    (p.free_models > 0 ? fmtNum(p.free_models) : "\u2014") +
    "</td>" +
    '<td class="num mono">' +
    fmtContext(p.max_context) +
    "</td>" +
    "<td>" +
    rowCaps(p.modalities) +
    "</td>" +
    "<td>" +
    frictionBadge(p) +
    "</td>" +
    "<td>" +
    keyCell(p) +
    "</td>" +
    "<td>" +
    statusCell(p) +
    "</td>" +
    "</tr>";

  const detail =
    '<tr class="detail-row" id="detail-' +
    esc(p.id) +
    '" data-pid="' +
    esc(p.id) +
    '"' +
    (open ? "" : " hidden") +
    '><td colspan="8"><div class="detail">' +
    detailBody(p, drafts) +
    "</div></td></tr>";

  return summary + detail;
}

function rowCaps(mods) {
  mods = mods || [];
  const present = CAP_ROW_ORDER.filter((m) => mods.includes(m)).concat(
    mods.filter((m) => !CAP_ROW_ORDER.includes(m)),
  );
  if (!present.length) return '<span class="cap cap-more">\u2014</span>';
  const shown = present.slice(0, 4);
  const extra = present.length - shown.length;
  let html =
    '<div class="caps" title="' + esc(present.map(capLabel).join(", ")) + '">';
  shown.forEach((m) => {
    html +=
      '<span class="cap' +
      (m === "reasoning" ? " cap-reason" : "") +
      '">' +
      esc(capLabel(m)) +
      "</span>";
  });
  if (extra > 0) html += '<span class="cap cap-more">+' + extra + "</span>";
  html += "</div>";
  return html;
}

function frictionBadge(p) {
  const level = { none: 0, registration: 1, phone: 2, card: 3 }[p.friction];
  const lvl = level == null ? 1 : level;
  const label =
    {
      none: "No signup",
      registration: "Sign up",
      phone: "Phone verify",
      card: "Card required",
    }[p.friction] || titleCase(p.friction);
  let pips = "";
  for (let i = 0; i < 3; i++)
    pips += '<i class="pip' + (i < lvl ? " on" : "") + '"></i>';
  return (
    '<span class="frict frict-' +
    esc(p.friction) +
    '" title="' +
    esc(p.signup || label) +
    '"><span class="meter" aria-hidden="true">' +
    pips +
    '</span><span class="frict-text">' +
    esc(label) +
    "</span></span>"
  );
}

function keyCell(p) {
  if (p.configured) {
    return (
      '<span class="keyst on" title="Key source: ' +
      esc(p.key_source || "store") +
      '"><span class="dot" aria-hidden="true"></span><span class="mono">' +
      esc(p.key_masked || "key set") +
      "</span></span>"
    );
  }
  return '<span class="keyst off"><span class="dot" aria-hidden="true"></span>None</span>';
}

function healthFor(p) {
  const rows = (lastState && lastState.health) || [];
  let cooling = false;
  let failures = 0;
  let traffic = false;
  rows.forEach((h) => {
    if (h.provider === p.id || h.provider === p.name) {
      if (h.cooling_down) cooling = true;
      failures += Number(h.consecutive_failures) || 0;
      if ((Number(h.requests_in_window) || 0) > 0) traffic = true;
    }
  });
  return { cooling, failures, traffic };
}

function statusBadge(cls, ic, text) {
  return (
    '<span class="stbadge ' +
    cls +
    '"><span class="ic" aria-hidden="true">' +
    ic +
    "</span>" +
    esc(text) +
    "</span>"
  );
}

function statusCell(p) {
  if (!p.routable) return statusBadge("st-err", "\u2298", "Blocked");
  if (p.configured && p.enabled === false)
    return statusBadge("st-warn", "\u25B2", "Disabled");
  if (p.configured) {
    const h = healthFor(p);
    if (h.cooling) return statusBadge("st-warn", "\u25F4", "Cooling");
    if (h.failures > 0) return statusBadge("st-warn", "\u25B2", "Degraded");
    if (h.traffic) return statusBadge("st-ok", "\u25CF", "Online");
    return statusBadge("st-ready", "\u25CF", "Ready");
  }
  return statusBadge("st-none", "\u2013", "No key");
}

function detailBody(p, drafts) {
  const parts = [];
  if (!p.routable) {
    parts.push(blockedNote(p));
  } else {
    parts.push(keyEditor(p, drafts));
    parts.push(detailActions(p));
    parts.push(
      '<div class="test-slot" data-test="' +
        esc(p.id) +
        '">' +
        testLineInner(testResults[p.id]) +
        "</div>",
    );
  }
  parts.push(modelsPreview(p));
  return parts.join("");
}

function keyEditor(p, drafts) {
  const dkey = "key:" + p.id;
  const kv = drafts[dkey] != null ? esc(drafts[dkey]) : "";
  let html =
    '<div class="key-editor"><div class="field">' +
    '<label class="field-label" for="key-' +
    esc(p.id) +
    '">API key</label>' +
    '<input class="key-input" id="key-' +
    esc(p.id) +
    '" type="password" autocomplete="off" spellcheck="false" data-draft="' +
    esc(dkey) +
    '" data-key-input="' +
    esc(p.id) +
    '" placeholder="Paste ' +
    esc(p.env || "API key") +
    '" aria-label="' +
    esc(p.name) +
    ' API key" value="' +
    kv +
    '" /></div>';
  if (p.requires_vars && p.requires_vars.length) {
    html += '<div class="var-grid">';
    p.requires_vars.forEach((v) => {
      const dk = "var:" + p.id + ":" + v;
      const vv = drafts[dk] != null ? esc(drafts[dk]) : "";
      html +=
        '<div class="field"><label class="field-label" for="var-' +
        esc(p.id) +
        "-" +
        esc(v) +
        '">' +
        esc(v) +
        "</label>" +
        '<input class="key-input" id="var-' +
        esc(p.id) +
        "-" +
        esc(v) +
        '" type="text" autocomplete="off" spellcheck="false" data-draft="' +
        esc(dk) +
        '" data-var="' +
        esc(v) +
        '" data-var-provider="' +
        esc(p.id) +
        '" placeholder="' +
        esc(v) +
        '" value="' +
        vv +
        '" /></div>';
    });
    html += "</div>";
  }
  html += "</div>";
  return html;
}

function detailActions(p) {
  const enabled = p.enabled !== false;
  const getKey = p.api_key_url
    ? '<a class="link-out" href="' +
      esc(p.api_key_url) +
      '" target="_blank" rel="noopener noreferrer">Get key \u2197</a>'
    : "";
  return (
    '<div class="detail-actions">' +
    '<label class="toggle"><input type="checkbox" data-toggle-enable="' +
    esc(p.id) +
    '" ' +
    (enabled ? "checked" : "") +
    ' aria-label="Enable ' +
    esc(p.name) +
    '" /><span class="track"></span><span class="toggle-text">' +
    (enabled ? "Enabled" : "Disabled") +
    "</span></label>" +
    '<span class="spacer"></span>' +
    getKey +
    '<button class="btn btn-ghost btn-sm" type="button" data-action="test" data-id="' +
    esc(p.id) +
    '">Test</button>' +
    (p.configured
      ? '<button class="btn btn-danger btn-sm" type="button" data-action="remove" data-id="' +
        esc(p.id) +
        '">Remove</button>'
      : "") +
    '<button class="btn btn-primary btn-sm" type="button" data-action="save" data-id="' +
    esc(p.id) +
    '">Save key</button>' +
    "</div>"
  );
}

function testLineInner(tr) {
  if (!tr) return "";
  if (tr.status === "testing") {
    return (
      '<div class="test-line testing"><span class="ic" aria-hidden="true">\u2026</span>' +
      '<span class="test-detail">Testing\u2026 sending a probe request.</span></div>'
    );
  }
  if (tr.status === "ok") {
    const detail =
      (tr.model
        ? '<span class="mono">' + esc(tr.model) + "</span> \u00b7 "
        : "") + esc(fmtMs(tr.latencyMs));
    return (
      '<div class="test-line ok"><span class="ic" aria-hidden="true">\u2713</span>' +
      "<span><strong>Responded</strong> " +
      '<span class="test-detail">' +
      detail +
      "</span></span></div>"
    );
  }
  return (
    '<div class="test-line err"><span class="ic" aria-hidden="true">\u2715</span>' +
    "<span><strong>Test failed</strong> " +
    '<span class="test-detail">' +
    esc(tr.error || "Unknown error") +
    "</span></span></div>"
  );
}

function updateTestSlot(id) {
  const slot = providerBody.querySelector('[data-test="' + cssEsc(id) + '"]');
  if (slot) slot.innerHTML = testLineInner(testResults[id]);
}

function modelsPreview(p) {
  const models = p.models || [];
  if (!models.length) return "";
  const rows = models
    .slice(0, 6)
    .map(
      (m) =>
        '<tr><td class="m-id">' +
        esc(m.id || m.name || "\u2014") +
        "</td><td>" +
        fmtContext(m.context) +
        "</td><td>" +
        esc(m.limits || "\u2014") +
        "</td></tr>",
    )
    .join("");
  return (
    '<div class="models-preview"><table><thead><tr>' +
    "<th>Sample model</th><th>Context</th><th>Rate limit</th>" +
    "</tr></thead><tbody>" +
    rows +
    "</tbody></table></div>"
  );
}

function blockedNote(p) {
  const reasons = [];
  if (p.compat && p.compat !== "openai") {
    reasons.push(
      "It speaks the <strong>" +
        esc(p.compat) +
        "</strong> protocol, which the gateway does not route through its OpenAI-compatible endpoint.",
    );
  }
  if (p.requires_vars && p.requires_vars.length) {
    reasons.push(
      "It needs extra configuration this dashboard cannot set: <strong>" +
        p.requires_vars.map(esc).join(", ") +
        "</strong>.",
    );
  }
  if (!reasons.length) {
    reasons.push("This provider is not routable in the current build.");
  }
  return (
    '<div class="blocked-note"><strong>Not routable.</strong> ' +
    reasons.join(" ") +
    "</div>"
  );
}

// ---- Providers: interaction ------------------------------------------------
function providerById(id) {
  return ((lastState && lastState.providers) || []).find((p) => p.id === id);
}

function detailFor(id) {
  return providerBody.querySelector(
    'tr.detail-row[data-pid="' + cssEsc(id) + '"]',
  );
}

function toggleExpand(id) {
  if (expanded.has(id)) expanded.delete(id);
  else expanded.add(id);
  const open = expanded.has(id);
  const prow = providerBody.querySelector(
    'tr.prow[data-pid="' + cssEsc(id) + '"]',
  );
  const detail = detailFor(id);
  if (!prow || !detail) return;
  detail.hidden = !open;
  prow.classList.toggle("open", open);
  const btn = prow.querySelector("[data-expand]");
  if (btn) btn.setAttribute("aria-expanded", String(open));
  if (open) {
    const inp = detail.querySelector(".key-input");
    if (inp) inp.focus();
  }
}

providerBody.addEventListener("click", (e) => {
  const act = e.target.closest("[data-action]");
  if (act) {
    handleAction(act.dataset.action, act.dataset.id);
    return;
  }
  if (e.target.closest("a, input, label, .toggle")) return;
  const prow = e.target.closest("tr.prow");
  if (prow && prow.dataset.pid) toggleExpand(prow.dataset.pid);
});

providerBody.addEventListener("keydown", (e) => {
  if (e.key !== "Enter") return;
  const t = e.target;
  if (t && (t.dataset.keyInput || t.dataset.varProvider)) {
    e.preventDefault();
    const id = t.dataset.keyInput || t.dataset.varProvider;
    const p = providerById(id);
    if (p) saveProvider(p, detailFor(id));
  }
});

providerBody.addEventListener("change", (e) => {
  const tog = e.target.closest("[data-toggle-enable]");
  if (tog) onToggleEnable(tog);
});

// Escape clears the focused key/var field.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  const el = document.activeElement;
  if (el && (el.dataset.keyInput || el.dataset.var || el.dataset.draft)) {
    el.value = "";
    el.blur();
  }
});

function handleAction(action, id) {
  const p = providerById(id);
  if (!p) return;
  const detail = detailFor(id);
  if (action === "save") saveProvider(p, detail);
  else if (action === "remove") removeProvider(p);
  else if (action === "test") testProvider(p, detail);
}

async function saveProvider(p, detail) {
  const keyInput = detail
    ? detail.querySelector('[data-key-input="' + cssEsc(p.id) + '"]')
    : null;
  const apiKey = keyInput ? keyInput.value.trim() : "";
  if (!apiKey) {
    toast("err", "Nothing to save", "Paste an API key first.");
    if (keyInput) keyInput.focus();
    return;
  }
  const vars = {};
  if (detail)
    detail
      .querySelectorAll('[data-var-provider="' + cssEsc(p.id) + '"]')
      .forEach((inp) => {
        if (inp.value.trim()) vars[inp.dataset.var] = inp.value.trim();
      });
  const missing = (p.requires_vars || []).filter((v) => !vars[v]);
  if (missing.length) {
    toast("err", "Missing fields", "Fill in: " + missing.join(", "));
    return;
  }
  try {
    await api.saveKey(p.id, apiKey, vars);
    if (keyInput) keyInput.value = "";
    delete testResults[p.id];
    expanded.add(p.id);
    toast("ok", "Key saved", p.name + " is ready to route.");
    await refreshState();
  } catch (e) {
    toast("err", "Could not save key", e.message);
  }
}

async function removeProvider(p) {
  try {
    await api.removeKey(p.id);
    delete testResults[p.id];
    toast("ok", "Key removed", p.name);
    await refreshState();
  } catch (e) {
    toast("err", "Could not remove key", e.message);
  }
}

async function testProvider(p, detail) {
  testResults[p.id] = { status: "testing" };
  updateTestSlot(p.id);
  const btn = detail
    ? detail.querySelector(
        '[data-action="test"][data-id="' + cssEsc(p.id) + '"]',
      )
    : null;
  if (btn) btn.disabled = true;
  try {
    const r = await api.test(p.id);
    if (r && r.ok) {
      testResults[p.id] = {
        status: "ok",
        model: r.model,
        latencyMs: r.latency_ms,
      };
    } else {
      testResults[p.id] = {
        status: "err",
        error: (r && r.error) || "Unknown error",
      };
    }
  } catch (e) {
    testResults[p.id] = { status: "err", error: e.message };
  }
  if (btn) btn.disabled = false;
  updateTestSlot(p.id);
}

async function onToggleEnable(input) {
  const id = input.dataset.toggleEnable;
  const p = providerById(id);
  if (!p) return;
  const want = input.checked;
  input.disabled = true;
  try {
    await api.setEnabled(id, want);
    p.enabled = want;
    const txt = input.parentElement.querySelector(".toggle-text");
    if (txt) txt.textContent = want ? "Enabled" : "Disabled";
    toast("ok", p.name + (want ? " enabled" : " disabled"));
    await refreshState();
  } catch (e) {
    input.checked = !want;
    toast("err", "Could not update " + p.name, e.message);
  } finally {
    input.disabled = false;
  }
}

// ---- Health ----------------------------------------------------------------
function renderHealth(state) {
  const body = document.getElementById("health-body");
  const empty = document.getElementById("health-empty");
  const rows = state.health || [];
  if (!rows.length) {
    body.innerHTML = "";
    empty.hidden = false;
    empty.innerHTML = emptyBlock(
      "\uD83D\uDCE1",
      "No live routes yet",
      "Health rows appear once the gateway has served a request. Configure a provider and send one through the base URL.",
    );
    return;
  }
  empty.hidden = true;
  body.innerHTML = rows.map(healthRow).join("");
}

function healthRow(h) {
  const head = Math.max(0, Math.min(1, Number(h.headroom) || 0));
  const pct = Math.round(head * 100);
  const warn = head < 0.25;
  const fails = Number(h.consecutive_failures) || 0;

  let stateCell;
  if (h.cooling_down) {
    stateCell =
      '<span class="pill pill-cool" data-cooldown-ends="' +
      esc(h.cooldown_ends || "") +
      '"><span class="ic" aria-hidden="true">\u25F4</span>Cooling<span class="cool-left"></span></span>';
  } else if (fails > 0) {
    stateCell =
      '<span class="pill pill-warn"><span class="ic" aria-hidden="true">\u25B2</span>Degraded</span>';
  } else {
    stateCell =
      '<span class="pill pill-ok"><span class="ic" aria-hidden="true">\u25CF</span>Healthy</span>';
  }

  return (
    "<tr>" +
    '<td><div class="route-cell"><span class="route-main">' +
    esc(h.model || h.key) +
    "</span>" +
    '<span class="route-sub">' +
    esc(h.provider || "") +
    "</span></div></td>" +
    '<td class="mono">' +
    fmtMs(h.avg_latency_ms) +
    "</td>" +
    "<td>" +
    '<div class="bar"><div class="bar-track"><div class="bar-fill' +
    (warn ? " warn" : "") +
    '" style="width:' +
    pct +
    '%"></div></div>' +
    '<span class="bar-val">' +
    pct +
    "%</span></div>" +
    "</td>" +
    '<td class="mono">' +
    fmtCompact(h.requests_in_window || 0) +
    " req \u00b7 " +
    fmtCompact(h.tokens_in_window || 0) +
    " tok</td>" +
    '<td class="mono">' +
    (fails > 0 ? '<span class="err-text">' + fails + "</span>" : "0") +
    "</td>" +
    "<td>" +
    (h.cooling_down
      ? stateCell.replace(
          '<span class="cool-left"></span>',
          '<span class="cool-left"> \u00b7 ' +
            esc(fmtRemaining(h.cooldown_ends)) +
            "</span>",
        )
      : stateCell) +
    "</td>" +
    "</tr>"
  );
}

// ---- Usage -----------------------------------------------------------------
function renderUsage(usage) {
  renderUsageChart(usage);
  renderUsageProviders(usage);
  renderUsageRecent(usage);
}

function renderUsageChart(usage) {
  const wrap = document.getElementById("usage-chart");
  const rows = (usage.by_provider || [])
    .slice()
    .sort((a, b) => (b.requests || 0) - (a.requests || 0));
  if (!rows.length) {
    wrap.innerHTML = '<div class="muted">No requests recorded yet.</div>';
    return;
  }
  const max = Math.max(1, ...rows.map((r) => r.requests || 0));
  wrap.innerHTML = rows
    .map((r) => {
      const pct = Math.round(((r.requests || 0) / max) * 100);
      return (
        '<div class="sparkbar-row">' +
        '<span class="sparkbar-label" title="' +
        esc(r.provider) +
        '">' +
        esc(r.provider) +
        "</span>" +
        '<div class="sparkbar-track"><div class="sparkbar-fill" style="width:' +
        Math.max(pct, 2) +
        '%"></div></div>' +
        '<span class="sparkbar-count">' +
        fmtNum(r.requests || 0) +
        "</span>" +
        "</div>"
      );
    })
    .join("");
}

function renderUsageProviders(usage) {
  const body = document.getElementById("usage-provider-body");
  const rows = usage.by_provider || [];
  if (!rows.length) {
    body.innerHTML =
      '<tr><td colspan="5" class="muted">No provider usage yet.</td></tr>';
    return;
  }
  body.innerHTML = rows
    .map(
      (r) =>
        "<tr>" +
        "<td><strong>" +
        esc(r.provider) +
        "</strong></td>" +
        '<td class="num mono">' +
        fmtNum(r.requests || 0) +
        "</td>" +
        '<td class="num mono">' +
        fmtCompact((r.tokens_in || 0) + (r.tokens_out || 0)) +
        "</td>" +
        '<td class="num mono">' +
        (r.failures > 0
          ? '<span class="err-text">' + fmtNum(r.failures) + "</span>"
          : "0") +
        "</td>" +
        '<td class="num mono">' +
        fmtMs(r.avg_latency_ms) +
        "</td>" +
        "</tr>",
    )
    .join("");
}

function renderUsageRecent(usage) {
  const body = document.getElementById("usage-recent-body");
  const empty = document.getElementById("usage-empty");
  const rows = usage.recent || [];
  if (!rows.length) {
    body.innerHTML = "";
    empty.hidden = false;
    empty.innerHTML = emptyBlock(
      "\uD83D\uDCED",
      "No requests yet",
      "Once a client hits the base URL, every routed request shows up here with its status and any failover.",
    );
    return;
  }
  empty.hidden = true;
  body.innerHTML = rows
    .slice()
    .reverse()
    .map((r) => {
      const ok = Number(r.status) >= 200 && Number(r.status) < 300 && !r.error;
      const route =
        '<div class="route-cell"><span class="route-main">' +
        esc(r.provider) +
        "/" +
        esc(r.model) +
        "</span>" +
        (r.routed_from
          ? '<span class="failover">\u21b3 failover from ' +
            esc(r.routed_from) +
            "</span>"
          : "") +
        (r.error ? '<span class="err-text">' + esc(r.error) + "</span>" : "") +
        "</div>";
      return (
        "<tr>" +
        '<td class="mono">' +
        fmtClock(r.at) +
        "</td>" +
        "<td>" +
        route +
        "</td>" +
        '<td><span class="status-code ' +
        (ok ? "ok" : "err") +
        '">' +
        esc(r.status || (r.error ? "ERR" : "\u2014")) +
        "</span></td>" +
        '<td class="mono">' +
        fmtCompact(r.tokens_in || 0) +
        " / " +
        fmtCompact(r.tokens_out || 0) +
        "</td>" +
        '<td class="mono">' +
        fmtMs(r.latency_ms) +
        "</td>" +
        '<td class="mono">' +
        (Number(r.attempt) > 1
          ? '<span class="failover">#' + esc(r.attempt) + "</span>"
          : "#" + esc(r.attempt || 1)) +
        "</td>" +
        "</tr>"
      );
    })
    .join("");
}

// ---- Boot ------------------------------------------------------------------
async function boot() {
  const initial = (location.hash.slice(1) || "providers").toLowerCase();
  const known = tabs.some((t) => t.dataset.view === initial);
  activeView = known ? initial : "providers";

  renderLoading();

  try {
    const [state, usage] = await Promise.all([
      api.state(),
      api.usage().catch(() => null),
    ]);
    lastState = state;
    if (usage) lastUsage = usage;
    if (!capsBuilt) buildCapChips(state);
    syncFilterChips();
    renderTopbar(state);
    renderStats();
    renderStrategy(state);
    renderProviders();
    renderHealth(state);
    if (lastUsage) renderUsage(lastUsage);
  } catch (e) {
    renderProvidersError(e.message);
    toast(
      "err",
      "Cannot reach the gateway",
      e.message + " \u2014 is the gateway process running?",
    );
  }

  showView(activeView);
}

window.addEventListener("hashchange", () => {
  const v = (location.hash.slice(1) || "providers").toLowerCase();
  if (tabs.some((t) => t.dataset.view === v) && v !== activeView) showView(v);
});

boot();
