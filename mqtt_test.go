package mqtt

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

const (
	broker = "tcp://raspberrypi4.fritz.box:1883"
	topic1 = "/test/hello1"
	topic2 = "/test/hello2"
)

func TestSubscribePublish(t *testing.T) {
	var wg sync.WaitGroup

	handler1 := func(msg Message) {
		fmt.Printf("handler 1 %v\n", msg)
		wg.Done()
	}

	handler2 := func(msg Message) {
		fmt.Printf("handler 2 %v\n", msg)
		wg.Done()
	}

	h, err := New(broker)
	if err != nil {
		t.Errorf("open failed: %v", err)
	}

	if err = h.Subscribe(topic1, 0, handler1); err != nil {
		t.Errorf("subscribe handler1 failed: %v", err)
	}

	if err = h.Subscribe(topic2, 0, handler2); err != nil {
		t.Errorf("subscribe handler2 failed: %v", err)
	}

	wg.Add(2)
	if err = h.Publish(Message{Topic: topic1, Payload: []byte("test1"), Qos: 0, Retained: false}); err != nil {
		t.Errorf("publish topic1 failed: %v", err)
	}

	if err = h.Publish(Message{Topic: topic2, Payload: []byte("test2"), Qos: 0, Retained: false}); err != nil {
		t.Errorf("publish topic 2failed: %v", err)
	}

	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return
	case <-time.After(time.Second):
		t.Errorf("no message received")
	}
}
