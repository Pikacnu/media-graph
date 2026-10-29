package nodes

import (
	"fmt"
	"slices"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

type FlvMuxNode struct {
	*core.BaseNode
	srcPad     gst.Pad
	muxer      gst.Element
	sinkLeases []flvMuxSinkLease
}

type flvMuxSinkLease struct {
	id         string
	ghostSink  gst.GhostPad
	requestPad gst.Pad
}

var _ core.ManyToOneNode = (*FlvMuxNode)(nil)

const NodeTypeFlvMux = "FlvMux"

func init() {
	registerNode(NodeTypeFlvMux, core.NodeSpec{Sink: 2, Src: 1}, core.AdaptFactory(CreateFlvMuxNode))
}

var flvMuxPadNames = []string{"video", "audio"}

type FlvMuxConfig struct {
	ID string
}

func CreateFlvMuxNode(_ core.NodeRuntime, config FlvMuxConfig) (*FlvMuxNode, error) {
	id := config.ID
	if id == "" {
		return nil, fmt.Errorf("%w: node id is empty", core.ErrInvalidConfig)
	}

	bin := gst.NewBin(id).(gst.Bin)
	muxer := gst.ElementFactoryMake("flvmux", id+"_Mux")
	if muxer == nil {
		return nil, fmt.Errorf("%w: create flvmux", core.ErrGSTNoElements)
	}

	muxer.SetObjectProperty("streamable", true)
	bin.Add(muxer)

	srcPad := gst.NewGhostPad(id+"_src_0", muxer.GetStaticPad("src"))
	if srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	bin.AddPad(srcPad)

	muxer.SyncStateWithParent()

	base := core.NewBaseNode(id, NodeTypeFlvMux, bin)

	return &FlvMuxNode{
		BaseNode:   base,
		srcPad:     srcPad,
		muxer:      muxer,
		sinkLeases: make([]flvMuxSinkLease, 0, len(flvMuxPadNames)),
	}, nil
}

func (n *FlvMuxNode) RequestSinkPad() (gst.Pad, string, error) {
	index := len(n.sinkLeases)
	if index >= len(flvMuxPadNames) {
		return nil, "", fmt.Errorf("%w: flvmux accepts at most %d sink pads", core.ErrGSTPadNotFound, len(flvMuxPadNames))
	}
	padID := fmt.Sprintf("%s_sink_%d", n.ID(), index)

	requestPad := n.muxer.RequestPadSimple(flvMuxPadNames[index])
	if requestPad == nil {
		return nil, "", core.ErrGSTPadNotFound
	}

	ghostSink, ok := gst.NewGhostPad(padID, requestPad).(gst.GhostPad)
	if !ok || ghostSink == nil {
		n.muxer.ReleaseRequestPad(requestPad)
		return nil, "", core.ErrGSTPadNotFound
	}
	if !n.Bin().AddPad(ghostSink) {
		ghostSink.SetTarget(nil)
		n.muxer.ReleaseRequestPad(requestPad)
		return nil, "", core.ErrGSTPadNotFound
	}

	n.sinkLeases = append(n.sinkLeases, flvMuxSinkLease{
		id:         padID,
		ghostSink:  ghostSink,
		requestPad: requestPad,
	})
	return ghostSink, padID, nil
}

func (n *FlvMuxNode) RemoveSinkPad(id string) error {
	for i, lease := range n.sinkLeases {
		if lease.id == id {
			// The ghost's target is the muxer's request pad, so the pair is a
			// target relationship: clear it with SetTarget, not Unlink.
			lease.ghostSink.SetTarget(nil)
			n.Bin().RemovePad(lease.ghostSink)
			n.muxer.ReleaseRequestPad(lease.requestPad)
			n.sinkLeases = slices.Delete(n.sinkLeases, i, i+1)
			return nil
		}
	}
	return core.ErrUnLinkFailed
}

func (n *FlvMuxNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *FlvMuxNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *FlvMuxNode) GetDstIDs() []string {
	ids := make([]string, 0, len(n.sinkLeases))
	for _, lease := range n.sinkLeases {
		ids = append(ids, lease.id)
	}
	return ids
}

func (n *FlvMuxNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()
	bin.SetState(gst.State(gst.StateNull))

	for _, lease := range n.sinkLeases {
		lease.ghostSink.SetTarget(nil)
		bin.RemovePad(lease.ghostSink)
		n.muxer.ReleaseRequestPad(lease.requestPad)
	}

	n.muxer = nil
	n.srcPad = nil
	n.sinkLeases = nil
	n.Clear()
	return nil
}
