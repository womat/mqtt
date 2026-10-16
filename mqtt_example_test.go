package mqtt

import (
	"log/slog"
	"os"
	"testing"
)

// Test_Example is the runnable version of the package documentation. It needs a
// real broker and is skipped unless one is named in MQTT_TEST_BROKER, e.g.
//
//	MQTT_TEST_BROKER=tcp://localhost:1883 go test ./mqtt/
func Test_Example(t *testing.T) {
	broker := os.Getenv("MQTT_TEST_BROKER")
	if broker == "" {
		t.Skip("set MQTT_TEST_BROKER to run this test against a real broker")
	}

	// New never reports an error: a broker that is down is retried in the
	// background, so ask IsConnectionOpen whether it was actually reached.
	m, _ := New(broker, "clientID",
		WithLogger(slog.Default()),
		WithOnConnected(func() {
			slog.Info("MQTT connected", "broker", broker)
		}),
		WithOnConnectionLost(func(err error) {
			slog.Warn("MQTT connection lost", "error", err)
		}))

	defer m.Disconnect()

	if !m.IsConnectionOpen() {
		t.Fatalf("no connection to %s", broker)
	}

	msg := Message{
		Topic:   "test/temperature",
		Payload: []byte("22.5"),
		Qos:     1,
	}

	if err := m.Publish(msg); err != nil {
		t.Errorf("Failed to publish MQTT message: %v", err)
	}
}
