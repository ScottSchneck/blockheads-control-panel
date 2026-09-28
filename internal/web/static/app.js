"use strict";
// Panel page: server list, add, start/stop/update, and per server a Players
// tab (online, allowlist, operators, tried to join) and a live Console tab.

const $ = (id) => document.getElementById(id);
const labels = {
  running: "Running", starting: "Starting", stopping: "Stopping", stopped: "Stopped",
  installing: "Installing", updating: "Updating", crashed: "Crashed", error: "Needs attention",
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
  const busy = ["installing", "updating", "starting", "stopping"].includes(s.state);
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
      el("button", { onclick: confirmUpdate(s), disabled: s.state === "installing" || s.state === "updating" }, "Update"),
      el("button", { onclick: () => openDetail(s, "players") }, "Players"),
      el("button", { onclick: () => openDetail(s, "settings") }, "Settings"),
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
  try {
    const list = await api("/api/servers");
    $("servers").replaceChildren(...list.map(renderServer));
    $("empty").hidden = list.length > 0;
    if (selected) {
      const s = list.find((x) => x.id === selected);
      if (s) $("detail-name").textContent = s.name;
    }
  } catch (err) {
    $("servers").replaceChildren(el("p", { class: "error" }, "Can't reach the panel: " + err.message));
  }
}

// ---- detail: tabs ----

function openDetail(s, which) {
  if (selected !== s.id) {
    if (settingsDirty() && !confirm("Leave without saving your settings changes?")) return;
    closeConsole();
    resetSettings();
  }
  selected = s.id;
  $("detail-name").textContent = s.name;
  $("detail").hidden = false;
  showTab(which);
  $("detail").scrollIntoView({ behavior: "smooth" });
}

function showTab(which) {
  tab = which;
  for (const t of ["players", "settings", "console"]) {
    $("tab-" + t).setAttribute("aria-selected", String(t === which));
    $("pane-" + t).hidden = t !== which;
  }
  if (which === "console") openConsole();
  else if (which === "settings") { if (!settingsView || !settingsDirty()) loadSettings(); }
  else loadPlayers();
}

for (const b of document.querySelectorAll(".tabs button")) {
  b.addEventListener("click", () => showTab(b.dataset.tab));
}

$("detail-close").addEventListener("click", () => {
  if (settingsDirty() && !confirm("Close without saving your settings changes?")) return;
  resetSettings();
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

function btn(text, onclick, cls) {
  return el("button", { class: cls || "", onclick }, text);
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

// ---- start ----

api("/api/info").then((i) => { $("version").textContent = i.version; }).catch(() => {});
api("/api/settings").then((s) => { settings = s || {}; }).catch(() => {});
refresh();
setInterval(refresh, 2000);
setInterval(() => { if (selected && tab === "players" && !document.hidden) loadPlayers(); }, 3000);
