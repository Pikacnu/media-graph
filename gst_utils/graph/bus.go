package graph

import (
	"context"
	"fmt"
	"live_tools/utils"
	"sync"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

type BusEventType gst.MessageType

const (
	BusEventTypeUnknown          BusEventType = BusEventType(gst.MessageUnknown)
	BusEventTypeError            BusEventType = BusEventType(gst.MessageError)
	BusEventTypeWarning          BusEventType = BusEventType(gst.MessageWarning)
	BusEventTypeInfo             BusEventType = BusEventType(gst.MessageInfo)
	BusEventTypeEOS              BusEventType = BusEventType(gst.MessageEOS)
	BusEventTypeStateChanged     BusEventType = BusEventType(gst.MessageStateChanged)
	BusEventTypeStateDirty       BusEventType = BusEventType(gst.MessageStateDirty)
	BusEventTypeStreamStart      BusEventType = BusEventType(gst.MessageStreamStart)
	BusEventTypeStreamCollection BusEventType = BusEventType(gst.MessageStreamCollection)
	BusEventTypeSegmentStart     BusEventType = BusEventType(gst.MessageSegmentStart)
	BusEventTypeSegmentDone      BusEventType = BusEventType(gst.MessageSegmentDone)
	BusEventTypeDurationChanged  BusEventType = BusEventType(gst.MessageDurationChanged)
	BusEventTypeLatency          BusEventType = BusEventType(gst.MessageLatency)
	BusEventTypeAsyncDone        BusEventType = BusEventType(gst.MessageAsyncDone)
	BusEventTypeAsyncStart       BusEventType = BusEventType(gst.MessageAsyncStart)
	BusEventTypeRequestState     BusEventType = BusEventType(gst.MessageRequestState)
	BusEventTypeStepDone         BusEventType = BusEventType(gst.MessageStepDone)
	BusEventTypeStepStart        BusEventType = BusEventType(gst.MessageStepStart)
	BusEventTypeNeedContext      BusEventType = BusEventType(gst.MessageNeedContext)
	BusEventTypeHaveContext      BusEventType = BusEventType(gst.MessageHaveContext)
	BusEventTypeCustom           BusEventType = BusEventType(gst.MessageApplication)
)

type BusEvent struct {
	Type      BusEventType
	Error     error
	Debug     string
	Source    string
	Parent    string
	Timestamp time.Time
	OldState  gst.State
	NewState  gst.State
	Pending   gst.State
}

type busControllerImpl struct {
	mu     sync.Mutex
	gstBus gst.Bus

	stopContext context.Context
	stop        context.CancelFunc

	busChanRegistry map[string]chan BusEvent
	stopped         bool
	done            chan struct{}
}

type BusController interface {
	RegisterBusEventsChan() (<-chan BusEvent, error)
	Stop()
}

func CreateBusController(pipeline gst.Pipeline) (BusController, error) {

	bus := pipeline.GetBus()
	if bus == nil {
		return nil, fmt.Errorf("pipeline bus is nil")
	}
	stopContext, cancel := context.WithCancel(context.Background())

	busImpl := &busControllerImpl{
		gstBus:          bus,
		stopContext:     stopContext,
		stop:            cancel,
		busChanRegistry: make(map[string]chan BusEvent),
		done:            make(chan struct{}),
	}

	go func() {
		defer close(busImpl.done)
		defer busImpl.closeListeners()
		for {
			select {
			case <-stopContext.Done():
				return
			default:
				message := bus.TimedPop(gst.ClockTime(time.Duration(time.Millisecond * 100).Nanoseconds()))
				if message == nil {
					continue
				}
				event := getGoEventFromGstMessage(message)
				event.Timestamp = time.Now()
				busImpl.publish(event)
			}
		}
	}()

	return busImpl, nil
}

func (b *busControllerImpl) RegisterBusEventsChan() (<-chan BusEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return nil, fmt.Errorf("bus controller is stopped")
	}
	busEventChan := make(chan BusEvent, 10)
	b.busChanRegistry[utils.GenerateRandomString(12)] = busEventChan
	return busEventChan, nil
}

func (b *busControllerImpl) Stop() {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.stopped = true
	b.mu.Unlock()
	b.stop()
	// The polling goroutine owns closing listener channels. Waiting here makes
	// Stop's return a complete shutdown boundary for graph Destroy.
	if b.done != nil {
		<-b.done
	}
}

func (b *busControllerImpl) publish(event BusEvent) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	for id, listenerChan := range b.busChanRegistry {
		select {
		case listenerChan <- event:
		default:
			close(listenerChan)
			delete(b.busChanRegistry, id)
		}
	}
	b.mu.Unlock()
}

func (b *busControllerImpl) closeListeners() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.busChanRegistry {
		close(ch)
		delete(b.busChanRegistry, id)
	}
}

func getGoEventFromGstMessage(message *gst.Message) BusEvent {
	event := BusEvent{}
	if message == nil {
		return event
	}
	event.Source, event.Parent = messageSourceNames(message)

	switch message.Type() {
	case gst.MessageError:
		debug, err := message.ParseError()
		event.Type = BusEventTypeError
		event.Error = err
		event.Debug = debug
	case gst.MessageWarning:
		debug, err := message.ParseWarning()
		event.Type = BusEventTypeWarning
		event.Error = err
		event.Debug = debug
	case gst.MessageInfo:
		debug, err := message.ParseInfo()
		event.Type = BusEventTypeInfo
		event.Error = err
		event.Debug = debug
	case gst.MessageEOS:
		event.Type = BusEventTypeEOS
	case gst.MessageStateChanged:
		event.Type = BusEventTypeStateChanged
		event.OldState, event.NewState, event.Pending = message.ParseStateChanged()
	case gst.MessageStateDirty:
		event.Type = BusEventTypeStateDirty
	case gst.MessageStreamStart:
		event.Type = BusEventTypeStreamStart
	case gst.MessageStreamCollection:
		event.Type = BusEventTypeStreamCollection
	case gst.MessageSegmentStart:
		event.Type = BusEventTypeSegmentStart
	case gst.MessageSegmentDone:
		event.Type = BusEventTypeSegmentDone
	case gst.MessageDurationChanged:
		event.Type = BusEventTypeDurationChanged
	case gst.MessageLatency:
		event.Type = BusEventTypeLatency
	case gst.MessageAsyncDone:
		event.Type = BusEventTypeAsyncDone
	case gst.MessageAsyncStart:
		event.Type = BusEventTypeAsyncStart
	case gst.MessageRequestState:
		event.Type = BusEventTypeRequestState
	case gst.MessageStepDone:
		event.Type = BusEventTypeStepDone
	case gst.MessageStepStart:
		event.Type = BusEventTypeStepStart
	case gst.MessageNeedContext:
		event.Type = BusEventTypeNeedContext
	case gst.MessageHaveContext:
		event.Type = BusEventTypeHaveContext
	case gst.MessageApplication:
		event.Type = BusEventTypeCustom
	default:
		event.Type = BusEventTypeUnknown
	}
	return event
}

func messageSourceNames(message *gst.Message) (string, string) {
	source := message.Source()
	if source == nil {
		return "", ""
	}
	parent := source.GetParent()
	if parent == nil {
		return source.GetName(), ""
	}
	return source.GetName(), parent.GetName()
}
