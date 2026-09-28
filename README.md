# Blockheads Control Panel

A free, open-source, all-in-one panel for running Minecraft and other game
servers at home. One container runs your servers, a web panel for desktop and
phone, a tiny DNS server, and a console server list so Xbox, PlayStation and
Switch players can join without extra apps.

> **Status: early development.** The console server list and built-in DNS
> are tested on a Nintendo Switch and a PS5, and phase 1 (Bedrock at home)
> runs a family's servers day to day. See the [design](docs/design.md) for the
> full plan.

## What works today

- **Bedrock servers, run by the panel.** Add a server from the web page: the
  panel downloads the official Bedrock server from Mojang, gives it its own
  port and starts it. Start, stop, restart, a live console, players online,
  automatic restart after a crash, and servers that were running come back
  after a reboot.
- **Safe updates.** Update backs the server up first (to `/data/backups`),
  downloads the current version, keeps your worlds, `server.properties`,
  allowlist and permissions, and sets the execute bit itself, so
  "Permission denied: ./bedrock_server" can't happen.
- **Players page** for each server: who's online (message, kick, make
  operator), the allowlist (add, remove, turn on or off), operators, and
  **Tried to join**: anyone the console menu sent to a server whose allowlist
  they aren't on shows up with an **Allow** button. Changes apply without a
  restart. Put your gamertag in **Add server** and you're added to the
  allowlist and made an operator on every new server.
- **Settings page** for each server: game mode, difficulty, cheats, what new
  players can do, max players, chat, skins, view distance and more, as
  labelled controls with a line of explanation each. Only the lines you change
  are rewritten, and the panel offers to restart the server to apply them.
  An advanced editor opens the whole `server.properties` for everything else.
