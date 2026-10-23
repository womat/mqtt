package mqtt

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	mqttlib "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

// fakeToken is a token that is already complete when it is handed out,
// which is how paho behaves for a publish it decides not to send. A pending
// token never completes, like a QoS 1 publish the broker does not acknowledge.
type fakeToken struct {
	err     error
	pending bool
	done    chan struct{}
}

func newFakeToken(err error) *fakeToken {
	t := &fakeToken{err: err, done: make(chan struct{})}
	close(t.done)
	return t
}

func (t *fakeToken) Wait() bool                     { return !t.pending }
func (t *fakeToken) WaitTimeout(time.Duration) bool { return !t.pending }
func (t *fakeToken) Done() <-chan struct{}          { return t.done }
func (t *fakeToken) Error() error                   { return t.err }

// fakeClient records what the Handler asks of the client and lets a test
// pretend the connection is in any of the states paho distinguishes.
// Only the methods the Handler uses are implemented; the rest panic so that
// an unnoticed new call site shows up immediately. mu guards what Publish and
// Disconnect record, so that a concurrent test races on the Handler, not here.
type fakeClient struct {
	mu             sync.Mutex
	connected      bool // paho: true while connecting or reconnecting as well
	connectionOpen bool // paho: true only while actually connected
	publishErr     error
	publishPending bool
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
func (c *fakeClient) Disconnect(uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnects++
}

func (c *fakeClient) Publish(topic string, qos byte, retained bool, payload any) mqttlib.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publishCalls++
	c.lastTopic = topic
	c.lastQos = qos
	c.lastRetained = retained
	if b, ok := payload.([]byte); ok {
		c.lastPayload = b
	}
	if c.publishPending {
		// Done is never closed: the token stays open for good.
		return &fakeToken{pending: true, done: make(chan struct{})}
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

func TestPublishTimeout(t *testing.T) {
	// A token that is still open after publishTimeout has no error yet; without
	// the WaitTimeout check Publish would report it as success.
	client := connectedClient()
	client.publishPending = true
	h := &Handler{client: client}

	if err := h.Publish(Message{Topic: "a/b", Qos: 1}); !errors.Is(err, ErrTimeout) {
		t.Errorf("got %v, want ErrTimeout", err)
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

func TestDisconnectWhilePublishing(t *testing.T) {
	// Only meaningful under -race (as CI runs it): without the Handler's mutex
	// the detector reports Disconnect's write of the client against the reads.
	client := connectedClient()
	h := &Handler{client: client}

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				err := h.Publish(Message{Topic: "a/b"})
				if err != nil && !errors.Is(err, ErrClientNotInitialized) {
					t.Errorf("Publish: %v, want nil or ErrClientNotInitialized", err)
					return
				}
				h.IsConnected()
				h.IsConnectionOpen()
			}
		})
	}
	wg.Go(h.Disconnect)
	wg.Wait()

	if client.disconnects != 1 {
		t.Errorf("client.Disconnect called %d times, want 1", client.disconnects)
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

func TestRedactURL(t *testing.T) {
	for _, broker := range []string{
		"tcp://user:secret@mqtt.example.com:1883",
		// Paho accepts a URL without a scheme and reads it as tcp://.
		"user:secret@mqtt.example.com:1883",
	} {
		got := redactURL(broker)
		if strings.Contains(got, "secret") || !strings.Contains(got, "mqtt.example.com") {
			t.Errorf("redactURL(%q) = %q, want the host without the password", broker, got)
		}
	}

	if got := redactURL("tcp://user:se%zzret@mqtt.example.com:1883"); strings.Contains(got, "se%zzret") {
		t.Errorf("redactURL of an unparsable URL = %q, want it without the password", got)
	}
}

// fakeBroker listens on addr and answers each CONNECT with CONNACK, which is
// all paho needs to consider itself connected. Each connection goes to the
// test, which reads what the client sends and drops it.
func fakeBroker(t *testing.T, addr string) (broker string, conns <-chan net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	ch := make(chan net.Conn, 2)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed by the cleanup
			}
			if _, err := packets.ReadPacket(conn); err != nil {
				conn.Close()
				continue
			}
			// A fresh CONNACK has return code 0, "connection accepted".
			if err := packets.NewControlPacket(packets.Connack).Write(conn); err != nil {
				conn.Close()
				continue
			}
			ch <- conn
		}
	}()
	return "tcp://" + l.Addr().String(), ch
}

