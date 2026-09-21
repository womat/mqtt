package mqtt

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	mqttlib "github.com/eclipse/paho.mqtt.golang"
)

// fakeToken is a token that is already complete when it is handed out,
// which is how paho behaves for a publish it decides not to send.
type fakeToken struct {
	err  error
	done chan struct{}
}

func newFakeToken(err error) *fakeToken {
	t := &fakeToken{err: err, done: make(chan struct{})}
	close(t.done)
	return t
}

func (t *fakeToken) Wait() bool                     { return true }
func (t *fakeToken) WaitTimeout(time.Duration) bool { return true }
func (t *fakeToken) Done() <-chan struct{}          { return t.done }
func (t *fakeToken) Error() error                   { return t.err }

// fakeClient records what the Handler asks of the client and lets a test
// pretend the connection is in any of the states paho distinguishes.
// Only the methods the Handler uses are implemented; the rest panic so that
// an unnoticed new call site shows up immediately.
type fakeClient struct {
	connected      bool // paho: true while connecting or reconnecting as well
	connectionOpen bool // paho: true only while actually connected
	publishErr     error
	publishCalls   int
	lastTopic      string
	lastQos        byte
	lastRetained   bool
	lastPayload    []byte
	disconnects    int
}

func (c *fakeClient) IsConnected() bool      { return c.connected }
func (c *fakeClient) IsConnectionOpen() bool { return c.connectionOpen }
func (c *fakeClient) Connect() mqttlib.Token { return newFakeToken(nil) }
func (c *fakeClient) Disconnect(uint)        { c.disconnects++ }

func (c *fakeClient) Publish(topic string, qos byte, retained bool, payload any) mqttlib.Token {
	c.publishCalls++
	c.lastTopic = topic
	c.lastQos = qos
	c.lastRetained = retained
	if b, ok := payload.([]byte); ok {
		c.lastPayload = b
	}
	return newFakeToken(c.publishErr)
}

func (c *fakeClient) Subscribe(string, byte, mqttlib.MessageHandler) mqttlib.Token {
	panic("Subscribe is not used by Handler")
}

func (c *fakeClient) SubscribeMultiple(map[string]byte, mqttlib.MessageHandler) mqttlib.Token {
	panic("SubscribeMultiple is not used by Handler")
}

func (c *fakeClient) Unsubscribe(...string) mqttlib.Token {
	panic("Unsubscribe is not used by Handler")
}

func (c *fakeClient) AddRoute(string, mqttlib.MessageHandler) {
	panic("AddRoute is not used by Handler")
}

func (c *fakeClient) OptionsReader() mqttlib.ClientOptionsReader {
	panic("OptionsReader is not used by Handler")
}

// connectedClient is a client that is fully connected to a broker.
func connectedClient() *fakeClient {
	return &fakeClient{connected: true, connectionOpen: true}
}

// reconnectingClient mimics paho during an automatic reconnect: IsConnected
// reports true because AutoReconnect/ConnectRetry are set, but no connection
// is actually open, so nothing published now reaches the broker.
func reconnectingClient() *fakeClient {
	return &fakeClient{connected: true, connectionOpen: false}
}

func TestPublishReconnectingIsNotSilentlyDropped(t *testing.T) {
	// A QoS 0 publish during a reconnect is discarded by paho without an
	// error. Publish must not report success for a message that is gone.
	for _, qos := range []byte{0, 1, 2} {
		client := reconnectingClient()
		h := &Handler{client: client}

		err := h.Publish(Message{Topic: "sensors/temperature", Payload: []byte("22.5"), Qos: qos})

		if !errors.Is(err, ErrNotConnected) {
			t.Errorf("qos %d: got error %v, want ErrNotConnected", qos, err)
		}
		if client.publishCalls != 0 {
			t.Errorf("qos %d: message was handed to the client although no connection is open", qos)
		}
	}
}

