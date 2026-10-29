package chan_registry

import (
	"reflect"
	"sync"

	"live_tools/gst_utils/core"
)

const DefaultOutputBufferSize = 64

// Subscriber holds one consumer's channel plus whatever extra data the producer
// wants to associate with that consumer.
type Subscriber[T any] struct {
	ch     chan T
	params map[string]any
}

type OutputEntry[T any] struct {
	id         string
	stopChan   chan struct{}
	once       sync.Once
	bufferSize int
	dropType   core.ChannelDropType
	mu         sync.RWMutex
	subs       map[string]*Subscriber[T]
}

var _ core.OutputRegistryEntry = (*OutputEntry[[]byte])(nil)

func NewOutputEntry[T any](id string, bufferSize int, dropType core.ChannelDropType) (*OutputEntry[T], error) {
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if bufferSize <= 0 {
		bufferSize = DefaultOutputBufferSize
	}
	if dropType == core.DropNone {
		return nil, core.ErrInvalidConfig
	}
	return &OutputEntry[T]{
		id:         id,
		stopChan:   make(chan struct{}),
		bufferSize: bufferSize,
		dropType:   dropType,
		subs:       make(map[string]*Subscriber[T]),
	}, nil
}

func (e *OutputEntry[T]) ID() string { return e.id }

func (e *OutputEntry[T]) Done() <-chan struct{} { return e.stopChan }

func (e *OutputEntry[T]) Stop() error {
	e.once.Do(func() {
		close(e.stopChan)
		e.mu.Lock()
		defer e.mu.Unlock()
		for id, sub := range e.subs {
			close(sub.ch)
			delete(e.subs, id)
		}
	})
	return nil
}

func (e *OutputEntry[T]) AddOutput(id string, bufferSize int) (any, error) {
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if bufferSize <= 0 {
		bufferSize = e.bufferSize
	}

	select {
	case <-e.stopChan:
		return nil, core.ErrEntryStopped
	default:
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.subs[id]; exists {
		return nil, core.ErrInvalidConfig
	}
	sub := &Subscriber[T]{ch: make(chan T, bufferSize), params: make(map[string]any)}
	e.subs[id] = sub
	return sub.ch, nil
}

func (e *OutputEntry[T]) GetOutput(id string) (any, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	sub, exists := e.subs[id]
	if !exists {
		return nil, false
	}
	return sub.ch, true
}

func (e *OutputEntry[T]) GetOutputs() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]any, len(e.subs))
	for id, sub := range e.subs {
		out[id] = sub.ch
	}
	return out
}

func (e *OutputEntry[T]) RemoveOutput(id string) error {
	select {
	case <-e.stopChan:
		return core.ErrEntryStopped
	default:
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	sub, exists := e.subs[id]
	if !exists {
		return core.ErrNotFound
	}
	close(sub.ch)
	delete(e.subs, id)
	return nil
}

func (e *OutputEntry[T]) SubscriberParams(id string) (map[string]any, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	sub, exists := e.subs[id]
	if !exists {
		return nil, false
	}
	out := make(map[string]any, len(sub.params))
	for key, value := range sub.params {
		out[key] = value
	}
	return out, true
}

func (e *OutputEntry[T]) SetSubscriberParam(id, key string, value any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sub, exists := e.subs[id]
	if !exists {
		return core.ErrNotFound
	}
	if value == nil {
		delete(sub.params, key)
		return nil
	}
	sub.params[key] = value
	return nil
}

func (e *OutputEntry[T]) SubscriberIDs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := make([]string, 0, len(e.subs))
	for id := range e.subs {
		ids = append(ids, id)
	}
	return ids
}

func (e *OutputEntry[T]) Publish(value T) int {
	select {
	case <-e.stopChan:
		return 0
	default:
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	delivered := 0
	for _, sub := range e.subs {
		select {
		case sub.ch <- value:
			delivered++
		default:
			if e.dropType == core.DropOldest {
				select {
				case <-sub.ch:
				default:
				}
				select {
				case sub.ch <- value:
					delivered++
				default:
				}
			}
		}
	}
	return delivered
}

func (e *OutputEntry[T]) Type() reflect.Type {
	var zero T
	return reflect.TypeOf(zero)
}
