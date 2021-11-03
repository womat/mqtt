package mqtt

import (
	"errors"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Handler contains the handler of the mqtt broker.
type Handler struct {
	mqtt.Client
}

// quiesce is the specified number of milliseconds to wait for existing work to be completed.
const (
	quiesce = 250
	timeout = time.Second
)

// New generate a new mqtt broker client and connect to the mqtt broker.
func New(broker string) (*Handler, error) {
	if broker == "" {
		return nil, errors.New("missing broker")
	}

	h := Handler{Client: mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker))}
	return &h, h.reConnect()
}

// Close will end the connection to the broker.
func (m *Handler) Close() error {
	if m.Client == nil {
		return nil
	}

	m.Client.Disconnect(quiesce)
	return nil
}

// Publish the message to mqtt broker
// If none topic is defined, no mqtt message are send.
func (m *Handler) Publish(msg Message) error {
	if msg.Topic == "" {
		return errors.New("missing topic")
	}

	if !m.Client.IsConnected() {
		if err := m.reConnect(); err != nil {
			return err
		}
	}

	t := m.Client.Publish(msg.Topic, msg.Qos, msg.Retained, msg.Payload)
	if !t.WaitTimeout(timeout) {
		return errors.New("time out")
	}
	return t.Error()
}

// Subscribe waits for a message and call the function handler
func (m *Handler) Subscribe(topic string, qos byte, handler func(Message)) error {
	msgHandler := func(c mqtt.Client, msg mqtt.Message) {
		handler(Message{
			Topic:    msg.Topic(),
			Payload:  msg.Payload(),
			Qos:      msg.Qos(),
			Retained: msg.Retained(),
		})

		return
	}

	t := m.Client.Subscribe(topic, qos, msgHandler)
	if !t.WaitTimeout(time.Second) {
		return errors.New("time out")
	}
	return t.Error()
}

// Unsubscribe will end the subscription from each of the topic provided.
// Messages published to those topics from other clients will no longer be
// received.
func (m *Handler) Unsubscribe(topic string) error {
	t := m.Client.Unsubscribe(topic)

	if !t.WaitTimeout(time.Second) {
		return errors.New("time out")
	}
	return t.Error()
}

// ReConnect reconnects to the defined mqtt broker.
func (m *Handler) reConnect() error {
	t := m.Client.Connect()
	if !t.WaitTimeout(timeout) {
		return errors.New("time out")
	}

	return t.Error()
}
