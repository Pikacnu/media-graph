package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// VolumeNode applies linear gain to an audio stream. It is the backend half of
// the frontend's volume slider.
//
// The sink-side audioconvert is there so the node accepts any audio, including
// formats `volume` cannot process directly; the output format is whatever
// negotiation settles on, which is the graph's business to constrain.
type VolumeNode struct {
	*core.BaseNode
	sinkPad gst.Pad
	srcPad  gst.Pad
	convert gst.Element
	volume  gst.Element
}

var _ core.OneToOneNode = (*VolumeNode)(nil)

// NodeTypeVolume is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeVolume = "Volume"

func init() {
	registerNode(NodeTypeVolume, core.SpecOneToOne, core.AdaptFactory(CreateVolumeNode))
}

type VolumeConfig struct {
	ID string
	// Volume is linear gain where 1.0 is unity. It is a pointer so an omitted
	// field means "leave the default" instead of muting the stream, which is
	// what a plain zero value would do. A caller that wants silence passes 0.
	Volume *float64
}

func CreateVolumeNode(_ core.NodeRuntime, config VolumeConfig) (*VolumeNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}

	gain := 1.0
	if config.Volume != nil {
		if *config.Volume < 0 {
			return nil, fmt.Errorf("%w: volume must not be negative", core.ErrInvalidConfig)
		}
		gain = *config.Volume
	}

	bin := gst.NewBin(id).(gst.Bin)
	convert := gst.ElementFactoryMake("audioconvert", id+"_AudioConvert")
	volume := gst.ElementFactoryMake("volume", id+"_Volume")
	if convert == nil || volume == nil {
		return nil, fmt.Errorf("%w: create volume elements", core.ErrGSTNoElements)
	}

	bin.AddMany(convert, volume)

	// Order matters: add, link, then ghost. See pads.go.
	if !convert.Link(volume) {
		return nil, fmt.Errorf("%w: link audioconvert to volume", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), convert.GetStaticPad("sink"))
	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), volume.GetStaticPad("src"))
	if sinkPad == nil || srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(sinkPad) || !bin.AddPad(srcPad) {
		return nil, fmt.Errorf("%w: add pads to %q", core.ErrGSTPadNotFound, id)
	}

	volume.SetObjectProperty("volume", gain)

	node := &VolumeNode{
		sinkPad: sinkPad,
		srcPad:  srcPad,
		convert: convert,
		volume:  volume,
	}
	node.BaseNode = core.NewBaseNode(id, NodeTypeVolume, bin)

	if err := node.BaseNode.UpdateProperties(map[string]any{"Volume": gain}); err != nil {
		return nil, err
	}

	convert.SyncStateWithParent()
	volume.SyncStateWithParent()
	return node, nil
}

func (n *VolumeNode) UpdateProperties(properties map[string]interface{}) error {
	if len(properties) == 0 {
		return nil
	}
	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}

	stored := n.BaseNode.Properties()
	gain := propertyNumber(stored["Volume"], 1.0)
	if gain < 0 {
		return fmt.Errorf("%w: volume must not be negative", core.ErrInvalidConfig)
	}
	n.volume.SetObjectProperty("volume", gain)
	return nil
}

func (n *VolumeNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *VolumeNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *VolumeNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *VolumeNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *VolumeNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	n.Bin().SetState(gst.State(gst.StateNull))

	n.convert = nil
	n.volume = nil
	n.sinkPad = nil
	n.srcPad = nil
	n.Clear()
	return nil
}
