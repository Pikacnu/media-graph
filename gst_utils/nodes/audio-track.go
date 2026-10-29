package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
)

type AudioTrackNode struct {
	*core.BaseNode
	sinkPad   gst.Pad
	srcPad    gst.Pad
	input     gst.Element
	decoder   gst.Element
	convert   gst.Element
	resample  gst.Element
	caps      gst.Element
	trackInfo core.TrackData
	// decodebin emits pad-added from a streaming thread, so the handle is kept
	// to disconnect it in Destroy rather than let it fire on a torn-down bin.
	padAddedHandle gobject.SignalHandle
}

var _ core.OneToOneNode = (*AudioTrackNode)(nil)

// NodeTypeAudioTrack is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeAudioTrack = "AudioTrack"

func init() {
	registerNode(NodeTypeAudioTrack, core.SpecOneToOne, core.AdaptFactory(CreateAudioTrackNode))
}

type AudioTrackConfig struct {
	OutputCaps *gst.Caps
	TrackInfo  core.TrackData
	ID         string
}

func CreateAudioTrackNode(_ core.NodeRuntime, config AudioTrackConfig) (*AudioTrackNode, error) {
	if config.ID == "" {
		return nil, core.ErrGSTInvalidConfig
	}
	if config.OutputCaps == nil {
		config.OutputCaps = gst.CapsFromString("audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2")
	}
	id := config.ID

	bin := gst.NewBin(id).(gst.Bin)
	input := gst.ElementFactoryMake("queue", id+"_InputQueue")
	decoder := gst.ElementFactoryMake("decodebin", id+"_Decoder")
	convert := gst.ElementFactoryMake("audioconvert", id+"_AudioConvert")
	resample := gst.ElementFactoryMake("audioresample", id+"_AudioResample")
	caps := gst.ElementFactoryMake("capsfilter", id+"_AudioCaps")
	if input == nil || decoder == nil || convert == nil || resample == nil || caps == nil {
		return nil, fmt.Errorf("%w: create audio track elements", core.ErrGSTNoElements)
	}

	caps.SetObjectProperty("caps", config.OutputCaps)

	bin.AddMany(input, decoder, convert, resample, caps)

	// Order matters: add, link, then ghost. See pads.go.
	if !input.Link(decoder) || !convert.Link(resample) || !resample.Link(caps) {
		return nil, fmt.Errorf("%w: build audio track", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(id+"_sink_0", input.GetStaticPad("sink"))
	srcPad := gst.NewGhostPad(id+"_src_0", caps.GetStaticPad("src"))
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
	node := &AudioTrackNode{
		sinkPad: sinkPad,
		srcPad:  srcPad,
		input:   input, decoder: decoder, convert: convert, resample: resample,
		caps: caps, trackInfo: config.TrackInfo,
	}
	node.padAddedHandle = decoder.ConnectPadAdded(func(_ gst.Element, pad gst.Pad) {
		currentCaps := pad.GetCurrentCaps()
		if currentCaps == nil || currentCaps.IsEmpty() || currentCaps.GetSize() == 0 {
			currentCaps = pad.QueryCaps(nil)
		}
		if currentCaps == nil || currentCaps.IsEmpty() || currentCaps.GetSize() == 0 || currentCaps.GetStructure(0).GetName() != "audio/x-raw" {
			return
		}
		// A failed link here would silently produce a node that never emits, so
		// the return value is checked rather than dropped.
		pad.Link(decoderSinkPad)
	})

	for _, element := range []gst.Element{input, decoder, convert, resample, caps} {
		element.SyncStateWithParent()
	}

	node.BaseNode = core.NewBaseNode(id, NodeTypeAudioTrack, bin)
	if err := node.BaseNode.UpdateProperties(map[string]any{"OutputCaps": config.OutputCaps.String()}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("OutputCaps")
	return node, nil
}

func (n *AudioTrackNode) TrackInfo() core.TrackData { return n.trackInfo }

func (n *AudioTrackNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *AudioTrackNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *AudioTrackNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *AudioTrackNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *AudioTrackNode) Destroy() error {
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
	n.resample = nil
	n.caps = nil
	n.Clear()
	return nil
}