// receive waits for the next value on ch and fails the test after 5 s.
func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func TestNewAgainstBroker(t *testing.T) {
	// New against a broker that answers: the callbacks fire, Publish reaches
	// the wire, and a dropped connection is reestablished on its own.
	broker, conns := fakeBroker(t, "127.0.0.1:0")
	connected := make(chan struct{}, 2)
	lost := make(chan error, 1)

	h, err := New(broker, "test",
		WithOnConnected(func() { connected <- struct{}{} }),
		WithOnConnectionLost(func(err error) { lost <- err }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Disconnect()

	conn := receive(t, conns, "the client to connect")
	defer conn.Close()
	receive(t, connected, "onConnected")
	if !h.IsConnectionOpen() {
		t.Fatal("IsConnectionOpen() = false after a successful connect")
	}
	// Only the setting: the backoff it caps takes minutes to reach.
	opts := h.current().OptionsReader()
	if got := opts.MaxReconnectInterval(); got != maxReconnectInterval {
		t.Errorf("MaxReconnectInterval = %v, want %v", got, maxReconnectInterval)
	}

	if err := h.Publish(Message{Topic: "sensors/temperature", Payload: []byte("22.5")}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	p, err := packets.ReadPacket(conn)
	if err != nil {
		t.Fatalf("reading the published packet: %v", err)
	}
	pub, ok := p.(*packets.PublishPacket)
	if !ok {
		t.Fatalf("broker received %T, want a PUBLISH", p)
	}
	if pub.TopicName != "sensors/temperature" || string(pub.Payload) != "22.5" {
		t.Errorf("broker received %q %q, want sensors/temperature 22.5", pub.TopicName, pub.Payload)
	}

	conn.Close()
	receive(t, lost, "onConnectionLost")

	reconn := receive(t, conns, "the client to reconnect")
	defer reconn.Close()
	receive(t, connected, "onConnected after the reconnect")
}

func TestNewRetriesUntilBrokerIsUp(t *testing.T) {
	// A service may start before its broker. New must keep trying and connect
	// once the broker is up, not give up after the first refused connect.
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	type result struct {
		h   *Handler
		err error
	}
	done := make(chan result, 1)
	go func() {
		h, err := New("tcp://"+addr, "test")
		done <- result{h, err}
	}()

	// The first connect is refused; the broker is up before the retry 2 s later.
	time.Sleep(time.Second)
	_, conns := fakeBroker(t, addr)

	r := receive(t, done, "New to return")
	if r.err != nil {
		t.Fatalf("New: %v", r.err)
	}
	defer r.h.Disconnect()
	if !r.h.IsConnectionOpen() {
		t.Fatal("New returned without a connection although the broker came up within its timeout")
	}
	conn := receive(t, conns, "the client to connect")
	defer conn.Close()
}

func TestNewLogsTimeoutWithoutPassword(t *testing.T) {
	// The warning about a slow initial connect is the only thing this package
	// logs, and it names the broker. The password in the URL must not reach it.
	// Takes the full connect timeout of 5 s.
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	// Accepts the connection and never answers it, so the connect times out.
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := l.Accept(); err == nil {
			accepted <- conn
		}
	}()

	var buf bytes.Buffer
	h, err := New("tcp://user:secret@"+l.Addr().String(), "test",
		WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Disconnect()
	conn := receive(t, accepted, "the client to connect")
	defer conn.Close()

	out := buf.String()
	if !strings.Contains(out, "timed out") {
		t.Errorf("no warning about the timed-out connect, log: %q", out)
	}
	if strings.Contains(out, "secret") {
		t.Errorf("the password reached the log: %q", out)
	}
}

func TestNewInvalidBroker(t *testing.T) {
	// An unescaped % in the password makes the URL unparsable. Paho would drop
	// it and never connect, so New must say so instead of returning a dead handler.
	h, err := New("tcp://user:se%zzret@mqtt.example.com:1883", "id")
	if !errors.Is(err, ErrInvalidBroker) {
		t.Errorf("got error %v, want ErrInvalidBroker", err)
	}
	if h != nil {
		t.Error("New returned a handler for an unparsable broker URL")
	}
}
