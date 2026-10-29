package nodes

import (
	"fmt"
	"slices"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TransitionNode cross-fades one video stream into another over a fixed
// duration. Two branches in, one stream out.
//
// It is built on `smpte`, the SMPTE wipe family, because `xfade` (the modern
// equivalent) is not installed here. `smpte` is I420-only on both sides, so each
// branch gets a videoconvert and the output gets one too.
type TransitionNode struct {
	*core.BaseNode
	srcPad     gst.Pad
	element    gst.Element
	outConvert gst.Element
	sinkLeases []transitionSinkLease
}

type transitionSinkLease struct {
	id         string
	ghostSink  gst.GhostPad
	convert    gst.Element
	requestPad gst.Pad
}

var _ core.ManyToOneNode = (*TransitionNode)(nil)

// NodeTypeTransition is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeTransition = "Transition"

func init() {
	registerNode(NodeTypeTransition, core.NodeSpec{Sink: 2, Src: 1}, core.AdaptFactory(CreateTransitionNode))
}

// smpte exposes two Always pads named sink1/sink2, not sink_0/sink_1. The
// outward ghost pads still use the canonical index form.
var smptePadNames = []string{"sink1", "sink2"}

// defaultTransitionType is smpte's own default, named so a caller that omits the
// field gets a visible transition rather than an error.
const defaultTransitionType = "bar-wipe-lr"

type TransitionConfig struct {
	ID   string
	Type string
	// DurationMs is how long the wipe takes to cross the frame.
	DurationMs int
	// Border is the wipe's border width in pixels.
	Border int
	// Depth is the mask bit depth.
	Depth int
	// Invert runs the wipe backwards.
	Invert bool
}

func CreateTransitionNode(_ core.NodeRuntime, config TransitionConfig) (*TransitionNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if config.Type == "" {
		config.Type = defaultTransitionType
	}

	bin := gst.NewBin(id).(gst.Bin)
	element := gst.ElementFactoryMake("smpte", id+"_Smpte")
	outConvert := gst.ElementFactoryMake("videoconvert", id+"_OutConvert")
	if element == nil || outConvert == nil {
		return nil, fmt.Errorf("%w: create transition elements", core.ErrGSTNoElements)
	}

	bin.AddMany(element, outConvert)

	// Order matters: add, link, then ghost. See pads.go.
	if !element.Link(outConvert) {
		return nil, fmt.Errorf("%w: link smpte to videoconvert", core.ErrGSTLinkErr)
	}

	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), outConvert.GetStaticPad("src"))
	if srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(srcPad) {
		return nil, fmt.Errorf("%w: add src pad to %q", core.ErrGSTPadNotFound, id)
	}

	node := &TransitionNode{
		srcPad:     srcPad,
		element:    element,
		outConvert: outConvert,
		sinkLeases: []transitionSinkLease{},
	}
	node.BaseNode = core.NewBaseNode(id, NodeTypeTransition, bin)

	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Type":       config.Type,
		"DurationMs": config.DurationMs,
		"Border":     config.Border,
		"Depth":      config.Depth,
		"Invert":     config.Invert,
	}); err != nil {
		return nil, err
	}
	node.applyTransitionProperties(config.Type, config.DurationMs, config.Border, config.Depth, config.Invert)

	element.SyncStateWithParent()
	outConvert.SyncStateWithParent()
	return node, nil
}

// applyTransitionProperties writes the live-changeable transition settings.
//
// `type` is an enum, and GValue has no string-to-enum transform, so
// SetObjectProperty would refuse the nickname. UtilSetObjectArg is GStreamer's
// own string setter and is what gst-launch uses for the same job.
func (n *TransitionNode) applyTransitionProperties(transitionType string, durationMs, border, depth int, invert bool) {
	if transitionType != "" {
		gst.UtilSetObjectArg(n.element, "type", transitionType)
	}
	if durationMs > 0 {
		n.element.SetObjectProperty("duration", uint64(durationMs)*1_000_000)
	}
	n.element.SetObjectProperty("border", int32(border))
	if depth > 0 {
		n.element.SetObjectProperty("depth", int32(depth))
	}
	n.element.SetObjectProperty("invert", invert)
}

