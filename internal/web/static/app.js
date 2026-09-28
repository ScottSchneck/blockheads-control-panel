"use strict";
// The panel page ("Control Room" style). One page, with views chosen by the
// address after # (see route): the servers dashboard, one server with its
// Overview, Players, Settings, Backups and Console tabs, and the Backups,
// Joining, Import, Add server, Setup guide and Account pages.

const $ = (id) => document.getElementById(id);
const labels = {
  running: "Running", starting: "Starting", stopping: "Stopping", stopped: "Stopped",
  installing: "Installing", importing: "Importing", updating: "Updating", restoring: "Restoring", crashed: "Crashed", error: "Needs attention",
};
let selected = null;      // server ID shown in the detail section
let tab = "overview";
let consoleStream = null;
let settings = {};

async function api(path, options = {}) {
  const res = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", "X-Blockheads": "1", ...(options.headers || {}) },
  });
  let body = null;
  try { body = await res.json(); } catch (_) { /* empty */ }
  if (res.status === 401 && !["/api/auth/state", "/api/auth/login", "/api/auth/setup", "/api/auth/reset", "/api/auth/password"].includes(path)) {
    signedOut(); // the session ended (signed out elsewhere, or expired)
  }
  if (!res.ok) throw new Error((body && body.error) || res.statusText);
  return body;
}
const post = (path, body) => api(path, { method: "POST", body: JSON.stringify(body || {}) });
const del = (path) => api(path, { method: "DELETE" });
const sid = () => `/api/servers/${encodeURIComponent(selected)}`;

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children) if (c != null) e.append(c);
  return e;
}

// ---- server list (the dashboard) ----

let serverList = [];       // last /api/servers answer
let latestVersion = "";    // newest Bedrock release, when known
let joinInfo = null;       // console list name and IP, from the setup guide info

function action(id, what) {
  return async (ev) => {
    ev.currentTarget.disabled = true;
    try { await post(`/api/servers/${encodeURIComponent(id)}/${what}`); }
    catch (err) { alert(err.message); }
    refresh();
  };
}

const busyStates = ["installing", "importing", "updating", "restoring", "starting", "stopping"];
const isLive = (s) => s.state === "running" || s.state === "starting";

function stateEl(s) {
  return el("span", { class: `state ${s.state}` }, el("span", { class: "dot" }), labels[s.state] || s.state);
}

// needsUpdate says whether a server is behind the newest release, and
// whether it's far behind (a different 1.x, or ten or more versions back).
function needsUpdate(s) {
  if (!latestVersion || !s.version || s.preview) return null;
  if (!newer(latestVersion, s.version)) return null;
  const l = latestVersion.split(".").map(Number), v = s.version.split(".").map(Number);
  return { far: l[0] !== v[0] || l[1] !== v[1] || (l[2] || 0) - (v[2] || 0) >= 10 };
}

function newer(a, b) {
  const pa = a.split(".").map(Number), pb = b.split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] || 0, y = pb[i] || 0;
    if (x !== y) return x > y;
  }
  return false;
}

function whenShort(iso) {
  if (!iso) return "Never";
  const d = new Date(iso);
  const today = new Date();
  const yesterday = new Date(Date.now() - 864e5);
  const time = d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (d.toDateString() === today.toDateString()) return `Today ${time}`;
  if (d.toDateString() === yesterday.toDateString()) return `Yesterday ${time}`;
  return d.toLocaleDateString([], { month: "short", day: "numeric" }) + " " + time;
}

function playingText(s) {
  if (s.players.length) return s.players.map((p) => p.name).join(", ");
  return s.state === "running" ? "Nobody" : "";
}

const serverHref = (id, tab) => `#/server/${encodeURIComponent(id)}/${tab || "overview"}`;

function startStopButton(s) {
  const busy = busyStates.includes(s.state);
  return isLive(s)
    ? el("button", { class: "danger", onclick: action(s.id, "stop"), disabled: s.state === "stopping" }, "Stop")
    : el("button", { class: "go", onclick: action(s.id, "start"), disabled: busy }, "Start");
}

function renderServerRow(s) {
  const upd = needsUpdate(s);
  const extra = [];
  if (s.players.length) extra.push(`${s.players.length} playing`);
  if (s.waiting) extra.push(`${s.waiting} waiting to be let in`);
  if (upd) extra.push("update ready");
  return el("div", { class: "srow" },
    el("div", {},
      el("a", { class: "name", href: serverHref(s.id) }, s.name),
      el("div", { class: "sub" }, `port ${s.port}`),
      s.message ? el("div", { class: "note warn" }, s.message) : null),
    el("div", {}, stateEl(s)),
    el("div", { class: "cell-muted" }, playingText(s), s.waiting ? el("div", { class: "note warn" }, `${s.waiting} waiting to be let in`) : null),
    el("div", {},
      el("div", { class: "mono" }, s.version || "…"),
      upd ? el("div", { class: "note upd" + (upd.far ? " far" : "") }, upd.far ? "Well behind: update" : "Update ready") : null),
    el("div", { class: "cell-muted" }, whenShort(s.lastBackup)),
    el("div", { class: "acts" },
      startStopButton(s),
      el("a", { class: "button", href: serverHref(s.id) }, "Open")),
    extra.length ? el("div", { class: "mobile-extra" }, extra.join(" · ")) : null);
}

function renderDashboard(list, force) {
  const key = JSON.stringify([list, latestVersion]);
  if (key === dashKey && !force) return; // unchanged: keep keyboard focus
  dashKey = key;
  const head = el("div", { class: "srow head" },
    el("div", {}, "Server"), el("div", {}, "Status"), el("div", {}, "Playing"),
    el("div", {}, "Version"), el("div", {}, "Last backup"), el("div", {}));
  $("servers").replaceChildren(...(list.length ? [head, ...list.map(renderServerRow)] : []));
  $("servers").hidden = list.length === 0;
  $("empty").hidden = list.length > 0;
  const running = list.filter((s) => s.state === "running").length;
  const playing = list.reduce((n, s) => n + s.players.length, 0);
  $("servers-summary").textContent = list.length
    ? `${list.length} server${list.length === 1 ? "" : "s"} · ${running} running · ${playing} ${playing === 1 ? "person" : "people"} playing`
    : "";

  // Update note
  const behind = list.filter((s) => needsUpdate(s) && !busyStates.includes(s.state));
  $("update-banner").hidden = behind.length === 0;
  if (behind.length) {
    $("update-banner-text").replaceChildren(el("b", {}, `Bedrock ${latestVersion} is out. `),
      `${behind.length} server${behind.length === 1 ? " can" : "s can"} update. Each one is backed up first and started again if it was running.`);
    $("update-all").textContent = behind.length === 1 ? "Update it" : `Update all ${behind.length}`;
  }

  // Backups card
  const never = list.filter((s) => !s.lastBackup).map((s) => s.name);
  if (!list.length) $("dash-backups").textContent = "No servers yet.";
  else if (never.length) $("dash-backups").textContent = `Not backed up yet: ${never.join(", ")}.`;
  else {
    const oldest = list.reduce((a, b) => (new Date(a.lastBackup) <= new Date(b.lastBackup) ? a : b));
    $("dash-backups").textContent = list.length === 1
      ? `Last backup ${whenShort(oldest.lastBackup).toLowerCase()}.`
      : `All ${list.length} are backed up. Longest ago: ${oldest.name}, ${whenShort(oldest.lastBackup).toLowerCase()}.`;
  }
  loadDashAttempts(list);
}

