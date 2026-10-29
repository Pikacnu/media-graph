package nodes

import (
	"fmt"
	"slices"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

type AudioCombinerNode struct {
	*core.BaseNode
	srcPad         gst.GhostPad
	processElement gst.Element
	sinkCounter    uint16
	sinkLeases     []AudioMixerSinkLease
}

// AudioMixerSinkLease records everything taken for one sink branch, so Destroy
// and RemoveSinkPad can release it. The request pad must be given back to the
// owner element; dropping the Go reference does not release it.
type AudioMixerSinkLease struct {
	id             string
	ghostSinkPad   gst.GhostPad
	convertElement gst.Element
	requestPad     gst.Pad
}

var _ core.ManyToOneNode = (*AudioCombinerNode)(nil)

// NodeTypeAudioCombiner is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeAudioCombiner = "AudioCombiner"

func init() {
	registerNode(NodeTypeAudioCombiner, core.SpecManyToOne, core.AdaptFactory(CreateAudioCombinerNode))
}

var possibleAudioCombinerElements = []string{
	"audiomixer",
	"adder",
}

type AudioCombinerConfig struct {
	ID          string
	ElementName string
}

func CreateAudioCombinerNode(_ core.NodeRuntime, config AudioCombinerConfig) (*AudioCombinerNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	elementName := config.ElementName
	if elementName == "" {
		elementName = "adder"
	}

	if !slices.Contains(possibleAudioCombinerElements, elementName) {
		return nil, core.ErrInvalidConfig
	}

	mixerBin := gst.NewBin(id).(gst.Bin)
	processElement := gst.ElementFactoryMake(elementName, fmt.Sprintf("%s_Mixer", id))
	if processElement == nil {
		return nil, fmt.Errorf("%w: create %s", core.ErrGSTNoElements, elementName)
	}
	processElementSrcPad := processElement.GetStaticPad("src")
	mixerBin.Add(processElement)

	// Ghost pad last: gst_ghost_pad_new links the ghost to its target, so the
	// element should already be inside the bin when it is created.
	ghostSrc := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), processElementSrcPad).(gst.GhostPad)
	if ghostSrc == nil {
		return nil, core.ErrGSTPadNotFound
	}
	mixerBin.AddPad(ghostSrc)

	base := core.NewBaseNode(id, NodeTypeAudioCombiner, mixerBin)
	if err := base.UpdateProperties(map[string]any{"ElementName": elementName}); err != nil {
		return nil, err
	}

	return &AudioCombinerNode{
		BaseNode:       base,
		srcPad:         ghostSrc,
		processElement: processElement,
		sinkCounter:    0,
		sinkLeases:     []AudioMixerSinkLease{},
	}, nil
}

// RequestSinkPad adds one mixer input. The mixer sink pad is a request pad this
// node must release.
func (n *AudioCombinerNode) RequestSinkPad() (gst.Pad, string, error) {
	branchIndex := n.sinkCounter
	n.sinkCounter++
	padID := fmt.Sprintf("%s_sink_%d", n.ID(), branchIndex)
	audioConvertElement := gst.ElementFactoryMake(
		"audioconvert",
		fmt.Sprintf("%s_Sink_%d", n.ID(), branchIndex),
	)
	if audioConvertElement == nil {
		n.sinkCounter--
		return nil, "", core.ErrGSTNoElements
	}
	processElementSinkPad := n.processElement.RequestPadSimple("sink_%u")
	if processElementSinkPad == nil {
		n.sinkCounter--
		return nil, "", core.ErrGSTPadNotFound
	}
	audioConvertSinkPad := audioConvertElement.GetStaticPad("sink")

	bin := n.Bin()
	bin.Add(audioConvertElement)

	ghostSink, ok := gst.NewGhostPad(padID, audioConvertSinkPad).(gst.GhostPad)
	if !ok || ghostSink == nil {
		bin.Remove(audioConvertElement)
		n.processElement.ReleaseRequestPad(processElementSinkPad)
		n.sinkCounter--
		return nil, "", core.ErrGSTPadNotFound
	}

	linkRet := audioConvertElement.GetStaticPad("src").Link(processElementSinkPad)
	if linkRet != gst.PadLinkOK {
		bin.Remove(audioConvertElement)
		n.processElement.ReleaseRequestPad(processElementSinkPad)
		n.sinkCounter--
		return nil, "", fmt.Errorf("%w: failed to link audioconvert to audiomixer", core.ErrGSTLinkErr)
	}

	bin.AddPad(ghostSink)
	audioConvertElement.SyncStateWithParent()
	n.sinkLeases = append(n.sinkLeases, AudioMixerSinkLease{
		id:             padID,
		ghostSinkPad:   ghostSink,
		convertElement: audioConvertElement,
		requestPad:     processElementSinkPad,
	})
	return ghostSink, padID, nil
}

