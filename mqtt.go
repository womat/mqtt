// Package mqtt provides a thread-safe MQTT client handler for Go.
//
// This package wraps the Eclipse Paho MQTT library to provide a simple, safe interface
// for connecting to a broker, publishing messages, and handling reconnections.
//
// Features:
//   - Thread-safe Handler for a single MQTT client
//   - Automatic reconnect and retry on connection loss
//   - Synchronous publish with timeout support, refused while no connection is open
//   - Optional callbacks for connection and disconnection events
//   - Optional logger for an initial connect that does not succeed in time
//   - Safe initialization and shutdown of the client
//
// # Example usage
//
// The same steps as Example_usage in example_test.go:
//
//	h, err := mqtt.New("tcp://broker:1883", "sensor-livingroom",
//	    mqtt.WithLogger(slog.Default()),
//	    mqtt.WithOnConnected(func() { slog.Info("MQTT connected") }),
//	    mqtt.WithOnConnectionLost(func(err error) { slog.Warn("MQTT connection lost", "error", err) }),
//	)
//	if err != nil {
//	    log.Fatal(err) // ErrInvalidBroker: the URL cannot be parsed
//	}
//	defer h.Disconnect()
//
//	err = h.Publish(mqtt.Message{
//	    Topic:   "sensors/livingroom/temperature",
//	    Payload: []byte("22.5"),
//	    Qos:     1,
//	})
//	if err != nil {
//	    log.Println("publish failed:", err)
//	}
//
// Publish only hands a message to the broker while a connection is actually
// open; otherwise it returns ErrNotConnected rather than dropping the message
// silently. Use IsConnectionOpen to ask for that state, not IsConnected.
package mqtt

import (
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	mqttlib "github.com/eclipse/paho.mqtt.golang"
)

const (
	// connectTimeout defines the maximum duration to wait for the initial broker connection.
	// After this timeout, the handler will retry connecting in the background.
	connectTimeout = 5 * time.Second

	// retryInterval defines the wait time between automatic reconnect attempts
	// when the connection to the broker is lost.
	retryInterval = 2 * time.Second

	// publishTimeout defines the maximum duration to wait for a Publish() token to complete.
	// If the token is not done within this time, Publish() returns an error.
	publishTimeout = 5 * time.Second

	// quiesce is the duration in milliseconds to wait during Disconnect()
	// for any pending work to complete.
	quiesce = 250
)

var (
	ErrClientNotInitialized = errors.New("mqtt client not initialized")
	ErrTopicEmpty           = errors.New("mqtt topic must not be empty")
	ErrInvalidQos           = errors.New("mqtt qos must be 0, 1 or 2")
	ErrNotConnected         = errors.New("mqtt not connected to the broker")
	ErrTimeout              = errors.New("publish timeout")
	ErrInvalidBroker        = errors.New("mqtt broker URL cannot be parsed")
)

// Handler manages a thread-safe MQTT client connection.
type Handler struct {
	mu               sync.Mutex
	client           mqttlib.Client
	logger           *slog.Logger
	onConnected      func()
	onConnectionLost func(err error)
}

// Message contains the properties of the mqtt message
type Message struct {
	Topic    string
	Payload  []byte
	Qos      byte
	Retained bool
}

// Option configures a Handler.
type Option func(*Handler)

// New creates and initializes a new MQTT Handler for the given broker.
//
// It sets up the client with automatic reconnect and retry on connection loss.
// The initial connection is attempted synchronously with a timeout. If it fails
// or times out, the Handler is still returned and will retry in the background,
// so that a service can start while its broker is down.
//
// Optional callbacks for connection events can be provided via opts.
//
// Parameters:
//   - broker:		the MQTT broker URL (e.g., "tcp://localhost:1883"); credentials
//     go into the URL ("tcp://user:password@host:1883"), and the schemes
//     ssl://, tls:// and mqtts:// connect with TLS
//   - clientID: 	a unique client identifier
//   - opts: 		optional functional options, e.g. WithOnConnected, WithOnConnectionLost
//
// Returns:
//   - *Handler: the initialized MQTT Handler, ready to use
//   - error:    ErrInvalidBroker if broker cannot be parsed, otherwise nil; a
//     failed initial connection is not an error here, it is retried in the
//     background. Pass WithLogger to see it, or ask IsConnectionOpen whether
//     the broker was actually reached.
func New(broker, clientID string, opts ...Option) (*Handler, error) {
	h := &Handler{}

	for _, opt := range opts {
		opt(h)
	}

	mqttOpts := mqttlib.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(retryInterval).
		SetConnectionLostHandler(func(_ mqttlib.Client, err error) {
			if h.onConnectionLost != nil {
				h.onConnectionLost(err)
			}
		}).
		SetOnConnectHandler(func(_ mqttlib.Client) {
			if h.onConnected != nil {
				h.onConnected()
			}
		})

	// Paho drops a broker URL it cannot parse without a word, and a client
	// without one fails its connect once and never retries.
	if len(mqttOpts.Servers) == 0 {
		return nil, ErrInvalidBroker
	}

	client := mqttlib.NewClient(mqttOpts)
	h.client = client

	// With ConnectRetry the token completes only on success: Paho retries every
	// connect error itself and reports none of them, so a timeout is the only
	// failure New can see.
	if !client.Connect().WaitTimeout(connectTimeout) && h.logger != nil {
		h.logger.Warn("mqtt initial connect timed out, retrying in the background",
			"broker", redactURL(broker), "timeout", connectTimeout)
	}
	return h, nil
}