$("update-all").addEventListener("click", async (ev) => {
  const button = ev.currentTarget;
  const behind = serverList.filter((s) => needsUpdate(s) && !busyStates.includes(s.state));
  const playing = behind.reduce((n, s) => n + s.players.length, 0);
  const note = playing ? `\n\n${playing} player(s) will be disconnected.` : "";
  if (!confirm(`Update ${behind.map((s) => s.name).join(", ")} to Bedrock ${latestVersion}? Each is backed up first.${note}`)) return;
  button.disabled = true;
  for (const s of behind) {
    try { await post(`/api/servers/${encodeURIComponent(s.id)}/update`); } catch (err) { alert(`${s.name}: ${err.message}`); }
  }
  button.disabled = false;
  refresh();
});

// Tried to join, across servers. Only servers with someone waiting are asked.
let attemptsLoadedAt = 0;
async function loadDashAttempts(list) {
  const waiting = list.filter((s) => s.waiting > 0);
  if (!waiting.length) { $("dash-attempts").hidden = true; return; }
  if (Date.now() - attemptsLoadedAt < 5000) return;
  attemptsLoadedAt = Date.now();
  const rows = [];
  for (const s of waiting) {
    try {
      const v = await api(`/api/servers/${encodeURIComponent(s.id)}/players`);
      for (const a of v.attempts || []) {
        rows.push(row(a.name, `${s.name} · ${ago(a.at)}`,
          btn("Allow", async () => {
            try { await post(`/api/servers/${encodeURIComponent(s.id)}/allowlist`, { name: a.name }); } catch (err) { alert(err.message); }
            attemptsLoadedAt = 0;
            refresh();
          }, "primary")));
      }
    } catch (_) { /* shown on the server's page */ }
  }
  $("dash-attempts-list").replaceChildren(...rows);
  $("dash-attempts").hidden = rows.length === 0;
}

function renderBackupsOverview(list) {
  const head = el("div", { class: "srow head" },
    el("div", {}, "Server"), el("div", {}, "Status"), el("div", {}, ""), el("div", {}, ""), el("div", {}, "Last backup"), el("div", {}));
  $("backups-overview").replaceChildren(head, ...list.map((s) => el("div", { class: "srow" },
    el("div", {}, el("a", { class: "name", href: serverHref(s.id, "backups") }, s.name)),
    el("div", {}, stateEl(s)), el("div", {}), el("div", {}),
    el("div", { class: "cell-muted" }, whenShort(s.lastBackup)),
    el("div", { class: "acts" }, el("a", { class: "button", href: serverHref(s.id, "backups") }, "Backups")),
    el("div", { class: "mobile-extra" }, `Last backup: ${whenShort(s.lastBackup)}`))));
}

let detailKey = "";   // what the server page header last showed
let ovPlayersKey = ""; // what the Overview player list last showed
let dashKey = "";      // what the dashboard last showed

function renderDetailHead(s) {
  const key = JSON.stringify([s.id, s.name, s.state, s.version, s.port, s.message, latestVersion, s.players.length]);
  if (key === detailKey) return; // unchanged: keep keyboard focus where it is
  detailKey = key;
  $("detail-name").textContent = s.name;
  const upd = needsUpdate(s);
  $("detail-facts").replaceChildren(stateEl(s),
    el("span", {}, "Bedrock ", el("span", { class: "mono" }, s.version || "…")),
    el("span", {}, "Port ", el("span", { class: "mono" }, String(s.port))));
  $("detail-message").textContent = s.message || "";
  $("detail-message").hidden = !s.message;
  const busy = busyStates.includes(s.state);
  const copying = s.state === "importing";
  const updBtn = el("button", { class: upd ? "info" : "", onclick: confirmUpdate(s), disabled: copying || s.state === "installing" || s.state === "updating" || s.state === "restoring" },
    "Update", upd ? el("span", { class: "long" }, ` to ${latestVersion}`) : null);
  $("detail-actions").replaceChildren(startStopButton(s),
    el("button", { onclick: action(s.id, "restart"), disabled: !isLive(s) || busy }, "Restart"),
    updBtn);
  for (const t of ["players", "settings", "backups"]) $("tab-" + t).hidden = copying;
}

function confirmUpdate(s) {
  const run = action(s.id, "update");
  return (ev) => {
    const note = s.players.length ? `\n\n${s.players.length} player(s) will be disconnected.` : "";
    if (confirm(`Update ${s.name}? It's backed up first, then started again if it was running.${note}`)) run(ev);
  };
}

async function refresh() {
  if (!signedIn) return;
  try {
    const list = await api("/api/servers");
    if (!signedIn) return; // signed out while this was loading
    serverList = list;
    importsFinished(list);
    if (view === "servers") renderDashboard(list);
    if (view === "backups") renderBackupsOverview(list);
    if (view === "server") {
      const s = list.find((x) => x.id === selected);
      if (s) {
        renderDetailHead(s);
        if (tab === "overview") renderOverviewStatus(s);
      } else {
        resetSettings(); // it's gone (a failed import, say): nothing to save
        location.hash = "#/";
      }
    }
  } catch (err) {
    if (!signedIn) return;
    $("servers").hidden = false;
    $("servers").replaceChildren(el("p", { class: "error card" }, "Can't reach the panel: " + err.message));
  }
}

// checkUpdates asks which Bedrock release is newest. The panel checks with
// Mojang in the background, so the first answer may be empty: ask again soon.
async function checkUpdates(tries = 6) {
  try {
    const u = await api("/api/updates");
    if (u.latest && u.latest !== latestVersion) { latestVersion = u.latest; refresh(); }
    if (!u.latest && !u.error && tries > 0 && signedIn) setTimeout(() => checkUpdates(tries - 1), 5000);
  } catch (_) { /* try again later */ }
}

// ---- pages ----

let view = "servers";

// route shows the page the address names: #/, #/backups, #/joining,
// #/import, #/add, #/guide, #/account, #/server/<id>/<tab>.
let acceptedHash = "#/"; // the address of the page on screen

// stay puts the address back to the page on screen, after the person chose
// not to leave it (unsaved settings). It doesn't add to the history.
function stay() {
  history.replaceState(null, "", acceptedHash);
}

