package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

type AudioRegulatorNode struct {
	*core.BaseNode
	sinkPad    gst.Pad
	srcPad     gst.Pad
	capsFilter gst.Element
}

var _ core.OneToOneNode = (*AudioRegulatorNode)(nil)

// NodeTypeAudioRegulator is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeAudioRegulator = "AudioRegulator"

func init() {
	registerNode(NodeTypeAudioRegulator, core.SpecOneToOne, core.AdaptFactory(CreateAudioRegulatorNode))
}

type AudioRegulatorConfig struct {
	ID           string
	TargetFormat string
}

func CreateAudioRegulatorNode(_ core.NodeRuntime, config AudioRegulatorConfig) (*AudioRegulatorNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	targetFormat := config.TargetFormat
	if targetFormat == "" {
		return nil, core.ErrInvalidConfig
	}
	regulatorBin := gst.NewBin(id).(gst.Bin)
	audioConvertElement := gst.ElementFactoryMake("audioconvert", fmt.Sprintf("%s_AudioConvert", id))
	audioResampleElement := gst.ElementFactoryMake("audioresample", fmt.Sprintf("%s_AudioResample", id))
	audioCapsFilterElement := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("%s_AudioCapsFilter", id))
	audioCaps := gst.CapsFromString(targetFormat)
	audioCapsFilterElement.SetObjectProperty("caps", audioCaps)

	regulatorBin.Add(audioConvertElement)
	regulatorBin.Add(audioResampleElement)
	regulatorBin.Add(audioCapsFilterElement)

	// Order matters: add, link, then ghost. See pads.go.
	if !audioConvertElement.Link(audioResampleElement) {
		return nil, fmt.Errorf("%w: failed to link audioconvert to audioresample", core.ErrGSTLinkErr)
	}
	if !audioResampleElement.Link(audioCapsFilterElement) {
		return nil, fmt.Errorf("%w: failed to link audioresample to capsfilter", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), audioConvertElement.GetStaticPad("sink"))
	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), audioCapsFilterElement.GetStaticPad("src"))
	if sinkPad == nil || srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}

	regulatorBin.AddPad(sinkPad)
	regulatorBin.AddPad(srcPad)

	audioConvertElement.SyncStateWithParent()
	audioResampleElement.SyncStateWithParent()
	audioCapsFilterElement.SyncStateWithParent()

	base := core.NewBaseNode(id, NodeTypeAudioRegulator, regulatorBin)
	if err := base.UpdateProperties(map[string]any{"TargetFormat": targetFormat}); err != nil {
		return nil, err
	}

	return &AudioRegulatorNode{
		BaseNode:   base,
		sinkPad:    sinkPad,
		srcPad:     srcPad,
		capsFilter: audioCapsFilterElement,
	}, nil
}

func (n *AudioRegulatorNode) UpdateProperties(properties map[string]interface{}) error {
	raw, hasTarget := properties["TargetFormat"]
	if hasTarget {
		target, ok := raw.(string)
		if !ok || target == "" {
			return core.ErrInvalidConfig
		}
	}

	previous, _ := n.BaseNode.Properties()["TargetFormat"].(string)
	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}

	target, _ := raw.(string)
	if !hasTarget || target == previous {
		return nil
	}
	n.capsFilter.SetObjectProperty("caps", gst.CapsFromString(target))
	return nil
}

func (n *AudioRegulatorNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *AudioRegulatorNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *AudioRegulatorNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *AudioRegulatorNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *AudioRegulatorNode) Destroy() error {
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
