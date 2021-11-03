package mqtt

// Message contains the properties of the mqtt message.
type Message struct {
	Topic    string
	Payload  []byte
	Qos      byte
	Retained bool
}

type PublisherSubscriber interface {
	// Publish the message to mqtt broker
	Publish(Message) error
	// Subscribe the topic and the function handler if a message is received
	Subscribe(string, byte, func(Message)) error
	// Unsubscribe the topic
	Unsubscribe(string) error
	// Close the connection to mqtt broker
	Close() error
}