// redactURL returns broker with a password replaced by "xxxxx", so a broker URL that
// carries credentials (tcp://user:password@host) can be logged.
//
// Like Paho, it reads a URL without a scheme as tcp://; otherwise url.Parse would
// take "user" in user:password@host for the scheme and leave the password unredacted.
func redactURL(broker string) string {
	if !strings.Contains(broker, "://") {
		broker = "tcp://" + broker
	}
	u, err := url.Parse(broker)
	if err != nil {
		return "<unparsable broker URL>"
	}
	return u.Redacted()
}

// WithLogger sets an optional logger. New warns through it when the initial
// connect has not succeeded within its timeout; nothing else in this package
// logs. The warning names no cause: Paho retries every connect error, a
// rejected password included, without reporting it.
func WithLogger(l *slog.Logger) Option {
	return func(h *Handler) {
		h.logger = l
	}
}

// WithOnConnected sets a callback that is called when the client connects.
func WithOnConnected(fn func()) Option {
	return func(h *Handler) {
		h.onConnected = fn
	}
}

// WithOnConnectionLost sets a callback that is called when the connection is lost.
func WithOnConnectionLost(fn func(err error)) Option {
	return func(h *Handler) {
		h.onConnectionLost = fn
	}
}

// Disconnect safely ends the connection to the MQTT broker.
//
// This method is thread-safe and sets the internal client to nil before
// actually disconnecting, so that a concurrent Publish sees the client as
// uninitialized.
//
// The disconnect uses a quiesce period to allow pending work to complete.
// If no client is initialized, this method does nothing.
func (m *Handler) Disconnect() {
	m.mu.Lock()
	client := m.client
	m.client = nil
	m.mu.Unlock()

	if client != nil {
		client.Disconnect(quiesce)
	}
}

// current returns the client, or nil once Disconnect has run.
func (m *Handler) current() mqttlib.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

// Publish sends a message to the MQTT broker synchronously.
// It waits up to publishTimeout for the broker to acknowledge the message.
//
// The message is only handed to the client while a connection is actually open;
// otherwise Publish returns ErrNotConnected. That guard is not redundant: while
// Paho is reconnecting it discards a QoS 0 publish without reporting an error,
// and stores a QoS 1 or 2 publish in a session that the next connect clears.
// Without the guard, both cases would look like success or like a timeout for a
// message that is already gone.
func (m *Handler) Publish(msg Message) error {
	if msg.Topic == "" {
		return ErrTopicEmpty
	}
	if msg.Qos > 2 {
		return ErrInvalidQos
	}

	client := m.current()
	if client == nil {
		return ErrClientNotInitialized
	}
	if !client.IsConnectionOpen() {
		return ErrNotConnected
	}

	token := client.Publish(msg.Topic, msg.Qos, msg.Retained, msg.Payload)
	if !token.WaitTimeout(publishTimeout) {
		return ErrTimeout
	}
	return token.Error()
}

// IsConnected reports whether the MQTT client considers itself connected.
//
// Because New enables both auto-reconnect and connect-retry, this is true while
// a connection is merely pending as well — including a first connect that has
// never succeeded. It answers "is this handler still trying?", not "is the
// broker reachable?"; for the latter use IsConnectionOpen.
func (m *Handler) IsConnected() bool {
	client := m.current()
	return client != nil && client.IsConnected()
}

// IsConnectionOpen reports whether a connection to the broker is open right now.
// Unlike IsConnected it is false while a connect or reconnect is still pending,
// which makes it the state to check before publishing.
func (m *Handler) IsConnectionOpen() bool {
	client := m.current()
	return client != nil && client.IsConnectionOpen()
}
