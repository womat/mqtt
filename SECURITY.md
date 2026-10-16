# Security

mqtt is a library: a vulnerability in it is a vulnerability in every program that uses it, among them
[tadl](https://github.com/womat/tadl) and [s0meter](https://github.com/womat/s0meter). It handles broker
credentials, which travel in the broker URL and must never reach a log. Reports of security issues are taken
seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through GitHub instead:
**Security → Report a vulnerability** ([direct link](https://github.com/womat/mqtt/security/advisories/new)).

Helpful details:

- the affected version (the `github.com/womat/mqtt` line in your `go.mod`)
- steps to reproduce, ideally a small test
- what an attacker could achieve with it

You will usually get an answer within a week. mqtt is a spare-time project, so no fixed response time can be
promised.

## Supported versions

Security fixes are made for the latest release only. Update with `go get github.com/womat/mqtt@latest`.
Versions `v0.1.0` and `v0.2.0` are an unrelated draft from 2021, retracted, and receive no fixes.
