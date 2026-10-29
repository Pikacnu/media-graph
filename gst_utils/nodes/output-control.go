package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// OutputControlNode is an output branch's buffer-and-publish policy: it owns the
// branch's queue (its latency ceiling) and the gate that starts and stops
// publishing, without touching the graph.
//
// Chain: queue -> valve
//
// It exists for two reasons. "Go live" is a runtime decision, not a topology
// change — removing and re-adding an output node to stop publishing would
// rebuild the pipeline and drop the connection, while flipping one boolean does
// not. And how much an output buffers is a latency decision: a bounded, leaky
// queue lets a slow consumer shed old data instead of making the whole branch
// late, which is the same role the frozen FLV output's per-branch queue plays.
//
// The valve's `drop` and the queue's `max-size-*`/`leaky` are documented as
// changeable in PAUSED/PLAYING, so all of those are live. The valve's
// `drop-mode` is NOT live (NULL/READY only) and is a constructor setting.
type OutputControlNode struct {
	*core.BaseNode
	sinkPad gst.Pad
	srcPad  gst.Pad
	queue   gst.Element
	valve   gst.Element
}

var _ core.OneToOneNode = (*OutputControlNode)(nil)

// NodeTypeOutputControl is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeOutputControl = "OutputControl"

func init() {
	registerNode(NodeTypeOutputControl, core.SpecOneToOne, core.AdaptFactory(CreateOutputControlNode))
}

// The valve drop modes, as GStreamer names them. `transform-to-gap` is the
// default for a publishing gate because it keeps the timeline moving: downstream
// sees gap events instead of silence, so a muxer does not stall waiting for data
// that will never arrive while the gate is closed.
const (
	// valveDropAll drops buffers and events.
	valveDropAll = "drop-all"
	// valveForwardStickyEvents drops buffers but forwards sticky events.
	valveForwardStickyEvents = "forward-sticky-events"
	// valveTransformToGap converts dropped buffers into gap events.
	valveTransformToGap = "transform-to-gap"
)

// The queue leaky modes, as GStreamer names them. `downstream` is what a live
// output wants: when the branch backs up, drop the oldest data rather than
// letting latency grow without bound.
const (
	queueLeakyNo         = "no"
	queueLeakyUpstream   = "upstream"
	queueLeakyDownstream = "downstream"
)

// OutputBufferConfig is the branch queue's buffering policy — the output
// branch's latency ceiling.
type OutputBufferConfig struct {
	// MaxSizeBuffers, MaxSizeBytes and MaxSizeTime cap the queue. Zero leaves
	// that dimension at GStreamer's own default (200 buffers / 10 MB / 1 s).
	MaxSizeBuffers uint32
	MaxSizeBytes   uint32
	// MaxSizeTime is nanoseconds.
	MaxSizeTime uint64
	// Leaky picks which end sheds when the queue is full: "no" (block, the
	// default), "upstream" (drop newest) or "downstream" (drop oldest).
	Leaky string
}

// normalizeBufferConfig fills the leaky default and rejects unknown values.
func normalizeBufferConfig(buffer OutputBufferConfig) (OutputBufferConfig, error) {
	if buffer.Leaky == "" {
		buffer.Leaky = queueLeakyNo
	}
	switch buffer.Leaky {
	case queueLeakyNo, queueLeakyUpstream, queueLeakyDownstream:
		return buffer, nil
	default:
		return buffer, fmt.Errorf("%w: unknown queue leaky mode %q", core.ErrInvalidConfig, buffer.Leaky)
	}
}

// applyBufferPolicy writes a normalized policy onto the branch queue. Zero caps
// leave that dimension at GStreamer's default.
func applyBufferPolicy(queue gst.Element, buffer OutputBufferConfig) {
	if buffer.MaxSizeBuffers > 0 {
		queue.SetObjectProperty("max-size-buffers", buffer.MaxSizeBuffers)
	}
	if buffer.MaxSizeBytes > 0 {
		queue.SetObjectProperty("max-size-bytes", buffer.MaxSizeBytes)
	}
	if buffer.MaxSizeTime > 0 {
		queue.SetObjectProperty("max-size-time", buffer.MaxSizeTime)
	}
	// leaky is a string enum, so it needs GStreamer's own string setter.
	gst.UtilSetObjectArg(queue, "leaky", buffer.Leaky)
}

type OutputControlConfig struct {
	ID string
	// Publishing is the initial gate state. A branch usually starts closed and
	// is opened when the operator presses "go live".
	Publishing bool
	// DropMode selects what the closed gate does. Empty means transform-to-gap.
	DropMode string
	// Buffer is the branch queue's buffering/latency policy.
	Buffer OutputBufferConfig
}