function route() {
  if (!signedIn) return;
  let parts;
  try {
    parts = location.hash.replace(/^#\/?/, "").split("/").map(decodeURIComponent);
  } catch (_) {
    parts = [];
  }
  let next = parts[0] || "servers";
  if (!["servers", "backups", "joining", "import", "add", "guide", "account", "server"].includes(next)) next = "servers";
  if (next === "server") {
    const id = parts[1];
    const t = ["overview", "players", "settings", "backups", "console"].includes(parts[2]) ? parts[2] : "overview";
    if (!id) { location.hash = "#/"; return; }
    if (id !== selected || view !== "server") {
      if (settingsDirty() && !confirm("Leave without saving your settings changes?")) { stay(); return; }
      closeConsole();
      closeOverviewConsole();
      resetSettings();
      resetBackups();
      clearServerPanes();
      selected = id;
      $("detail-name").textContent = "";
      $("detail-facts").replaceChildren();
      $("detail-actions").replaceChildren();
      const s = serverList.find((x) => x.id === id);
      if (s) renderDetailHead(s);
    }
    const s = serverList.find((x) => x.id === id);
    const tabOK = !(s && s.state === "importing" && ["players", "settings", "backups"].includes(t));
    acceptedHash = location.hash;
    setView("server");
    showTab(tabOK ? t : "overview");
    refresh();
    return;
  }
  if (view === "server") {
    if (settingsDirty() && !confirm("Leave without saving your settings changes?")) { stay(); return; }
    resetSettings();
    resetBackups();
    closeConsole();
    closeOverviewConsole();
    selected = null;
  }
  acceptedHash = location.hash || "#/";
  setView(next);
  if (next === "servers") renderDashboard(serverList, true);
  if (next === "backups") renderBackupsOverview(serverList);
  if (next === "import") loadImports();
  if (next === "add") { $("add-owner").value = settings.ownerGamertag || ""; $("add-name").focus(); }
  if (next === "guide") openGuide(1);
  if (next === "joining") fillJoinInfo();
  refresh();
}

function setView(v) {
  view = v;
  for (const sec of document.querySelectorAll(".view")) sec.hidden = sec.id !== "view-" + v;
  const navKey = v === "server" || v === "add" ? "servers" : v;
  for (const a of document.querySelectorAll("[data-nav]")) {
    if (a.dataset.nav === navKey) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
  }
  window.scrollTo(0, 0);
}

function showTab(which) {
  tab = which;
  for (const t of ["overview", "players", "settings", "backups", "console"]) {
    const a = $("tab-" + t);
    a.href = serverHref(selected, t);
    if (t === which) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
    $("pane-" + t).hidden = t !== which;
  }
  if (which !== "console") closeConsole();
  if (which !== "overview") closeOverviewConsole();
  if (which === "console") openConsole();
  else if (which === "settings") { if (!settingsView || !settingsDirty()) loadSettings(); }
  else if (which === "backups") loadBackups();
  else if (which === "players") loadPlayers();
  else loadOverview();
}

window.addEventListener("hashchange", route);

// Links inside a server page that switch tabs.
for (const a of document.querySelectorAll("[data-goto]")) {
  a.addEventListener("click", (ev) => { ev.preventDefault(); location.hash = serverHref(selected, a.dataset.goto); });
}

// ---- overview tab ----

let ovStream = null;
const ovLines = [];

function closeOverviewConsole() {
  if (ovStream) ovStream.close();
  ovStream = null;
  ovLines.length = 0;
  $("ov-console").replaceChildren();
}

function renderOverviewStatus(s) {
  const key = JSON.stringify([s.id, s.state, s.players]);
  if (key === ovPlayersKey) return;
  ovPlayersKey = key;
  $("ov-playing-title").textContent = s.players.length ? `Playing now · ${s.players.length}` : "Playing now";
  $("ov-online").replaceChildren(...(s.players.length ? s.players.map((p) => row(p.name,
    `since ${new Date(p.since).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}`,
    btn("Message", () => {
      const text = prompt(`Message to ${p.name}:`);
      if (text) post(sid() + "/message", { name: p.name, text }).catch((err) => alert(err.message));
    }),
    btn("Kick", () => {
      const reason = prompt(`Kick ${p.name}? Optional reason:`, "");
      if (reason !== null) post(sid() + "/kick", { name: p.name, reason }).catch((err) => alert(err.message));
    }, "danger"))) : [emptyRow(s.state === "running" ? "Nobody is playing." : "The server isn't running.")]));
  $("ov-command").hidden = s.state !== "running";
}

async function loadOverview() {
  if (!signedIn || view !== "server" || tab !== "overview" || !selected) return;
  const id = selected;
  const s = serverList.find((x) => x.id === id);
  if (s) renderOverviewStatus(s);
  if (!ovStream) {
    ovStream = new EventSource(sid() + "/console");
    ovStream.onmessage = (ev) => {
      ovLines.push(ev.data);
      if (ovLines.length > 9) ovLines.splice(0, ovLines.length - 9);
      $("ov-console").replaceChildren(...ovLines.map((l) => el("span", { class: lineClass(l) }, l + "\n")));
    };
  }
  const [settingsV, players, backups] = await Promise.all([
    api(`/api/servers/${encodeURIComponent(id)}/settings`).catch(() => null),
    api(`/api/servers/${encodeURIComponent(id)}/players`).catch(() => null),
    api(`/api/servers/${encodeURIComponent(id)}/backups`).catch(() => null),
  ]);
  if (id !== selected || tab !== "overview") return;
  // World
  const fields = {};
  for (const g of (settingsV && settingsV.groups) || []) for (const f of g.fields) fields[f.key] = f;
  const shown = (key, label) => {
    const f = fields[key];
    if (!f) return [];
    let v = f.value;
    if (f.options) { const o = f.options.find((x) => x.value === v); if (o) v = o.label; }
    if (f.type === "bool") v = v === "true" ? "On" : "Off";
    return [el("div", {}, el("dt", {}, label), el("dd", {}, v || "(not set)"))];
  };
  $("ov-world").replaceChildren(...shown("gamemode", "Game mode"), ...shown("difficulty", "Difficulty"),
    ...shown("level-name", "World"), ...shown("allow-cheats", "Cheats"));
  // Who can join
  if (!players) $("ov-join").textContent = "Couldn't load the players.";
  if (players) {
    const ops = (players.operators || []).length;
    const allow = (players.allowlist || []).length;
    const wait = (players.attempts || []).length;
    $("ov-join").textContent = (players.allowlistEnabled ? `Allowlist on · ${allow} ${allow === 1 ? "person" : "people"}` : "Allowlist off: anyone who can reach it can join")
      + ` · ${ops} operator${ops === 1 ? "" : "s"}. ` + (wait ? `${wait} waiting to be let in.` : "Nobody waiting to be let in.");
  }
  // Backups
  if (!backups) $("ov-backups").textContent = "Couldn't load the backups.";
  if (backups) {
    const last = (backups.backups || [])[0];
    const p = backups.plan;
    const plan = p.every === "off" ? "Automatic backups are off." : (p.every === "daily" ? `Every day at ${p.at} · keeps ${p.keep}.` : `Every ${p.every.replace("h", " hours").replace(/^1 hours$/, "hour")} · keeps ${p.keep}.`);
    $("ov-backups").textContent = (backups.running ? "Backing up now… " : (last ? `Last: ${whenShort(last.time).toLowerCase()}. ` : "No backups yet. ")) + plan;
    $("ov-backup-now").disabled = backups.running;
  }
}

$("ov-command").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const input = $("ov-command-input");
  const command = input.value.trim();
  if (!command) return;
  try { await post(sid() + "/command", { command }); input.value = ""; } catch (err) { alert(err.message); }
});
for (const b of document.querySelectorAll("[data-cmd]")) {
  b.addEventListener("click", async () => {
    try { await post(sid() + "/command", { command: b.dataset.cmd }); } catch (err) { alert(err.message); }
  });
}
$("ov-backup-now").addEventListener("click", async (ev) => {
  ev.currentTarget.disabled = true; // before any await: currentTarget is gone after
  try { await post(sid() + "/backups"); } catch (err) { alert(err.message); }
  setTimeout(loadOverview, 1500);
});

// ---- players tab ----

function showPlayersError(err) {
  $("players-error").textContent = err ? err.message : "";
  $("players-error").hidden = !err;
}

// playersCall runs a Players tab request and shows the answer, unless the
// page has moved on to another server meanwhile.
async function playersCall(fn) {
  const id = selected;
  try {
    const v = await fn();
    if (id !== selected) return;
    showPlayersError(null);
    if (v) renderPlayers(v);
  } catch (err) {
    if (id === selected) showPlayersError(err);
  }
}

// clearServerPanes empties what the Players and Overview tabs show, so
// nothing from one server is left on another's page.
function clearServerPanes() {
  for (const id of ["online", "allowlist", "operators", "attempts", "ov-online", "ov-world"]) $(id).replaceChildren();
  $("attempts-box").hidden = true;
  showPlayersError(null);
  for (const id of ["ov-join", "ov-backups"]) $(id).textContent = "…";
  detailKey = "";
  ovPlayersKey = "";
}

