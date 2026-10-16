package mqtt_test

import (
	"log"
	"log/slog"

	"github.com/womat/mqtt"
)

// Example_usage connects to a broker and publishes one message.
//
// It deliberately carries no "Output:" comment: it needs a real broker, so it
// is compiled as documentation but never executed by go test. Test_Example in
// mqtt_example_test.go runs the same steps when MQTT_TEST_BROKER names one.
func Example_usage() {
	// Credentials and TLS come from the URL, e.g. "ssl://user:password@broker:8883".
	// New does not fail on an unreachable broker: it retries in the background.
	h, err := mqtt.New("tcp://broker:1883", "sensor-livingroom",
		mqtt.WithLogger(slog.Default()),
		mqtt.WithOnConnected(func() { slog.Info("MQTT connected") }),
		mqtt.WithOnConnectionLost(func(err error) { slog.Warn("MQTT connection lost", "error", err) }),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer h.Disconnect()

	// Publish refuses to send while no connection is open (ErrNotConnected)
	// rather than letting Paho drop the message silently.
	err = h.Publish(mqtt.Message{
		Topic:   "sensors/livingroom/temperature",
		Payload: []byte("22.5"),
		Qos:     1,
	})
	if err != nil {
		log.Println("publish failed:", err)
	}
}