func (n *AudioCombinerNode) UpdateProperties(properties map[string]interface{}) error {

	if properties == nil || len(properties) == 0 {
		return nil
	}

	backupProperties := n.BaseNode.Properties()

	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}

	for key, value := range properties {
		switch key {
		case "ElementName":
			if strVal, ok := value.(string); ok {
				if !slices.Contains(possibleAudioCombinerElements, strVal) {
					return core.ErrInvalidConfig
				}
				if strVal != backupProperties["ElementName"] {
					// Replace the process element with a new one
					probeIDs := make([]uint32, 0, len(n.sinkLeases))
					for _, lease := range n.sinkLeases {
						lease.ghostSinkPad.SetTarget(nil)
						leaseConvertSrcPad := lease.convertElement.GetStaticPad("src")
						leaseConvertSrcPad.Unlink(lease.requestPad)
						n.processElement.ReleaseRequestPad(lease.requestPad)
						probeID := leaseConvertSrcPad.AddProbe(gst.PadProbeType(gst.PadProbeTypeBlock), func(pad gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
							return gst.PadProbeReturn(gst.PadProbeOK)
						})
						probeIDs = append(probeIDs, probeID)
					}
					n.srcPad.SetTarget(nil)
					newProcessNode := gst.ElementFactoryMake(strVal, fmt.Sprintf("%s_Mixer", n.ID()))
					if newProcessNode == nil {
						return fmt.Errorf("%w: create %s", core.ErrGSTNoElements, strVal)
					}
					n.Bin().Add(newProcessNode)
					n.Bin().Remove(n.processElement)
					n.processElement = newProcessNode
					n.srcPad.SetTarget(n.processElement.GetStaticPad("src"))
					// reconnect all the sink pads to the new process element
					for i, lease := range n.sinkLeases {
						leaseConvertSrcPad := lease.convertElement.GetStaticPad("src")
						leaseProcessSinkPad := n.processElement.RequestPadSimple(fmt.Sprintf("sink_%d", i))
						leaseConvertSrcPad.Link(leaseProcessSinkPad)
						leaseConvertSinkPad := lease.convertElement.GetStaticPad("sink")
						leaseConvertSinkPad.RemoveProbe(probeIDs[i])
						lease.ghostSinkPad.SetTarget(leaseConvertSinkPad)
						n.sinkLeases[i] = AudioMixerSinkLease{
							id:             lease.id,
							ghostSinkPad:   lease.ghostSinkPad,
							convertElement: lease.convertElement,
							requestPad:     leaseProcessSinkPad,
						}
					}
				} else {
					// pass
				}
			} else {
				return core.ErrInvalidConfig
			}
		}
	}
	return nil
}

func (n *AudioCombinerNode) RemoveSinkPad(id string) error {
	for i, lease := range n.sinkLeases {
		if lease.id == id {
			lease.ghostSinkPad.SetTarget(nil)
			lease.convertElement.GetStaticPad("src").Unlink(lease.requestPad)
			bin := n.Bin()
			bin.RemovePad(lease.ghostSinkPad)
			bin.Remove(lease.convertElement)
			n.processElement.ReleaseRequestPad(lease.requestPad)
			n.sinkLeases = slices.Delete(n.sinkLeases, i, i+1)
			return nil
		}
	}
	return core.ErrUnLinkFailed
}

func (n *AudioCombinerNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *AudioCombinerNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

// GetDstIDs lists every sink branch this node exposes.
func (n *AudioCombinerNode) GetDstIDs() []string {
	ids := make([]string, 0, len(n.sinkLeases))
	for _, lease := range n.sinkLeases {
		ids = append(ids, lease.id)
	}
	return ids
}

func (n *AudioCombinerNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()
	bin.SetState(gst.State(gst.StateNull))

	for _, lease := range n.sinkLeases {
		bin.RemovePad(lease.ghostSinkPad)
		bin.Remove(lease.convertElement)
		n.processElement.ReleaseRequestPad(lease.requestPad)
	}

	n.processElement = nil
	n.srcPad = nil
	n.sinkLeases = nil
	n.Clear()
	return nil
}
