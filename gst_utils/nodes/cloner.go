package nodes

import (
	"fmt"
	"slices"
	"strings"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

type cloneSrcLease struct {
	id          string
	ghostSrcPad gst.GhostPad
	teeSrcPad   gst.Pad
	mqSinkPad   gst.Pad
}

type CloneNode struct {
	*core.BaseNode
	sinkPad    gst.Pad
	teeElement gst.Element
	multiQueue gst.Element
	leases     []cloneSrcLease
	branchSeq  int // next tee/multiqueue index; never reused
	padIDSeq   int // next minted pad id number; only ever grows
}

type CloneConfig struct {
	ID string
}

var _ core.OneToManyNode = (*CloneNode)(nil)

const NodeTypeClone = "Clone"

func init() {
	registerNode(NodeTypeClone, core.NodeSpec{Sink: 1, Src: core.UnlimitedPads}, core.AdaptFactory(CreateCloneNode))
}

func CreateCloneNode(_ core.NodeRuntime, config CloneConfig) (*CloneNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}
	cloneBin := gst.NewBin(id).(gst.Bin)
	teeElement := gst.ElementFactoryMake("tee", id+"_Tee")
	if teeElement == nil {
		return nil, fmt.Errorf("%w: create tee", core.ErrGSTNoElements)
	}
	multiQueueElement := gst.ElementFactoryMake("multiqueue", id+"_MultiQueue")
	if multiQueueElement == nil {
		return nil, fmt.Errorf("%w: create multiqueue", core.ErrGSTNoElements)
	}
	cloneBin.Add(teeElement)
	cloneBin.Add(multiQueueElement)

	ghostSink := gst.NewGhostPad(id+"_sink_0", teeElement.GetStaticPad("sink"))
	if ghostSink == nil {
		return nil, core.ErrGSTPadNotFound
	}
	cloneBin.AddPad(ghostSink)

	teeElement.SyncStateWithParent()
	multiQueueElement.SyncStateWithParent()

	base := core.NewBaseNode(id, NodeTypeClone, cloneBin)

	return &CloneNode{
		BaseNode:   base,
		sinkPad:    ghostSink,
		teeElement: teeElement,
		multiQueue: multiQueueElement,
		leases:     make([]cloneSrcLease, 0),
	}, nil
}

func (c *CloneNode) UpdateProperties(properties map[string]interface{}) error {
	return c.BaseNode.UpdateProperties(properties)
}

// RequestSrcPad adds one branch. The pad id is the caller's business: a
// non-nil padInfo names the pad, nil lets the node mint one. The tee and
// multiqueue indexes are an internal detail and are always allocated here, so
// they stay sequential even when pad ids are removed and re-added.
func (c *CloneNode) RequestSrcPad(padInfo *core.PadInfo) (gst.Pad, string, error) {
	padID := ""
	if padInfo != nil {
		padID = padInfo.ID
		if _, exists := c.leaseByID(padID); exists {
			return nil, "", fmt.Errorf("%w: clone pad %q already exists", core.ErrInvalidConfig, padID)
		}
	}

	bin := c.Bin()

	branchIndex := c.branchSeq
	teeSrcPad := c.teeElement.RequestPadSimple(fmt.Sprintf("src_%d", branchIndex))
	if teeSrcPad == nil {
		return nil, "", core.ErrGSTPadNotFound
	}

	mqSinkPad := c.multiQueue.RequestPadSimple("sink_%u")
	if mqSinkPad == nil {
		c.teeElement.ReleaseRequestPad(teeSrcPad)
		return nil, "", core.ErrGSTPadNotFound
	}

	// All four resources are held now, so the id can be minted without risking
	// a leak if a later step fails.
	if padID == "" {
		padID = fmt.Sprintf("%s_src_%d", c.ID(), c.padIDSeq)
	}

	// Targetless first: a ghost pad created with a target asserts that the
	// target is not linked, which is easy to violate while wiring the branch.
	ghostSrc, ok := gst.NewGhostPadNoTarget(padID, gst.PadDirection(gst.PadSrc)).(gst.GhostPad)
	if !ok || ghostSrc == nil {
		c.multiQueue.ReleaseRequestPad(mqSinkPad)
		c.teeElement.ReleaseRequestPad(teeSrcPad)
		return nil, "", core.ErrGSTPadNotFound
	}
	if !bin.AddPad(ghostSrc) {
		c.multiQueue.ReleaseRequestPad(mqSinkPad)
		c.teeElement.ReleaseRequestPad(teeSrcPad)
		return nil, "", core.ErrGSTPadNotFound
	}

	// Requesting sink_%u creates the paired src_%u, so the source pad is looked
	// up rather than requested.
	mqSrcPad := c.multiQueue.GetStaticPad("src_" + strings.TrimPrefix(mqSinkPad.GetName(), "sink_"))
	if mqSrcPad == nil || !ghostSrc.SetTarget(mqSrcPad) {
		c.multiQueue.ReleaseRequestPad(mqSinkPad)
		c.teeElement.ReleaseRequestPad(teeSrcPad)
		bin.RemovePad(ghostSrc)
		return nil, "", core.ErrGSTPadNotFound
	}

	if rt := teeSrcPad.Link(mqSinkPad); rt != gst.PadLinkOK {
		ghostSrc.SetTarget(nil)
		c.multiQueue.ReleaseRequestPad(mqSinkPad)
		c.teeElement.ReleaseRequestPad(teeSrcPad)
		bin.RemovePad(ghostSrc)
		return nil, "", core.ErrGSTLinkErr
	}

	c.branchSeq++
	if padInfo == nil {
		c.padIDSeq++
	}
	c.leases = append(c.leases, cloneSrcLease{
		id:          padID,
		ghostSrcPad: ghostSrc,
		teeSrcPad:   teeSrcPad,
		mqSinkPad:   mqSinkPad,
	})
	return ghostSrc, padID, nil
}

