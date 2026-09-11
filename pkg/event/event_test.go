package event

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// eventSeq gives every test event a unique id so publishing and comparing stay
// independent of any wall clock.
var eventSeq atomic.Int64

type testEvent struct {
	id   int64
	name string
}

func newTestEvent() testEvent {
	return testEvent{id: eventSeq.Add(1), name: "created"}
}

func (e testEvent) GetId() int64    { return e.id }
func (e testEvent) GetName() string { return e.name }

type missingEvent struct{}

func newMissingEvent() missingEvent { return missingEvent{} }

func (missingEvent) GetId() int64    { return 0 }
func (missingEvent) GetName() string { return "not-subscribed" }

type testEventHandler struct {
	mu      sync.Mutex
	count   int
	receive chan Event
}

func (h *testEventHandler) Handle(ent Event) {
	h.mu.Lock()
	h.count++
	h.mu.Unlock()
	if h.receive != nil {
		h.receive <- ent
	}
}

func (h *testEventHandler) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func TestEventBusPublishesToAllSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()
	first := &testEventHandler{receive: make(chan Event, 1)}
	second := &testEventHandler{receive: make(chan Event, 1)}
	ent := newTestEvent()
	if err := bus.Subscribe(ent, first); err != nil {
		t.Fatal(err)
	}
	if err := bus.Subscriber(ent, second); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ent); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if got := waitForEvent(first.receive); got != ent {
		t.Fatalf("first handler event = %#v, want %#v", got, ent)
	}
	if got := waitForEvent(second.receive); got != ent {
		t.Fatalf("second handler event = %#v, want %#v", got, ent)
	}
}

func TestEventBusRejectsInvalidOperations(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()
	handler := &testEventHandler{}
	ent := newTestEvent()

	if err := bus.Subscribe(testEvent{}, handler); !errors.Is(err, ErrNilEvent) {
		t.Fatalf("empty id error = %v", err)
	}
	if err := bus.Subscribe(ent, nil); !errors.Is(err, ErrNilEventHandler) {
		t.Fatalf("nil handler error = %v", err)
	}
	if err := bus.Subscribe(ent, handler); err != nil {
		t.Fatal(err)
	}
	if err := bus.Subscribe(ent, handler); !errors.Is(err, ErrDuplicateSubscription) {
		t.Fatalf("duplicate subscription error = %v", err)
	}
	if err := bus.Publish(newMissingEvent()); !errors.Is(err, ErrNoSubscribers) {
		t.Fatalf("unknown event error = %v", err)
	}
	if err := bus.Publish(nil); !errors.Is(err, ErrNilEvent) {
		t.Fatalf("nil event error = %v", err)
	}
}

func TestEventBusQueueAndClose(t *testing.T) {
	bus := NewEventBus()
	handler := &testEventHandler{}
	ent := newTestEvent()
	if err := bus.Subscribe(ent, handler); err != nil {
		t.Fatal(err)
	}
	if got := cap(bus.queue); got != QueueCapacity {
		t.Fatalf("queue capacity = %d, want %d", got, QueueCapacity)
	}

	const events = 100
	for i := 0; i < events; i++ {
		if err := bus.Publish(newTestEvent()); err != nil {
			t.Fatalf("Publish(%d) error = %v", i, err)
		}
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := handler.Count(); got != events {
		t.Fatalf("handled events = %d, want %d", got, events)
	}
	if err := bus.Publish(ent); !errors.Is(err, ErrEventBusClosed) {
		t.Fatalf("Publish() after Close error = %v", err)
	}
	if err := bus.Subscribe(ent, handler); !errors.Is(err, ErrEventBusClosed) {
		t.Fatalf("Subscribe() after Close error = %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestEventBusConcurrentPublish(t *testing.T) {
	bus := NewEventBus()
	handler := &testEventHandler{}
	ent := newTestEvent()
	if err := bus.Subscribe(ent, handler); err != nil {
		t.Fatal(err)
	}

	const events = 100
	var wg sync.WaitGroup
	wg.Add(events)
	for i := 0; i < events; i++ {
		go func() {
			defer wg.Done()
			if err := bus.Publish(newTestEvent()); err != nil {
				t.Errorf("Publish() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	if got := handler.Count(); got != events {
		t.Fatalf("handled events = %d, want %d", got, events)
	}
}

func waitForEvent(ch <-chan Event) Event {
	select {
	case ent := <-ch:
		return ent
	case <-time.After(time.Second):
		return nil
	}
}
