# Security

The panel runs servers that kids play on and can be reachable from the
internet, so security problems matter a lot here.

## Reporting a problem

Please report it privately through GitHub's
[private vulnerability reporting](https://github.com/yourname/blockheads-control-panel/security/advisories/new),
not as a public issue. Include what you found, how to reproduce it, and what
someone could do with it. You should get a reply within a week.

## What's in scope

- The built-in DNS answering or forwarding lookups it shouldn't (for example,
  acting as an open resolver for the internet).
- Ways to bypass Xbox sign-in checks on the console server list.
- Once the web panel exists: anything that lets someone sign in, see or change
  things their role doesn't allow.

## Safe defaults

- The built-in DNS forwards lookups only for home-network addresses. Internet
  clients get answers only for the featured-server names, with rate limits.
- `AUTH_OFF=true` turns off Xbox sign-in checks and is for local testing only.
  The log warns when it's on.
- The container runs as user 99:100, not root.