func CreateOutputControlNode(_ core.NodeRuntime, config OutputControlConfig) (*OutputControlNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}

	dropMode := config.DropMode
	if dropMode == "" {
		dropMode = valveTransformToGap
	}
	switch dropMode {
	case valveDropAll, valveForwardStickyEvents, valveTransformToGap:
	default:
		return nil, fmt.Errorf("%w: unknown valve drop mode %q", core.ErrInvalidConfig, dropMode)
	}

	bin := gst.NewBin(id).(gst.Bin)
	queue := gst.ElementFactoryMake("queue", id+"_Queue")
	valve := gst.ElementFactoryMake("valve", id+"_Valve")
	if queue == nil || valve == nil {
		return nil, fmt.Errorf("%w: create output control elements", core.ErrGSTNoElements)
	}

	// `drop` must be set before the pipeline starts, so a branch that begins
	// closed never emits a first frame.
	valve.SetObjectProperty("drop", !config.Publishing)
	// drop-mode is an enum and is only changeable in NULL/READY, so it is set
	// here with GStreamer's own string setter and never touched again.
	gst.UtilSetObjectArg(valve, "drop-mode", dropMode)

	// The queue's buffering policy is the branch's latency ceiling; set it up
	// front so the branch is bounded from the first buffer.
	buffer, err := normalizeBufferConfig(config.Buffer)
	if err != nil {
		return nil, err
	}
	applyBufferPolicy(queue, buffer)

	bin.AddMany(queue, valve)

	// Order matters: add, link, then ghost. See pads.go.
	if !queue.Link(valve) {
		return nil, fmt.Errorf("%w: link queue to valve", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), queue.GetStaticPad("sink"))
	srcPad := gst.NewGhostPad(fmt.Sprintf("%s_src_0", id), valve.GetStaticPad("src"))
	if sinkPad == nil || srcPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !bin.AddPad(sinkPad) || !bin.AddPad(srcPad) {
		return nil, fmt.Errorf("%w: add pads to %q", core.ErrGSTPadNotFound, id)
	}

	node := &OutputControlNode{
		sinkPad: sinkPad,
		srcPad:  srcPad,
		queue:   queue,
		valve:   valve,
	}
	node.BaseNode = core.NewBaseNode(id, NodeTypeOutputControl, bin)

	if err := node.BaseNode.UpdateProperties(map[string]any{
		"Publishing":     config.Publishing,
		"DropMode":       dropMode,
		"MaxSizeBuffers": buffer.MaxSizeBuffers,
		"MaxSizeBytes":   buffer.MaxSizeBytes,
		"MaxSizeTime":    buffer.MaxSizeTime,
		"Leaky":          buffer.Leaky,
	}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("DropMode")

	queue.SyncStateWithParent()
	valve.SyncStateWithParent()
	return node, nil
}

// SetPublishing opens or closes the gate. This is the whole point of the node,
// so it is a named method as well as a property: a caller does not have to know
// that `Publishing` maps to `!valve.drop`.
func (n *OutputControlNode) SetPublishing(publishing bool) {
	n.valve.SetObjectProperty("drop", !publishing)
	_ = n.BaseNode.UpdateProperties(map[string]any{"Publishing": publishing})
}

// Publishing reports the current gate state.
func (n *OutputControlNode) Publishing() bool {
	publishing, _ := n.BaseNode.Properties()["Publishing"].(bool)
	return publishing
}

func (n *OutputControlNode) UpdateProperties(properties map[string]interface{}) error {
	if len(properties) == 0 {
		return nil
	}
	if raw, ok := properties["Publishing"]; ok {
		publishing, isBool := raw.(bool)
		if !isBool {
			return fmt.Errorf("%w: Publishing must be a boolean", core.ErrInvalidConfig)
		}
		n.SetPublishing(publishing)
	}
	if raw, ok := properties["Leaky"]; ok {
		leaky, isString := raw.(string)
		if !isString {
			return fmt.Errorf("%w: Leaky must be a string", core.ErrInvalidConfig)
		}
		if _, err := normalizeBufferConfig(OutputBufferConfig{Leaky: leaky}); err != nil {
			return err
		}
	}
	if err := n.BaseNode.UpdateProperties(properties); err != nil {
		return err
	}
	return n.reapplyBufferPolicy()
}

// reapplyBufferPolicy re-reads the stored caps and rewrites them onto the queue,
// so the buffer policy is adjustable live like the gate is.
func (n *OutputControlNode) reapplyBufferPolicy() error {
	stored := n.BaseNode.Properties()
	buffer := OutputBufferConfig{
		MaxSizeBuffers: uint32(propertyNumber(stored["MaxSizeBuffers"], 0)),
		MaxSizeBytes:   uint32(propertyNumber(stored["MaxSizeBytes"], 0)),
		MaxSizeTime:    uint64(propertyNumber(stored["MaxSizeTime"], 0)),
	}
	if leaky, ok := stored["Leaky"].(string); ok {
		buffer.Leaky = leaky
	}
	buffer, err := normalizeBufferConfig(buffer)
	if err != nil {
		return err
	}
	applyBufferPolicy(n.queue, buffer)
	return nil
}

func (n *OutputControlNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *OutputControlNode) GetSrcPad() (gst.Pad, string, error) {
	return padOrErr(n.srcPad, n.SrcPadID())
}

func (n *OutputControlNode) GetSrcIDs() []string {
	if n.srcPad == nil {
		return nil
	}
	return []string{n.SrcPadID()}
}

func (n *OutputControlNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *OutputControlNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	n.Bin().SetState(gst.State(gst.StateNull))

	n.queue = nil
	n.valve = nil
	n.sinkPad = nil
	n.srcPad = nil
	n.Clear()
	return nil
}
