"use strict";
// The panel page ("Control Room" style). One page, with views chosen by the
// address after # (see route): the servers dashboard, one server with its
// Overview, Players, Settings, Backups and Console tabs, and the Backups,
// Help, Import, Add server, Setup guide, People and Account pages.
//
// What shows depends on who's signed in: the owner sees everything; anyone
// else sees only the servers the owner gave them, with the buttons their
// role on each allows (see access).

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
let joinInfo = null;       // console list name and IP, from /api/info
let me = { owner: false, canAdd: false, name: "" }; // who's signed in

// Access levels on one server, as /api/servers reports them.
const SEE = 1, RUN = 2, CARE = 3;
const access = (s) => (s && s.access) || 0;

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

// Words that differ between the two styles: Treehouse uses plainer ones.
const words = {
  control: { start: "Start", stop: "Stop", allow: "Allow", running: "Running", stopped: "Stopped", waiting: "Tried to join", nobody: "Nobody", noVersion: "…" },
  treehouse: { start: "Turn on", stop: "Turn off", allow: "Let in", running: "On", stopped: "Off", waiting: "Wants to join", nobody: "Nobody playing", noVersion: "" },
};
const word = (k) => (words[prefs.style] || words.control)[k];

function stateEl(s) {
  const text = s.state === "running" || s.state === "stopped" ? word(s.state) : (labels[s.state] || s.state);
  return el("span", { class: `state ${s.state}` }, el("span", { class: "dot" }), text);
}

// Each server gets its own colour band in Treehouse, the same every time.
const bands = [["#5fae4f", "#8b5a2b"], ["#e39ac6", "#b0648f"], ["#6fb3e0", "#3b7bb0"], ["#e3cf7b", "#b39b3f"],
  ["#e0925c", "#a95f2a"], ["#a99be0", "#6f5fb0"], ["#6fcfbf", "#3a8f82"], ["#b8b2a6", "#8a8478"]];
function bandStyle(id) {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  const [a, b] = bands[h % bands.length];
  return `--band: ${a}; --band2: ${b}`;
}

function greeting() {
  const h = new Date().getHours();
  const part = h < 5 ? "Good evening" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
  return `${part}, ${$("side-user").textContent || "there"}`;
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
  return s.state === "running" ? word("nobody") : "";
}

const serverHref = (id, tab) => `#/server/${encodeURIComponent(id)}/${tab || "overview"}`;

function startStopButton(s) {
  const busy = busyStates.includes(s.state);
  if (isLive(s)) {
    // Someone who may only turn it on can't turn it off.
    return access(s) >= RUN ? el("button", { class: "danger", onclick: action(s.id, "stop"), disabled: s.state === "stopping" }, word("stop")) : null;
  }
  return el("button", { class: "go", onclick: action(s.id, "start"), disabled: busy }, word("start"));
}

function renderServerRow(s) {
  const upd = needsUpdate(s);
  const extra = [];
  if (s.players.length) extra.push(`${s.players.length} playing`);
  if (s.waiting) extra.push(`${s.waiting} waiting to be let in`);
  if (upd && access(s) >= CARE) extra.push("update ready");
  const open = access(s) >= RUN;
  return el("div", { class: "srow", style: bandStyle(s.id) },
    el("div", {},
      open ? el("a", { class: "name", href: serverHref(s.id) }, s.name) : el("span", { class: "name" }, s.name),
      s.outside ? el("span", { class: "badge outside", title: "Friends outside the house can join" }, "Friends outside") : null,
      el("div", { class: "sub" }, `port ${s.port}`),
      s.message ? el("div", { class: "note warn" }, s.message) : null),
    el("div", {}, stateEl(s)),
    el("div", { class: "cell-muted" }, playingText(s), s.waiting ? el("div", { class: "note warn" }, `${s.waiting} waiting to be let in`) : null),
    el("div", {},
      el("div", { class: "mono" }, s.version || word("noVersion")),
      upd && access(s) >= CARE ? el("div", { class: "note upd" + (upd.far ? " far" : "") }, upd.far ? "Well behind: update" : "Update ready") : null),
    el("div", { class: "cell-muted" }, whenShort(s.lastBackup)),
    el("div", { class: "acts" },
      startStopButton(s),
      open ? el("a", { class: "button", href: serverHref(s.id) }, "Open") : null),
    extra.length ? el("div", { class: "mobile-extra" }, extra.join(" · ")) : null);
}

