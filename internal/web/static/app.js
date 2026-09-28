"use strict";
// Panel page: server list, add, start/stop/update, and per server a Players
// tab (online, allowlist, operators, tried to join) and a live Console tab.

const $ = (id) => document.getElementById(id);
const labels = {
  running: "Running", starting: "Starting", stopping: "Stopping", stopped: "Stopped",
  installing: "Installing", importing: "Importing", updating: "Updating", restoring: "Restoring", crashed: "Crashed", error: "Needs attention",
};
let selected = null;      // server ID shown in the detail section
let tab = "players";
let consoleStream = null;
let settings = {};

async function api(path, options = {}) {
  const res = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", "X-Blockheads": "1", ...(options.headers || {}) },
  });
  let body = null;
  try { body = await res.json(); } catch (_) { /* empty */ }
  if (res.status === 401 && !path.startsWith("/api/auth/")) {
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

// ---- server list ----

function action(id, what) {
  return async (ev) => {
    ev.target.disabled = true;
    try { await post(`/api/servers/${encodeURIComponent(id)}/${what}`); }
    catch (err) { alert(err.message); }
    refresh();
  };
}

function renderServer(s) {
  const busy = ["installing", "importing", "updating", "restoring", "starting", "stopping"].includes(s.state);
  const copying = s.state === "importing";
  const live = s.state === "running" || s.state === "starting";
  const players = s.players.length
    ? `${s.players.length} playing: ${s.players.map((p) => p.name).join(", ")}`
    : (s.state === "running" ? "Nobody playing" : "");
  return el("div", { class: "card" },
    el("div", { class: "server-top" },
      el("div", {},
        el("div", { class: "server-name" }, s.name),
        el("div", { class: "facts" }, `Bedrock ${s.version || "…"} · port ${s.port}`)),
      el("span", { class: `pill ${s.state}` }, labels[s.state] || s.state)),
    s.message ? el("p", { class: "message" }, s.message) : null,
    players ? el("p", { class: "players" }, players) : null,
    el("div", { class: "row" },
      live
        ? el("button", { class: "danger", onclick: action(s.id, "stop"), disabled: s.state === "stopping" }, "Stop")
        : el("button", { class: "primary", onclick: action(s.id, "start"), disabled: busy }, "Start"),
      el("button", { onclick: action(s.id, "restart"), disabled: !live || busy }, "Restart"),
      el("button", { onclick: confirmUpdate(s), disabled: copying || s.state === "installing" || s.state === "updating" }, "Update"),
      el("button", { onclick: () => openDetail(s, "players"), disabled: copying }, "Players"),
      el("button", { onclick: () => openDetail(s, "settings"), disabled: copying }, "Settings"),
      el("button", { onclick: () => openDetail(s, "backups"), disabled: copying }, "Backups"),
      el("button", { onclick: () => openDetail(s, "console") }, "Console")));
}

function confirmUpdate(s) {
  const run = action(s.id, "update");
  return (ev) => {
    const note = s.players.length ? `\n\n${s.players.length} player(s) will be disconnected.` : "";
    if (confirm(`Update ${s.name}? It's backed up first, then restarted if it was running.${note}`)) run(ev);
  };
}

async function refresh() {
  if (!signedIn) return;
  try {
    const list = await api("/api/servers");
    if (!signedIn) return; // signed out while this was loading
    importsFinished(list);
    $("servers").replaceChildren(...list.map(renderServer));
    $("empty").hidden = list.length > 0;
    if (selected) {
      const s = list.find((x) => x.id === selected);
      if (s) $("detail-name").textContent = s.name;
    }
  } catch (err) {
    if (!signedIn) return;
    $("servers").replaceChildren(el("p", { class: "error" }, "Can't reach the panel: " + err.message));
  }
}

// ---- detail: tabs ----

function openDetail(s, which) {
  if (selected !== s.id) {
    if (settingsDirty() && !confirm("Leave without saving your settings changes?")) return;
    closeConsole();
    resetSettings();
    resetBackups();
  }
  selected = s.id;
  $("detail-name").textContent = s.name;
  $("detail").hidden = false;
  showTab(which);
  $("detail").scrollIntoView({ behavior: "smooth" });
}

function showTab(which) {
  tab = which;
  for (const t of ["players", "settings", "backups", "console"]) {
    $("tab-" + t).setAttribute("aria-selected", String(t === which));
    $("pane-" + t).hidden = t !== which;
  }
  if (which === "console") openConsole();
  else if (which === "settings") { if (!settingsView || !settingsDirty()) loadSettings(); }
  else if (which === "backups") loadBackups();
  else loadPlayers();
}

for (const b of document.querySelectorAll(".tabs button")) {
  b.addEventListener("click", () => showTab(b.dataset.tab));
}

$("detail-close").addEventListener("click", () => {
  if (settingsDirty() && !confirm("Close without saving your settings changes?")) return;
  resetSettings();
  resetBackups();
  closeConsole();
  selected = null;
  $("detail").hidden = true;
});

// ---- players tab ----

function showPlayersError(err) {
  $("players-error").textContent = err ? err.message : "";
  $("players-error").hidden = !err;
}

async function playersCall(fn) {
  try {
    const v = await fn();
    showPlayersError(null);
    if (v) renderPlayers(v);
  } catch (err) {
    showPlayersError(err);
  }
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
        f.link === "players" ? el("div", { class: "help" }, el("a", { href: "#", onclick: (e) => { e.preventDefault(); showTab("players"); } }, "Open the Players tab")) : null,
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

$("add-toggle").addEventListener("click", () => {
  $("add-form").hidden = !$("add-form").hidden;
  if (!$("add-form").hidden) {
    $("import-box").hidden = true;
    $("add-owner").value = settings.ownerGamertag || "";
    $("add-name").focus();
  }
});
$("add-cancel").addEventListener("click", () => { $("add-form").hidden = true; });
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
    $("add-form").hidden = true;
    await refresh();
    openDetail(s, "console");
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
  if (failed) $("import-box").hidden = false;
  if (!$("import-box").hidden) loadImports();
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

$("import-toggle").addEventListener("click", () => {
  $("import-box").hidden = !$("import-box").hidden;
  if (!$("import-box").hidden) {
    $("add-form").hidden = true;
    loadImports();
  }
});
$("import-close").addEventListener("click", () => { $("import-box").hidden = true; });

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
  $("version").textContent = st.version || "";
  if (st.mode === "signedIn") {
    enterApp(st);
    return;
  }
  $("auth-view").hidden = false;
  $("app-view").hidden = true;
  $("account-toggle").hidden = true;
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
  $("account-toggle").hidden = false;
  $("account-toggle").textContent = st.username + " ▾";
  $("account-name").textContent = st.username;
  $("reset-user").value = st.username;
  api("/api/settings").then((s) => { settings = s || {}; }).catch(() => {});
  refresh();
  timers.forEach(clearInterval);
  timers = [
    setInterval(refresh, 2000),
    setInterval(() => { if (selected && tab === "players" && !document.hidden) loadPlayers(); }, 3000),
    setInterval(() => { if (selected && tab === "backups" && !document.hidden) loadBackups(); }, 3000),
  ];
  if (!st.setupDone) openGuide(1);
}

// signedOut goes back to the sign-in page and forgets what was on screen.
function signedOut() {
  if (!signedIn) return;
  signedIn = false;
  timers.forEach(clearInterval);
  timers = [];
  closeConsole();
  resetSettings();
  resetBackups();
  selected = null;
  settings = {};
  guideInfo = null;
  gamertagWarned = false;
  for (const id of ["detail", "account-box", "guide", "import-box", "add-form", "password-msg"]) $(id).hidden = true;
  for (const f of ["password-form", "add-form"]) $(f).reset();
  for (const id of ["servers", "online", "allowlist", "operators", "attempts", "console-log", "import-list", "import-results", "guide-checklist"]) $(id).replaceChildren();
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

$("account-toggle").addEventListener("click", () => {
  $("account-box").hidden = !$("account-box").hidden;
  if (!$("account-box").hidden) $("account-box").scrollIntoView({ behavior: "smooth" });
});
$("account-close").addEventListener("click", () => { $("account-box").hidden = true; });
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
$("guide-open").addEventListener("click", () => { $("account-box").hidden = true; openGuide(1); });

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
  $("guide").scrollIntoView({ behavior: "smooth" });
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
  $("guide").hidden = true;
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
$("guide-import").addEventListener("click", () => { $("import-toggle").click(); $("import-box").scrollIntoView({ behavior: "smooth" }); });
$("guide-add").addEventListener("click", () => { $("add-toggle").click(); $("add-form").scrollIntoView({ behavior: "smooth" }); });

// ---- start ----

start();
