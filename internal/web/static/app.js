"use strict";
// Bare-bones panel page: server list, add, start/stop/update, live console.

const $ = (id) => document.getElementById(id);
const labels = {
  running: "Running", starting: "Starting", stopping: "Stopping", stopped: "Stopped",
  installing: "Installing", updating: "Updating", crashed: "Crashed", error: "Needs attention",
};
let consoleID = null;
let consoleStream = null;

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

function action(id, what) {
  return async (ev) => {
    ev.target.disabled = true;
    try { await api(`/api/servers/${encodeURIComponent(id)}/${what}`, { method: "POST" }); }
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
      el("button", { onclick: () => openConsole(s) }, "Console")));
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
    if (consoleID) {
      const s = list.find((x) => x.id === consoleID);
      if (s) $("console-name").textContent = s.name;
    }
  } catch (err) {
    $("servers").replaceChildren(el("p", { class: "error" }, "Can't reach the panel: " + err.message));
  }
}

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

function openConsole(s) {
  if (consoleStream) consoleStream.close();
  consoleID = s.id;
  $("console-name").textContent = s.name;
  $("console-log").replaceChildren();
  $("console-error").hidden = true;
  $("console-panel").hidden = false;
  consoleStream = new EventSource(`/api/servers/${encodeURIComponent(s.id)}/console`);
  consoleStream.onmessage = (ev) => appendLine(ev.data);
  $("console-panel").scrollIntoView({ behavior: "smooth" });
  $("console-input").focus();
}

$("console-close").addEventListener("click", () => {
  if (consoleStream) consoleStream.close();
  consoleStream = null;
  consoleID = null;
  $("console-panel").hidden = true;
});

$("console-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const input = $("console-input");
  const command = input.value.trim();
  if (!command || !consoleID) return;
  try {
    await api(`/api/servers/${encodeURIComponent(consoleID)}/command`, { method: "POST", body: JSON.stringify({ command }) });
    input.value = "";
    $("console-error").hidden = true;
  } catch (err) {
    $("console-error").textContent = err.message;
    $("console-error").hidden = false;
  }
});

$("add-toggle").addEventListener("click", () => {
  $("add-form").hidden = !$("add-form").hidden;
  if (!$("add-form").hidden) $("add-name").focus();
});
$("add-cancel").addEventListener("click", () => { $("add-form").hidden = true; });
$("add-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  $("add-error").hidden = true;
  try {
    const s = await api("/api/servers", {
      method: "POST",
      body: JSON.stringify({ name: $("add-name").value, acceptEula: $("add-eula").checked }),
    });
    $("add-form").reset();
    $("add-form").hidden = true;
    await refresh();
    openConsole(s);
  } catch (err) {
    $("add-error").textContent = err.message;
    $("add-error").hidden = false;
  }
});

api("/api/info").then((i) => { $("version").textContent = i.version; }).catch(() => {});
refresh();
setInterval(refresh, 2000);