async function loadPlayers() {
  if (!selected) return;
  await playersCall(() => api(sid() + "/players"));
}

function row(name, detail, ...buttons) {
  return el("li", {}, el("span", { class: "who" }, name, detail ? el("small", {}, detail) : null), ...buttons);
}

function btn(text, onclick, cls, disabled) {
  return el("button", { class: cls || "", onclick, disabled: !!disabled }, text);
}

function emptyRow(text) {
  return el("li", {}, el("span", { class: "empty" }, text));
}

function ago(iso) {
  const s = Math.round((Date.now() - new Date(iso)) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  return `${Math.round(s / 3600)} h ago`;
}

function renderPlayers(v) {
  for (const k of ["online", "allowlist", "operators", "attempts"]) v[k] = v[k] || [];
  const ops = new Set(v.operators.filter((o) => !o.pending).map((o) => o.name.toLowerCase()));
  const listed = new Set(v.allowlist.map((a) => a.name.toLowerCase()));
  const enc = encodeURIComponent;

  // Tried to join
  $("attempts-box").hidden = v.attempts.length === 0;
  $("attempts").replaceChildren(...v.attempts.map((a) => row(a.name,
    `${ago(a.at)}${a.verified ? "" : " · Xbox sign-in not checked (PlayStation or Switch)"}`,
    btn("Allow", () => playersCall(() => post(sid() + "/allowlist", { name: a.name })), "primary"),
    btn("Dismiss", () => playersCall(() => del(sid() + "/attempts/" + enc(a.name)))))));

  // Online
  $("online").replaceChildren(...(v.online.length ? v.online.map((p) => row(p.name,
    `playing since ${new Date(p.since).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}`,
    ops.has(p.name.toLowerCase()) ? el("span", { class: "badge" }, "operator")
      : btn("Make operator", () => playersCall(() => post(sid() + "/operators", { name: p.name }))),
    btn("Message", () => {
      const text = prompt(`Message to ${p.name}:`);
      if (text) playersCall(() => post(sid() + "/message", { name: p.name, text }));
    }),
    btn("Kick", () => {
      const reason = prompt(`Kick ${p.name}? Optional reason:`, "");
      if (reason !== null) playersCall(() => post(sid() + "/kick", { name: p.name, reason }));
    }, "danger"),
  )) : [emptyRow(v.running ? "Nobody is playing." : "The server isn't running.")]));
  $("say-form").hidden = !v.running;

  // Allowlist
  $("allow-enabled").checked = v.allowlistEnabled;
  $("allow-enabled-label").textContent = v.allowlistEnabled ? "On" : "Off";
  $("allow-help").textContent = v.allowlistEnabled
    ? "Only these players can join."
    : "Off: anyone who can reach the server can join. The list is kept for when you turn it back on.";
  $("allowlist").replaceChildren(...(v.allowlist.length ? v.allowlist.map((a) => row(a.name,
    a.xuid ? "" : "hasn't joined yet",
    ops.has(a.name.toLowerCase()) ? el("span", { class: "badge" }, "operator") : null,
    btn("Remove", () => {
      if (confirm(`Remove ${a.name} from the allowlist?`)) playersCall(() => del(sid() + "/allowlist/" + enc(a.name)));
    }, "danger"),
  )) : [emptyRow("Nobody yet.")]));

  // Operators
  $("operators").replaceChildren(...(v.operators.length ? v.operators.map((o) => row(o.name,
    o.pending ? "becomes operator on their first join" : (listed.has(o.name.toLowerCase()) || !v.allowlistEnabled ? "" : "not on the allowlist"),
    btn("Remove", () => playersCall(() => del(sid() + "/operators/" + enc(o.pending ? o.name : (o.xuid || o.name))))),
  )) : [emptyRow("No operators.")]));
}

$("allow-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const name = $("allow-name").value.trim();
  if (!name) return;
  playersCall(async () => { const v = await post(sid() + "/allowlist", { name }); $("allow-name").value = ""; return v; });
});
$("op-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const name = $("op-name").value.trim();
  if (!name) return;
  playersCall(async () => { const v = await post(sid() + "/operators", { name }); $("op-name").value = ""; return v; });
});
$("say-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const text = $("say-text").value.trim();
  if (!text) return;
  playersCall(async () => { const v = await post(sid() + "/message", { text }); $("say-text").value = ""; return v; });
});
$("allow-enabled").addEventListener("change", (ev) => {
  const enabled = ev.target.checked;
  if (!enabled && !confirm("Turn the allowlist off? Anyone who can reach this server will be able to join.")) {
    ev.target.checked = true;
    return;
  }
  playersCall(() => post(sid() + "/allowlist-enabled", { enabled }));
});

// ---- settings tab ----

let settingsView = null; // last loaded settings
let settingsFor = null;  // server ID settingsView and edits belong to
let rawFor = null;       // server ID the raw editor text belongs to
let savedFor = null;     // server ID the "Restart now" notice belongs to
const edits = {};        // key -> new value, for unsaved changes

function settingsDirty() { return Object.keys(edits).length > 0; }

// resetSettings forgets everything the Settings tab holds, so nothing from
// one server can be saved to another.
function resetSettings() {
  for (const k of Object.keys(edits)) delete edits[k];
  settingsView = settingsFor = rawFor = savedFor = null;
  $("settings-groups").replaceChildren();
  $("settings-bar").hidden = true;
  $("settings-saved").hidden = true;
  $("raw-box").open = false;
  $("raw-text").value = "";
  showSettingsError(null);
}

function showSettingsError(err) {
  $("settings-error").textContent = err ? err.message : "";
  $("settings-error").hidden = !err;
}

async function loadSettings() {
  if (!selected) return;
  const id = selected;
  try {
    const v = await api(sid() + "/settings");
    if (id !== selected) return; // switched servers while loading
    settingsView = v;
    settingsFor = id;
    for (const k of Object.keys(edits)) delete edits[k];
    $("rename-name").value = settingsView.name;
    renderSettings();
    showSettingsError(null);
  } catch (err) { showSettingsError(err); }
}

function current(f) { return f.key in edits ? edits[f.key] : f.value; }

function setEdit(f, value, redraw = true) {
  if (value === f.value) delete edits[f.key]; else edits[f.key] = value;
  if (redraw) renderSettings(); else updateSaveBar();
}

function updateSaveBar() {
  const n = Object.keys(edits).length;
  $("settings-bar").hidden = n === 0;
  $("settings-dirty").textContent = `${n} unsaved change${n === 1 ? "" : "s"}`;
}

function control(f) {
  const val = current(f);
  switch (f.type) {
    case "select": {
      const options = f.options.map((o) => el("option", { value: o.value, selected: o.value === val }, o.label));
      if (!f.options.some((o) => o.value === val)) {
        // The file has a value that isn't one of the usual choices: show it
        // as it is rather than pretending it's the first choice.
        options.unshift(el("option", { value: val, selected: true }, `${val || "(not set)"} (as in the file)`));
      }
      return el("select", { "aria-label": f.label, onchange: (e) => setEdit(f, e.target.value) }, ...options);
    }
    case "bool": {
      const odd = val !== "true" && val !== "false";
      return el("label", { class: "switch" },
        el("input", { type: "checkbox", checked: val.toLowerCase() === "true", onchange: (e) => setEdit(f, e.target.checked ? "true" : "false") }),
        el("span", {}, odd ? `${val || "(not set)"} (as in the file)` : (val === "true" ? "On" : "Off")));
    }
    case "int":
      return el("input", { type: "number", min: f.min, max: f.max, value: val, "aria-label": f.label,
        oninput: (e) => setEdit(f, String(e.target.value).trim(), false),
        onchange: (e) => setEdit(f, String(e.target.value).trim()) });
    case "text":
      return el("input", { type: "text", maxlength: f.max || 64, value: val, "aria-label": f.label,
        oninput: (e) => setEdit(f, e.target.value, false),
        onchange: (e) => setEdit(f, e.target.value) });
    default: {
      let shown = val || "(not set)";
      if (f.key === "allow-list") shown = val === "true" ? "On" : "Off";
      return el("span", { class: "ro" }, shown);
    }
  }
}

