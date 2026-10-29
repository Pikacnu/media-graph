package core

import (
	"context"
	"errors"
	"fmt"
	"live_tools/utils"
	"reflect"
	"sync"
)

type RegistryEntryBase struct {
	RegistryEntry
	id       string
	stopChan chan struct{}
	// params   map[string]any
}

type RegistryEntry interface {
	ID() string
	Stop() error
	Done() <-chan struct{}
	Type() reflect.Type
	// GetParams() map[string]any
	// SetParams(params map[string]any)
	// SetParam(key string, value any)
	// GetParam(key string) (any, bool)
}

var _ RegistryEntry = (*RegistryEntryBase)(nil)

func CreateRegistryEntry(id string) (RegistryEntry, error) {
	if id == "" {
		return nil, ErrInvalidConfig
	}
	return &RegistryEntryBase{
		id:       id,
		stopChan: make(chan struct{}),
	}, nil
}

func (e *RegistryEntryBase) ID() string {
	return e.id
}

func (e *RegistryEntryBase) Stop() error {
	select {
	case <-e.stopChan:
		return ErrEntryStopped
	default:
		close(e.stopChan)
		return nil
	}
}

// func (e *RegistryEntryBase) GetParams() map[string]any {
// 	if e.params == nil {
// 		e.params = make(map[string]any)
// 	}
// 	return e.params
// }

// func (e *RegistryEntryBase) SetParams(params map[string]any) {
// 	e.params = params
// }

// func (e *RegistryEntryBase) SetParam(key string, value any) {
// 	if e.params == nil {
// 		e.params = make(map[string]any)
// 	}
// 	if value == nil {
// 		delete(e.params, key)
// 	}
// 	e.params[key] = value
// }

// func (e *RegistryEntryBase) GetParam(key string) (any, bool) {
// 	if e.params == nil {
// 		return nil, false
// 	}
// 	if value, exists := e.params[key]; exists {
// 		return value, true
// 	}
// 	return nil, false
// }

func (e *RegistryEntryBase) Done() <-chan struct{} {
	return e.stopChan
}

type ChanRegistry interface {
	RegisterEntry(entry RegistryEntry) error
	RemoveEntryById(id string) error
	GetEntryById(id string) (RegistryEntry, bool)
	GetEventChan() <-chan ChanRegistryEvent
	HasID(id string) bool
	Clear() error
}

type ChanRegistryEventType int

const (
	ChanRegistryEventTypeEntryRegistered ChanRegistryEventType = iota
	ChanRegistryEventTypeEntryRemoved
)

type ChanRegistryEvent struct {
	// Just RandomString No Meaning
	ID      string
	Type    ChanRegistryEventType
	Payload ChanRegistryEventPayload
}

type ChanRegistryEventPayload struct {
	ChanEntryId string
}

type chanRegistryImpl struct {
	ChanRegistry
	id                        string
	entries                   map[string]RegistryEntry
	eventListenerChanRegistry map[string]chan<- ChanRegistryEvent
	eventChan                 chan ChanRegistryEvent
	context                   *context.Context
	contextCancel             context.CancelFunc
	mutex                     *sync.RWMutex
	stopped                   bool
}

var _ ChanRegistry = (*chanRegistryImpl)(nil)

func CreateChanRegistry(id string) (*chanRegistryImpl, error) {
	mutex := &sync.RWMutex{}
	context, cancel := context.WithCancel(context.Background())
	eventChan := make(chan ChanRegistryEvent, 10)
	eventListenerChanRegistry := make(map[string]chan<- ChanRegistryEvent)
	go func() {
		for {
			select {
			case <-context.Done():
				return
			case event, ok := <-eventChan:
				if !ok {
					return
				}
				mutex.RLock()
				for _, listenerChan := range eventListenerChanRegistry {
					select {
					case listenerChan <- event:
					default:
						// If the listener channel is full, we can choose to drop the event or handle it differently.
						// For now, we'll just drop it to avoid blocking.
					}
				}
				mutex.RUnlock()
			}
		}
	}()

	return &chanRegistryImpl{
		id:                        id,
		entries:                   make(map[string]RegistryEntry),
		eventChan:                 eventChan,
		mutex:                     mutex,
		context:                   &context,
		contextCancel:             cancel,
		eventListenerChanRegistry: eventListenerChanRegistry,
	}, nil
}

func (r *chanRegistryImpl) RegisterEntry(entry RegistryEntry) error {
	defer r.mutex.Unlock()
	r.mutex.Lock()
	if r.stopped {
		return ErrEntryStopped
	}
	id := entry.ID()
	if _, exists := r.entries[id]; exists {
		return ErrInvalidConfig
	}
	r.entries[id] = entry
	event := ChanRegistryEvent{
		ID:   utils.GenerateRandomString(20),
		Type: ChanRegistryEventTypeEntryRegistered,
		Payload: ChanRegistryEventPayload{
			ChanEntryId: id,
		},
	}
	select {
	case r.eventChan <- event:
	default:
		delete(r.entries, id)
		return ErrChanFull
	}
	return nil
}

func (r *chanRegistryImpl) RemoveEntryById(id string) error {
	defer r.mutex.Unlock()
	r.mutex.Lock()
	if r.stopped {
		return ErrEntryStopped
	}
	entry, exists := r.entries[id]
	if !exists {
		return ErrInvalidConfig
	}
	delete(r.entries, id)
	event := ChanRegistryEvent{
		ID:   utils.GenerateRandomString(20),
		Type: ChanRegistryEventTypeEntryRemoved,
		Payload: ChanRegistryEventPayload{
			ChanEntryId: id,
		},
	}
	select {
	case r.eventChan <- event:
	default:
		r.entries[id] = entry
		return ErrChanFull
	}
	return nil
}

func (r *chanRegistryImpl) GetEntryById(id string) (RegistryEntry, bool) {
	defer r.mutex.RUnlock()
	r.mutex.RLock()
	entry, exists := r.entries[id]
	return entry, exists
}

func (r *chanRegistryImpl) HasID(id string) bool {
	defer r.mutex.RUnlock()
	r.mutex.RLock()
	_, exists := r.entries[id]
	return exists
}

func (r *chanRegistryImpl) Clear() error {
	r.mutex.Lock()
	if r.stopped {
		r.mutex.Unlock()
		return nil
	}
	r.stopped = true
	entries := r.entries
	r.entries = make(map[string]RegistryEntry)
	listeners := r.eventListenerChanRegistry
	for id, listenerChan := range listeners {
		close(listenerChan)
		delete(listeners, id)
	}
	r.mutex.Unlock()

	var errs []error
	for _, entry := range entries {
		if err := entry.Stop(); err != nil && !errors.Is(err, ErrEntryStopped) {
			errs = append(errs, err)
		}
	}
	r.contextCancel()
	if len(errs) > 0 {
		return fmt.Errorf("errors occurred while clearing registry entries: %w", errors.Join(errs...))
	}
	return nil
}

func (r *chanRegistryImpl) GetEventChan() <-chan ChanRegistryEvent {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.stopped {
		closed := make(chan ChanRegistryEvent)
		close(closed)
		return closed
	}
	// random gen id
	id := utils.GenerateRandomString(14)
	newEventListenerChan := make(chan ChanRegistryEvent, 10)
	r.eventListenerChanRegistry[id] = newEventListenerChan
	return newEventListenerChan
}
