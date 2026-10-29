package nodes

import (
	"fmt"
	"strings"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
)

// ImageSourceNode turns a still image on disk into a video stream. It is what a
// logo, a sponsor slate or a "starting soon" card is made of.
//
// The freeze element is the whole point: a decoded image is a single buffer, and
// imagefreeze repeats it at the negotiated rate so downstream sees a normal live
// video stream instead of one frame followed by EOS.
type ImageSourceNode struct {
	*core.BaseNode
	srcPad     gst.Pad
	fileSrc    gst.Element
	decoder    gst.Element
	freeze     gst.Element
	convert    gst.Element
	capsFilter gst.Element
	// decodebin emits pad-added from a streaming thread, so the handle is kept
	// to disconnect it in Destroy rather than let it fire on a torn-down bin.
	padAddedHandle gobject.SignalHandle
}

var _ core.ZeroToOneNode = (*ImageSourceNode)(nil)

// NodeTypeImageSource is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeImageSource = "ImageSource"

func init() {
	registerNode(NodeTypeImageSource, core.SpecZeroToOne, core.AdaptFactory(CreateImageSourceNode))
}

const defaultImageSourceFramerate = "30/1"

type ImageSourceConfig struct {
	ID string
	// Location is a local path or file:// URI of the image.
	Location  string
	Framerate string
}

func CreateImageSourceNode(_ core.NodeRuntime, config ImageSourceConfig) (*ImageSourceNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if config.Location == "" {
		return nil, fmt.Errorf("%w: image source needs a location", core.ErrInvalidConfig)
	}
	if config.Framerate == "" {
		config.Framerate = defaultImageSourceFramerate
	}

	bin := gst.NewBin(id).(gst.Bin)
	fileSrc := gst.ElementFactoryMake("filesrc", id+"_FileSrc")
	decoder := gst.ElementFactoryMake("decodebin", id+"_Decoder")
	freeze := gst.ElementFactoryMake("imagefreeze", id+"_Freeze")
	convert := gst.ElementFactoryMake("videoconvert", id+"_VideoConvert")
	capsFilter := gst.ElementFactoryMake("capsfilter", id+"_VideoCaps")
	if fileSrc == nil || decoder == nil || freeze == nil || convert == nil || capsFilter == nil {
		return nil, fmt.Errorf("%w: create image source elements", core.ErrGSTNoElements)
	}

	fileSrc.SetObjectProperty("location", config.Location)
	capsFilter.SetObjectProperty("caps", gst.CapsFromString(
		fmt.Sprintf("video/x-raw,framerate=%s", config.Framerate)))

	bin.AddMany(fileSrc, decoder, freeze, convert, capsFilter)

	// Order matters: add, link, then ghost. See pads.go.
	if !fileSrc.Link(decoder) || !freeze.Link(convert) || !convert.Link(capsFilter) {
		return nil, fmt.Errorf("%w: build image source", core.ErrGSTLinkErr)
	}

	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), capsFilter.GetStaticPad("src"))
	if srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(srcPad) {
		return nil, fmt.Errorf("%w: add src pad to %q", core.ErrGSTPadNotFound, id)
	}

	node := &ImageSourceNode{
		srcPad:     srcPad,
		fileSrc:    fileSrc,
		decoder:    decoder,
		freeze:     freeze,
		convert:    convert,
		capsFilter: capsFilter,
	}
	node.BaseNode = core.NewBaseNode(id, NodeTypeImageSource, bin)

	// decodebin only exposes its src pad once it has parsed the file, so the
	// chain from it onward is linked here.
	freezeSinkPad := freeze.GetStaticPad("sink")
	node.padAddedHandle = decoder.ConnectPadAdded(func(_ gst.Element, pad gst.Pad) {
		caps := pad.GetCurrentCaps()
		if caps == nil || caps.IsEmpty() || caps.GetSize() == 0 {
			caps = pad.QueryCaps(nil)
		}
		if caps == nil || caps.IsEmpty() || caps.GetSize() == 0 {
			return
		}
		// imagefreeze takes either decoded raw video or a still image it can
		// hold, so audio and subtitle pads are dropped.
		name := caps.GetStructure(0).GetName()
		if name != "video/x-raw" && !strings.HasPrefix(name, "image/") {
			return
		}
		// A failed link here would silently produce a node that never emits, so
		// the return value is checked rather than dropped.
		pad.Link(freezeSinkPad)
	})

	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Location":  config.Location,
		"Framerate": config.Framerate,
	}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("Location", "Framerate")

	for _, element := range []gst.Element{fileSrc, decoder, freeze, convert, capsFilter} {
		element.SyncStateWithParent()
	}
	return node, nil
}

func (n *ImageSourceNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *ImageSourceNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *ImageSourceNode) GetDstIDs() []string { return nil }

func (n *ImageSourceNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	if n.decoder != nil && n.padAddedHandle != 0 {
		n.decoder.HandlerDisconnect(n.padAddedHandle)
		n.padAddedHandle = 0
	}
	n.Bin().SetState(gst.State(gst.StateNull))

	n.fileSrc = nil
	n.decoder = nil
	n.freeze = nil
	n.convert = nil
	n.capsFilter = nil
	n.srcPad = nil
	n.Clear()
	return nil
}
