package nodes

import (
	"encoding/json"
	"fmt"
	"slices"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// CompositorNode composites several video streams onto one canvas. It is the
// backend half of the frontend's Scene/Mixer node: the graph decides which
// sources enter, and each branch's placement is a property of its compositor
// pad.
//
// Distinct from AudioCombinerNode in one way that matters: a mixer pad has no
// geometry, while a compositor pad carries x/y/size/z-order/alpha. Those live on
// the request pad itself, so they are set on the pad rather than on the element.
type CompositorNode struct {
	*core.BaseNode
	srcPad     gst.Pad
	element    gst.Element
	capsFilter gst.Element
	sinkLeases []compositorSinkLease
	// layouts is the placement template, applied to each branch as it is
	// requested. It is a template rather than a list of branches to create
	// because how many branches exist is the graph's declaration to make.
	layouts []CompositorPadLayout
}

// compositorSinkLease records one branch. requestPad IS the compositor's own pad,
// so placement is written straight onto it.
type compositorSinkLease struct {
	id         string
	ghostSink  gst.GhostPad
	convert    gst.Element
	requestPad gst.Pad
}

var _ core.ManyToOneNode = (*CompositorNode)(nil)

// NodeTypeCompositor is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeCompositor = "Compositor"

func init() {
	registerNode(NodeTypeCompositor, core.SpecManyToOne, core.AdaptFactory(CreateCompositorNode))
}

// defaultCompositorFramerate is used when a caller names no canvas rate.
const defaultCompositorFramerate = "30/1"

// CompositorPadLayout is one branch's placement on the canvas.
//
// Zero is a valid X/Y and ZOrder, so those are always applied. Width, Height and
// Alpha are NOT applied when they are zero, because the compositor's own
// defaults (natural size, opaque) are the useful ones and a zero would mean an
// invisible or zero-sized picture. A caller that genuinely wants alpha 0 can
// remove the branch instead.
type CompositorPadLayout struct {
	X      int
	Y      int
	Width  int
	Height int
	ZOrder int
	Alpha  float64
}

type BackgroundType uint

const (
	BackgroundChecker BackgroundType = iota
	BackgroundBlack
	BackgroundWhite
	BackgroundTransparent
)

// applyCompositorBackground sets the canvas background by enum ordinal. An enum
// takes its ordinal straight through SetObjectProperty (int32 is the C size);
// only string→enum needs UtilSetObjectArg, so no nick table is needed here.
func applyCompositorBackground(element gst.Element, background BackgroundType) {
	if background > BackgroundTransparent {
		return
	}
	element.SetObjectProperty("background", int32(background))
}

type CompositorConfig struct {
	ID         string
	Width      int
	Height     int
	Framerate  string
	Background BackgroundType
	// Pads is the initial layout, one entry per branch in request order.
	Pads []CompositorPadLayout
}

func CreateCompositorNode(_ core.NodeRuntime, config CompositorConfig) (*CompositorNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("%w: compositor needs a canvas size", core.ErrInvalidConfig)
	}
	if config.Framerate == "" {
		config.Framerate = defaultCompositorFramerate
	}

	bin := gst.NewBin(id).(gst.Bin)
	element := gst.ElementFactoryMake("compositor", id+"_Compositor")
	capsFilter := gst.ElementFactoryMake("capsfilter", id+"_CanvasCaps")
	if element == nil || capsFilter == nil {
		return nil, fmt.Errorf("%w: create compositor elements", core.ErrGSTNoElements)
	}

	capsFilter.SetObjectProperty("caps", gst.CapsFromString(compositorCanvasCaps(config)))

	bin.AddMany(element, capsFilter)

	// Order matters: add, link, then ghost. See pads.go.
	if !element.Link(capsFilter) {
		return nil, fmt.Errorf("%w: link compositor to canvas capsfilter", core.ErrGSTLinkErr)
	}

	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), capsFilter.GetStaticPad("src"))
	if srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(srcPad) {
		return nil, fmt.Errorf("%w: add src pad to %q", core.ErrGSTPadNotFound, id)
	}

	node := &CompositorNode{
		srcPad:     srcPad,
		element:    element,
		capsFilter: capsFilter,
		sinkLeases: []compositorSinkLease{},
	}
	node.BaseNode = core.NewBaseNode(id, NodeTypeCompositor, bin)

	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Width":      config.Width,
		"Height":     config.Height,
		"Framerate":  config.Framerate,
		"Background": config.Background,
	}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("Width", "Height", "Framerate")

	applyCompositorBackground(element, config.Background)

	// Pads is a placement template, not a request to create branches: the graph
	// owns how many sink pads a node declares, and it asks for them itself.
	node.layouts = append(node.layouts, config.Pads...)

	element.SyncStateWithParent()
	capsFilter.SyncStateWithParent()
	return node, nil
}

// compositorCanvasCaps renders the canvas constraint as a caps string. The
// compositor has no width/height property of its own: its output size is settled
// by negotiation, so forcing a canvas means constraining the pad downstream of
// it.
func compositorCanvasCaps(config CompositorConfig) string {
	return fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=%s",
		config.Width, config.Height, config.Framerate)
}