func (c *CloneNode) leaseByID(id string) (cloneSrcLease, bool) {
	for _, lease := range c.leases {
		if lease.id == id {
			return lease, true
		}
	}
	return cloneSrcLease{}, false
}

// GetSinkPad reports the single input this node exposes.
func (c *CloneNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(c.sinkPad, c.SinkPadID())
}

// GetSrcPad reports the most recently requested branch.
func (c *CloneNode) GetSrcPad() (gst.Pad, string, error) {
	if len(c.leases) == 0 {
		return nil, "", core.ErrGSTPadNotFound
	}
	last := c.leases[len(c.leases)-1]
	return last.ghostSrcPad, last.id, nil
}

func (c *CloneNode) RemoveSrcPad(id string) error {
	for i, lease := range c.leases {
		if lease.id != id {
			continue
		}
		if peer := lease.teeSrcPad.GetPeer(); peer != nil {
			lease.teeSrcPad.Unlink(peer)
		}
		if peer := lease.ghostSrcPad.GetPeer(); peer != nil {
			lease.ghostSrcPad.Unlink(peer)
		}

		lease.ghostSrcPad.SetTarget(nil)
		c.Bin().RemovePad(lease.ghostSrcPad)
		c.multiQueue.ReleaseRequestPad(lease.mqSinkPad)
		c.teeElement.ReleaseRequestPad(lease.teeSrcPad)

		c.leases = slices.Delete(c.leases, i, i+1)
		return nil
	}
	return core.ErrGSTPadNotFound
}

// GetDstIDs reports the single sink this node exposes.
func (c *CloneNode) GetDstIDs() []string {
	if c.sinkPad == nil {
		return nil
	}
	return []string{c.SinkPadID()}
}

// GetSrcIDs lists every branch this node exposes.
func (c *CloneNode) GetSrcIDs() []string {
	ids := make([]string, 0, len(c.leases))
	for _, lease := range c.leases {
		ids = append(ids, lease.id)
	}
	return ids
}

func (c *CloneNode) Destroy() error {
	if c.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := c.Bin()
	bin.SetState(gst.State(gst.StateNull))

	for _, lease := range c.leases {
		lease.ghostSrcPad.SetTarget(nil)
		bin.RemovePad(lease.ghostSrcPad)
		c.multiQueue.ReleaseRequestPad(lease.mqSinkPad)
		c.teeElement.ReleaseRequestPad(lease.teeSrcPad)
	}

	c.teeElement = nil
	c.multiQueue = nil
	c.sinkPad = nil
	c.leases = nil
	c.branchSeq = 0
	c.padIDSeq = 0
	c.Clear()
	return nil
}