function renderSettings() {
  const v = settingsView;
  if (!v) return;
  $("settings-groups").replaceChildren(...v.groups.map((g) => el("div", { class: "card" },
    el("h3", {}, g.title),
    ...g.fields.map((f) => {
      const dirty = f.key in edits;
      const warn = dirty && f.warning && (f.warnWhen === "*" || f.warnWhen === edits[f.key]);
      return el("div", { class: "field" + (dirty ? " dirty" : "") },
        el("div", { class: "top" }, el("span", { class: "label" }, f.label), control(f)),
        f.help ? el("div", { class: "help" }, f.help) : null,
        f.link === "players" ? el("div", { class: "help" }, el("a", { href: serverHref(selected, "players") }, "Open the Players tab")) : null,
        dirty ? el("div", { class: "changed" }, `Changed from ${f.value || "(not set)"}`) : null,
        warn ? el("div", { class: "warn" }, "⚠ " + f.warning) : null);
    }))));
  updateSaveBar();
}

$("settings-discard").addEventListener("click", () => {
  for (const k of Object.keys(edits)) delete edits[k];
  renderSettings();
});

$("settings-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  if (!settingsDirty()) return;
  if (settingsFor !== selected) { resetSettings(); loadSettings(); return; }
  const id = settingsFor;
  try {
    const res = await post(`/api/servers/${encodeURIComponent(id)}/settings`, { values: { ...edits } });
    if (id !== selected) return;
    savedFor = id;
    settingsView = res.settings;
    for (const k of Object.keys(edits)) delete edits[k];
    renderSettings();
    showSettingsError(null);
    const n = res.changes.length;
    const running = settingsView.running;
    $("settings-saved-text").textContent = n === 0 ? "Nothing changed."
      : `Saved ${n} setting${n === 1 ? "" : "s"}.` + (running ? " Restart the server to apply them. Players will be disconnected briefly." : " They'll apply when the server starts.");
    $("settings-restart").hidden = !(running && n > 0);
    $("settings-saved").hidden = false;
    $("settings-saved").scrollIntoView({ behavior: "smooth", block: "nearest" });
  } catch (err) { showSettingsError(err); }
});

$("settings-saved-close").addEventListener("click", () => { $("settings-saved").hidden = true; });
$("settings-restart").addEventListener("click", async () => {
  $("settings-saved").hidden = true;
  if (!savedFor || savedFor !== selected) return;
  try { await post(`/api/servers/${encodeURIComponent(savedFor)}/restart`); } catch (err) { showSettingsError(err); }
  refresh();
});

$("rename-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await post(sid() + "/rename", { name: $("rename-name").value });
    showSettingsError(null);
    refresh();
  } catch (err) { showSettingsError(err); }
});

async function loadRaw() {
  const id = selected;
  try {
    const res = await api(sid() + "/properties");
    if (id !== selected) return;
    $("raw-text").value = res.text;
    rawFor = id;
  } catch (err) { showSettingsError(err); }
}
$("raw-box").addEventListener("toggle", () => { if ($("raw-box").open) loadRaw(); });
$("raw-reload").addEventListener("click", loadRaw);
$("raw-save").addEventListener("click", async () => {
  if (!rawFor || rawFor !== selected) { loadRaw(); return; } // text belongs to another server
  if (settingsDirty() && !confirm("You have unsaved changes above. Saving the file discards them. Continue?")) return;
  const id = rawFor;
  try {
    const res = await api(`/api/servers/${encodeURIComponent(id)}/properties`, { method: "PUT", body: JSON.stringify({ text: $("raw-text").value }) });
    if (id !== selected) return;
    $("raw-text").value = res.text;
    await loadSettings();
    savedFor = id;
    const running = settingsView && settingsView.running;
    $("settings-saved-text").textContent = "server.properties saved." + (running ? " Restart the server to apply it." : "");
    $("settings-restart").hidden = !running;
    $("settings-saved").hidden = false;
  } catch (err) { showSettingsError(err); }
});

window.addEventListener("beforeunload", (ev) => { if (settingsDirty()) { ev.preventDefault(); ev.returnValue = ""; } });

// ---- console tab ----

function lineClass(line) {
  if (line.startsWith("[Panel]")) return "panel";
  if (line.startsWith("> ")) return "cmd";
  if (/\bERROR\b/.test(line)) return "err";
  if (/\bWARN\b/.test(line)) return "warn";
  return "";
}

function appendLine(text) {
  const log = $("console-log");
  const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  log.append(el("span", { class: lineClass(text) }, text + "\n"));
  while (log.childNodes.length > 2000) log.firstChild.remove();
  if (atBottom) log.scrollTop = log.scrollHeight;
}

function openConsole() {
  if (consoleStream) return;
  $("console-log").replaceChildren();
  $("console-error").hidden = true;
  consoleStream = new EventSource(sid() + "/console");
  consoleStream.onmessage = (ev) => appendLine(ev.data);
  $("console-input").focus();
}

function closeConsole() {
  if (consoleStream) consoleStream.close();
  consoleStream = null;
}

$("console-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const input = $("console-input");
  const command = input.value.trim();
  if (!command || !selected) return;
  try {
    await post(sid() + "/command", { command });
    input.value = "";
    $("console-error").hidden = true;
  } catch (err) {
    $("console-error").textContent = err.message;
    $("console-error").hidden = false;
  }
});

// ---- add server ----

$("add-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  $("add-error").hidden = true;
  try {
    const s = await post("/api/servers", {
      name: $("add-name").value,
      ownerGamertag: $("add-owner").value.trim(),
      acceptEula: $("add-eula").checked,
    });
    settings.ownerGamertag = $("add-owner").value.trim();
    $("add-form").reset();
    await refresh();
    location.hash = serverHref(s.id, "console");
  } catch (err) {
    $("add-error").textContent = err.message;
    $("add-error").hidden = false;
  }
});

// ---- backups tab ----

let backupsFor = null; // server ID the plan form belongs to
let planDirty = false;
const kindLabels = {
  scheduled: "Automatic", manual: "Made by hand",
  "before-update": "Before an update", "before-restore": "Before a restore",
};

function resetBackups() {
  backupsFor = null;
  planDirty = false;
  // Nothing from another server can be saved here until this one's plan loads.
  for (const id of ["plan-every", "plan-at", "plan-keep"]) $(id).disabled = true;
  $("backups").replaceChildren();
  $("backups-summary").textContent = "";
  $("plan-save").disabled = true;
  showBackupsError(null);
}

function showBackupsError(err) {
  $("backups-error").textContent = err ? err.message : "";
  $("backups-error").hidden = !err;
}

function size(bytes) {
  if (bytes >= 1e9) return (bytes / 1e9).toFixed(1) + " GB";
  if (bytes >= 1e6) return (bytes / 1e6).toFixed(1) + " MB";
  return Math.max(1, Math.round(bytes / 1e3)) + " KB";
}

