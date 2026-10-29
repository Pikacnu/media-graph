package graph

import (
	"context"
	"testing"
)

func TestBusControllerEvictsOnlySlowListener(t *testing.T) {
	stopContext, cancel := context.WithCancel(context.Background())
	controller := &busControllerImpl{
		stopContext:     stopContext,
		stop:            cancel,
		busChanRegistry: make(map[string]chan BusEvent),
	}

	slow, err := controller.RegisterBusEventsChan()
	if err != nil {
		t.Fatalf("register slow listener: %v", err)
	}
	active, err := controller.RegisterBusEventsChan()
	if err != nil {
		t.Fatalf("register active listener: %v", err)
	}

	for index := 0; index < cap(slow)+1; index++ {
		controller.publish(BusEvent{Debug: "event"})
		select {
		case <-active:
		case <-stopContext.Done():
			t.Fatal("controller stopped after one listener became slow")
		}
	}

	for range cap(slow) {
		select {
		case <-slow:
		case <-stopContext.Done():
			t.Fatal("controller stopped while draining evicted listener")
		}
	}
	if _, ok := <-slow; ok {
		t.Fatal("slow listener channel remained open")
	}

	controller.publish(BusEvent{Debug: "later event"})
	select {
	case event := <-active:
		if event.Debug != "later event" {
			t.Fatalf("active listener received %q, want later event", event.Debug)
		}
	case <-stopContext.Done():
		t.Fatal("controller stopped before publishing to active listener")
	}

	controller.Stop()
	controller.Stop()
	controller.closeListeners()
}
