package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// FileSinkNode writes whatever reaches it straight to disk through GStreamer.
//
// Chain: queue -> filesink
//
// TEMPORARILY UNUSED. Writing to local disk is not a product requirement on its
// own; saving a stream needs object storage (S3 and the like) to be useful.
// Kept because it is what makes the muxer path verifiable end to end: an
// AudioEncode -> flvmux -> FileSink chain is how we prove a real container was
// produced.
type FileSinkNode struct {
	*core.BaseNode
	sinkPad gst.Pad
	queue   gst.Element
	sink    gst.Element
}

var _ core.OneToZeroNode = (*FileSinkNode)(nil)

// NodeTypeFileSink is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeFileSink = "FileSink"

func init() {
	registerNode(NodeTypeFileSink, core.SpecOneToZero, core.AdaptFactory(CreateFileSinkNode))
}

type FileSinkConfig struct {
	ID       string
	Location string
	// Append keeps an existing file instead of truncating it on open.
	Append bool
}

func CreateFileSinkNode(_ core.NodeRuntime, config FileSinkConfig) (*FileSinkNode, error) {
	id := config.ID
	if id == "" {
		return nil, fmt.Errorf("%w: node id is empty", core.ErrGSTInvalidConfig)
	}
	if config.Location == "" {
		return nil, fmt.Errorf("%w: file location is empty", core.ErrGSTInvalidConfig)
	}

	bin := gst.NewBin(id).(gst.Bin)
	queue := gst.ElementFactoryMake("queue", id+"_Queue")
	sink := gst.ElementFactoryMake("filesink", id+"_FileSink")
	if queue == nil || sink == nil {
		return nil, fmt.Errorf("%w: create file sink elements", core.ErrGSTNoElements)
	}

	sink.SetObjectProperties(map[string]any{
		"location": config.Location,
		// A live pipeline drives media time upstream; the file sink must not
		// additionally wait on the clock, or it would throttle the graph.
		"sync":   false,
		"append": config.Append,
	})

	bin.AddMany(queue, sink)

	if !queue.Link(sink) {
		return nil, fmt.Errorf("%w: build file sink", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(id+"_sink_0", queue.GetStaticPad("sink"))
	if sinkPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(sinkPad) {
		return nil, fmt.Errorf("%w: add sink pad to %q", core.ErrGSTPadNotFound, id)
	}

	queue.SyncStateWithParent()
	sink.SyncStateWithParent()

	base := core.NewBaseNode(id, NodeTypeFileSink, bin)
	if err := base.UpdateProperties(map[string]any{"Location": config.Location, "Append": config.Append}); err != nil {
		return nil, err
	}
	base.ProtectProperties("Location", "Append")

	return &FileSinkNode{
		BaseNode: base,
		sinkPad:  sinkPad,
		queue:    queue,
		sink:     sink,
	}, nil
}

func (n *FileSinkNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *FileSinkNode) GetSrcIDs() []string { return nil }

func (n *FileSinkNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *FileSinkNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	// NULL state is what makes filesink flush and close the file; skipping it
	// would leave the container tail unwritten.
	n.Bin().SetState(gst.State(gst.StateNull))
	n.sinkPad = nil
	n.queue = nil
	n.sink = nil
	n.Clear()
	return nil
}
