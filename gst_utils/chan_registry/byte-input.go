package chan_registry

import (
	"live_tools/gst_utils/core"
	"reflect"
	"sync"
)

type byteInputChan struct {
	id         string
	stopChan   chan struct{}
	inputChan  chan []byte
	once       sync.Once
	bufferSize int
	dropType   core.ChannelDropType
}

var _ core.InputRegistryEntry = (*byteInputChan)(nil)

const DefaultInputBufferSize = 10

func NewbyteInputChan(id string, bufferSize int, dropType core.ChannelDropType) core.InputRegistryEntry {
	return &byteInputChan{
		id:         id,
		stopChan:   make(chan struct{}),
		inputChan:  make(chan []byte, bufferSize),
		bufferSize: bufferSize,
		dropType:   dropType,
	}
}

func (s *byteInputChan) ID() string {
	return s.id
}

func (s *byteInputChan) Stop() error {
	s.once.Do(func() {
		close(s.stopChan)
	})
	return nil
}

func (s *byteInputChan) Done() <-chan struct{} {
	return s.stopChan
}

func (s *byteInputChan) GetInput() any {
	return s.inputChan
}

func (s *byteInputChan) DropType() core.ChannelDropType {
	return s.dropType
}

func (s *byteInputChan) BufferSize() int {
	return s.bufferSize
}
func (s *byteInputChan) Type() reflect.Type {
	return reflect.TypeOf([]byte{})
}
