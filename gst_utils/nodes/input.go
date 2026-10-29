package nodes

import (
	"fmt"
	"slices"

	"live_tools/gst_utils/chan_registry"
	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

type SourceInput struct {
	*core.BaseNode
	srcPad  gst.Pad
	entry   core.InputRegistryEntry
	config  SourceInputConfig
	runtime core.NodeRuntime
}

var _ core.ZeroToOneNode = (*SourceInput)(nil)

// NodeTypeSourceInput is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeSourceInput = "SourceInput"

func init() {
	registerNode(NodeTypeSourceInput, core.NodeSpec{Sink: 0, Src: 1}, core.AdaptFactory(CreateSourceInput))
}

type SourceInputConfig struct {
	ID         string
	IsLive     bool
	StreamType core.StreamType
	BufferSize int
	// Caps is the container format of the bytes pushed into this input.
	Caps string
	// InputDropType is the policy a producer follows when the input channel is
	// full. DropNone (the zero value) means Send reports ErrChanFull and the
	// caller decides; an ingest path can ask for DropOldest so a slow consumer
	// cannot stall the GStreamer streaming thread.
	InputDropType core.ChannelDropType
}

// defaultInputCaps matches the reference SRT pipeline, which feeds Matroska.
//const defaultInputCaps = "video/x-matroska"

func CreateSourceInput(runtime core.NodeRuntime, config SourceInputConfig) (*SourceInput, error) {
	if config.ID == "" {
		return nil, fmt.Errorf("%w: ID cannot be empty", core.ErrGSTInvalidConfig)
	}
	if config.BufferSize < 0 {
		return nil, fmt.Errorf("%w: buffer size cannot be negative", core.ErrGSTInvalidConfig)
	}

	resizerBin := gst.NewBin(config.ID).(gst.Bin)
	inputElement := gst.ElementFactoryMake("appsrc", fmt.Sprintf("%s_Input", config.ID)).(gstapp.AppSrc)
	queueElement := gst.ElementFactoryMake("queue", fmt.Sprintf("%s_Queue", config.ID))

	// vaild config

	if !slices.Contains([]core.StreamType{core.StreamTypeStream, core.StreamTypeSeekable, core.StreamTypeRandomAccess}, config.StreamType) {
		return nil, fmt.Errorf("%w: invalid stream type", core.ErrGSTInvalidConfig)
	}

	inputCaps := config.Caps
	if inputCaps == "" {
		inputCaps = "ANY" //defaultInputCaps
	}

	inputElement.SetObjectProperties(map[string]any{
		"caps":         gst.CapsFromString(inputCaps),
		"do-timestamp": true,
		"emit-signals": true,
		"is-live":      config.IsLive,
	})

	resizerBin.Add(inputElement)
	resizerBin.Add(queueElement)

	if !inputElement.Link(queueElement) {
		return nil, fmt.Errorf("%w: failed to link appsrc to queue", core.ErrGSTLinkErr)
	}

	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", config.ID), queueElement.GetStaticPad("src"))
	if srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}

	resizerBin.AddPad(srcPad)

	registryEntry := chan_registry.NewbyteInputChan(config.ID, inputBufferSize(config.BufferSize), config.InputDropType)
	if err := runtime.RegisterEntry(registryEntry); err != nil {
		return nil, fmt.Errorf("%w: register entry %q: %v", core.ErrInvalidConfig, config.ID, err)
	}
	dataChan, ok := registryEntry.GetInput().(chan []byte)
	if !ok {
		return nil, core.ErrTypeMismatch
	}

	node := &SourceInput{
		srcPad:  srcPad,
		entry:   registryEntry,
		config:  config,
		runtime: runtime,
	}
	node.BaseNode = core.NewBaseNode(config.ID, NodeTypeSourceInput, resizerBin)
	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Caps":       inputCaps,
		"IsLive":     config.IsLive,
		"StreamType": config.StreamType,
		"BufferSize": config.BufferSize,
	}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("Caps", "IsLive", "StreamType", "BufferSize")
	go node.handleInput(inputElement, dataChan, registryEntry.Done())

	return node, nil
}

func inputBufferSize(requested int) int {
	if requested < chan_registry.DefaultInputBufferSize {
		return chan_registry.DefaultInputBufferSize
	}
	return requested
}

func (n *SourceInput) Valid() error {
	if n.entry == nil {
		return fmt.Errorf("%w: input entry is not registered", core.ErrInvalidConfig)
	}
	if n.Bin() == nil {
		return fmt.Errorf("%w: node %q has no bin", core.ErrInvalidConfig, n.ID())
	}
	return nil
}

func (n *SourceInput) handleInput(inputElement gstapp.AppSrc, dataChan chan []byte, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case data, ok := <-dataChan:
			if !ok {
				return
			}
			buffer := gst.NewBufferAllocate(nil, uint(len(data)), nil)
			mapped, ok := buffer.Map(gst.MapFlags(gst.MapWrite))
			if !ok {
				continue
			}
			if _, err := mapped.Write(data); err != nil {
				mapped.Unmap()
				continue
			}
			mapped.Unmap()
			inputElement.PushBuffer(buffer)
		}
	}
}

func (n *SourceInput) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *SourceInput) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *SourceInput) GetDstIDs() []string { return nil }

func (n *SourceInput) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()

	bin.SetState(gst.State(gst.StateNull))
	n.entry.Stop()

	err := n.runtime.RemoveEntryById(n.ID())
	if err != nil {
		return fmt.Errorf("%w: failed to remove entry %q: %v", core.ErrInvalidConfig, n.ID(), err)
	}

	n.srcPad = nil
	n.Clear()
	return nil
}
