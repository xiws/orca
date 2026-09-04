// Package event provides an asynchronous in-memory event bus.
package event

import (
	"errors"
	"reflect"
	"sync"
)

const QueueCapacity = 100

var (
	ErrNilEvent              = errors.New("event is nil")
	ErrNilEventHandler       = errors.New("event handler is nil")
	ErrDuplicateSubscription = errors.New("event handler is already subscribed")
	ErrNoSubscribers         = errors.New("event has no subscribers")
	ErrEventBusClosed        = errors.New("event bus is closed")
)

// Event identifies an event and its subscription topic.
type Event interface {
	GetId() string
	GetName() string
}

// EventHandler receives events for a subscribed event id.
type EventHandler interface {
	Handle(ent Event)
}

// EventPublisher publishes events to an event bus.
type EventPublisher interface {
	Publish(ent Event) error
}

// EventBus asynchronously dispatches events using an in-memory queue.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]EventHandler
	queue       chan Event
	done        chan struct{}
	closed      bool
	worker      sync.WaitGroup
	closeOnce   sync.Once
}

// NewEventBus creates and starts an event bus with a 100-event queue.
func NewEventBus() *EventBus {
	bus := &EventBus{
		subscribers: make(map[string][]EventHandler),
		queue:       make(chan Event, QueueCapacity),
		done:        make(chan struct{}),
	}
	bus.worker.Add(1)
	go bus.consume()
	return bus
}

// Subscribe registers a handler for an event name.
func (t *EventBus) Subscribe(eventName string, handler EventHandler) error {
	if eventName == "" {
		return ErrNilEvent
	}
	if isNil(handler) {
		return ErrNilEventHandler
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrEventBusClosed
	}
	for _, registered := range t.subscribers[eventName] {
		if sameHandler(registered, handler) {
			return ErrDuplicateSubscription
		}
	}
	t.subscribers[eventName] = append(t.subscribers[eventName], handler)
	return nil
}

// Subscriber is an alias for Subscribe, matching the event handler design draft.
func (t *EventBus) Subscriber(eventName string, handler EventHandler) error {
	return t.Subscribe(eventName, handler)
}

// Publish enqueues an event and returns after it has been accepted by the queue.
// If the queue is full, Publish waits until space is available or the bus closes.
func (t *EventBus) Publish(ent Event) error {
	if isNil(ent) {
		return ErrNilEvent
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrEventBusClosed
	}
	if len(t.subscribers[ent.GetName()]) == 0 {
		return ErrNoSubscribers
	}
	t.queue <- ent
	return nil
}

// Close stops accepting events and waits for all accepted events to finish.
func (t *EventBus) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		close(t.done)
		t.mu.Unlock()
	})
	t.worker.Wait()
	return nil
}

func (t *EventBus) consume() {
	defer t.worker.Done()
	for {
		select {
		case ent := <-t.queue:
			t.dispatch(ent)
		case <-t.done:
			for {
				select {
				case ent := <-t.queue:
					t.dispatch(ent)
				default:
					return
				}
			}
		}
	}
}

func (t *EventBus) dispatch(ent Event) {
	t.mu.RLock()
	handlers := append([]EventHandler(nil), t.subscribers[ent.GetName()]...)
	t.mu.RUnlock()
	for _, handler := range handlers {
		invoke(handler, ent)
	}
}

func invoke(handler EventHandler, ent Event) {
	defer func() {
		_ = recover()
	}()
	handler.Handle(ent)
}

func sameHandler(left, right EventHandler) bool {
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() || !leftValue.Type().Comparable() {
		return false
	}
	return leftValue.Interface() == rightValue.Interface()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
