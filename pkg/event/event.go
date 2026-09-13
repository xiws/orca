// Package event 提供异步内存事件总线。
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

// Event 标识一个事件及其订阅主题。
type Event interface {
	GetId() int64
	GetName() string
}

// EventHandler 接收已订阅事件 id 的事件。
type EventHandler interface {
	Handle(ent Event)
}

// EventPublisher 将事件发布到事件总线。
type EventPublisher interface {
	Publish(ent Event) error
}

// EventBus 使用内存队列异步分发事件。
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]EventHandler
	queue       chan Event
	done        chan struct{}
	closed      bool
	worker      sync.WaitGroup
	closeOnce   sync.Once
}

// NewEventBus 创建并启动一个 100 事件容量的事件总线。
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

// Subscribe 为事件名称注册处理器。
func (t *EventBus) Subscribe(eventName Event, handler EventHandler) error {
	if eventName.GetName() == "" {
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
	name := eventName.GetName()
	for _, registered := range t.subscribers[name] {
		if sameHandler(registered, handler) {
			return ErrDuplicateSubscription
		}
	}
	t.subscribers[name] = append(t.subscribers[name], handler)
	return nil
}

// Subscriber 是 Subscribe 的别名，匹配事件处理器设计草案。
func (t *EventBus) Subscriber(eventName Event, handler EventHandler) error {
	return t.Subscribe(eventName, handler)
}

// Publish 将事件入队，在被队列接受后返回。
// 如果队列已满，Publish 等待直到有空间或总线关闭。
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

// Close 停止接受事件并等待所有已接受事件处理完成。
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
