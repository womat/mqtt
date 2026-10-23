# mqtt

[![CI](https://github.com/womat/mqtt/actions/workflows/ci.yml/badge.svg)](https://github.com/womat/mqtt/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/womat/mqtt.svg)](https://pkg.go.dev/github.com/womat/mqtt)

```sh
go get github.com/womat/mqtt
```

This module used to be part of
[github.com/womat/golib](https://github.com/womat/golib) as `golib/mqtt`. The
API is unchanged; only the import path differs. The copy in golib is
deprecated and is removed there in golib v2. Versions `v0.1.0` and `v0.2.0` of
this repository are an unrelated draft from 2021 with a different API; use
`v1.0.0` or later.

A thread-safe wrapper around the Eclipse Paho MQTT client for the one thing
the services in this ecosystem do with MQTT: connect to a broker and publish
telemetry. Reconnecting is left to Paho, publishing is synchronous, and the
handler can be shared across goroutines.

Read [Delivery model](#delivery-model) before you build on it. The short
version: `Publish` refuses to send while no connection is open, because Paho
would otherwise discard the message and call it a success.

## API

| Function | Comment |
|---|---|
| `func New(broker, clientID string, opts ...Option) (*Handler, error)` | creates the client and attempts one connect; the only error is `ErrInvalidBroker`, see [`New` fails only on an unparsable URL](#new-fails-only-on-an-unparsable-url) |
| `func WithLogger(l *slog.Logger) Option` | the only thing that reports a failed initial connection |
| `func WithOnConnected(fn func()) Option` | called on every successful connect, including reconnects |
| `func WithOnConnectionLost(fn func(err error)) Option` | called when an established connection drops |
| `func (m *Handler) Publish(msg Message) error` | synchronous publish; `ErrNotConnected` while no connection is open, otherwise waits up to 5 s for the token |
| `func (m *Handler) IsConnectionOpen() bool` | true only while actually connected — the state to check before publishing |
| `func (m *Handler) IsConnected() bool` | true while a connect or reconnect is merely pending as well, see [Two connection states](#two-connection-states) |
| `func (m *Handler) Disconnect()` | disconnects with a 250 ms quiesce period; the handler is unusable afterwards |
| `type Message struct{ Topic string; Payload []byte; Qos byte; Retained bool }` | the message to publish |

Sentinel errors: `ErrTopicEmpty`, `ErrInvalidQos`, `ErrClientNotInitialized`,
`ErrNotConnected`, `ErrTimeout`, `ErrInvalidBroker`.

Fixed, not configurable: connect timeout 5 s, retry interval of the initial
connect 2 s, reconnect after a lost connection with Paho's backoff from 1 s up
to 1 min (Paho's default cap is 10 min), publish timeout 5 s, quiesce 250 ms
(the constants at the top of `mqtt.go`). Everything else is the Paho
default, notably `CleanSession: true`, `KeepAlive: 30s` and an in-memory
message store.

```go
h, err := mqtt.New("tcp://broker:1883", "clientID",
    mqtt.WithLogger(slog.Default()),
    mqtt.WithOnConnected(func() { slog.Info("MQTT connected") }),
    mqtt.WithOnConnectionLost(func(err error) { slog.Warn("MQTT lost", "error", err) }),
)
if err != nil {
    log.Fatal(err) // ErrInvalidBroker: the URL cannot be parsed
}
defer h.Disconnect()

err = h.Publish(mqtt.Message{Topic: "sensors/temperature", Payload: []byte("22.5"), Qos: 1})
```

## Broker URL

Authentication and TLS come from the broker URL, which `New` hands to Paho
unchanged:

| URL | Effect |
|---|---|
| `tcp://host:1883` (or `mqtt://`) | plain TCP |
| `tcp://user:password@host:1883` | username and password from the URL |
| `ssl://host:8883` (or `tls://`, `mqtts://`) | TLS with Go's default configuration: the server certificate is verified against the system roots |
| `ws://host/mqtt`, `wss://host/mqtt` | MQTT over WebSocket |

A password in the URL never reaches a log: everything this package logs goes
through `redactURL`, which replaces it with `xxxxx` — also in a URL without a
scheme, which Paho reads as `tcp://`. Keep the URL itself out of
`config.yaml` in plain text — reference an environment variable instead.

A URL that cannot be parsed — typically a password with an unescaped `%` —
makes `New` return `ErrInvalidBroker`. Paho would otherwise drop it silently
and never connect. Escape special characters in the password (`%25` for `%`).

## Delivery model

`New` sets `SetAutoReconnect(true)` and `SetConnectRetry(true)`, and those
two flags change how Paho answers questions
about the connection and what it does with a publish it cannot send right now.
Line references below are to `github.com/eclipse/paho.mqtt.golang v1.5.1`.

### Publish refuses while no connection is open

`Publish` checks `client.IsConnectionOpen()` and returns `ErrNotConnected`
before handing anything to Paho. That guard is the reason
the package is usable, not an extra safety net: without it, Paho reacts to a
publish during a reconnect as follows.

For **QoS 0** it short-circuits in `client.go:779`:

```go
case c.status.ConnectionStatus() == reconnecting && qos == 0:
    // message written to store and will be sent when connection comes up
    token.flowComplete()
    return token
```

The comment is wrong about its own code — the function returns *before*
`persistOutbound`, so nothing is stored and nothing is ever sent. The token
completes without an error, which is exactly what a successful publish looks
like. Telemetry published with QoS 0 would be lost silently, and only during
an outage, i.e. precisely when one wants to know.

For **QoS 1 and 2** the packet goes into the message store and the token stays
open, so the call would block for the full 5 s and then return `ErrTimeout`.
With the Paho default `CleanSession: true` that stored message is discarded on
the next successful connect (`client.go:290-293`), so the wait buys nothing.

With the guard, both cases fail immediately and honestly. A caller that wants
the message anyway has to buffer it itself — this package does not queue.

*Unverified, but likely:* on a reconnect Paho completes only subscribe tokens
(`client.go:575-576`), so a publish token left open there — and with it the
reserved message ID — is never released. The guard keeps this package from
producing such tokens in the first place.

### Two connection states

Paho's `IsConnected` (`client.go:197`) reports true in three cases, and `New`
enables all of them:

| state | `IsConnected()` | `IsConnectionOpen()` |
|---|---|---|
| connected | `true` | `true` |
| connecting, with `ConnectRetry` | `true` — even if no connection was ever established | `false` |
| reconnecting, with `AutoReconnect` | `true` | `false` |

So `IsConnected()` answers "is this handler still trying?" — useful to tell a
live handler from one that has been disconnected, useless as a broker health
check. `IsConnectionOpen()` is the one to ask, and the one `Publish` uses.

### `New` fails only on an unparsable URL

`New` returns an error only for a broker URL that cannot be parsed
(`ErrInvalidBroker`, see [Broker URL](#broker-url)): that is a configuration
error, and Paho would never retry it. Everything else returns `nil`. A broker
that is unreachable, a DNS name that does not resolve, credentials that would
be rejected — none of it reaches the caller through the return value. That is
deliberate: a service should start even when its broker is down, and Paho
retries in the background.

With `WithLogger`, `New` logs a warning with the broker when the initial
connect has not succeeded within 5 s. The warning names no cause: with
`ConnectRetry`, Paho retries every connect error every 2 s without reporting
it (`client.go:263-275`) — an unreachable host, a name that does not resolve
and a rejected password look the same, a connection that never opens. To
decide programmatically whether the broker was reached, call
`IsConnectionOpen()` after `New`.

`New` still blocks for up to 5 s at startup when the broker does not answer.

### Disconnect is final

`Disconnect` sets the internal client to `nil`. Every
later `Publish` returns `ErrClientNotInitialized`, and there is no reconnect
path: a new handler has to be constructed. The error name is misleading, since
an uninitialised client cannot otherwise occur — `New` always leaves one
behind.

## Validation

`Publish` rejects an empty topic (`ErrTopicEmpty`) and a QoS above 2
(`ErrInvalidQos`, which Paho would otherwise encode into a malformed packet
that makes the broker drop the connection). Wildcard characters (`+`, `#`) in
a publish topic are **not** rejected.

## Testing

```sh
go test -race ./...
MQTT_TEST_BROKER=tcp://localhost:1883 go test -count=1 -v ./...
```

Coverage is 100 % of statements without a broker. `mqtt_test.go` runs without
one: it substitutes a fake for the `mqtt.Client` interface, which is what makes
the states above testable at all: a fake in the reconnecting state
(`IsConnected() == true`, `IsConnectionOpen() == false`) is how the silent
QoS 0 loss is pinned down. `New` itself runs against a fake broker in the
test, a local listener that answers each CONNECT with CONNACK: it checks the
callbacks, a publish arriving on the wire, the reconnect after a dropped
connection, a first connect that succeeds only on a retry, and the warning
about an initial connect that times out — without the password from the URL.
That last test waits the full 5 s.

`mqtt_example_test.go` is the runnable version of the package documentation
and needs a real broker; it skips unless `MQTT_TEST_BROKER` names one.

## Not addressed

Known and deliberately left alone, so that a future version does not have to
rediscover it:

- No client certificates and no TLS configuration of its own: TLS always uses
  Go's defaults (system roots, server certificate verified), so a broker that
  wants mutual TLS or a private CA not in the system store cannot be used.
  Username and password work through the URL, see [Broker URL](#broker-url).
- No `Subscribe`. The package doc calls this an "MQTT client handler", but it
  only publishes.
- No buffering. A message published while the connection is down is rejected,
  not queued.
- The `IsConnectionOpen` guard in `Publish` leaves a window: if the connection
  drops between the check and Paho's `Publish`, a QoS 0 message is lost
  silently after all. Closing it would take a change in Paho; QoS 1 or 2 ends
  in `ErrTimeout` instead.
- Connect timeout, retry interval, reconnect cap, publish timeout, quiesce and Paho's own
  `CleanSession`/`KeepAlive` are compiled-in constants rather than options,
  unlike the rest of the repository.
- `New` blocks for up to 5 s at startup when the broker is unreachable, and
  cannot report that through its return value without breaking its signature.
- A handler is unusable after `Disconnect`, and the error it then returns is
  called `ErrClientNotInitialized`.
- Wildcards in a publish topic are not rejected.
