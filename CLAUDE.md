# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository layout

`github.com/womat/mqtt` is a thread-safe publish-only wrapper around the Eclipse Paho MQTT
client, split out of `github.com/womat/golib` (where it lived as `golib/mqtt`) with its history.
The copy in golib is deprecated and removed there in golib v2; consumers (`tadl`, `s0meter`)
move over one by one.

This repository existed before as an unrelated draft from 2021 with a different API (tags
`v0.1.0`, `v0.2.0`). The first commit after it removes the draft, the merge after that brings in
the golib history. The tags stay because the module proxy has them anyway; `v1.0.0` is the
first version of the current API.

## Commands

```sh
go test -race ./...
MQTT_TEST_BROKER=tcp://localhost:1883 go test -count=1 -v ./...   # example against a broker
golangci-lint run ./...
```

There is one branch, `main`; a version is an annotated tag `vX.Y.Z` on it.

**This repository always authenticates as `womat`.** The remote goes through the
`github-priv` host alias from `~/.ssh/config`; a fresh clone needs
`git remote set-url origin git@github-priv:womat/mqtt.git`.

## Architecture

`Handler` wraps a Paho client behind a mutex. Reconnecting is left to Paho; `Publish` is
synchronous and refuses to send while no connection is open (`IsConnectionOpen()`), because
Paho would otherwise drop a QoS 0 message and report success — README, *Delivery model*. The
tests substitute a fake for the `mqtt.Client` interface, which is how that state is tested
without a broker. The broker URL is redacted before it is logged; the password never reaches
a log. The logger comes in through `WithLogger` (no `slog.Default()`), configuration through
functional options.

## Conventions

- The package doc comment with an `# Example usage` section lives in `mqtt.go`; it shows
  the same steps as `Example_usage` in `example_test.go`, which the compiler checks;
  `README.md` covers the package and ends with *Not addressed* — decisions against a fix go
  there. Keep both updated when the API changes.
- Sentinel errors are exported vars compared with `errors.Is`; their messages start with
  `mqtt ` (except `ErrTimeout`). Don't change an existing message — services consume this
  module.
- `Disconnect()` must be deferred by callers.
- Commit subjects follow Conventional Commits, `type(scope): description`, types `feat`,
  `fix`, `docu`, `chore`, `refactor`.

## Known state

**CI:** `.github/workflows/ci.yml`, on pushes to `main` and pull requests: gofmt, `go fix`, vet,
golangci-lint, govulncheck, `go test -race -cover`, and a cross-compile for the Raspberry Pi
targets, Windows and macOS. Actions are pinned to a commit SHA with the release in a comment;
dependabot updates the pins and the Go dependencies in one group. The module declares `go 1.27`,
the same as golib and its consumers; keep it in step.
