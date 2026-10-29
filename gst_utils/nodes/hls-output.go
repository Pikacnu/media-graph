package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// HLSOutputNode writes a live stream out as HLS: MPEG-TS segments plus a
// playlist. It is the file/HTTP counterpart of RTMPOutput.
//
// It accepts an already-encoded stream and only muxes, so the encoder stays a
// separate node the graph can share between platforms. mpegtsmux requires a
// byte-stream H.264 (or another MPEG-TS payload), which is what
// VideoTranscode produces with its default x264enc.
type HLSOutputNode struct {
	*core.BaseNode
	sinkPad    gst.Pad
	queue      gst.Element
	muxer      gst.Element
	sink       gst.Element
	muxSinkPad gst.Pad
}

var _ core.OneToZeroNode = (*HLSOutputNode)(nil)

// NodeTypeHLSOutput is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeHLSOutput = "HLSOutput"

func init() {
	registerNode(NodeTypeHLSOutput, core.SpecOneToZero, core.AdaptFactory(CreateHLSOutputNode))
}

type HLSOutputConfig struct {
	ID string
	// Location is the segment filename pattern, e.g. "segment%05d.ts". It must
	// carry a counter, otherwise every segment overwrites the previous one.
	Location string
	// PlaylistLocation is the .m3u8 path.
	PlaylistLocation string
	// TargetDuration is the segment length in seconds.
	TargetDuration int
	// MaxFiles caps how many segments stay on disk (0 = keep all).
	MaxFiles int
	// PlaylistLength caps how many segments the playlist lists (0 = infinite).
	PlaylistLength int
}

func CreateHLSOutputNode(_ core.NodeRuntime, config HLSOutputConfig) (*HLSOutputNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if config.Location == "" || config.PlaylistLocation == "" {
		return nil, fmt.Errorf("%w: HLS output needs a segment location and a playlist location", core.ErrInvalidConfig)
	}

	bin := gst.NewBin(id).(gst.Bin)
	queue := gst.ElementFactoryMake("queue", id+"_Queue")
	muxer := gst.ElementFactoryMake("mpegtsmux", id+"_MpegTsMux")
	sink := gst.ElementFactoryMake("hlssink", id+"_HLSSink")
	if queue == nil || muxer == nil || sink == nil {
		return nil, fmt.Errorf("%w: create HLS output elements", core.ErrGSTNoElements)
	}

	sink.SetObjectProperties(map[string]any{
		"location":          config.Location,
		"playlist-location": config.PlaylistLocation,
		// guint properties need the unsigned width: gobject transforms between
		// the same signedness only, so an int32 would be refused.
		"target-duration": uint32(config.TargetDuration),
		"max-files":       uint32(config.MaxFiles),
		"playlist-length": uint32(config.PlaylistLength),
	})

	bin.AddMany(queue, muxer, sink)

	muxSinkPad := muxer.RequestPadSimple("sink_%d")
	if muxSinkPad == nil {
		return nil, fmt.Errorf("%w: request mpegtsmux sink pad", core.ErrGSTPadNotFound)
	}
	// Order matters: add, link, then ghost. See pads.go.
	if queue.GetStaticPad("src").Link(muxSinkPad) != gst.PadLinkOK || !muxer.Link(sink) {
		muxer.ReleaseRequestPad(muxSinkPad)
		return nil, fmt.Errorf("%w: build HLS output", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), queue.GetStaticPad("sink"))
	if sinkPad == nil {
		muxer.ReleaseRequestPad(muxSinkPad)
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(sinkPad) {
		muxer.ReleaseRequestPad(muxSinkPad)
		return nil, fmt.Errorf("%w: add sink pad to %q", core.ErrGSTPadNotFound, id)
	}

	node := &HLSOutputNode{
		sinkPad:    sinkPad,
		queue:      queue,
		muxer:      muxer,
		sink:       sink,
		muxSinkPad: muxSinkPad,
	}
	node.BaseNode = core.NewBaseNode(id, NodeTypeHLSOutput, bin)

	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Location":         config.Location,
		"PlaylistLocation": config.PlaylistLocation,
		"TargetDuration":   config.TargetDuration,
		"MaxFiles":         config.MaxFiles,
		"PlaylistLength":   config.PlaylistLength,
	}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("Location", "PlaylistLocation")

	queue.SyncStateWithParent()
	muxer.SyncStateWithParent()
	sink.SyncStateWithParent()
	return node, nil
}

func (n *HLSOutputNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *HLSOutputNode) GetSrcIDs() []string { return nil }

func (n *HLSOutputNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *HLSOutputNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	// NULL state is what makes hlssink close the current segment and flush the
	// playlist; skipping it leaves an unplayable tail.
	n.Bin().SetState(gst.State(gst.StateNull))

	if n.muxer != nil && n.muxSinkPad != nil {
		n.muxer.ReleaseRequestPad(n.muxSinkPad)
		n.muxSinkPad = nil
	}

	n.queue = nil
	n.muxer = nil
	n.sink = nil
	n.sinkPad = nil
	n.Clear()
	return nil
}