function renderDashboard(list, force) {
  const key = JSON.stringify([list, latestVersion, prefs.style, me]);
  if (key === dashKey && !force) return; // unchanged: keep keyboard focus
  dashKey = key;
  $("servers-title").textContent = prefs.style === "treehouse" ? greeting() : "Servers";
  $("dash-attempts-title").textContent = word("waiting");
  const head = el("div", { class: "srow head" },
    el("div", {}, "Server"), el("div", {}, "Status"), el("div", {}, "Playing"),
    el("div", {}, "Version"), el("div", {}, "Last backup"), el("div", {}));
  $("servers").replaceChildren(...(list.length ? [head, ...list.map(renderServerRow)] : []));
  $("servers").hidden = list.length === 0;
  $("empty").hidden = list.length > 0;
  if (!list.length) {
    $("empty").replaceChildren(...(me.owner
      ? ["No servers yet. ", el("a", { href: "#/add" }, "Add a server"), " to download Bedrock and start one, or ", el("a", { href: "#/import" }, "import"), " your servers from Crafty."]
      : me.canAdd
        ? ["No servers yet. ", el("a", { href: "#/add" }, "Add a server"), ", or ask ", ownerName(), " to give you one."]
        : ["No servers for you yet. Ask ", ownerName(), " to give you one on the People page."]));
  }
  const running = list.filter((s) => s.state === "running").length;
  const playing = list.reduce((n, s) => n + s.players.length, 0);
  $("servers-summary").textContent = list.length
    ? `${list.length} server${list.length === 1 ? "" : "s"} · ${running} running · ${playing} ${playing === 1 ? "person" : "people"} playing`
    : "";

  // Update note
  const behind = list.filter((s) => access(s) >= CARE && needsUpdate(s) && !busyStates.includes(s.state));
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
  if (me.owner) loadDashOutside();
}

let dashOutsideAt = 0;
async function loadDashOutside() {
  if (Date.now() - dashOutsideAt < 15000) return;
  dashOutsideAt = Date.now();
  try {
    const st = await api("/api/outside");
    const open = (st.servers || []).filter((x) => x.reached).length;
    $("dash-outside-text").textContent = !st.settings.enabled
      ? "Off. Friends who don't live here can't join."
      : `On · ${open} server${open === 1 ? "" : "s"} open to them` + (st.address ? ` at ${st.address}` : "") + ".";
  } catch (_) { /* shown on the page itself */ }
}

