// Package mqtt provides a thread-safe MQTT client handler for Go.
//
// This package wraps the Eclipse Paho MQTT library to provide a simple, safe interface
// for connecting to a broker, publishing messages, and handling reconnections.
//
// Features:
// - Thread-safe Handler for a single MQTT client
// - Automatic reconnect and retry on connection loss
// - Synchronous publish with timeout support
// - Binary-safe logging of message payloads
// - Safe initialization and shutdown of the client
//
// Example usage:
//
//	import "yourmodule/mqtt"
//	import "time"
//
//	handler, err := mqtt.New("tcp://broker:1883", "clientID")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer handler.Disconnect()
//
//	msg := mqtt.Message{
//	    Topic:   "sensors/temperature",
//	    Payload: []byte("22.5"),
//	    Qos:     1,
//	}
//
//	if err := handler.Publish(msg); err != nil {
//	    log.Println("Publish failed:", err)
//	}
package mqtt

import (
	"errors"
	"log/slog"
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

// Handler manages a thread-safe MQTT client connection.
type Handler struct {
	mu     sync.Mutex
	client mqttlib.Client
	broker string
}

// Message contains the properties of the mqtt message
type Message struct {
	Topic    string
	Payload  []byte
	Qos      byte
	Retained bool
}

// New creates and initializes a new MQTT broker client Handler.
//
// The returned Handler is ready to use: it sets up a client for the specified broker
// and clientID, enables automatic reconnect and retry, and starts the initial connection.
//
// The initial connect is attempted synchronously with a timeout. If it fails or times out,
// the Handler is still returned and will retry connections automatically in the background.
//
// The Handler is thread-safe and can be used immediately for publishing messages.
//
// Parameters:
// - broker: the MQTT broker URL (e.g., "tcp://localhost:1883")
// - clientID: a unique client identifier
//
// Returns:
// - *Handler: the initialized MQTT Handler
// - error: only returned if client creation fails; initial connection errors are logged
func New(broker, clientID string) (*Handler, error) {
	h := &Handler{}

	opts := mqttlib.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(retryInterval).
		SetConnectionLostHandler(func(_ mqttlib.Client, err error) {
			slog.Warn("MQTT connection lost", "error", err)
		}).
		SetOnConnectHandler(func(_ mqttlib.Client) {
			slog.Info("MQTT connected", "broker", broker)
		})

	client := mqttlib.NewClient(opts)
	h.client = client
	h.broker = broker

	token := client.Connect()
	if !token.WaitTimeout(connectTimeout) {
		slog.Warn("Initial MQTT connect timed out, will retry in background")
		return h, nil
	}

	// Initially connect (non-blocking retries are handled by Paho)
	if err := token.Error(); err != nil {
		slog.Warn("Initial MQTT connect failed, will retry in background", "error", err)
	}

	return h, nil
}

// Disconnect safely ends the connection to the MQTT broker.
//
// This method is thread-safe and sets the internal client to nil before
// actually disconnecting, so that concurrent Publish or Connect calls
// will see the client as uninitialized.
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

	return
}

// Publish sends a message to the mqtt broker. If the connection is lost, it will try to reconnect.
// If the connection can't be established, it will return an error.
// The message is sent asynchronously. If the message can't be sent, it will be logged.
func (m *Handler) Publish(msg Message) error {
	if msg.Topic == "" {
		return errors.New("mqtt topic must not be empty")
	}

	m.mu.Lock()
	client := m.client
	m.mu.Unlock()

	if client == nil {
		return errors.New("mqtt client not initialized")
	}

	slog.Debug("Publishing MQTT message",
		"topic", msg.Topic,
		"qos", msg.Qos,
		"payload_len", len(msg.Payload),
	)

	token := client.Publish(msg.Topic, msg.Qos, msg.Retained, msg.Payload)

	if !token.WaitTimeout(publishTimeout) {
		slog.Error("MQTT publish timeout", "topic", msg.Topic)
		return errors.New("publish timeout")
	}
	if err := token.Error(); err != nil {
		slog.Error("MQTT publish failed", "topic", msg.Topic, "error", err)
		return err
	}

	return nil
}

// IsConnected reports whether the MQTT client is currently connected.
// This is a snapshot and does not indicate pending auto-reconnects.
func (m *Handler) IsConnected() bool {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()

	if client == nil {
		return false
	}
	return client.IsConnected()
}
