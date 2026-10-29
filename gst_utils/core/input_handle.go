package core

type InputRegistryEntry interface {
	RegistryEntry
	GetInput() any
	DropType() ChannelDropType
}

type InputHandle[T any] struct {
	inputChan chan T
	policy    ChannelDropType
	stopChan  <-chan struct{}
}

func GetInputHandle[T any](entry InputRegistryEntry) (*InputHandle[T], error) {
	input, ok := entry.GetInput().(chan T)
	if !ok {
		return nil, ErrTypeMismatch
	}
	return &InputHandle[T]{
		inputChan: input,
		policy:    entry.DropType(),
		stopChan:  entry.Done(),
	}, nil
}

func GetInputHandleWithType[T any](entry InputRegistryEntry, t T) (*InputHandle[T], error) {
	input, ok := entry.GetInput().(chan T)
	if !ok {
		return nil, ErrTypeMismatch
	}
	return &InputHandle[T]{
		inputChan: input,
		policy:    entry.DropType(),
		stopChan:  entry.Done(),
	}, nil
}

func (h *InputHandle[T]) DropType() ChannelDropType {
	return h.policy
}

func (h *InputHandle[T]) Send(data T) error {
	select {
	case <-h.stopChan:
		return ErrEntryStopped
	default:
	}

	select {
	case h.inputChan <- data:
		return nil
	default:
	}

	switch h.policy {
	case DropNewest:
		return ErrInputDropped
	case DropOldest:
		select {
		case <-h.inputChan:
		default:
			return ErrChanFull
		}
		select {
		case h.inputChan <- data:
			return nil
		case <-h.stopChan:
			return ErrEntryStopped
		}
	default:
		return ErrChanFull
	}
}

func (h *InputHandle[T]) Done() <-chan struct{} {
	return h.stopChan
}
