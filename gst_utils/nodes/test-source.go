package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestSourceNode generates a synthetic video pattern. It is what fills the frame
// when a graph has nothing real to show yet, so a preview or a placeholder
// branch does not have to be a special case in the frontend.
type TestSourceNode struct {
	*core.BaseNode
	srcPad     gst.Pad
	source     gst.Element
	convert    gst.Element
	capsFilter gst.Element
}

var _ core.ZeroToOneNode = (*TestSourceNode)(nil)

// NodeTypeTestSource is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeTestSource = "TestSource"

func init() {
	registerNode(NodeTypeTestSource, core.SpecZeroToOne, core.AdaptFactory(CreateTestSourceNode))
}

const (
	defaultTestSourceWidth     = 1280
	defaultTestSourceHeight    = 720
	defaultTestSourceFramerate = "30/1"
	// "smpte" is videotestsrc's own default pattern name.
	defaultTestSourcePattern = "smpte"
)

type TestSourceConfig struct {
	ID string
	// Pattern is a videotestsrc pattern nickname, e.g. "smpte", "ball", "snow".
	Pattern   string
	Width     int
	Height    int
	Framerate string
	// IsLive makes the source behave as a live capture, which keeps the graph
	// from waiting for it to finish.
	IsLive bool
}

func CreateTestSourceNode(_ core.NodeRuntime, config TestSourceConfig) (*TestSourceNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if config.Pattern == "" {
		config.Pattern = defaultTestSourcePattern
	}
	if config.Width <= 0 {
		config.Width = defaultTestSourceWidth
	}
	if config.Height <= 0 {
		config.Height = defaultTestSourceHeight
	}
	if config.Framerate == "" {
		config.Framerate = defaultTestSourceFramerate
	}

	bin := gst.NewBin(id).(gst.Bin)
	source := gst.ElementFactoryMake("videotestsrc", id+"_TestSrc")
	convert := gst.ElementFactoryMake("videoconvert", id+"_VideoConvert")
	capsFilter := gst.ElementFactoryMake("capsfilter", id+"_VideoCaps")
	if source == nil || convert == nil || capsFilter == nil {
		return nil, fmt.Errorf("%w: create test source elements", core.ErrGSTNoElements)
	}

	capsFilter.SetObjectProperty("caps", gst.CapsFromString(testSourceCaps(config)))
	// `pattern` is an enum, which has no string-to-enum GValue transform, so it
	// needs GStreamer's own string setter.
	gst.UtilSetObjectArg(source, "pattern", config.Pattern)
	source.SetObjectProperty("is-live", config.IsLive)

	bin.AddMany(source, convert, capsFilter)

	// Order matters: add, link, then ghost. See pads.go.
	if !source.Link(convert) || !convert.Link(capsFilter) {
		return nil, fmt.Errorf("%w: build test source", core.ErrGSTLinkErr)
	}

	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), capsFilter.GetStaticPad("src"))
	if srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(srcPad) {
		return nil, fmt.Errorf("%w: add src pad to %q", core.ErrGSTPadNotFound, id)
	}

	node := &TestSourceNode{
		srcPad:     srcPad,
		source:     source,
		convert:    convert,
		capsFilter: capsFilter,
	}
	node.BaseNode = core.NewBaseNode(id, NodeTypeTestSource, bin)

	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Pattern":   config.Pattern,
		"Width":     config.Width,
		"Height":    config.Height,
		"Framerate": config.Framerate,
		"IsLive":    config.IsLive,
	}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("Width", "Height", "Framerate")

	source.SyncStateWithParent()
	convert.SyncStateWithParent()
	capsFilter.SyncStateWithParent()
	return node, nil
}

func testSourceCaps(config TestSourceConfig) string {
	return fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=%s",
		config.Width, config.Height, config.Framerate)
}

func (n *TestSourceNode) UpdateProperties(properties map[string]interface{}) error {
	if len(properties) == 0 {
		return nil
	}
	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}
	if pattern, ok := n.BaseNode.Properties()["Pattern"].(string); ok && pattern != "" {
		gst.UtilSetObjectArg(n.source, "pattern", pattern)
	}
	return nil
}

func (n *TestSourceNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *TestSourceNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *TestSourceNode) GetDstIDs() []string { return nil }

func (n *TestSourceNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	n.Bin().SetState(gst.State(gst.StateNull))

	n.source = nil
	n.convert = nil
	n.capsFilter = nil
	n.srcPad = nil
	n.Clear()
	return nil
}
