# Blockheads Control Panel

A free, open-source, all-in-one panel for running Minecraft and other game
servers at home. One container runs your servers, a web panel for desktop and
phone, a tiny DNS server, and a console server list so Xbox, PlayStation and
Switch players can join without extra apps.

> **Status: early development.** The console server list and built-in DNS
> are tested on a Nintendo Switch and a PS5. The server manager and a first,
> bare-bones web page are new (phase 1 is in progress). See the
> [design](docs/design.md) for the full plan.

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
- **A first web page** at `https://<container IP>:8443`, for desktop and phone.
  It's bare-bones for now; the full design comes next.
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

## Coming in phase 1

A settings UI instead of editing `server.properties`, players and allowlist,
scheduled backups and restore, a setup wizard with owner and user accounts,
the full web design, and importing servers from Crafty Controller. Java,
Hytale and more follow in later phases.

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
  -e LIST_IP=192.168.1.60 \
  -v /mnt/user/appdata/blockheads/config:/config \
  -v /mnt/user/appdata/blockheads/data:/data \
  ghcr.io/scottschneck/blockheads-control-panel:latest
```

`servers.json` is optional: it's for servers the panel doesn't run itself.

**Open the panel** at `https://192.168.1.60:8443`. Your browser warns about the
certificate once (it's made by the panel itself); continue anyway. Sign in
with any username and the panel password: the first start creates one and
prints it in the log (`docker logs blockheads | grep password`), and it's kept
in `/data/panel-password`. Set `PANEL_PASSWORD` to choose your own.

`--stop-timeout 60` gives game servers time to save their worlds when the
container stops.

To update: `docker pull ghcr.io/scottschneck/blockheads-control-panel:latest`,
then remove and re-run the container. To build it yourself instead, run
`docker build -t blockheads-control-panel .` in this folder.

**At home, try LAN Games first.** Consoles on the same network may find the
list by themselves under **Friends → LAN Games** (a PS5 does), with no DNS
change at all. Otherwise, set the console's DNS to the container's IP,
**restart the console**, open Minecraft, and join any featured server.

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
| `PANEL_PASSWORD` | generated | Password for the web panel. If unset, one is created and saved in `/data/panel-password` |
| `WEB_PORT` | `8443` | Web panel port |
| `WEB_TLS` | `true` | Serve the panel over HTTPS with a self-signed certificate. `false` for plain HTTP behind your own reverse proxy |
| `STOP_TIMEOUT` | `30` | Seconds a game server gets to save and stop before it's forced |
| `PLAYER_SERVERS` | `true` | Players can connect to any server by address from the console menu and save it to their own list |
| `REQUIRE_SIGN_IN` | `false` | Turn away consoles whose Xbox sign-in can't be verified. Off by default because the game server checks sign-in itself |
| `SERVERS_FILE` | `/config/servers.json` | Extra servers for the console menu that the panel doesn't run (optional) |
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
