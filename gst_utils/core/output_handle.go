package core

type OutputRegistryEntry interface {
	RegistryEntry
	AddOutput(id string, bufferSize int) (any, error)
	GetOutput(id string) (any, bool)
	GetOutputs() map[string]any
	RemoveOutput(id string) error
}

type OutputHandle[T any] struct {
	outputChan chan T
	stopChan   <-chan struct{}
}

func GetOutputHandle[T any](entry OutputRegistryEntry, id string, bufferSize int) (*OutputHandle[T], error) {
	output, exists := entry.GetOutput(id)
	if !exists {
		created, err := entry.AddOutput(id, bufferSize)
		if err != nil {
			return nil, err
		}
		output = created
	}
	outputChan, ok := output.(chan T)
	if !ok {
		return nil, ErrTypeMismatch
	}
	return &OutputHandle[T]{
		outputChan: outputChan,
		stopChan:   entry.Done(),
	}, nil
}

func (h *OutputHandle[T]) RecvChan() <-chan T { return h.outputChan }

func (h *OutputHandle[T]) Recv() (T, bool) {
	select {
	case <-h.stopChan:
		var zero T
		return zero, false
	case value, ok := <-h.outputChan:
		return value, ok
	}
}

func (h *OutputHandle[T]) Done() <-chan struct{} { return h.stopChan }