- **Backups** on a schedule for each server (every hour up to once a day,
  keeping the number you choose), plus **Back up now**, **Download** and
  one-click **Restore**. Running servers are backed up without stopping them,
  using Bedrock's own save hold, so nobody gets kicked. A restore backs up the
  current world first, so it can be undone. Servers nobody has played on since
  their last backup are skipped. See [Backups](#backups).
- **Import from Crafty Controller** (or any folder of Bedrock servers). Each
  server is copied in with its worlds, settings, allowlist and operators, and
  keeps its port. The original is only read, never changed. See
  [Moving from Crafty](#moving-from-crafty).
- **Owner account** with a sign-in page, a setup guide the first time, and
  a password reset from the command line. Passwords are hashed with Argon2id;
  repeated wrong passwords lock that address out for 15 minutes.
- **The web panel** at `https://<container IP>:8443`, for desktop and phone:
  a dashboard of every server (status, players, version, last backup, a note
  when a new Bedrock version is out), and for each server an Overview,
  Players, Settings, Backups and Console. Two looks, each light or dark (or
  following the device): **Control Room**, compact with every server in one
  table, and **Treehouse**, big tiles and buttons and plain words ("Turn on",
  "Let in"). Chosen under your name and saved with your account. Fonts are
  built in, so it works without internet.
- **Built-in DNS.** It answers the console "featured server" names (The Hive,
  Lifeboat and others) with the panel's address, and forwards every other
  lookup for devices on your network. It never acts as an open resolver for
  the internet.
- **Console server list.** This replaces BedrockConnect. A console that joins
  a featured server lands in a menu of *your* servers and is transferred to
  the one picked. Xbox sign-in is checked and logged, but a console is never
  turned away because its sign-in format is new; the game server does the
  real sign-in and allowlist checks.
- **Connect to any server from the console.** Like BedrockConnect, the menu
  has "Connect to a server…" for typing an address. Players can save servers
  to their own list and remove them again. The house servers stay under the
  owner's control.
- **RakNet by default, NetherNet ready.** Consoles join the list over RakNet
  today. NetherNet (Minecraft's newer connection type) is built in behind a
  setting in case consoles stop falling back to RakNet. See
  [the proof-of-concept notes](docs/proof-of-concept.md) for why.

## Coming next

Phase 2 adds more user accounts with roles (so a kid can run their own
server) and friends joining from outside; Java, Hytale and more follow
later.

## Install

You need Docker and a free IP address on your home network for the container,
because consoles only look for DNS on port 53 and the list on port 19132, and
each game server needs its own ports.

Every push to `main` publishes a ready-built image, so there's nothing to
compile. On Unraid (adjust the IP and folders for your network):

```bash
mkdir -p /mnt/user/appdata/blockheads/config /mnt/user/appdata/blockheads/data
cp deploy/servers.example.json /mnt/user/appdata/blockheads/config/servers.json   # then edit it
chown -R 99:100 /mnt/user/appdata/blockheads
docker run -d --name blockheads --restart unless-stopped --stop-timeout 60 \
  --network br0 --ip 192.168.1.60 \
  -e LIST_IP=192.168.1.60 -e TZ=America/Denver \
  -v /mnt/user/appdata/blockheads/config:/config \
  -v /mnt/user/appdata/blockheads/data:/data \
  ghcr.io/scottschneck/blockheads-control-panel:latest
```

`servers.json` is optional: it's for servers the panel doesn't run itself.

**Open the panel** at `https://192.168.1.60:8443`. Your browser warns about the
certificate once (it's made by the panel itself); continue anyway.

The first time, the panel asks you to **create the owner account**. To prove
it's you and not someone else on your network, it wants the setup code from
the log:

```bash
docker logs blockheads 2>&1 | grep -i "setup code"
```

(Upgrading from a version with a single panel password? That password is the
setup code.) A short setup guide follows: your gamertag, how consoles join,
your servers, and a checklist. You stay signed in on each browser for 30 days
after you last used it.

**Forgot the password?** Run `docker exec blockheads blockheads reset-password`
and choose "Forgot your password?" on the sign-in page. The code works once,
for an hour, and signs out every browser.

`--stop-timeout 60` gives game servers time to save their worlds when the
container stops.

To update: `docker pull ghcr.io/scottschneck/blockheads-control-panel:latest`,
then remove and re-run the container. To build it yourself instead, run
`docker build -t blockheads-control-panel .` in this folder.

**At home, try LAN Games first.** Consoles on the same network may find the
list by themselves under **Friends → LAN Games** (a PS5 does), with no DNS
change at all. Otherwise, set the console's DNS to the container's IP,
**restart the console**, open Minecraft, and join any featured server.

### Moving from Crafty

Mount Crafty's servers folder read-only at `/import` by adding this to the
`docker run` command (the path is binhex's Crafty on Unraid; adjust it for
yours):

```bash
  -v /mnt/user/appdata/binhex-crafty-4/crafty/servers:/import/crafty:ro \
```

Then, for each server:

1. **Stop it in Crafty** and leave it stopped (turn off its auto-start). Two
   copies can't share a port, and copying a running server can damage the
   world.
2. In the panel, press **Import**, tick the box to say it's stopped, check the
   name and port (it keeps its Crafty port when it can), and press **Import**.
   The server shows as *Importing* while it copies, then *Stopped*, or starts
   straight away if you ticked **Start when copied**.
3. If you port-forward the server for friends, point the router rule at the
   panel's IP instead of Crafty's.

Ports must be an even number from 19134 to 19198 (each server also uses the
next port up for IPv6). A server on 19132, Bedrock's default, gets a free port
instead, because 19132 is the console server list's.

If the console menu's `servers.json` has an entry with the same name as an
imported server, the menu shows only the panel's server, so there's nothing
to clean up there. The first **Update** of an imported server installs the
current release, since the panel doesn't know which version Crafty had.

Crafty's copy stays as it was. Once you're happy, remove the servers from
Crafty and the `/import` mount.

### Backups

Each server has a **Backups** tab. New and existing servers are backed up
once a day at 04:00, keeping the last 7; change that per server. Backups are
in `/data/backups/<server>` as ordinary `.tar.gz` files holding the worlds
and the settings files (`server.properties`, allowlist, permissions).

- **Automatic backups** skip a server nobody has played on since its last
  backup, so a quiet server's kept copies aren't all the same. A backup that
  fails is tried again 15 minutes later.
- **Back up now** makes one by hand; those stay until you delete them.
- **Restore** stops the server, backs up the current world ("Before a
  restore", the last 3 are kept), puts the backup back and starts the server
  again if it was running. Restoring a "Before an update" backup also puts
  back the server version from before that update.
- Set `TZ` (for example `-e TZ=America/Denver`) so "04:00" is your 4 am; the
  container is on UTC otherwise.

For copies somewhere else, have Unraid's backup tool (or anything else) copy
`/mnt/user/appdata/blockheads/data/backups`.

### `servers.json`

The same format as BedrockConnect's custom servers file:

```json
[
  { "name": "Survival", "address": "192.168.1.50", "port": 19134 }
]
```

`iconUrl` is optional. The file is re-read every time the menu opens, so edits
apply without a restart.

### Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `LIST_IP` | (required) | The container's IP. The DNS hands it to consoles, and the menu sends them to the panel's servers there |
| `PANEL_PASSWORD` | empty | Only used before the owner account exists, as the setup code (older versions used it as the panel password) |
| `WEB_PORT` | `8443` | Web panel port |
| `WEB_TLS` | `true` | Serve the panel over HTTPS with a self-signed certificate. `false` for plain HTTP behind your own reverse proxy |
| `STOP_TIMEOUT` | `30` | Seconds a game server gets to save and stop before it's forced |
| `PLAYER_SERVERS` | `true` | Players can connect to any server by address from the console menu and save it to their own list |
| `REQUIRE_SIGN_IN` | `false` | Turn away consoles whose Xbox sign-in can't be verified. Off by default because the game server checks sign-in itself |
| `SERVERS_FILE` | `/config/servers.json` | Extra servers for the console menu that the panel doesn't run (optional) |
| `TZ` | `UTC` | Time zone for scheduled backups, e.g. `America/Denver` |
| `IMPORT_DIR` | `/import` | Where other panels' server folders are mounted (read-only) for **Import** |
| `DATA_DIR` | `/data` | Game servers (`/data/servers`), backups (`/data/backups`), keys and certificates |
| `MENU_TITLE` | `Pick a server` | Title of the console menu |
| `LIST_NAME`, `LIST_SUBTITLE` | `Server List`, `Pick a server` | What consoles show for the list under Friends → LAN Games |
| `DNS_ENABLED` | `true` | Built-in DNS on port 53 |
| `DNS_UPSTREAM` | `1.1.1.1:53` | Where other lookups from home devices go |
| `PUBLIC_IP`, `DNS_ANSWER_INTERNET` | empty, `false` | Friends mode: answer the featured names for internet clients with your public IP |
| `CONNECTION` | `raknet` | `raknet`, `nethernet` or `both` |
| `NETHERNET_NAMES` | empty | With `both`, offer NetherNet only for these names |
| `SIGNALING` | `auto` | NetherNet join endpoint: `auto` (HTTPS and HTTP), `http` or `tls` |
| `EXTRA_SIGNALING_PORTS` | `80,443` | More TCP ports for the NetherNet endpoint; `none` for none |
| `RTC_PORT_MIN`, `RTC_PORT_MAX` | `19300`, `19399` | UDP ports for NetherNet gameplay |
| `LOAD_PRESET` | `standard` | How the empty menu world is built: `standard` or `bedrockconnect` |
| `LOG_LEVEL` | `info` | `debug` for more detail |

## Building from source

Go 1.26 or newer:

```bash
go mod tidy
make check build
```

`make fakeconsole` builds a test client that pretends to be a console, and
`sh scripts/smoke.sh` uses it to join the list many times. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

[GPL-3.0](LICENSE). Minecraft server software is never included; each server
is downloaded from the official source on your own machine.

This project is not affiliated with Mojang or Microsoft. Minecraft is a
trademark of Mojang Synergies AB.
