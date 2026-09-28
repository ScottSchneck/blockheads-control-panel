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
  if (selected !== s.id) closeConsole();
  selected = s.id;
  $("detail-name").textContent = s.name;
  $("detail").hidden = false;
  showTab(which);
  $("detail").scrollIntoView({ behavior: "smooth" });
}

function showTab(which) {
  tab = which;
  for (const t of ["players", "console"]) {
    $("tab-" + t).setAttribute("aria-selected", String(t === which));
    $("pane-" + t).hidden = t !== which;
  }
  if (which === "console") openConsole(); else loadPlayers();
}

for (const b of document.querySelectorAll(".tabs button")) {
  b.addEventListener("click", () => showTab(b.dataset.tab));
}

$("detail-close").addEventListener("click", () => {
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