async function loadBackups() {
  if (!selected) return;
  const id = selected;
  try {
    const v = await api(sid() + "/backups");
    if (id !== selected) return;
    renderBackups(v, id);
    showBackupsError(null);
  } catch (err) { showBackupsError(err); }
}

async function backupsCall(fn) {
  const id = selected;
  try {
    const v = await fn();
    if (id !== selected) return;
    showBackupsError(null);
    if (v && v.backups) renderBackups(v, id);
    else loadBackups();
  } catch (err) { showBackupsError(err); }
  refresh();
}

function renderBackups(v, id) {
  if (backupsFor !== id || !planDirty) {
    $("plan-every").value = v.plan.every;
    $("plan-at").value = v.plan.at || "04:00";
    $("plan-keep").value = v.plan.keep;
    planDirty = false;
    $("plan-save").disabled = true;
  }
  backupsFor = id;
  for (const x of ["plan-every", "plan-at"]) $(x).disabled = false;
  updatePlanForm();
  const utc = /^(UTC|GMT|Etc\/UTC)$/.test(v.timeZone);
  $("plan-help").replaceChildren(
    "A server nobody has played on since its last backup is skipped, so the kept backups aren't all the same. Backups you make by hand stay until you delete them; the panel also keeps the last 5 from before updates and 3 from before restores. ",
    `Times use the panel's clock: ${v.timeZone}, now ${v.now}.`,
    ...(utc ? [el("span", { class: "warn" }, " To use your own time zone, add -e TZ=America/Denver (or yours) to the docker run command.")] : []));
  $("backups-last-error").textContent = v.lastError || "";
  $("backups-last-error").hidden = !v.lastError;
  $("backup-now").disabled = v.running;
  const list = v.backups || [];
  $("backups-summary").textContent = v.running
    ? "Working on it…"
    : (list.length ? `${list.length} backup${list.length === 1 ? "" : "s"}, ${size(v.total)} in all. Restoring backs up the current world first, so you can undo it.` : "");
  const enc = encodeURIComponent;
  $("backups").replaceChildren(...(list.length ? list.map((b) => {
    const when = new Date(b.time).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
    const detail = `${kindLabels[b.kind] || b.kind} · ${size(b.size)}${b.full ? " · includes the server version from before the update" : ""}`;
    return row(when, detail,
      el("a", { class: "button", href: `${sid()}/backups/${enc(b.name)}`, download: b.name }, "Download"),
      btn("Restore", () => {
        const extra = b.full ? " This also puts back the server version from before that update (press Update to go forward again)." : "";
        if (confirm(`Put the world back as it was on ${when}?\n\nThe server stops, the current world is backed up first (so you can undo this), and it starts again if it was running.${extra}`)) {
          backupsCall(() => post(`${sid()}/backups/${enc(b.name)}/restore`));
        }
      }, "", v.running),
      btn("Delete", () => {
        if (confirm(`Delete the backup from ${when}? This can't be undone.`)) {
          backupsCall(() => del(`${sid()}/backups/${enc(b.name)}`));
        }
      }, "danger", v.running));
  }) : [emptyRow(v.running ? "Making the first backup…" : "No backups yet.")]));
}

function updatePlanForm() {
  const every = $("plan-every").value;
  $("plan-at-label").hidden = every !== "daily";
  $("plan-keep").disabled = every === "off";
}

for (const id of ["plan-every", "plan-at", "plan-keep"]) {
  $(id).addEventListener("input", () => { planDirty = true; $("plan-save").disabled = false; updatePlanForm(); });
}
$("plan-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  if (backupsFor !== selected) return;
  const plan = { every: $("plan-every").value, at: $("plan-at").value, keep: Number($("plan-keep").value) || 7 };
  const id = selected;
  backupsCall(async () => {
    const v = await api(sid() + "/backups/plan", { method: "PUT", body: JSON.stringify(plan) });
    if (id === selected) {
      planDirty = false;
      backupsFor = null; // take the saved plan
    }
    return v;
  });
});
$("backup-now").addEventListener("click", () => backupsCall(() => post(sid() + "/backups")));

// ---- import ----

const importDraft = {}; // path -> {name, port, start}, kept while the list reloads
let importing = new Set(); // IDs of servers being copied

function mb(bytes) {
  if (!bytes) return "no world files";
  if (bytes >= 1e9) return (bytes / 1e9).toFixed(1) + " GB";
  return Math.max(1, Math.round(bytes / 1e6)) + " MB";
}

// importsFinished reloads the import screen when a copy ends, so a failure
// shows up there (a failed import leaves no server card behind).
function importsFinished(list) {
  const now = new Set(list.filter((s) => s.state === "importing").map((s) => s.id));
  const ended = [...importing].filter((id) => !now.has(id));
  importing = now;
  if (!ended.length) return;
  // A failed import leaves no card, so open the import screen to show why.
  const failed = ended.some((id) => !list.some((s) => s.id === id));
  if (failed && view !== "import") location.hash = "#/import";
  else if (view === "import") loadImports();
}

async function loadImports() {
  try {
    const v = await api("/api/import");
    renderImports(v || {});
  } catch (err) {
    showImportError(err);
  }
}

function showImportError(err) {
  $("import-error").textContent = err ? err.message : "";
  $("import-error").hidden = !err;
}

function renderImports(v) {
  $("import-missing").hidden = !!v.available;
  $("import-ready").hidden = !v.available;
  if (!v.available) {
    $("import-missing").replaceChildren(
      "Nothing to import yet. Mount the other panel's servers folder read-only at /import and restart the container. For Crafty on Unraid, add this to the docker run command:",
      el("code", { class: "block" }, "-v /mnt/user/appdata/binhex-crafty-4/crafty/servers:/import/crafty:ro"));
    return;
  }
  showImportError(v.message ? new Error(v.message) : null);
  const done = new Set((v.candidates || []).filter((c) => c.imported).map((c) => c.path));
  const failed = (v.results || []).filter((r) => !r.ok && !done.has(r.path));
  $("import-results").replaceChildren(...failed.slice(0, 5).map((r) =>
    el("li", { class: "failed" }, el("span", { class: "who" }, `${r.name}: import failed`, el("small", {}, r.error)))));
  const list = v.candidates || [];
  if (!list.length) {
    $("import-list").replaceChildren(emptyRow("No Bedrock servers found in the import folder."));
    return;
  }
  $("import-list").replaceChildren(...list.map(importRow));
}

function importRow(c) {
  const facts = `${c.path} · world ${c.world || "?"} · ${mb(c.worlds)} · port ${c.port || "?"}`;
  if (c.imported) {
    return el("li", {}, el("span", { class: "who" }, c.name, el("small", {}, facts)),
      el("span", { class: "badge" }, `Imported as ${c.importedAs}`));
  }
  const d = importDraft[c.path] || (importDraft[c.path] = {
    name: c.name.slice(0, 40), port: c.portAvailable ? String(c.port) : "", start: false,
  });
  const name = el("input", { maxlength: "40", value: d.name, "aria-label": "Name in the panel", autocomplete: "off" });
  name.addEventListener("input", () => { d.name = name.value; });
  const port = el("input", { type: "number", min: "19134", max: "19198", step: "2", value: d.port, placeholder: "auto", "aria-label": "Port", class: "port" });
  port.addEventListener("input", () => { d.port = port.value; });
  const start = el("input", { type: "checkbox" });
  start.checked = d.start;
  start.addEventListener("change", () => { d.start = start.checked; });
  const go = el("button", { class: "primary", type: "button" }, "Import");
  go.addEventListener("click", async () => {
    if (!$("import-stopped").checked) {
      showImportError(new Error("Stop the server in the other panel first, then tick the box above."));
      return;
    }
    go.disabled = true;
    try {
      const s = await post("/api/import", {
        path: c.path, name: d.name.trim(), port: d.port ? Number(d.port) : 0, start: d.start,
      });
      delete importDraft[c.path];
      showImportError(null);
      importing.add(s.id);
      await loadImports();
      await refresh();
    } catch (err) {
      showImportError(err);
      go.disabled = false;
    }
  });
  const note = c.port && !c.portAvailable
    ? el("small", { class: "warn" }, `Port ${c.port} can't be kept (it's taken or outside ${19134}–${19198}). Leave the port empty to pick a free one.`)
    : null;
  return el("li", { class: "import" },
    el("span", { class: "who" }, c.name, el("small", {}, facts), note),
    el("div", { class: "row" },
      el("label", { class: "inline" }, "Name", name),
      el("label", { class: "inline" }, "Port", port),
      el("label", { class: "check inline" }, start, el("span", {}, "Start when copied")),
      go));
}