func (n *CompositorNode) RequestSinkPad() (gst.Pad, string, error) {
	branchIndex := len(n.sinkLeases)
	padID := fmt.Sprintf("%s_sink_%d", n.ID(), branchIndex)

	requestPad := n.element.RequestPadSimple("sink_%u")
	if requestPad == nil {
		return nil, "", core.ErrGSTPadNotFound
	}

	convert := gst.ElementFactoryMake("videoconvert", fmt.Sprintf("%s_Sink_%d", n.ID(), branchIndex))
	if convert == nil {
		n.element.ReleaseRequestPad(requestPad)
		return nil, "", core.ErrGSTNoElements
	}
	bin := n.Bin()
	bin.Add(convert)

	ghostSink, ok := gst.NewGhostPad(padID, convert.GetStaticPad("sink")).(gst.GhostPad)
	if !ok || ghostSink == nil {
		bin.Remove(convert)
		n.element.ReleaseRequestPad(requestPad)
		return nil, "", core.ErrGSTPadNotFound
	}

	if convert.GetStaticPad("src").Link(requestPad) != gst.PadLinkOK {
		bin.Remove(convert)
		n.element.ReleaseRequestPad(requestPad)
		return nil, "", fmt.Errorf("%w: link videoconvert to compositor", core.ErrGSTLinkErr)
	}

	if !bin.AddPad(ghostSink) {
		ghostSink.SetTarget(nil)
		bin.Remove(convert)
		n.element.ReleaseRequestPad(requestPad)
		return nil, "", core.ErrGSTPadNotFound
	}

	convert.SyncStateWithParent()
	n.sinkLeases = append(n.sinkLeases, compositorSinkLease{
		id:         padID,
		ghostSink:  ghostSink,
		convert:    convert,
		requestPad: requestPad,
	})
	if branchIndex < len(n.layouts) {
		setCompositorPadLayout(requestPad, n.layouts[branchIndex])
	}
	return ghostSink, padID, nil
}

func (n *CompositorNode) RemoveSinkPad(id string) error {
	for index, lease := range n.sinkLeases {
		if lease.id != id {
			continue
		}
		// The ghost's target is the branch convert, so the pair is a target
		// relationship: clear it with SetTarget, not Unlink.
		lease.ghostSink.SetTarget(nil)
		lease.convert.GetStaticPad("src").Unlink(lease.requestPad)
		bin := n.Bin()
		bin.RemovePad(lease.ghostSink)
		bin.Remove(lease.convert)
		n.element.ReleaseRequestPad(lease.requestPad)
		n.sinkLeases = slices.Delete(n.sinkLeases, index, index+1)
		return nil
	}
	return core.ErrUnLinkFailed
}

// setPadLayout writes one branch's placement onto its compositor pad. Property
// values must be the C-sized Go types: gobject refuses a plain int.
func setCompositorPadLayout(pad gst.Pad, layout CompositorPadLayout) {
	pad.SetObjectProperty("xpos", int32(layout.X))
	pad.SetObjectProperty("ypos", int32(layout.Y))
	pad.SetObjectProperty("zorder", uint32(layout.ZOrder))
	if layout.Width > 0 {
		pad.SetObjectProperty("width", int32(layout.Width))
	}
	if layout.Height > 0 {
		pad.SetObjectProperty("height", int32(layout.Height))
	}
	if layout.Alpha > 0 {
		pad.SetObjectProperty("alpha", layout.Alpha)
	}
}

func (n *CompositorNode) applyLayout(index int, layout CompositorPadLayout) error {
	if index < 0 || index >= len(n.sinkLeases) {
		return fmt.Errorf("%w: compositor has no branch %d", core.ErrPadCantBeUsed, index)
	}
	setCompositorPadLayout(n.sinkLeases[index].requestPad, layout)
	return nil
}

// decodeCompositorLayouts converts the raw property value the graph carries into
// typed layouts. It goes through JSON because that is the shape properties
// arrive in, so a caller writes the same object it would write in a graph.
func decodeCompositorLayouts(raw any) ([]CompositorPadLayout, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: encode compositor layouts: %v", core.ErrInvalidConfig, err)
	}
	var layouts []CompositorPadLayout
	if err := json.Unmarshal(data, &layouts); err != nil {
		return nil, fmt.Errorf("%w: decode compositor layouts: %v", core.ErrInvalidConfig, err)
	}
	return layouts, nil
}

func (n *CompositorNode) UpdateProperties(properties map[string]interface{}) error {
	if len(properties) == 0 {
		return nil
	}

	if raw, hasPads := properties["Pads"]; hasPads {
		layouts, err := decodeCompositorLayouts(raw)
		if err != nil {
			return err
		}
		if len(layouts) > len(n.sinkLeases) {
			return fmt.Errorf("%w: compositor has %d branches, layout names %d",
				core.ErrPadCantBeUsed, len(n.sinkLeases), len(layouts))
		}
		n.layouts = layouts
		for index, layout := range layouts {
			if err := n.applyLayout(index, layout); err != nil {
				return err
			}
		}
	}

	if raw, ok := properties["Background"]; ok {
		applyCompositorBackground(n.element, BackgroundType(propertyNumber(raw, 0)))
	}

	return n.BaseNode.UpdateProperties(properties)
}

func (n *CompositorNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *CompositorNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *CompositorNode) GetDstIDs() []string {
	ids := make([]string, 0, len(n.sinkLeases))
	for _, lease := range n.sinkLeases {
		ids = append(ids, lease.id)
	}
	return ids
}

func (n *CompositorNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()
	bin.SetState(gst.State(gst.StateNull))

	for _, lease := range n.sinkLeases {
		lease.ghostSink.SetTarget(nil)
		bin.RemovePad(lease.ghostSink)
		bin.Remove(lease.convert)
		n.element.ReleaseRequestPad(lease.requestPad)
	}

	n.element = nil
	n.capsFilter = nil
	n.srcPad = nil
	n.sinkLeases = nil
	n.layouts = nil
	n.Clear()
	return nil
}
