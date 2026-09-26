# Blockheads Control Panel

A free, open-source, all-in-one panel for running Minecraft and other game
servers at home. One container runs your servers, a web panel for desktop and
phone, a tiny DNS server, and a console server list so Xbox, PlayStation and
Switch players can join without extra apps.

> **Status: early development.** The console server list and built-in DNS
> work and have been tested on a Nintendo Switch. The web panel and server
> manager are being built now (phase 1). See the [design](docs/design.md) for
> the full plan.

## What works today

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

Bedrock server manager (start, stop, console, logs, safe updates), a settings
UI instead of editing `server.properties`, players and allowlist, backups, a
setup wizard with an owner login, phone layout, and importing servers from
Crafty Controller. Java, Hytale and more follow in later phases.

## Try the console server list

You need Docker and a free IP address on your home network for the container,
because consoles only look for DNS on port 53 and the list on port 19132.

```bash
docker build -t blockheads-control-panel .
mkdir -p config data
cp deploy/servers.example.json config/servers.json   # then edit it
docker run -d --name blockheads --network br0 --ip 192.168.1.60 \
  -e LIST_IP=192.168.1.60 \
  -v "$PWD/config:/config" -v "$PWD/data:/data" \
  blockheads-control-panel
```

Replace `br0` with your macvlan or bridge network, and `192.168.1.60` with
the container's address. On Unraid, `br0` with a fixed IP works as-is. Make
sure the `config` and `data` folders are owned by 99:100 (`nobody:users`).

Then set the console's DNS to the container's IP, **restart the console**, open
Minecraft, and join any featured server.

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
| `LIST_IP` | (required) | The container's IP. The DNS hands it to consoles |
| `PLAYER_SERVERS` | `true` | Players can connect to any server by address from the console menu and save it to their own list |
| `REQUIRE_SIGN_IN` | `false` | Turn away consoles whose Xbox sign-in can't be verified. Off by default because the game server checks sign-in itself |
| `SERVERS_FILE` | `/config/servers.json` | The server list |
| `DATA_DIR` | `/data` | Keys and certificates the panel creates |
| `MENU_TITLE` | `Pick a server` | Title of the console menu |
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