// ---- signing in ----

let signedIn = false;
let timers = [];

function showOnly(formId) {
  for (const id of ["setup-form", "login-form", "reset-form"]) $(id).hidden = id !== formId;
  const first = $(formId).querySelector("input");
  if (first) first.focus();
}

function formError(id, err) {
  $(id).textContent = err ? err.message : "";
  $(id).hidden = !err;
}

async function start() {
  let st;
  try {
    st = await api("/api/auth/state");
  } catch (err) {
    // Try again shortly (the container may be restarting).
    $("auth-view").hidden = false;
    for (const id of ["setup-form", "login-form", "reset-form"]) $(id).hidden = true;
    let note = $("auth-offline");
    if (!note) {
      note = el("p", { id: "auth-offline", class: "card error" });
      $("auth-view").prepend(note);
    }
    note.textContent = "Can't reach the panel (" + err.message + "). Trying again…";
    note.hidden = false;
    setTimeout(start, 3000);
    return;
  }
  if ($("auth-offline")) $("auth-offline").hidden = true;
  $("version").textContent = st.version ? "Version " + st.version : "";
  $("version-2").textContent = st.version ? "Blockheads Control Panel " + st.version : "";
  if (st.mode === "signedIn") {
    enterApp(st);
    return;
  }
  $("auth-view").hidden = false;
  $("app-view").hidden = true;
  if (st.mode === "setup") {
    $("setup-code-help").replaceChildren(st.usesOldPassword
      ? (st.oldPasswordFromEnv
        ? "Type the panel password you used before (the PANEL_PASSWORD setting). After this, it isn't used any more; you can remove it from the docker run command."
        : "Type the panel password you used before (it's in /data/panel-password). After this, it isn't used any more.")
      : "It's in the container log. On the Unraid server, run: docker logs blockheads 2>&1 | grep -i \"setup code\"");
    showOnly("setup-form");
  } else {
    showOnly("login-form");
  }
}

function enterApp(st) {
  signedIn = true;
  $("auth-view").hidden = true;
  $("app-view").hidden = false;
  $("account-name").textContent = st.username;
  $("side-user").textContent = st.username;
  $("side-avatar").textContent = (st.username || "?").slice(0, 1).toUpperCase();
  $("tab-user").textContent = st.username;
  $("reset-user").value = st.username;
  applyPrefs(st.prefs || {}, false);
  api("/api/settings").then((s) => { settings = s || {}; }).catch(() => {});
  loadJoinInfo();
  checkUpdates();
  timers.forEach(clearInterval);
  const onServer = (t) => signedIn && view === "server" && selected && tab === t && !document.hidden;
  timers = [
    setInterval(refresh, 2000),
    setInterval(() => { if (onServer("players")) loadPlayers(); }, 3000),
    setInterval(() => { if (onServer("backups")) loadBackups(); }, 3000),
    setInterval(() => { if (onServer("overview")) loadOverview(); }, 10000),
    setInterval(checkUpdates, 10 * 60 * 1000),
  ];
  if (!st.setupDone && !location.hash.startsWith("#/guide")) location.hash = "#/guide";
  else route();
}

// ---- console list info (Joining page, sidebar, dashboard) ----

async function loadJoinInfo() {
  try {
    joinInfo = await api("/api/setup-guide");
  } catch (_) { return; }
  fillJoinInfo();
}

function fillJoinInfo() {
  if (!joinInfo) return;
  const name = joinInfo.listName || "Server List";
  const ip = joinInfo.listIP || "the panel's IP";
  for (const id of ["side-list-name", "dash-list-name", "join-list-name"]) $(id).textContent = name;
  for (const id of ["side-list-ip", "dash-list-ip", "join-list-ip"]) $(id).textContent = ip;
}

// ---- appearance ----

let prefs = { style: "control", mode: "auto" };

// applyPrefs sets the look, and remembers it in this browser so the next
// visit starts with it before the account's copy loads.
function applyPrefs(p, save) {
  prefs = { style: p.style === "treehouse" ? "treehouse" : "control", mode: ["light", "dark", "auto"].includes(p.mode) ? p.mode : "auto" };
  // Treehouse isn't built yet: show Control Room until it is.
  document.documentElement.dataset.style = "control";
  document.documentElement.dataset.mode = prefs.mode;
  try { localStorage.setItem("bh-prefs", JSON.stringify(prefs)); } catch (_) { /* private window */ }
  for (const r of document.querySelectorAll("input[name=style]")) r.checked = r.value === prefs.style || (prefs.style === "treehouse" && r.value === "control");
  for (const r of document.querySelectorAll("input[name=mode]")) r.checked = r.value === prefs.mode;
  if (save) {
    api("/api/auth/prefs", { method: "PUT", body: JSON.stringify(prefs) })
      .then(() => { $("prefs-error").hidden = true; })
      .catch((err) => { $("prefs-error").textContent = "Couldn't save: " + err.message; $("prefs-error").hidden = false; });
  }
}
for (const r of document.querySelectorAll("input[name=style], input[name=mode]")) {
  r.addEventListener("change", () => {
    const style = document.querySelector("input[name=style]:checked");
    const mode = document.querySelector("input[name=mode]:checked");
    applyPrefs({ style: style ? style.value : "control", mode: mode ? mode.value : "auto" }, true);
  });
}

// signedOut goes back to the sign-in page and forgets what was on screen.
function signedOut() {
  if (!signedIn) return;
  signedIn = false;
  timers.forEach(clearInterval);
  timers = [];
  closeConsole();
  closeOverviewConsole();
  resetSettings();
  resetBackups();
  selected = null;
  settings = {};
  guideInfo = null;
  joinInfo = null;
  serverList = [];
  gamertagWarned = false;
  view = "servers";
  acceptedHash = "#/";
  clearServerPanes();
  dashKey = "";
  latestVersion = "";
  $("update-banner").hidden = true;
  $("dash-attempts").hidden = true;
  $("dash-backups").textContent = "";
  $("servers-summary").textContent = "";
  $("detail-name").textContent = "";
  $("detail-facts").replaceChildren();
  $("detail-actions").replaceChildren();
  $("password-msg").hidden = true;
  for (const f of ["password-form", "add-form"]) $(f).reset();
  for (const id of ["servers", "online", "allowlist", "operators", "attempts", "console-log", "import-list", "import-results", "guide-checklist", "ov-online", "dash-attempts-list", "backups-overview"]) $(id).replaceChildren();
  start();
}

$("setup-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  if ($("setup-pass").value !== $("setup-pass2").value) {
    formError("setup-error", new Error("The two passwords aren't the same."));
    return;
  }
  try {
    await post("/api/auth/setup", { code: $("setup-code").value, username: $("setup-user").value, password: $("setup-pass").value });
    $("setup-form").reset();
    formError("setup-error", null);
    start();
  } catch (err) { formError("setup-error", err); }
});

