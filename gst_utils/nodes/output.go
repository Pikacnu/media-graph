package nodes

import (
	"fmt"
	"sync"

	"live_tools/gst_utils/chan_registry"
	"live_tools/gst_utils/core"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

type OutputNode struct {
	*core.BaseNode
	entry      *chan_registry.OutputEntry[[]byte]
	runtime    core.NodeRuntime
	sinkPad    gst.Pad
	cancel     chan struct{}
	cancelOnce sync.Once
	// The new-sample handler runs on a streaming thread, so the handle is kept
	// to disconnect it in Destroy.
	appSink      gstapp.AppSink
	sampleHandle gobject.SignalHandle
}

var _ core.OneToZeroNode = (*OutputNode)(nil)

// NodeTypeOutput is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeOutput = "Output"

func init() {
	registerNode(NodeTypeOutput, core.SpecOneToZero, core.AdaptFactory(CreateOutputNode))
}

// OutputConfig is identity and caps only; the node owns its buffer channel and
// publishes it through the registry.
type OutputConfig struct {
	ID         string
	Caps       string
	TargetCaps *gst.Caps
	BufferSize int
}

func CreateOutputNode(runtime core.NodeRuntime, config OutputConfig) (*OutputNode, error) {
	id := config.ID

	videoOutputCaps := config.TargetCaps
	if videoOutputCaps == nil {
		videoOutputCaps = gst.CapsFromString(config.Caps)
	}

	if id == "" {
		return nil, core.ErrInvalidConfig
	}

	entry, err := chan_registry.NewByteOutputEntry(id, config.BufferSize, core.DropOldest)
	if err != nil {
		return nil, err
	}
	if err := runtime.RegisterEntry(entry); err != nil {
		return nil, fmt.Errorf("%w: register entry %q: %v", core.ErrInvalidConfig, id, err)
	}

	outputBin := gst.NewBin(id).(gst.Bin)
	videoConvertElement := gst.ElementFactoryMake("videoconvert", fmt.Sprintf("%s_VideoConvert", id))
	videoCapsFilterElement := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("%s_VideoCapsFilter", id))
	appSinkElement := gst.ElementFactoryMake("appsink", fmt.Sprintf("%s_AppSink", id)).(gstapp.AppSink)

	videoCapsFilterElement.SetObjectProperty("caps", videoOutputCaps)

	outputBin.Add(videoConvertElement)
	outputBin.Add(videoCapsFilterElement)
	outputBin.Add(appSinkElement)

	// NewGhostPad links its target internally, so the chain must be linked first.
	if !videoConvertElement.Link(videoCapsFilterElement) {
		return nil, fmt.Errorf("%w: failed to link videoconvert to capsfilter", core.ErrGSTLinkErr)
	}
	if !videoCapsFilterElement.Link(appSinkElement) {
		return nil, fmt.Errorf("%w: failed to link capsfilter to appsink", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), videoConvertElement.GetStaticPad("sink"))
	if sinkPad == nil {
		return nil, core.ErrGSTPadNotFound
	}

	outputBin.AddPad(sinkPad)

	appSinkElement.SetObjectProperties(map[string]any{
		"drop": true,
		"sync": false,
	})

	appSinkElement.SetEmitSignals(true)

	node := &OutputNode{
		entry:   entry,
		runtime: runtime,
		cancel:  make(chan struct{}),
		appSink: appSinkElement,
	}
	node.sampleHandle = appSinkElement.Connect("new-sample", func(sink gstapp.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowOK
		}
		buffer := sample.GetBuffer()
		if buffer == nil {
			return gst.FlowOK
		}
		mappedBuffer, ok := buffer.Map(gst.MapFlags(gst.MapRead))
		if !ok {
			return gst.FlowOK
		}

		data := mappedBuffer.Data()
		copiedData := make([]byte, len(data))
		copy(copiedData, data)
		mappedBuffer.Unmap()

		select {
		case <-node.cancel:
			return gst.FlowEOS
		default:
		}

		node.entry.Publish(copiedData)
		return gst.FlowOK
	})

	videoConvertElement.SyncStateWithParent()
	videoCapsFilterElement.SyncStateWithParent()
	appSinkElement.SyncStateWithParent()

	base := core.NewBaseNode(id, NodeTypeOutput, outputBin)
	node.BaseNode = base
	node.sinkPad = sinkPad

	if err := base.UpdateProperties(map[string]any{"Caps": config.Caps, "BufferSize": config.BufferSize}); err != nil {
		return nil, err
	}
	base.ProtectProperties("Caps", "BufferSize")

	return node, nil
}

func (n *OutputNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *OutputNode) GetSrcIDs() []string { return nil }

func (n *OutputNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *OutputNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()

	if n.appSink != nil && n.sampleHandle != 0 {
		n.appSink.HandlerDisconnect(n.sampleHandle)
		n.sampleHandle = 0
	}

	bin.SetState(gst.State(gst.StateNull))

	n.cancelOnce.Do(func() {
		close(n.cancel)
	})

	if n.entry != nil {
		n.entry.Stop()
		if err := n.runtime.RemoveEntryById(n.ID()); err != nil {
			return fmt.Errorf("%w: failed to remove entry %q: %v", core.ErrInvalidConfig, n.ID(), err)
		}
		n.entry = nil
	}

	n.sinkPad = nil
	n.appSink = nil
	n.Clear()
	return nil
}
