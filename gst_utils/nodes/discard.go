package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// DiscardNode consumes a branch and throws the data away.
//
// Chain: queue -> fakesink
//
// The frozen srt/ receive pipeline used exactly this shape to keep an unused
// branch flowing so that its upstream tee was not blocked by a branch nobody
// consumed.
type DiscardNode struct {
	*core.BaseNode
	sinkPad gst.Pad
	queue   gst.Element
	sink    gst.Element
}

var _ core.OneToZeroNode = (*DiscardNode)(nil)

// NodeTypeDiscard is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeDiscard = "Discard"

func init() {
	registerNode(NodeTypeDiscard, core.SpecOneToZero, core.AdaptFactory(CreateDiscardNode))
}

type DiscardConfig struct {
	ID string
	// Sync makes the sink wait on the clock. Default false: a discarding branch
	// exists to drain, not to pace the graph.
	Sync bool
}

func CreateDiscardNode(_ core.NodeRuntime, config DiscardConfig) (*DiscardNode, error) {
	id := config.ID
	if id == "" {
		return nil, fmt.Errorf("%w: node id is empty", core.ErrGSTInvalidConfig)
	}

	bin := gst.NewBin(id).(gst.Bin)
	queue := gst.ElementFactoryMake("queue", id+"_Queue")
	sink := gst.ElementFactoryMake("fakesink", id+"_FakeSink")
	if queue == nil || sink == nil {
		return nil, fmt.Errorf("%w: create discard elements", core.ErrGSTNoElements)
	}

	sink.SetObjectProperties(map[string]any{
		"sync": config.Sync,
		// async=false lets the sink report preroll immediately; otherwise a
		// discard branch can hold the whole pipeline in PAUSED.
		"async": false,
	})

	bin.AddMany(queue, sink)

	if !queue.Link(sink) {
		return nil, fmt.Errorf("%w: build discard sink", core.ErrGSTLinkErr)
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

	base := core.NewBaseNode(id, NodeTypeDiscard, bin)
	if err := base.UpdateProperties(map[string]any{"Sync": config.Sync}); err != nil {
		return nil, err
	}
	base.ProtectProperties("Sync")

	return &DiscardNode{
		BaseNode: base,
		sinkPad:  sinkPad,
		queue:    queue,
		sink:     sink,
	}, nil
}

func (n *DiscardNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *DiscardNode) GetSrcIDs() []string { return nil }

func (n *DiscardNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *DiscardNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	n.Bin().SetState(gst.State(gst.StateNull))
	n.sinkPad = nil
	n.queue = nil
	n.sink = nil
	n.Clear()
	return nil
}