func (n *TransitionNode) RequestSinkPad() (gst.Pad, string, error) {
	branchIndex := len(n.sinkLeases)
	if branchIndex >= len(smptePadNames) {
		return nil, "", fmt.Errorf("%w: transition accepts exactly %d sink pads",
			core.ErrGSTPadNotFound, len(smptePadNames))
	}
	padID := fmt.Sprintf("%s_sink_%d", n.ID(), branchIndex)

	convert := gst.ElementFactoryMake("videoconvert", fmt.Sprintf("%s_Sink_%d", n.ID(), branchIndex))
	if convert == nil {
		return nil, "", core.ErrGSTNoElements
	}
	bin := n.Bin()
	bin.Add(convert)

	requestPad := n.element.GetStaticPad(smptePadNames[branchIndex])
	if requestPad == nil {
		bin.Remove(convert)
		return nil, "", core.ErrGSTPadNotFound
	}

	ghostSink, ok := gst.NewGhostPad(padID, convert.GetStaticPad("sink")).(gst.GhostPad)
	if !ok || ghostSink == nil {
		bin.Remove(convert)
		return nil, "", core.ErrGSTPadNotFound
	}

	if convert.GetStaticPad("src").Link(requestPad) != gst.PadLinkOK {
		bin.Remove(convert)
		return nil, "", fmt.Errorf("%w: link videoconvert to smpte %s", core.ErrGSTLinkErr, smptePadNames[branchIndex])
	}

	if !bin.AddPad(ghostSink) {
		ghostSink.SetTarget(nil)
		bin.Remove(convert)
		return nil, "", core.ErrGSTPadNotFound
	}

	convert.SyncStateWithParent()
	n.sinkLeases = append(n.sinkLeases, transitionSinkLease{
		id:         padID,
		ghostSink:  ghostSink,
		convert:    convert,
		requestPad: requestPad,
	})
	return ghostSink, padID, nil
}

func (n *TransitionNode) RemoveSinkPad(id string) error {
	for index, lease := range n.sinkLeases {
		if lease.id != id {
			continue
		}
		lease.ghostSink.SetTarget(nil)
		lease.convert.GetStaticPad("src").Unlink(lease.requestPad)
		bin := n.Bin()
		bin.RemovePad(lease.ghostSink)
		bin.Remove(lease.convert)
		n.sinkLeases = slices.Delete(n.sinkLeases, index, index+1)
		return nil
	}
	return core.ErrUnLinkFailed
}

func (n *TransitionNode) UpdateProperties(properties map[string]interface{}) error {
	if len(properties) == 0 {
		return nil
	}
	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}

	// Read back from the store rather than from the incoming map: a caller may
	// send one key at a time, and the store holds the resolved value for the rest.
	stored := n.BaseNode.Properties()
	transitionType, _ := stored["Type"].(string)
	invert, _ := stored["Invert"].(bool)

	n.applyTransitionProperties(transitionType,
		int(propertyNumber(stored["DurationMs"], 0)),
		int(propertyNumber(stored["Border"], 0)),
		int(propertyNumber(stored["Depth"], 0)),
		invert)
	return nil
}

func (n *TransitionNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *TransitionNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *TransitionNode) GetDstIDs() []string {
	ids := make([]string, 0, len(n.sinkLeases))
	for _, lease := range n.sinkLeases {
		ids = append(ids, lease.id)
	}
	return ids
}

func (n *TransitionNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()
	bin.SetState(gst.State(gst.StateNull))

	for _, lease := range n.sinkLeases {
		lease.ghostSink.SetTarget(nil)
		bin.RemovePad(lease.ghostSink)
		bin.Remove(lease.convert)
	}

	n.element = nil
	n.outConvert = nil
	n.srcPad = nil
	n.sinkLeases = nil
	n.Clear()
	return nil
}
