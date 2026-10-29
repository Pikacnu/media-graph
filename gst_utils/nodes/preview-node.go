package nodes

import (
	"fmt"
	"log"
	"time"

	"live_tools/gst_utils/chan_registry"
	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

type PreviewNode struct {
	*core.BaseNode
	entry      *chan_registry.OutputEntry[*PreviewSample]
	runtime    core.NodeRuntime
	sinkPad    gst.Pad
	srcPad     gst.Pad
	teeElement gst.Element
	// Two tee src pads: the downstream branch and the preview branch. They
	// cannot be the same pad, because NewGhostPad consumes its target and the
	// appsink link would then fail as already-linked.
	downstreamPad gst.Pad
	previewPad    gst.Pad
	appSink       gstapp.AppSink
	bus           gst.Bus
}

type PreviewSample struct {
	Caps    gst.Caps
	CapsStr string
	Data    []byte
	PTS     time.Duration
}

// PreviewConfig is identity only; the node owns its sample channel.
type PreviewConfig struct {
	ID         string
	BufferSize int
}

var _ core.OneToOneNode = (*PreviewNode)(nil)

// NodeTypePreview is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypePreview = "Preview"

func init() {
	registerNode(NodeTypePreview, core.SpecOneToOne, core.AdaptFactory(CreatePreviewNode))
}

func CreatePreviewNode(runtime core.NodeRuntime, config PreviewConfig) (*PreviewNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}

	entry, err := chan_registry.NewOutputEntry[*PreviewSample](id, config.BufferSize, core.DropOldest)
	if err != nil {
		return nil, err
	}
	if err := runtime.RegisterEntry(entry); err != nil {
		return nil, fmt.Errorf("%w: register entry %q: %v", core.ErrInvalidConfig, id, err)
	}

	previewBin := gst.NewBin(id).(gst.Bin)
	appSinkElement := gst.ElementFactoryMake("appsink", id+"_AppSink").(gstapp.AppSink)
	teeElement := gst.ElementFactoryMake("tee", id+"_Tee")
	if appSinkElement == nil || teeElement == nil {
		return nil, fmt.Errorf("%w: create preview elements", core.ErrGSTNoElements)
	}
	previewBin.Add(teeElement)
	previewBin.Add(appSinkElement)

	teeSinkPad := teeElement.GetStaticPad("sink")
	ghostSink := gst.NewGhostPad(id+"_sink_0", teeSinkPad)
	if ghostSink == nil {
		return nil, core.ErrGSTPadNotFound
	}

	downstreamPad := teeElement.RequestPadSimple("src_%u")
	if downstreamPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	previewPad := teeElement.RequestPadSimple("src_%u")
	if previewPad == nil {
		teeElement.ReleaseRequestPad(downstreamPad)
		return nil, core.ErrGSTPadNotFound
	}

	ghostSrc := gst.NewGhostPad(id+"_src_0", downstreamPad)
	if ghostSrc == nil {
		teeElement.ReleaseRequestPad(downstreamPad)
		teeElement.ReleaseRequestPad(previewPad)
		return nil, core.ErrGSTPadNotFound
	}

	previewBin.AddPad(ghostSink)
	previewBin.AddPad(ghostSrc)

	if rt := previewPad.Link(appSinkElement.GetStaticPad("sink")); rt != gst.PadLinkOK {
		teeElement.ReleaseRequestPad(downstreamPad)
		teeElement.ReleaseRequestPad(previewPad)
		return nil, core.ErrGSTLinkErr
	}
	appSinkElement.SetObjectProperties(map[string]any{
		// GObject rejects a plain Go int: its size is platform dependent, and
		// the value bridge panics rather than guessing. Widen to int32.
		"leaky-type": int32(1), // Drop oldest buffer when full
		"sync":       false,
	})
	bus := appSinkElement.GetBus()
	done := make(chan struct{}, 1)

	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}

			sample := appSinkElement.TryPullSample(gst.ClockTime(time.Millisecond * 100))
			if sample == nil {
				continue
			}
			gstBuffer := sample.GetBuffer()
			if gstBuffer == nil {
				continue
			}
			mapInfo, ok := gstBuffer.Map(gst.MapFlags(gst.MapRead))
			if !ok {
				continue
			}
			sourceData := make([]byte, gstBuffer.GetSize())
			length := copy(sourceData, mapInfo.Data())
			mapInfo.Unmap()
			if length == 0 {
				continue
			}

			caps := sample.GetCaps()
			var capsVal gst.Caps
			capsStr := ""
			if caps != nil {
				capsVal = *caps
				capsStr = caps.String()
			}

			select {
			case <-done:
				return
			default:
			}

			entry.Publish(&PreviewSample{
				Caps:    capsVal,
				CapsStr: capsStr,
				Data:    sourceData,
				PTS:     time.Duration(gstBuffer.DTS()),
			})
		}
	}()

	go func() {
		defer close(done)
		for {
			msg := bus.TimedPop(gst.ClockTime(time.Millisecond * 100))
			if msg == nil {
				continue
			}
			if msg.Type() == gst.MessageError {
				msg, err := msg.ParseError()
				if err != nil {
					log.Printf("PreviewNode %s received error message but failed to parse: %v", id, err)
					continue
				}
				log.Printf("PreviewNode %s received error message: %v", id, msg)
			}
			appSinkElement.SetState(gst.State(gst.StateNull))
			return
		}
	}()

	base := core.NewBaseNode(id, NodeTypePreview, previewBin)
	if err := base.UpdateProperties(map[string]any{"BufferSize": config.BufferSize}); err != nil {
		return nil, err
	}
	base.ProtectProperties("BufferSize")

	return &PreviewNode{
		BaseNode:      base,
		entry:         entry,
		runtime:       runtime,
		sinkPad:       ghostSink,
		srcPad:        ghostSrc,
		teeElement:    teeElement,
		downstreamPad: downstreamPad,
		previewPad:    previewPad,
		appSink:       appSinkElement,
		bus:           bus,
	}, nil
}

func (n *PreviewNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *PreviewNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *PreviewNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *PreviewNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *PreviewNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()
	bin.SetState(gst.State(gst.StateNull))

	// tee src pads are request pads this node owns; release them explicitly.
	if n.teeElement != nil {
		if n.downstreamPad != nil {
			n.teeElement.ReleaseRequestPad(n.downstreamPad)
		}
		if n.previewPad != nil {
			n.teeElement.ReleaseRequestPad(n.previewPad)
		}
	}

	if n.entry != nil {
		n.entry.Stop()
		if err := n.runtime.RemoveEntryById(n.ID()); err != nil {
			return fmt.Errorf("%w: failed to remove entry %q: %v", core.ErrInvalidConfig, n.ID(), err)
		}
		n.entry = nil
	}

	n.sinkPad = nil
	n.srcPad = nil
	n.teeElement = nil
	n.downstreamPad = nil
	n.previewPad = nil
	n.appSink = nil
	n.bus = nil
	n.Clear()
	return nil
}
