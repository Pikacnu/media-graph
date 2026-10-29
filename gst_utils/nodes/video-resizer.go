package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

type VideoResizerNode struct {
	*core.BaseNode
	sinkPad    gst.Pad
	srcPad     gst.Pad
	capsFilter gst.Element
}

var _ core.OneToOneNode = (*VideoResizerNode)(nil)

// NodeTypeVideoResizer is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeVideoResizer = "VideoResizer"

func init() {
	registerNode(NodeTypeVideoResizer, core.SpecOneToOne, core.AdaptFactory(CreateVideoResizerNode))
}

type VideoResizerConfig struct {
	ID         string
	Caps       string
	TargetCaps *gst.Caps
}

func CreateVideoResizerNode(_ core.NodeRuntime, config VideoResizerConfig) (*VideoResizerNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}

	targetSize := config.TargetCaps
	if targetSize == nil {
		targetSize = gst.CapsFromString(config.Caps)
	}
	if targetSize == nil {
		return nil, fmt.Errorf("%w: video resizer needs target caps", core.ErrInvalidConfig)
	}

	resizerBin := gst.NewBin(id).(gst.Bin)
	videoConvertElement := gst.ElementFactoryMake("videoconvert", fmt.Sprintf("%s_VideoConvert", id))

	videoScaler := gst.ElementFactoryMake("videoscale", fmt.Sprintf("%s_VideoScaler", id))
	videoCapsFilterElement := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("%s_VideoCapsFilter", id))

	videoCapsFilterElement.SetObjectProperty("caps", targetSize)

	resizerBin.Add(videoConvertElement)
	resizerBin.Add(videoScaler)
	resizerBin.Add(videoCapsFilterElement)

	// Order matters: add, link, then ghost. See pads.go.
	if !videoConvertElement.Link(videoScaler) {
		return nil, fmt.Errorf("%w: failed to link videoconvert to videoscale", core.ErrGSTLinkErr)
	}
	if !videoScaler.Link(videoCapsFilterElement) {
		return nil, fmt.Errorf("%w: failed to link videoscale to capsfilter", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), videoConvertElement.GetStaticPad("sink"))
	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), videoCapsFilterElement.GetStaticPad("src"))
	if sinkPad == nil || srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}

	resizerBin.AddPad(sinkPad)
	resizerBin.AddPad(srcPad)

	base := core.NewBaseNode(id, NodeTypeVideoResizer, resizerBin)
	if err := base.UpdateProperties(map[string]any{"Caps": config.Caps}); err != nil {
		return nil, err
	}

	return &VideoResizerNode{
		BaseNode:   base,
		sinkPad:    sinkPad,
		srcPad:     srcPad,
		capsFilter: videoCapsFilterElement,
	}, nil
}

func (n *VideoResizerNode) UpdateProperties(properties map[string]interface{}) error {
	raw, hasCaps := properties["Caps"]
	if hasCaps {
		caps, ok := raw.(string)
		if !ok || caps == "" {
			return core.ErrInvalidConfig
		}
	}

	previous, _ := n.BaseNode.Properties()["Caps"].(string)
	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}

	caps, _ := raw.(string)
	if !hasCaps || caps == previous {
		return nil
	}
	n.capsFilter.SetObjectProperty("caps", gst.CapsFromString(caps))
	return nil
}

func (n *VideoResizerNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *VideoResizerNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *VideoResizerNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *VideoResizerNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *VideoResizerNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()

	bin.SetState(gst.State(gst.StateNull))

	n.sinkPad = nil
	n.srcPad = nil
	n.Clear()
	return nil
}
