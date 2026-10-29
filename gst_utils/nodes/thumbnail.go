package nodes

import (
	"fmt"
	"sync"

	"live_tools/gst_utils/chan_registry"
	"live_tools/gst_utils/core"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// ThumbnailNode produces still frames from a video branch and publishes them as
// JPEG bytes.
//
// It is a lighter sibling of OutputNode: same "encode and hand bytes to a
// subscriber" shape, but scaled down and JPEG-encoded so a UI can render a node
// card without decoding a full frame.
type ThumbnailNode struct {
	*core.BaseNode
	entry      *chan_registry.OutputEntry[[]byte]
	runtime    core.NodeRuntime
	sinkPad    gst.Pad
	convert    gst.Element
	scale      gst.Element
	capsFilter gst.Element
	encoder    gst.Element
	appSink    gstapp.AppSink
	cancel     chan struct{}
	cancelOnce sync.Once
	// The new-sample handler runs on a streaming thread, so the handle is kept
	// to disconnect it in Destroy.
	sampleHandle gobject.SignalHandle
}

var _ core.OneToZeroNode = (*ThumbnailNode)(nil)

// NodeTypeThumbnail is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeThumbnail = "Thumbnail"

func init() {
	registerNode(NodeTypeThumbnail, core.SpecOneToZero, core.AdaptFactory(CreateThumbnailNode))
}

const (
	defaultThumbnailWidth       = 320
	defaultThumbnailHeight      = 180
	defaultThumbnailJPEGQuality = 80
)

type ThumbnailConfig struct {
	ID         string
	Width      int
	Height     int
	Quality    int
	BufferSize int
}

func CreateThumbnailNode(runtime core.NodeRuntime, config ThumbnailConfig) (*ThumbnailNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	if config.Width <= 0 {
		config.Width = defaultThumbnailWidth
	}
	if config.Height <= 0 {
		config.Height = defaultThumbnailHeight
	}
	if config.Quality <= 0 || config.Quality > 100 {
		config.Quality = defaultThumbnailJPEGQuality
	}

	entry, err := chan_registry.NewByteOutputEntry(id, config.BufferSize, core.DropOldest)
	if err != nil {
		return nil, err
	}
	if err := runtime.RegisterEntry(entry); err != nil {
		return nil, fmt.Errorf("%w: register entry %q: %v", core.ErrInvalidConfig, id, err)
	}

	bin := gst.NewBin(id).(gst.Bin)
	convert := gst.ElementFactoryMake("videoconvert", id+"_VideoConvert")
	scale := gst.ElementFactoryMake("videoscale", id+"_VideoScale")
	capsFilter := gst.ElementFactoryMake("capsfilter", id+"_ThumbCaps")
	encoder := gst.ElementFactoryMake("jpegenc", id+"_JpegEnc")
	appSink := gst.ElementFactoryMake("appsink", id+"_AppSink").(gstapp.AppSink)
	if convert == nil || scale == nil || capsFilter == nil || encoder == nil || appSink == nil {
		return nil, fmt.Errorf("%w: create thumbnail elements", core.ErrGSTNoElements)
	}

	capsFilter.SetObjectProperty("caps", gst.CapsFromString(
		fmt.Sprintf("video/x-raw,width=%d,height=%d", config.Width, config.Height)))
	encoder.SetObjectProperty("quality", uint32(config.Quality))
	appSink.SetObjectProperties(map[string]any{
		// Only the newest frame matters, so the queue is one deep and old frames
		// are dropped rather than backing up the branch.
		"max-buffers": uint32(1),
		"drop":        true,
		"sync":        false,
	})
	appSink.SetEmitSignals(true)

	bin.AddMany(convert, scale, capsFilter, encoder, appSink)

	// Order matters: add, link, then ghost. See pads.go.
	if !convert.Link(scale) || !scale.Link(capsFilter) || !capsFilter.Link(encoder) || !encoder.Link(appSink) {
		runtime.RemoveEntryById(id)
		return nil, fmt.Errorf("%w: build thumbnail chain", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), convert.GetStaticPad("sink"))
	if sinkPad == nil {
		runtime.RemoveEntryById(id)
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(sinkPad) {
		runtime.RemoveEntryById(id)
		return nil, fmt.Errorf("%w: add sink pad to %q", core.ErrGSTPadNotFound, id)
	}

	node := &ThumbnailNode{
		entry:      entry,
		runtime:    runtime,
		sinkPad:    sinkPad,
		convert:    convert,
		scale:      scale,
		capsFilter: capsFilter,
		encoder:    encoder,
		appSink:    appSink,
		cancel:     make(chan struct{}),
	}
	node.sampleHandle = appSink.Connect("new-sample", func(sink gstapp.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowOK
		}
		buffer := sample.GetBuffer()
		if buffer == nil {
			return gst.FlowOK
		}
		mappedBuffer, ok := buffer.Map(gst.MapFlags(gst.MapRead))
		if !ok {
			return gst.FlowOK
		}
		data := mappedBuffer.Data()
		copiedData := make([]byte, len(data))
		copy(copiedData, data)
		mappedBuffer.Unmap()

		select {
		case <-node.cancel:
			return gst.FlowEOS
		default:
		}
		node.entry.Publish(copiedData)
		return gst.FlowOK
	})

	node.BaseNode = core.NewBaseNode(id, NodeTypeThumbnail, bin)
	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Width":      config.Width,
		"Height":     config.Height,
		"Quality":    config.Quality,
		"BufferSize": config.BufferSize,
	}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("Width", "Height", "BufferSize")

	for _, element := range []gst.Element{convert, scale, capsFilter, encoder, appSink} {
		element.SyncStateWithParent()
	}
	return node, nil
}

// UpdateProperties changes the JPEG quality live. Width and height are
// constructor-fixed because the capsfilter is part of the negotiated chain.
func (n *ThumbnailNode) UpdateProperties(properties map[string]interface{}) error {
	if len(properties) == 0 {
		return nil
	}
	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}
	quality := int(propertyNumber(n.BaseNode.Properties()["Quality"], defaultThumbnailJPEGQuality))
	if quality <= 0 || quality > 100 {
		return fmt.Errorf("%w: thumbnail quality must be 1..100", core.ErrInvalidConfig)
	}
	n.encoder.SetObjectProperty("quality", uint32(quality))
	return nil
}

func (n *ThumbnailNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *ThumbnailNode) GetSrcIDs() []string { return nil }

func (n *ThumbnailNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *ThumbnailNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	if n.appSink != nil && n.sampleHandle != 0 {
		n.appSink.HandlerDisconnect(n.sampleHandle)
		n.sampleHandle = 0
	}
	n.Bin().SetState(gst.State(gst.StateNull))

	n.cancelOnce.Do(func() {
		close(n.cancel)
	})

	if n.entry != nil {
		n.entry.Stop()
		if err := n.runtime.RemoveEntryById(n.ID()); err != nil {
			return fmt.Errorf("%w: failed to remove entry %q: %v", core.ErrInvalidConfig, n.ID(), err)
		}
		n.entry = nil
	}

	n.convert = nil
	n.scale = nil
	n.capsFilter = nil
	n.encoder = nil
	n.appSink = nil
	n.sinkPad = nil
	n.Clear()
	return nil
}
