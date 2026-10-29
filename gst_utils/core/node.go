package core

import (
	"github.com/go-gst/go-gst/pkg/gst"
)

func SyncWithParents(elements ...gst.Element) error {
	for _, element := range elements {
		ok := element.SyncStateWithParent()
		if !ok {
			return ErrGSTSyncStateErr
		}
	}
	return nil
}

type Node interface {
	ID() string
	NodeType() string
	Bin() gst.Bin
	Destroy() error
	UpdateProperties(properties map[string]interface{}) error
	EditableProperties() map[string]interface{}
	Properties() map[string]interface{}
	GetSrcIDs() []string
	GetDstIDs() []string
}

type ManyToOneNode interface {
	RequestSinkPad() (gst.Pad, string, error)
	RemoveSinkPad(id string) error
	GetSrcPad() (gst.Pad, string, error)
	Node
}

type ManyToZeroNode interface {
	RequestSinkPad() (gst.Pad, string, error)
	RemoveSinkPad(id string) error
	Node
}

type OneToOneNode interface {
	GetSinkPad() (gst.Pad, string, error)
	GetSrcPad() (gst.Pad, string, error)
	Node
}

type OneToManyNode interface {
	RequestSrcPad(padInfo *PadInfo) (gst.Pad, string, error)
	RemoveSrcPad(id string) error
	GetSinkPad() (gst.Pad, string, error)
	Node
}

type ZeroToOneNode interface {
	GetSrcPad() (gst.Pad, string, error)
	Node
}

type OneToZeroNode interface {
	GetSinkPad() (gst.Pad, string, error)
	Node
}

type ChannelDropType int

// DropNone is the zero value on purpose: a producer that was told nothing must
// not silently discard data. It reports ErrChanFull and lets the caller decide,
// which is the only safe default. The drop policies are opt-in, chosen by
// whoever wires the graph, because losing data is a decision the graph author
// has to make deliberately.
const (
	DropNone ChannelDropType = iota
	// DropOldest keeps the newest data when a consumer falls behind. Ingest paths
	// use it so a slow reader cannot stall a GStreamer streaming thread.
	DropOldest
	// DropNewest keeps the oldest data.
	DropNewest
)

// PadCapsString reports the caps a pad can carry, as a GStreamer caps string.
//
// It asks the live pad rather than a declaration, because a declaration would
// be a guess: which media a pad carries is settled by caps negotiation between
// real elements. QueryCaps is the right source because it proxies through a
// ghost pad to its target, so a node's outward-facing ghost reports what its
// internal element can actually accept. GetPadTemplateCaps is useless here: a
// ghost pad's template is always ANY (verified).
//
// A pad with no opinion returns "" rather than nil, so callers can render it
// uniformly.
func PadCapsString(pad gst.Pad) string {
	if pad == nil {
		return ""
	}
	if caps := pad.QueryCaps(nil); caps != nil {
		return caps.String()
	}
	return ""
}

// CanLinkPads reports whether a src pad can link to a sink pad right now.
//
// gst_pad_link runs this exact predicate internally, so a false result here
// means Link would have failed. That makes it usable as a pre-flight check
// before a link is attempted, which is what keeps a bad connection out of the
// non-atomic ApplyGraph path.
func CanLinkPads(srcPad, sinkPad gst.Pad) bool {
	if srcPad == nil || sinkPad == nil {
		return false
	}
	return srcPad.CanLink(sinkPad)
}