$("login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await post("/api/auth/login", { username: $("login-user").value, password: $("login-pass").value });
    $("login-pass").value = "";
    formError("login-error", null);
    start();
  } catch (err) {
    $("login-pass").value = "";
    formError("login-error", err);
  }
});

$("forgot-link").addEventListener("click", (ev) => { ev.preventDefault(); showOnly("reset-form"); });
$("reset-back").addEventListener("click", () => showOnly("login-form"));
$("reset-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  if ($("reset-pass").value !== $("reset-pass2").value) {
    formError("reset-error", new Error("The two passwords aren't the same."));
    return;
  }
  try {
    await post("/api/auth/reset", { code: $("reset-code").value, password: $("reset-pass").value });
    $("reset-form").reset();
    formError("reset-error", null);
    start();
  } catch (err) { formError("reset-error", err); }
});

// ---- account ----

$("logout").addEventListener("click", async () => {
  try { await post("/api/auth/logout"); } catch (_) { /* signed out anyway */ }
  signedOut();
});
$("logout-all").addEventListener("click", async () => {
  if (!confirm("Sign out on every browser and phone, including this one?")) return;
  try { await post("/api/auth/logout-all"); } catch (_) { /* signed out anyway */ }
  signedOut();
});
$("password-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const msg = $("password-msg");
  msg.hidden = false;
  if ($("pw-new").value !== $("pw-new2").value) {
    msg.className = "small error";
    msg.textContent = "The two new passwords aren't the same.";
    return;
  }
  try {
    await post("/api/auth/password", { current: $("pw-current").value, new: $("pw-new").value });
    $("password-form").reset();
    msg.className = "small ok";
    msg.textContent = "Password changed. Other browsers were signed out.";
  } catch (err) {
    msg.className = "small error";
    msg.textContent = err.message;
  }
});

// ---- setup guide ----

let guideStep = 1;
let guideInfo = null;
let gamertagWarned = false;

async function openGuide(step) {
  $("guide").hidden = false;
  try {
    guideInfo = await api("/api/setup-guide");
  } catch (err) { return; }
  $("guide-gamertag").value = guideInfo.ownerGamertag || settings.ownerGamertag || "";
  $("guide-list-name").textContent = guideInfo.listName || "Server List";
  $("guide-list-ip").textContent = guideInfo.listIP || "the panel's IP";
  const n = guideInfo.servers;
  $("guide-everywhere-label").hidden = n === 0;
  $("guide-everywhere-text").textContent = `Also add me to the allowlist and make me an operator on the ${n} server${n === 1 ? "" : "s"} I already have`;
  $("guide-gamertag-msg").hidden = true;
  showGuideStep(step);
}

function showGuideStep(step) {
  guideStep = step;
  for (const p of document.querySelectorAll(".guide-pane")) p.hidden = Number(p.dataset.step) !== step;
  for (const li of document.querySelectorAll("#guide-steps li")) {
    const n = Number(li.dataset.step);
    li.className = n === step ? "current" : (n < step ? "done" : "");
  }
  $("guide-step-label").textContent = `step ${step} of 4`;
  $("guide-back").disabled = step === 1;
  $("guide-next").textContent = step === 4 ? "Finish" : (step === 1 ? "Save and continue" : "Next");
  if (step === 3) renderGuideServers();
  if (step === 4) renderChecklist();
}

function renderGuideServers() {
  const g = guideInfo;
  const parts = [];
  parts.push(g.servers === 0 ? "You don't have any servers yet." : `You have ${g.servers} server${g.servers === 1 ? "" : "s"} in the panel.`);
  if (g.importWaiting > 0) parts.push(` ${g.importWaiting} server${g.importWaiting === 1 ? " is" : "s are"} in the import folder and not imported yet.`);
  else if (!g.importMounted && g.servers === 0) parts.push(" To bring servers over from Crafty, mount its servers folder at /import (see the README), or add a new one.");
  $("guide-servers-text").textContent = parts.join("");
  $("guide-import").hidden = !(g.importMounted && g.importWaiting > 0);
  $("guide-add").hidden = g.servers > 0 && g.importWaiting === 0;
}

function checkItem(ok, text) {
  return el("li", { class: ok ? "ok" : "todo" }, el("span", { class: "mark", "aria-hidden": "true" }, ok ? "✓" : "!"), el("span", {}, text));
}

async function renderChecklist() {
  try { guideInfo = await api("/api/setup-guide"); } catch (_) { /* keep the last one */ }
  const g = guideInfo;
  const items = [checkItem(true, "Owner account created.")];
  items.push(g.ownerGamertag
    ? checkItem(true, `Your gamertag is ${g.ownerGamertag}.`)
    : checkItem(false, "Add your gamertag in step 1 so you're never locked out of your own servers."));
  items.push(g.timeZoneIsUTC
    ? checkItem(false, "The panel's clock is on UTC, so a 4 am backup would run at the wrong time. Add -e TZ=America/Denver (or your time zone) to the docker run command.")
    : checkItem(true, `Time zone: ${g.timeZone}.`));
  if (g.backupsOff.length) items.push(checkItem(false, `Automatic backups are off for: ${g.backupsOff.join(", ")}.`));
  if (g.neverBackedUp.length) items.push(checkItem(false, `Not backed up yet: ${g.neverBackedUp.join(", ")}. The first automatic backup runs within a minute or two; or press Back up now on its Backups tab.`));
  if (!g.backupsOff.length && !g.neverBackedUp.length && g.servers > 0) items.push(checkItem(true, "Every server has a backup and a schedule."));
  if (g.importMounted && g.importWaiting === 0 && g.servers > 0) {
    items.push(checkItem(true, "Everything in the import folder is imported. Once you're happy, stop the servers in Crafty for good and remove the /import line from the docker run command."));
  } else if (g.importWaiting > 0) {
    items.push(checkItem(false, `${g.importWaiting} server${g.importWaiting === 1 ? " is" : "s are"} waiting in the import folder.`));
  }
  items.push(checkItem(true, `Bookmark ${location.origin} on your phone; the panel works there too.`));
  $("guide-checklist").replaceChildren(...items);
}

async function finishGuide() {
  try { await post("/api/setup-guide/done", { done: true }); } catch (_) { /* shown again next time */ }
  location.hash = "#/";
}

$("guide-back").addEventListener("click", () => showGuideStep(Math.max(1, guideStep - 1)));
$("guide-skip").addEventListener("click", finishGuide);
$("guide-next").addEventListener("click", async () => {
  if (guideStep === 1) {
    const name = $("guide-gamertag").value.trim();
    if (name) {
      const msg = $("guide-gamertag-msg");
      try {
        const r = await post("/api/setup-guide/gamertag", { gamertag: name, everywhere: $("guide-everywhere").checked });
        settings.ownerGamertag = name;
        guideInfo.ownerGamertag = name;
        if (r.skipped.length && !gamertagWarned) {
          gamertagWarned = true;
          msg.hidden = false;
          msg.className = "small error";
          msg.textContent = "Saved, but some servers were skipped: " + r.skipped.join("; ") + ". Press the button again to carry on.";
          return; // let them read it; Next again moves on
        }
      } catch (err) {
        msg.hidden = false;
        msg.className = "small error";
        msg.textContent = err.message;
        return;
      }
    }
    showGuideStep(2);
    return;
  }
  if (guideStep === 4) { finishGuide(); return; }
  showGuideStep(guideStep + 1);
});

// ---- start ----

start();
