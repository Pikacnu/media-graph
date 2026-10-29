package nodes

import (
	"fmt"
	"strings"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// defaultAudioEncodeCaps is the raw format the frozen srt/ pipeline normalises
// to before encoding, and the format every candidate encoder accepts.
const defaultAudioEncodeCaps = "audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2"

// AudioEncodeNode normalises raw audio and encodes it.
//
// Chain: audioconvert -> audioresample -> capsfilter -> encoder -> parser
type AudioEncodeNode struct {
	*core.BaseNode
	sinkPad    gst.Pad
	srcPad     gst.Pad
	convert    gst.Element
	resample   gst.Element
	capsFilter gst.Element
	encoder    gst.Element
	parser     gst.Element
}

var _ core.OneToOneNode = (*AudioEncodeNode)(nil)

// NodeTypeAudioEncode is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeAudioEncode = "AudioEncode"

func init() {
	registerNode(NodeTypeAudioEncode, core.SpecOneToOne, core.AdaptFactory(CreateAudioEncodeNode))
}

type AudioEncodeConfig struct {
	ID string
	// Caps is the raw format fed to the encoder; parsed only when OutputCaps is nil.
	Caps string
	// OutputCaps wins over Caps when set.
	OutputCaps *gst.Caps
	// Codec selects the encoder/parser pair; defaults to AAC.
	Codec AudioCodec
}

func CreateAudioEncodeNode(_ core.NodeRuntime, config AudioEncodeConfig) (*AudioEncodeNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrGSTInvalidConfig
	}
	if config.Codec == "" {
		config.Codec = defaultAudioCodec
	}
	chain, resolved := resolveAudioCodecChain(config.Codec)
	if !resolved {
		candidates, known := audioCodecCandidatesFor(config.Codec)
		if !known {
			return nil, fmt.Errorf("%w: unknown audio codec %q, known: %s",
				core.ErrGSTInvalidConfig, config.Codec, strings.Join(audioCodecNames(), ", "))
		}
		return nil, fmt.Errorf("%w: no %s encoder is installed; tried %s",
			core.ErrGSTNoElements, config.Codec, formatCodecCandidates(candidates))
	}
	if config.OutputCaps == nil {
		caps := config.Caps
		if caps == "" {
			caps = defaultAudioEncodeCaps
		}
		config.OutputCaps = gst.CapsFromString(caps)
	}
	if config.OutputCaps == nil {
		return nil, fmt.Errorf("%w: audio encode needs target caps", core.ErrGSTInvalidConfig)
	}

	bin := gst.NewBin(id).(gst.Bin)
	convert := gst.ElementFactoryMake("audioconvert", id+"_AudioConvert")
	resample := gst.ElementFactoryMake("audioresample", id+"_AudioResample")
	capsFilter := gst.ElementFactoryMake("capsfilter", id+"_AudioCaps")
	encoder := gst.ElementFactoryMake(chain.Encoder, id+"_Encoder")
	parser := gst.ElementFactoryMake(chain.Parser, id+"_Parser")
	if convert == nil || resample == nil || capsFilter == nil || encoder == nil || parser == nil {
		return nil, fmt.Errorf("%w: create %s chain (%s + %s)",
			core.ErrGSTNoElements, config.Codec, chain.Encoder, chain.Parser)
	}

	capsFilter.SetObjectProperty("caps", config.OutputCaps)
	bin.AddMany(convert, resample, capsFilter, encoder, parser)

	if !convert.Link(resample) || !resample.Link(capsFilter) || !capsFilter.Link(encoder) || !encoder.Link(parser) {
		return nil, fmt.Errorf("%w: build %s chain", core.ErrGSTLinkErr, config.Codec)
	}

	sinkPad := gst.NewGhostPad(id+"_sink_0", convert.GetStaticPad("sink"))
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

	for _, element := range []gst.Element{convert, resample, capsFilter, encoder, parser} {
		element.SyncStateWithParent()
	}

	base := core.NewBaseNode(id, NodeTypeAudioEncode, bin)
	if err := base.UpdateProperties(map[string]any{
		"Caps":  config.Caps,
		"Codec": string(config.Codec),
	}); err != nil {
		return nil, err
	}
	base.ProtectProperties("Caps", "Codec")

	return &AudioEncodeNode{
		BaseNode:   base,
		sinkPad:    sinkPad,
		srcPad:     srcPad,
		convert:    convert,
		resample:   resample,
		capsFilter: capsFilter,
		encoder:    encoder,
		parser:     parser,
	}, nil
}

func (n *AudioEncodeNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *AudioEncodeNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *AudioEncodeNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *AudioEncodeNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *AudioEncodeNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	n.Bin().SetState(gst.State(gst.StateNull))
	n.sinkPad = nil
	n.srcPad = nil
	n.convert = nil
	n.resample = nil
	n.capsFilter = nil
	n.encoder = nil
	n.parser = nil
	n.Clear()
	return nil
}
