# Console server list: proof-of-concept notes

The console server list (`internal/serverlist`) started as a stand-alone proof
of concept, tested on a Nintendo Switch (Minecraft 1.26.50) in September 2026
across four builds. This page records what was learned, so the reasons behind
the defaults aren't lost.

## How it works

1. The console looks up a featured-server name such as `geo.hivebedrock.network`.
2. The built-in DNS answers with the panel's address.
3. The console joins the panel, which spawns it into an empty world and shows
   a menu of your servers.
4. The player picks one and the panel sends a Transfer packet with that
   server's address and port.

## Results

| Question | Result |
| --- | --- |
| Does the built-in DNS catch the featured names? | Yes. The Switch looks them all up when Minecraft opens |
| Is Xbox sign-in verified? | Yes: gamertag and XUID arrive signed |
| Do the menu and transfer work? | Yes, including transfers to NetherNet game servers |
| Is there a trust prompt? | Yes, once per game server, from the NetherNet game server itself, not from the list |
| Can the redirect use NetherNet? | No. The Switch rejects the HTTPS certificate for another company's domain (a valid one is impossible to get). It pings over plain HTTP but always joins over RakNet. Offering NetherNet on a name doesn't break anything: the console falls back to RakNet |
| How long until the menu appears? | About 19 s on the Switch, against 32 s for the public BedrockConnect server. The handshake finishes in about 2 s; the rest is the Switch loading its world |

After changing a console's DNS, restart the console, or it keeps using cached
answers.

## Why these defaults

- **`CONNECTION=raknet`**: the only connection type consoles complete through
  a redirect today.
- **NetherNet stays built in** behind `CONNECTION=nethernet|both` and
  `NETHERNET_NAMES`, so it can be re-tested after Minecraft updates without a
  new build. If consoles ever drop RakNet for featured servers, the redirect
  stops working for every tool, BedrockConnect included. The design's fallback
  is LAN broadcast and Friends-tab broadcast, which don't depend on it.
- **Terrain goes out right after the view distance is agreed.** In build 1 the
  terrain was sent after the console reported it had spawned. The console
  waits for terrain before it finishes spawning, so both sides waited until
  it timed out (about 18 s).
- **`LOAD_PRESET=bedrockconnect`** copies BedrockConnect's empty world
  (survival at height 0, terrain with StartGame, its chunk format). It made no
  difference on the Switch and is kept only for comparison testing.

## The spawn race in gophertunnel

Testing with the fake console showed about 1 join in 10 stalling over
loopback. The cause is a timing gap in gophertunnel v1.62.0:
`Conn.StartGame` sends StartGame and flushes it, and only then marks the
client's view-distance request (RequestChunkRadius) as expected. A client
that answers inside that gap has its request set aside for `ReadPacket`
instead of handled, and the join waits until it times out.

`rescueViewDistance` in `session.go` works around it. It watches for the
request; if the library hasn't answered within 500 ms, it takes the set-aside
request with `ReadPacket` and answers it the way the library would. With it,
120 of 120 fake-console joins passed, with 6 rescued. Real consoles answer
more slowly than loopback, so they rarely hit the gap, but a busy host can
widen it.

This should be reported upstream. Once gophertunnel sets the expectation
before flushing, the workaround can be removed.