$("update-all").addEventListener("click", async (ev) => {
  const button = ev.currentTarget;
  const behind = serverList.filter((s) => access(s) >= CARE && needsUpdate(s) && !busyStates.includes(s.state));
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
  const waiting = list.filter((s) => s.waiting > 0 && access(s) >= RUN);
  if (!waiting.length) { $("dash-attempts").hidden = true; return; }
  if (Date.now() - attemptsLoadedAt < 5000) return;
  attemptsLoadedAt = Date.now();
  const rows = [];
  for (const s of waiting) {
    try {
      const v = await api(`/api/servers/${encodeURIComponent(s.id)}/players`);
      for (const a of v.attempts || []) {
        rows.push(row(a.name, `${s.name} · ${ago(a.at)}`,
          btn(word("allow"), async () => {
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
  list = list.filter((s) => access(s) >= RUN);
  $("backups-none").hidden = list.length > 0;
  $("backups-overview").hidden = list.length === 0;
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
  const key = JSON.stringify([s.id, s.name, s.state, s.version, s.port, s.message, latestVersion, s.players.length, prefs.style, s.access]);
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
  $("detail-actions").replaceChildren(...[startStopButton(s),
    el("button", { onclick: action(s.id, "restart"), disabled: !isLive(s) || busy }, "Restart"),
    access(s) >= CARE ? updBtn : null].filter(Boolean));
  for (const t of ["players", "settings", "addons", "backups"]) $("tab-" + t).hidden = copying;
  $("ov-backups-link").textContent = access(s) >= CARE ? "Restore or download" : "See backups";
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
      if (s && access(s) < RUN) {
        location.hash = "#/"; // the owner changed what this account may do
      } else if (s) {
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

// route shows the page the address names: #/, #/backups, #/help,
// #/import, #/add, #/guide, #/people, #/account, #/server/<id>/<tab>. (#/joining, the
// old address of the joining help, opens Help.)
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
  if (next === "joining") next = "help";
  if (!["servers", "backups", "help", "import", "add", "guide", "people", "outside", "account", "server"].includes(next)) next = "servers";
  if ((["import", "guide", "people", "outside"].includes(next) && !me.owner) || (next === "add" && !me.canAdd)) {
    location.hash = "#/"; // not for this account
    return;
  }
  if (next === "server") {
    const id = parts[1];
    const t = ["overview", "players", "settings", "addons", "backups", "console"].includes(parts[2]) ? parts[2] : "overview";
    if (!id) { location.hash = "#/"; return; }
    const known = serverList.find((x) => x.id === id);
    if (known && access(known) < RUN) { location.hash = "#/"; return; }
    if (id !== selected || view !== "server") {
      if (settingsDirty() && !confirm("Leave without saving your settings changes?")) { stay(); return; }
      closeConsole();
      closeOverviewConsole();
      resetSettings();
      resetBackups();
      resetAddons();
      clearServerPanes();
      selected = id;
      $("detail-name").textContent = "";
      $("detail-facts").replaceChildren();
      $("detail-actions").replaceChildren();
      const s = serverList.find((x) => x.id === id);
      if (s) renderDetailHead(s);
    }
    const s = serverList.find((x) => x.id === id);
    const tabOK = !(s && s.state === "importing" && ["players", "settings", "addons", "backups"].includes(t));
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
  if (next === "add") {
    $("add-owner").value = me.owner ? settings.ownerGamertag || "" : "";
    $("add-kid-note").hidden = me.owner;
    $("add-name").focus();
  }
  if (next === "people") loadPeople();
  if (next === "outside") loadOutside(false);
  if (next === "guide") openGuide(1);
  if (next === "help") fillJoinInfo();
  refresh();
}

function setView(v) {
  view = v;
  for (const sec of document.querySelectorAll(".view")) sec.hidden = sec.id !== "view-" + v;
  // Pages without their own menu entry light up the one they're reached from.
  const navKey = { server: "servers", add: "servers", import: "servers", guide: "help", outside: "help", people: me.owner ? "people" : "account" }[v] || v;
  for (const a of document.querySelectorAll("[data-nav]")) {
    if (a.dataset.nav === navKey) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
  }
  window.scrollTo(0, 0);
}

function showTab(which) {
  tab = which;
  for (const t of ["overview", "players", "settings", "addons", "backups", "console"]) {
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
  else if (which === "addons") loadAddons();
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
  const [settingsV, players, backups, acts, outsideV] = await Promise.all([
    api(`/api/servers/${encodeURIComponent(id)}/settings`).catch(() => null),
    api(`/api/servers/${encodeURIComponent(id)}/players`).catch(() => null),
    api(`/api/servers/${encodeURIComponent(id)}/backups`).catch(() => null),
    api(`/api/activity?server=${encodeURIComponent(id)}&limit=6`).catch(() => null),
    api(`/api/servers/${encodeURIComponent(id)}/outside`).catch(() => null),
  ]);
  if (id !== selected || tab !== "overview") return;
  renderOverviewOutside(outsideV, id);
  if (acts) $("ov-activity").replaceChildren(...(acts.length ? acts.map((e) => activityRow(e, false)) : [emptyRow("Nothing yet.")]));
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
  for (const id of ["online", "allowlist", "operators", "attempts", "ov-online", "ov-world", "ov-activity"]) $(id).replaceChildren();
  $("attempts-box").hidden = true;
  showPlayersError(null);
  for (const id of ["ov-join", "ov-backups", "ov-outside-text"]) $(id).textContent = "…";
  $("ov-outside-actions").replaceChildren();
  $("ov-outside-problem").hidden = true;
  $("ov-invite-box").hidden = true;
  $("ov-invite").value = "";
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
    btn(word("allow"), () => playersCall(() => post(sid() + "/allowlist", { name: a.name })), "primary"),
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
  const id = selected;
  post(sid() + "/allowlist-enabled", { enabled }).then((v) => {
    if (id === selected && v) { showPlayersError(null); renderPlayers(v); }
  }).catch((err) => {
    if (id !== selected) return;
    ev.target.checked = !enabled;
    alert(err.message);
  });
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
    if (me.owner) settings.ownerGamertag = $("add-owner").value.trim();
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
  "before-update": "Before an update", "before-restore": "Before a restore", "before-delete": "Before deleting a world",
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
  const care = access(serverList.find((x) => x.id === id)) >= CARE;
  $("plan-form").hidden = !care;
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
    : (list.length ? `${list.length} backup${list.length === 1 ? "" : "s"}, ${size(v.total)} in all. ` + (care ? "Restoring backs up the current world first, so you can undo it." : `To restore one, ask ${ownerWord()}.`) : "");
  const enc = encodeURIComponent;
  $("backups").replaceChildren(...(list.length ? list.map((b) => {
    const when = new Date(b.time).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
    const detail = `${kindLabels[b.kind] || b.kind} · ${size(b.size)}${b.full ? " · includes the server version from before the update" : ""}`;
    if (!care) return row(when, detail);
    return row(when, detail,
      el("a", { class: "button", href: `${sid()}/backups/${enc(b.name)}`, download: b.name }, "Download"),
      btn("Restore", () => {
        if (b.kind === "before-delete") {
          if (confirm(`Put back the world that was deleted on ${when}? It's added next to the other worlds; nothing else changes.`)) {
            backupsCall(() => post(`${sid()}/backups/${enc(b.name)}/restore`));
          }
          return;
        }
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

// ---- add-ons tab ----

let addonsFor = null; // server ID the add-ons tab shows

function showAddonsError(err) {
  $("addons-error").textContent = err ? err.message : "";
  $("addons-error").hidden = !err;
}

function resetAddons() {
  addonsFor = null;
  $("addons-list").replaceChildren();
  $("worlds-list").replaceChildren();
  $("addons-world").textContent = "";
  $("addons-restart").hidden = true;
  showAddonsError(null);
}

async function loadAddons() {
  if (!selected) return;
  const id = selected;
  if (addonsFor !== id) resetAddons();
  try {
    const [packs, worlds] = await Promise.all([api(sid() + "/packs"), api(sid() + "/worlds")]);
    if (id !== selected) return;
    addonsFor = id;
    renderAddons(packs, worlds);
    showAddonsError(null);
  } catch (err) { if (id === selected) showAddonsError(err); }
}

const mb1 = (bytes) => (bytes >= 1e9 ? (bytes / 1e9).toFixed(1) + " GB" : bytes >= 1e6 ? (bytes / 1e6).toFixed(1) + " MB" : Math.max(1, Math.round(bytes / 1e3)) + " KB");

function addonsNeedRestart(running) {
  $("addons-restart").hidden = !running;
}

function renderAddons(v, wv) {
  const s = serverList.find((x) => x.id === selected);
  const care = access(s) >= CARE;
  for (const e of document.querySelectorAll("#pane-addons [data-care]")) e.hidden = !care;
  $("addons-world").textContent = v.world;
  const enc = encodeURIComponent;
  $("addons-list").replaceChildren(...(v.packs.length ? v.packs.map((p) => {
    const bits = [p.kind === "behavior" ? "Behavior pack" : "Resource pack"];
    if (p.version) bits.push(p.version);
    if (p.size) bits.push(mb1(p.size));
    const name = el("span", { class: "who" }, el("span", {}, p.name,
      p.enabled ? null : el("span", { class: "tag" }, "Off"),
      p.missing ? el("span", { class: "tag warn" }, "Files missing") : null,
      p.needsBeta ? el("span", { class: "tag warn", title: "Uses scripting that needs the world's Beta APIs experiment" }, "Needs Beta APIs") : null),
      el("small", {}, bits.join(" · ") + (p.description ? " · " + p.description : "")),
      p.needsBeta && care ? el("small", { class: "warn" }, "This add-on needs the Beta APIs experiment, which can't be turned on from the panel. Turn it on for the world in the game (Edit world, Experiments), then export the world and upload it below.") : null);
    const buttons = [];
    if (care) {
      if (!p.missing) {
        buttons.push(btn(p.enabled ? "Turn off" : "Turn on", () => addonsCall(async () => {
          const nv = await post(`${sid()}/packs/${enc(p.uuid)}`, { enabled: !p.enabled });
          addonsNeedRestart(nv.running);
          return nv;
        })));
      }
      buttons.push(btn("Remove", () => {
        if (!confirm(`Remove ${p.name} from ${v.world}? Things in the world that came from it may disappear.`)) return;
        addonsCall(async () => { const nv = await del(`${sid()}/packs/${enc(p.uuid)}`); addonsNeedRestart(nv.running); return nv; });
      }, "danger"));
    }
    return el("li", { class: p.enabled ? "" : "off" }, name, ...buttons);
  }) : [emptyRow(care ? "No add-ons yet. Drop one above." : "No add-ons in this world.")]));

  const owner = me.owner;
  $("worlds-list").replaceChildren(...wv.worlds.map((w) => {
    const bits = [];
    if (w.name && w.name !== w.folder) bits.push(w.name);
    if (w.size) bits.push(mb1(w.size)); else if (w.current) bits.push("made when the server first starts");
    if (w.packs) bits.push(`${w.packs} add-on pack${w.packs === 1 ? "" : "s"}`);
    const buttons = [];
    if (w.current) buttons.push(el("span", { class: "badge" }, "Playing"));
    else {
      buttons.push(btn("Play this world", () => {
        const note = wv.running ? " The server restarts, so players are disconnected for a moment." : "";
        if (!confirm(`Switch the server to ${w.folder}? The current world is kept.${note}`)) return;
        addonsCall(async () => { await post(`${sid()}/worlds/${enc(w.folder)}/play`); return null; });
      }));
    }
    if (care && w.size) buttons.push(el("a", { class: "button", href: `${sid()}/worlds/${enc(w.folder)}/download`, download: "" }, "Download"));
    if (owner && !w.current) {
      buttons.push(btn("Delete", () => {
        if (!confirm(`Delete the world ${w.folder}? A backup of all the worlds is made first, so it can be put back from Backups.`)) return;
        addonsCall(async () => { await del(`${sid()}/worlds/${enc(w.folder)}`); return null; });
      }, "danger"));
    }
    return row(w.folder, bits.join(" · "), ...buttons);
  }));
}

async function addonsCall(fn) {
  const id = selected;
  try {
    await fn();
    if (id === selected) showAddonsError(null);
  } catch (err) { if (id === selected) showAddonsError(err); }
  if (id === selected) loadAddons();
  refresh();
}

// upload sends a file with progress shown in the drop box.
function upload(box, url, file) {
  return new Promise((resolve, reject) => {
    const bar = box.querySelector(".upload-progress");
    const progress = bar.querySelector("progress");
    const label = bar.querySelector("span");
    bar.hidden = false;
    box.classList.add("busy");
    progress.value = 0;
    label.textContent = `Uploading ${file.name}…`;
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    xhr.setRequestHeader("X-Blockheads", "1");
    xhr.setRequestHeader("X-File-Name", encodeURIComponent(file.name));
    xhr.upload.onprogress = (ev) => {
      if (!ev.lengthComputable) return;
      progress.value = Math.round((ev.loaded / ev.total) * 100);
      label.textContent = ev.loaded >= ev.total ? "Unpacking and installing…" : `Uploading ${file.name}… ${mb1(ev.loaded)} of ${mb1(ev.total)}`;
    };
    const done = () => { bar.hidden = true; box.classList.remove("busy"); };
    xhr.onload = () => {
      done();
      let body = null;
      try { body = JSON.parse(xhr.responseText); } catch (_) { /* empty */ }
      if (xhr.status === 401) { signedOut(); reject(new Error("Signed out")); return; }
      if (xhr.status < 200 || xhr.status >= 300) { reject(new Error((body && body.error) || xhr.statusText || "Upload failed")); return; }
      resolve(body);
    };
    xhr.onerror = () => { done(); reject(new Error("The upload was cut off. Check the connection and try again.")); };
    xhr.send(file);
  });
}

function dropZone(boxId, inputId, handle) {
  const box = $(boxId);
  const input = $(inputId);
  input.addEventListener("change", () => { if (input.files[0]) handle(input.files[0]); input.value = ""; });
  box.addEventListener("dragover", (ev) => { ev.preventDefault(); box.classList.add("over"); });
  box.addEventListener("dragleave", () => box.classList.remove("over"));
  box.addEventListener("drop", (ev) => {
    ev.preventDefault();
    box.classList.remove("over");
    const f = ev.dataTransfer.files[0];
    if (f) handle(f);
  });
}

dropZone("addon-drop", "addon-file", async (file) => {
  const id = selected;
  try {
    const res = await upload($("addon-drop"), `/api/servers/${encodeURIComponent(id)}/packs`, file);
    if (id !== selected) return;
    const names = res.installed.map((p) => p.name).join(", ");
    const skipped = res.skipped.length ? `\n\nSkipped: ${res.skipped.join("; ")}` : "";
    const beta = res.installed.some((p) => p.needsBeta) ? "\n\nSomething in it needs the Beta APIs experiment: see the note next to it." : "";
    alert(`Installed ${names} in ${res.world}.${skipped}${beta}`);
    addonsNeedRestart(res.running);
    showAddonsError(null);
  } catch (err) { if (id === selected) showAddonsError(err); }
  if (id === selected) loadAddons();
});

dropZone("world-drop", "world-file", async (file) => {
  const id = selected;
  const play = $("world-play").checked;
  if (play && !confirm(`Upload ${file.name} and switch the server to it? The current world is kept. If the server is running, it restarts.`)) return;
  try {
    const res = await upload($("world-drop"), `/api/servers/${encodeURIComponent(id)}/worlds?play=${play ? 1 : 0}`, file);
    if (id !== selected) return;
    alert(res.playing ? `Added ${res.world.folder}; the server plays it now.` : `Added ${res.world.folder}. Press "Play this world" to switch to it.`);
    showAddonsError(null);
  } catch (err) { if (id === selected) showAddonsError(err); }
  if (id === selected) loadAddons();
  refresh();
});

$("addons-restart-now").addEventListener("click", async () => {
  $("addons-restart").hidden = true;
  try { await post(sid() + "/restart"); } catch (err) { showAddonsError(err); }
  refresh();
});
$("addons-restart-later").addEventListener("click", () => { $("addons-restart").hidden = true; });
// Files dropped anywhere else would open in the browser and leave the panel.
window.addEventListener("dragover", (ev) => ev.preventDefault());
window.addEventListener("drop", (ev) => ev.preventDefault());

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


// ---- friends outside the house ----

// inviteText tells a friend how to join a server from outside the house.
function inviteText(v, name) {
  const lines = [`Come play on "${name}" with me in Minecraft!`, "",
    "On a PC, phone or tablet: open Minecraft, go to Play, then Servers, then Add Server, and type",
    `  Server address: ${v.address}`, `  Port: ${v.port}`, ""];
  lines.push("On an Xbox, PlayStation or Switch (they can't type an address):");
  if (v.consoles && v.consoleDNS) {
    lines.push(`  1. In the console's network settings, set the DNS by hand: primary ${v.consoleDNS}, secondary 1.1.1.1.`,
      "  2. Restart the console, open Minecraft, go to Servers and join The Hive (or any featured server).",
      `  3. A list of servers appears instead. Pick "${name}".`,
      "  If the console can't get online with that DNS, try the way below instead.", "");
    lines.push("Another way for consoles:");
  }
  lines.push("  1. Set the DNS by hand: primary 104.238.130.180 (PlayStation: 45.55.68.52), secondary 8.8.8.8.",
    "     (That's BedrockConnect, a free public server list.)",
    "  2. Restart the console, open Minecraft, go to Servers and join any featured server.",
    `  3. Choose "Connect to a Server" and type ${v.address} and port ${v.port}.`, "",
    "Send me your gamertag first so I can put you on the allowlist.");
  return lines.join("\n");
}

function renderOverviewOutside(v, id) {
  const s = serverList.find((x) => x.id === id);
  const care = access(s) >= CARE;
  const actions = [];
  $("ov-outside-problem").hidden = true;
  $("ov-invite-box").hidden = true;
  if (!v) { $("ov-outside-text").textContent = "Couldn't load this."; $("ov-outside-actions").replaceChildren(); return; }
  if (!v.enabled) {
    $("ov-outside-text").replaceChildren(...(me.owner
      ? ["Friends who don't live here can't join yet. ", el("a", { href: "#/outside" }, "Set up outside access")]
      : ["Friends who don't live here can't join yet. Ask ", ownerName(), " to turn on outside access."]));
    $("ov-outside-actions").replaceChildren();
    return;
  }
  if (v.open) {
    $("ov-outside-text").replaceChildren(v.reached ? "Open: friends on the allowlist can join from anywhere at " : "Marked open, but friends can't join right now. Address: ",
      el("b", { class: "mono" }, `${v.address}:${v.port}`), ".");
  } else {
    $("ov-outside-text").textContent = "Closed: only people in the house can join.";
  }
  const notes = [];
  if (v.problem) notes.push(`It can't be reached while ${v.problem}. Turn it back on (Players or Settings tab).`);
  if (v.open && v.shared) notes.push("Your internet provider (or a second router) shares this home's address, so friends can't reach it. See Friends outside the house.");
  if (v.open && !v.problem && !v.opened && me.owner) {
    notes.push(v.upnp && v.portError
      ? `The router didn't open port ${v.port}: ${v.portError}. Forward UDP ${v.port} to this panel by hand.`
      : (v.upnp ? "" : `Forward UDP port ${v.port} to this panel in your router (see Friends outside the house).`));
  }
  const note = notes.filter(Boolean).join(" ");
  $("ov-outside-problem").textContent = note;
  $("ov-outside-problem").hidden = !note;
  if (care) {
    actions.push(btn(v.open ? "Close to friends outside" : "Open to friends outside", async (ev) => {
      const b = ev.currentTarget;
      if (!v.open && !confirm(`Open ${s ? s.name : "this server"} to friends outside the house?\n\nOnly players on its allowlist can join, and their Xbox sign-in is checked.`)) return;
      b.disabled = true;
      try {
        const nv = await post(`/api/servers/${encodeURIComponent(id)}/outside`, { open: !v.open });
        if (id === selected) renderOverviewOutside(nv, id);
        refresh();
      } catch (err) { alert(err.message); b.disabled = false; }
    }, v.open ? "" : "primary"));
  }
  if (v.open && v.address) {
    const text = inviteText(v, s ? s.name : "my server");
    $("ov-invite").value = text;
    $("ov-invite-box").hidden = false;
    actions.push(btn("Copy invite", async (ev) => {
      const b = ev.currentTarget;
      try {
        await navigator.clipboard.writeText(text);
        b.textContent = "Copied";
        setTimeout(() => { b.textContent = "Copy invite"; }, 2000);
      } catch (_) {
        $("ov-invite-box").open = true;
        $("ov-invite").select();
      }
    }));
  }
  $("ov-outside-actions").replaceChildren(...actions);
}

let outsideDirty = false;

function showOutsideError(err) {
  $("outside-error").textContent = err ? err.message : "";
  $("outside-error").hidden = !err;
}

async function loadOutside(check) {
  try {
    const st = check ? await post("/api/outside/check") : await api("/api/outside");
    if (view !== "outside") return;
    renderOutside(st);
    showOutsideError(null);
  } catch (err) { showOutsideError(err); }
}

function fact(ok, text) { return checkItem(ok, text); }

function renderOutside(st) {
  const set = st.settings;
  if (!outsideDirty) {
    $("outside-enabled").checked = set.enabled;
    $("outside-address").value = set.address || "";
    $("outside-upnp").checked = set.upnp;
    $("outside-consoles").checked = set.consoles;
  }
  $("outside-address").placeholder = st.publicIP ? `Leave empty to use ${st.publicIP}, or type a hostname` : "mc.example.com, or leave empty for this home's internet address";
  const facts = [];
  if (st.publicIP) facts.push(fact(!st.shared, `This home's internet address is ${st.publicIP}` + (st.publicIPFrom === "router" ? " (from the router)." : ".")));
  else if (set.enabled) facts.push(fact(false, st.publicIPError || "Looking up this home's internet address…"));
  if (st.shared) facts.push(fact(false, "Your internet provider or a second router shares this address (called CGNAT or double NAT), so forwarded ports can't be reached from outside. Ask your provider for a public IP, or put the router in front into bridge mode."));
  if (set.address && st.addressIP && !st.addressWarning) facts.push(fact(true, `${set.address} leads here (${st.addressIP}).`));
  if (st.addressWarning) facts.push(fact(false, st.addressWarning));
  if (set.enabled && st.address) facts.push(fact(true, `Friends join at ${st.address}, with each server's own port.`));
  $("outside-facts").replaceChildren(...facts);
  $("outside-facts").hidden = facts.length === 0;

  const r = $("outside-router");
  if (!set.upnp) { r.textContent = ""; r.className = "small"; }
  else if (st.router) { r.textContent = `Router: ${st.router}.`; r.className = "small ok"; }
  else { r.textContent = st.routerError || (set.enabled ? "Looking for the router…" : ""); r.className = "small warn"; }

  const ports = st.ports || [];
  $("outside-ports").replaceChildren(...(ports.length
    ? [el("tr", {}, el("th", {}, "Port"), el("th", {}, "Type"), el("th", {}, "Forward to"), el("th", {}, "For"), el("th", {}, "Router")),
      ...ports.map((p) => el("tr", {},
        el("td", { class: "mono" }, String(p.port)), el("td", {}, p.proto), el("td", { class: "mono" }, st.localIP), el("td", {}, p.for),
        el("td", { class: p.opened ? "ok" : (p.error ? "warn" : "muted") }, p.opened ? "Opened" : (set.upnp ? (p.error || "…") : "Forward by hand"))))]
    : [el("tr", {}, el("td", { class: "muted" }, set.enabled ? "Nothing to forward yet: no server is open to friends outside." : "Nothing is forwarded while outside access is off."))]));

  $("outside-servers").replaceChildren(...((st.servers || []).length ? st.servers.map((x) => {
    const detail = x.problem ? `Can't be reached: ${x.problem}` : (x.reached ? `Open at ${st.address}:${x.port}` : (x.open ? "Open (outside access is off)" : `Closed · port ${x.port}`));
    return row(x.name, detail,
      el("a", { class: "button", href: serverHref(x.id) }, "Open page"));
  }) : [emptyRow("No servers yet.")]));
}

for (const id of ["outside-enabled", "outside-upnp", "outside-consoles", "outside-address"]) {
  $(id).addEventListener("input", () => { outsideDirty = true; $("outside-saved").hidden = true; });
}
$("outside-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const b = $("outside-save");
  b.disabled = true;
  b.textContent = "Saving…";
  try {
    const st = await api("/api/outside", { method: "PUT", body: JSON.stringify({
      enabled: $("outside-enabled").checked, address: $("outside-address").value.trim(),
      upnp: $("outside-upnp").checked, consoles: $("outside-consoles").checked }) });
    outsideDirty = false;
    dashOutsideAt = 0;
    renderOutside(st);
    showOutsideError(null);
    $("outside-saved").hidden = false;
  } catch (err) { showOutsideError(err); }
  b.disabled = false;
  b.textContent = "Save";
});
$("outside-check").addEventListener("click", async (ev) => {
  const b = ev.currentTarget;
  b.disabled = true;
  await loadOutside(true);
  b.disabled = false;
});

// ---- activity ----

function activityRow(e, withServer) {
  const d = new Date(e.time);
  const when = whenShort(e.time);
  const where = withServer && e.server ? ` · ${e.server}` : "";
  return el("li", {}, el("span", { class: "who" }, `${e.user || "The panel"} ${e.text}`,
    el("small", { title: d.toLocaleString() }, when + where)));
}

// ---- people (owner only) ----

let people = null; // last /api/users answer

const roleChoices = [["", "No access"], ["start", "Can turn it on"], ["run", "Runs it"], ["care", "Takes care of it"]];

function showPeopleError(err) {
  $("people-error").textContent = err ? err.message : "";
  $("people-error").hidden = !err;
}

async function loadPeople() {
  try {
    const [v, acts] = await Promise.all([api("/api/users"), api("/api/activity?limit=30")]);
    if (view !== "people") return;
    people = v;
    renderPeople();
    $("people-activity").replaceChildren(...(acts.length ? acts.map((e) => activityRow(e, true)) : [emptyRow("Nothing yet.")]));
    showPeopleError(null);
  } catch (err) { showPeopleError(err); }
}

// savePerson sends one change to what someone may do: {canAdd} or
// {grants: {serverID: role}}. Only what's sent changes, so quick changes
// in a row don't undo each other.
async function savePerson(u, change) {
  try {
    await api(`/api/users/${encodeURIComponent(u.username)}`, { method: "PUT", body: JSON.stringify(change) });
    showPeopleError(null);
  } catch (err) { showPeopleError(err); }
  loadPeople();
}

function renderPeople() {
  const v = people;
  if (!v) return;
  if (!v.users.length) {
    $("people-list").replaceChildren(el("p", { class: "card muted" }, "Nobody else has an account yet. Add someone below."));
    return;
  }
  $("people-list").replaceChildren(...v.users.map((u) => {
    const canAdd = el("input", { type: "checkbox", checked: u.canAdd });
    canAdd.addEventListener("change", () => savePerson(u, { canAdd: canAdd.checked }));
    const grants = v.servers.length ? v.servers.map((srv) => {
      const role = u.grants[srv.id] || "";
      const sel = el("select", { "aria-label": `${u.username} on ${srv.name}` },
        ...roleChoices.map(([val, label]) => el("option", { value: val, selected: val === role }, label)));
      sel.addEventListener("change", () => savePerson(u, { grants: { [srv.id]: sel.value } }));
      return el("li", { class: role ? "" : "none" }, el("span", {}, srv.name), sel);
    }) : [el("li", {}, el("span", {}, "No servers yet."))];
    return el("section", { class: "card person" },
      el("div", { class: "person-head" },
        el("span", { class: "avatar" }, u.username.slice(0, 1).toUpperCase()),
        el("div", { class: "grow" }, el("h2", {}, u.username),
          el("small", {}, u.lastSeen ? `Last used the panel ${whenShort(u.lastSeen).toLowerCase()}` : "Hasn't signed in yet")),
        btn("New password", async () => {
          const pw = prompt(`New password for ${u.username} (at least 8 characters). They'll be signed out everywhere.`);
          if (!pw) return;
          try {
            await post(`/api/users/${encodeURIComponent(u.username)}/password`, { password: pw });
            showPeopleError(null);
            alert(`${u.username}'s password is changed. Tell them the new one.`);
          } catch (err) { showPeopleError(err); }
          loadPeople();
        }),
        btn("Remove", async () => {
          if (!confirm(`Remove ${u.username}'s account? They're signed out, and their servers stay as they are.`)) return;
          try { await del(`/api/users/${encodeURIComponent(u.username)}`); showPeopleError(null); } catch (err) { showPeopleError(err); }
          loadPeople();
        }, "danger")),
      el("ul", { class: "grants" }, ...grants),
      el("label", { class: "check" }, canAdd, el("span", {}, "Can add new servers (they take care of the ones they add)")));
  }));
}

$("person-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await post("/api/users", { username: $("person-name").value.trim(), password: $("person-pass").value, canAdd: $("person-canadd").checked });
    $("person-form").reset();
    formError("person-error", null);
    loadPeople();
  } catch (err) { formError("person-error", err); }
});

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
  for (const id of ["version-2", "version-3"]) $(id).textContent = st.version ? "Blockheads Control Panel " + st.version : "";
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
  me = { owner: !!st.owner, canAdd: !!st.canAdd, name: st.username };
  for (const e of document.querySelectorAll("[data-owner-only]")) e.hidden = !me.owner;
  for (const e of document.querySelectorAll("[data-not-owner]")) e.hidden = me.owner;
  for (const e of document.querySelectorAll("[data-can-add]")) e.hidden = !me.canAdd;
  $("auth-view").hidden = true;
  $("app-view").hidden = false;
  $("account-name").textContent = st.username;
  $("side-user").textContent = st.username;
  $("side-avatar").textContent = (st.username || "?").slice(0, 1).toUpperCase();
  $("tab-user").textContent = st.username;
  $("reset-user").value = st.username;
  applyPrefs(st.prefs || {}, false);
  if (me.owner) api("/api/settings").then((s) => { settings = s || {}; }).catch(() => {});
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
    setInterval(() => { if (signedIn && view === "outside" && !document.hidden) loadOutside(false); }, 10000),
  ];
  if (me.owner && !st.setupDone && !location.hash.startsWith("#/guide")) location.hash = "#/guide";
  else route();
}

// ---- console list info (Joining page, sidebar, dashboard) ----

async function loadJoinInfo() {
  try {
    joinInfo = await api("/api/info");
  } catch (_) { return; }
  fillJoinInfo();
}

// ownerName is the owner's username, for "ask Scott".
function ownerWord() { return (joinInfo && joinInfo.owner) || "the owner"; }
function ownerName() { return el("b", {}, ownerWord()); }

function fillJoinInfo() {
  if (!joinInfo) return;
  for (const e of document.querySelectorAll(".owner-name")) e.textContent = ownerWord();
  if (view === "servers") renderDashboard(serverList, true);
  const name = joinInfo.listName || "Server List";
  const ip = joinInfo.hostIP || "the panel's IP";
  for (const id of ["side-list-name", "dash-list-name", "join-list-name"]) $(id).textContent = name;
  for (const id of ["side-list-ip", "dash-list-ip", "join-list-ip"]) $(id).textContent = ip;
}

// ---- appearance ----

let prefs = { style: "control", mode: "auto" };

// applyPrefs sets the look, and remembers it in this browser so the next
// visit starts with it before the account's copy loads.
function applyPrefs(p, save) {
  prefs = { style: p.style === "treehouse" ? "treehouse" : "control", mode: ["light", "dark", "auto"].includes(p.mode) ? p.mode : "auto" };
  const changed = document.documentElement.dataset.style !== prefs.style;
  document.documentElement.dataset.style = prefs.style;
  document.documentElement.dataset.mode = prefs.mode;
  if (changed && signedIn) {
    // Words and layout differ between styles: draw everything again.
    dashKey = detailKey = "";
    if (view === "servers") renderDashboard(serverList, true);
    if (view === "server" && tab === "players") loadPlayers();
    refresh();
  }
  try { localStorage.setItem("bh-prefs", JSON.stringify(prefs)); } catch (_) { /* private window */ }
  for (const r of document.querySelectorAll("input[name=style]")) r.checked = r.value === prefs.style;
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
  resetAddons();
  selected = null;
  settings = {};
  me = { owner: false, canAdd: false, name: "" };
  people = null;
  outsideDirty = false;
  dashOutsideAt = 0;
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
  for (const id of ["servers", "online", "allowlist", "operators", "attempts", "console-log", "import-list", "import-results", "guide-checklist", "ov-online", "dash-attempts-list", "backups-overview", "people-list", "people-activity", "ov-activity"]) $(id).replaceChildren();
  $("person-form").reset();
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
