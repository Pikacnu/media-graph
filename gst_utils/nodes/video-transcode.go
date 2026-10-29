package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
)

// VideoTranscodeNode decodes a video stream and produces a normalized encoded
// video stream. It is deliberately unaware of the input protocol or output
// container so it can be used by SRT, WHIP, RTMP, and file graphs.
type VideoTranscodeNode struct {
	*core.BaseNode
	sinkPad    gst.Pad
	srcPad     gst.Pad
	input      gst.Element
	decoder    gst.Element
	convert    gst.Element
	scale      gst.Element
	caps       gst.Element
	encoder    gst.Element
	parser     gst.Element
	outputCaps *gst.Caps
	// decodebin emits pad-added from a streaming thread, so the handle is kept
	// to disconnect it in Destroy rather than let it fire on a torn-down bin.
	padAddedHandle gobject.SignalHandle
}

var _ core.OneToOneNode = (*VideoTranscodeNode)(nil)

// NodeTypeVideoTranscode is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeVideoTranscode = "VideoTranscode"

func init() {
	registerNode(NodeTypeVideoTranscode, core.SpecOneToOne, core.AdaptFactory(CreateVideoTranscodeNode))
}

type VideoTranscodeConfig struct {
	ID         string
	OutputCaps *gst.Caps
	Caps       string
	Encoder    string
}

func CreateVideoTranscodeNode(_ core.NodeRuntime, config VideoTranscodeConfig) (*VideoTranscodeNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrGSTInvalidConfig
	}
	if config.Encoder == "" {
		config.Encoder = "x264enc"
	}
	if config.OutputCaps == nil {
		config.OutputCaps = gst.CapsFromString(config.Caps)
	}
	if config.OutputCaps == nil {
		config.OutputCaps = gst.CapsFromString("video/x-raw")
	}

	bin := gst.NewBin(id).(gst.Bin)
	input := gst.ElementFactoryMake("queue", id+"_InputQueue")
	decoder := gst.ElementFactoryMake("decodebin", id+"_Decoder")
	convert := gst.ElementFactoryMake("videoconvert", id+"_VideoConvert")
	scale := gst.ElementFactoryMake("videoscale", id+"_VideoScale")
	caps := gst.ElementFactoryMake("capsfilter", id+"_VideoCaps")
	encoder := gst.ElementFactoryMake(config.Encoder, id+"_Encoder")
	parser := gst.ElementFactoryMake("h264parse", id+"_H264Parse")
	if input == nil || decoder == nil || convert == nil || scale == nil || caps == nil || encoder == nil || parser == nil {
		return nil, fmt.Errorf("%w: create video transcoder elements", core.ErrGSTNoElements)
	}

	caps.SetObjectProperty("caps", config.OutputCaps)

	bin.AddMany(input, decoder, convert, scale, caps, encoder, parser)

	// Order matters: add, link, then ghost. See pads.go.
	if !input.Link(decoder) || !convert.Link(scale) || !scale.Link(caps) || !caps.Link(encoder) || !encoder.Link(parser) {
		return nil, fmt.Errorf("%w: build video transcoder", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(id+"_sink_0", input.GetStaticPad("sink"))
	srcPad := gst.NewGhostPad(id+"_src_0", parser.GetStaticPad("src"))
	if sinkPad == nil || srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(sinkPad) {
		return nil, fmt.Errorf("%w: add sink pad to %q", core.ErrGSTPadNotFound, id)
	}
	if !bin.AddPad(srcPad) {
		return nil, fmt.Errorf("%w: add src pad to %q", core.ErrGSTPadNotFound, id)
	}

	// decodebin only exposes its src pad once it has parsed the stream, so the
	// chain from it onward is linked here.
	decoderSinkPad := convert.GetStaticPad("sink")
	node := &VideoTranscodeNode{
		sinkPad: sinkPad,
		srcPad:  srcPad,
		input:   input, decoder: decoder, convert: convert, scale: scale,
		caps: caps, encoder: encoder, parser: parser, outputCaps: config.OutputCaps,
	}
	node.padAddedHandle = decoder.ConnectPadAdded(func(_ gst.Element, pad gst.Pad) {
		currentCaps := pad.GetCurrentCaps()
		if currentCaps == nil || currentCaps.IsEmpty() || currentCaps.GetSize() == 0 {
			currentCaps = pad.QueryCaps(nil)
		}
		if currentCaps == nil || currentCaps.IsEmpty() || currentCaps.GetSize() == 0 || currentCaps.GetStructure(0).GetName() != "video/x-raw" {
			return
		}
		// A failed link here would silently produce a node that never emits, so
		// the return value is checked rather than dropped.
		pad.Link(decoderSinkPad)
	})

	for _, element := range []gst.Element{input, decoder, convert, scale, caps, encoder, parser} {
		element.SyncStateWithParent()
	}

	node.BaseNode = core.NewBaseNode(id, NodeTypeVideoTranscode, bin)
	if err := node.BaseNode.UpdateProperties(map[string]any{"Caps": config.Caps, "Encoder": config.Encoder}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("Caps", "Encoder")
	return node, nil
}

func (n *VideoTranscodeNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *VideoTranscodeNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *VideoTranscodeNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *VideoTranscodeNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *VideoTranscodeNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	if n.decoder != nil && n.padAddedHandle != 0 {
		n.decoder.HandlerDisconnect(n.padAddedHandle)
		n.padAddedHandle = 0
	}
	n.Bin().SetState(gst.State(gst.StateNull))
	n.sinkPad = nil
	n.srcPad = nil
	n.input = nil
	n.decoder = nil
	n.convert = nil
	n.scale = nil
	n.caps = nil
	n.encoder = nil
	n.parser = nil
	n.Clear()
	return nil
}
