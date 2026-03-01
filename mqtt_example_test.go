package mqtt

import (
	"log/slog"
	"testing"
)

func Test_Example(t *testing.T) {

	broker := "tcp://mysmarthome:1883"

	m, err := New(broker, "clientID",
		WithOnConnected(func() {
			slog.Info("MQTT connected", "broker", broker)
		}),
		WithOnConnectionLost(func(err error) {
			slog.Warn("MQTT connection lost", "error", err)
		}))
	if err != nil {
		t.Fatalf("Failed to connect to MQTT broker: %v", err)
	}

	defer m.Disconnect()

	msg := Message{
		Topic:   "test/temperature",
		Payload: []byte("22.5"),
		Qos:     1,
	}

	if err = m.Publish(msg); err != nil {
		t.Errorf("Failed to publish MQTT message: %v", err)
	}
}