func TestPublishConnected(t *testing.T) {
	client := connectedClient()
	h := &Handler{client: client}

	msg := Message{Topic: "sensors/temperature", Payload: []byte("22.5"), Qos: 1, Retained: true}
	if err := h.Publish(msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if client.publishCalls != 1 {
		t.Fatalf("client.Publish called %d times, want 1", client.publishCalls)
	}
	if client.lastTopic != msg.Topic || client.lastQos != msg.Qos || !client.lastRetained {
		t.Errorf("published %q qos %d retained %v, want %q qos %d retained true",
			client.lastTopic, client.lastQos, client.lastRetained, msg.Topic, msg.Qos)
	}
	if string(client.lastPayload) != string(msg.Payload) {
		t.Errorf("payload %q, want %q", client.lastPayload, msg.Payload)
	}
}

func TestPublishPropagatesTokenError(t *testing.T) {
	brokerErr := errors.New("broker said no")
	client := connectedClient()
	client.publishErr = brokerErr
	h := &Handler{client: client}

	err := h.Publish(Message{Topic: "a/b", Qos: 1})
	if !errors.Is(err, brokerErr) {
		t.Errorf("got %v, want %v", err, brokerErr)
	}
}

func TestPublishRejectsBadMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want error
	}{
		{"empty topic", Message{Topic: "", Qos: 0}, ErrTopicEmpty},
		{"qos above 2", Message{Topic: "a/b", Qos: 3}, ErrInvalidQos},
		{"qos 255", Message{Topic: "a/b", Qos: 255}, ErrInvalidQos},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := connectedClient()
			h := &Handler{client: client}

			if err := h.Publish(tt.msg); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
			if client.publishCalls != 0 {
				t.Error("an invalid message was handed to the client")
			}
		})
	}
}

func TestPublishAfterDisconnect(t *testing.T) {
	client := connectedClient()
	h := &Handler{client: client}

	h.Disconnect()

	if client.disconnects != 1 {
		t.Errorf("client.Disconnect called %d times, want 1", client.disconnects)
	}
	if err := h.Publish(Message{Topic: "a/b"}); !errors.Is(err, ErrClientNotInitialized) {
		t.Errorf("got %v, want ErrClientNotInitialized", err)
	}

	// A second Disconnect must not reach the client again and must not panic.
	h.Disconnect()
	if client.disconnects != 1 {
		t.Errorf("client.Disconnect called %d times after the second Disconnect, want 1", client.disconnects)
	}
}

func TestConnectionState(t *testing.T) {
	tests := []struct {
		name               string
		client             *fakeClient
		wantConnected      bool
		wantConnectionOpen bool
	}{
		{"connected", connectedClient(), true, true},
		{"reconnecting", reconnectingClient(), true, false},
		{"down", &fakeClient{}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{client: tt.client}

			if got := h.IsConnected(); got != tt.wantConnected {
				t.Errorf("IsConnected() = %v, want %v", got, tt.wantConnected)
			}
			if got := h.IsConnectionOpen(); got != tt.wantConnectionOpen {
				t.Errorf("IsConnectionOpen() = %v, want %v", got, tt.wantConnectionOpen)
			}
		})
	}

	h := &Handler{}
	if h.IsConnected() || h.IsConnectionOpen() {
		t.Error("a handler without a client reports a connection")
	}
}

func TestOptions(t *testing.T) {
	h := &Handler{}

	logger := slog.New(slog.DiscardHandler)
	connected := false
	var lostErr error

	for _, opt := range []Option{
		WithLogger(logger),
		WithOnConnected(func() { connected = true }),
		WithOnConnectionLost(func(err error) { lostErr = err }),
	} {
		opt(h)
	}

	if h.logger != logger {
		t.Error("WithLogger did not install the logger")
	}
	if h.onConnected == nil || h.onConnectionLost == nil {
		t.Fatal("callbacks were not installed")
	}

	h.onConnected()
	h.onConnectionLost(errors.New("boom"))

	if !connected {
		t.Error("onConnected was not called")
	}
	if lostErr == nil {
		t.Error("onConnectionLost was not called")
	}

	if (&Handler{}).logger != nil {
		t.Error("a handler without WithLogger has a logger installed")
	}
}
