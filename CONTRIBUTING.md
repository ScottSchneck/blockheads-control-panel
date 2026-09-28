# Contributing

Thanks for helping. The panel is meant for families and small groups who
aren't server admins, so the bar for every change is: does it keep things
simple for them?

## Ways to help

- **Test on a console.** Xbox and PlayStation reports are the most useful
  right now. Use the "Console test report" issue template.
- **Report bugs** with the version and the relevant log lines.
- **Code.** Open an issue first for anything bigger than a small fix, so we
  can agree on the approach before you spend time on it.

## Development

You need Go 1.26 or newer.

```bash
go mod tidy
make check          # vet, gofmt check, tests with the race detector
make build fakeconsole
sh scripts/smoke.sh # joins the server list 40 times per connection type
```

The fake console needs the server to run with `AUTH_OFF=true`, because it has
no Xbox account. Never use that setting outside testing.

### Layout

| Path | What's there |
| --- | --- |
| `cmd/blockheads` | The main program |
| `internal/servers` | Server manager: install, run, update and back up game servers |
| `internal/web` | Web panel: API, sign-in and the page (`static/`) |
| `internal/serverlist` | Built-in DNS and the console server list |
| `tools/fakeconsole` | Test client that pretends to be a console |
| `docs/` | Design and test notes |
| `deploy/` | Example files for running it |

## Guidelines

- Run `gofmt` and keep `make check` passing.
- Add a test for new behaviour where you can. The DNS rules, for example, are
  all covered by `dns_test.go`.
- Write log messages and settings help for a parent, not a network engineer:
  say what happened and what to do about it.
- Don't add anything that downloads or bundles Minecraft server software into
  the repository or image. Servers download it from the official source.
- Pull requests should say what changed and how it was tested.

## License of contributions

The project is GPL-3.0. By contributing you agree your contribution is
licensed under the same terms.
