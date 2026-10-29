package nodes

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"live_tools/gst_utils/chan_registry"
	"live_tools/gst_utils/core"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
)

const (
	padPropTrackName = "track_name"
	padPropTrackID   = "track_id"
	padPropMediaType = "media_type"
)

type ContainerParseNode struct {
	*core.BaseNode
	// Pad info
	sinkGhostPad gst.Pad
	// Chan info
	entry *chan_registry.OutputEntry[core.TrackData]
	// Pads Info
	// Four identity spaces meet here, so each name says which one it holds:
	//   padID        canonical, "parser_src_0"
	//   parsePadName parsebin's own, "src_0"
	//   mqSinkName   multiqueue request pad, "sink_0"
	//   selectorKey  track identity, see selectorKey()
	//
	// A "slot" is one output: the ghost pad, its multiqueue sink pad, and the
	// PadInfo describing them. The maps below are all keyed either by a slot's
	// padID or by the selectorKey of the track it carries.
	padIDToGhost        map[string]gst.GhostPad // padID -> ghost pad exposed outside
	padIDToMQSink       map[string]gst.Pad      // padID -> multiqueue sink pad feeding it
	selectorToPadID     map[string]string       // selectorKey -> padID of the slot bound to that track
	mqSinkPads          map[gst.Pad]struct{}    // every request pad, held so Destroy can release it
	parkedEOSSlots      map[string]core.PadInfo // selectorKey -> unbound slot whose downstream is in EOS, waiting to be re-bound
	padInfos            []core.PadInfo          // declared pads, plus ones added at runtime
	parseBinIDToPadInfo map[string]core.PadInfo // parsebin pad name -> pad info of the slot it feeds
	// trackKeyToParseBinPad records which tracks are actually flowing right now,
	// keyed by selector, and is deliberately separate from selectorToPadID: that
	// map answers "which slot owns this track", while this one answers "is this
	// track in the stream at all". A selector change needs the second question,
	// because parsebin emits no signal when a selector changes.
	trackKeyToParseBinPad map[string]string // selectorKey -> parsebin pad name
	// Node Runtime
	runtime core.NodeRuntime
	// Utils
	mu         sync.RWMutex
	nextPadSeq int // next sequence number for a new pad id; only ever grows
	// Elements
	parseBinElement   gst.Element
	multiQueueElement gst.Element
	padAddedHandle    gobject.SignalHandle
	padRemovedHandle  gobject.SignalHandle
}

type ContainerParseConfig struct {
	ID         string
	SourcePads []core.PadInfo
}

func selectorKey(name, trackID, mediaType string) string {
	return strings.Join([]string{name, trackID, mediaType}, "\x00")
}

var emptySelectorKey = selectorKey("", "", "")

var _ core.OneToManyNode = (*ContainerParseNode)(nil)

// NodeTypeContainerParse is this node's registered type name.
const NodeTypeContainerParse = "ContainerParse"

func init() {
	// ContainerParse needs its declared source pads, which AdaptFactory does
	// not carry: DecodeNodeConfig only decodes Properties + ID.
	//
	// UpdatablePads is true because a pad's properties are its track selector:
	// changing them changes which track the pad is bound to, which the node
	// carries out in UpdatePad. Every other node type records pad properties as
	// metadata only, so DiffGraph rejects updates for them.
	registerNode(NodeTypeContainerParse, core.NodeSpec{Sink: 1, Src: core.UnlimitedPads, UpdatablePads: true},
		func(runtime core.NodeRuntime, info core.NodeInfo) (core.Node, error) {
			config, err := core.DecodeNodeConfig[ContainerParseConfig](info)
			if err != nil {
				return nil, err
			}
			config.SourcePads = info.SourcePads
			return CreateContainerParseNode(runtime, config)
		})
}

type trackInfo struct {
	Name      string
	TrackID   string
	MediaType string
}

func (t trackInfo) key() string {
	return selectorKey(t.Name, t.TrackID, t.MediaType)
}

func parseTrackInfo(tags *gst.TagList, mediaType string) trackInfo {
	info := trackInfo{MediaType: mediaType}
	if tags == nil {
		return info
	}
	if value, ok := tags.GetString(gst.TAG_TITLE); ok {
		info.Name = value
	}
	if value, ok := tags.GetString(gst.TAG_CONTAINER_SPECIFIC_TRACK_ID); ok {
		info.TrackID = value
	}
	return info
}

func selectorKeyFromProperties(properties map[string]any) string {
	name, _ := properties[padPropTrackName].(string)
	trackID, _ := properties[padPropTrackID].(string)
	mediaType, _ := properties[padPropMediaType].(string)
	return selectorKey(name, trackID, mediaType)
}

func padIDSeq(padID string) int {
	separator := strings.LastIndex(padID, "_src_")
	if separator < 0 {
		return -1
	}
	seq, err := strconv.Atoi(padID[separator+len("_src_"):])
	if err != nil {
		return -1
	}
	return seq
}

func CreateContainerParseNode(nr core.NodeRuntime, config ContainerParseConfig) (*ContainerParseNode, error) {
	id := config.ID
	if id == "" {
		return nil, core.ErrInvalidConfig
	}

	// bin Creation
	parseBin := gst.NewBin(id).(gst.Bin)

	// input Buffer Queue
	queueElement := gst.ElementFactoryMake("queue", fmt.Sprintf("%s_queue", id))
	parseBinElement := gst.ElementFactoryMake("parsebin", fmt.Sprintf("%s_parsebin", id))
	// output Buffer
	multiQueueElement := gst.ElementFactoryMake("multiqueue", fmt.Sprintf("%s_multiqueue", id))

	// add elements to bin
	parseBin.Add(parseBinElement)
	parseBin.Add(queueElement)
	parseBin.Add(multiQueueElement)

	// sync state with parent
	queueElement.SyncStateWithParent()
	parseBinElement.SyncStateWithParent()
	multiQueueElement.SyncStateWithParent()

	if !queueElement.Link(parseBinElement) {
		return nil, fmt.Errorf("%w: failed to link queue and parsebin", core.ErrGSTLinkErr)
	}

	//----------------------
	// Pad Creation

	// media input Pad
	sinkGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), queueElement.GetStaticPad("sink"))
	if sinkGhostPad == nil {
		return nil, core.ErrGSTPadNotFound
	}
	if !parseBin.AddPad(sinkGhostPad) {
		return nil, fmt.Errorf("%w: add ghost pad %q", core.ErrGSTPadNotFound, sinkGhostPad.GetName())
	}

	//----------------------

	// Create a new output entry for the media track data.
	mediaTrackEntry, err := chan_registry.NewTrackDataOutputEntry(id, 100, core.DropOldest)
	if err != nil {
		return nil, err
	}
	if err := nr.RegisterEntry(mediaTrackEntry); err != nil {
		return nil, fmt.Errorf("%w: register entry %q: %v", core.ErrInvalidConfig, id, err)
	}

	//----------------------

	parseNode := &ContainerParseNode{
		sinkGhostPad:          sinkGhostPad,
		entry:                 mediaTrackEntry,
		padIDToGhost:          make(map[string]gst.GhostPad, len(config.SourcePads)),
		padIDToMQSink:         make(map[string]gst.Pad, len(config.SourcePads)),
		selectorToPadID:       make(map[string]string, len(config.SourcePads)),
		parkedEOSSlots:        make(map[string]core.PadInfo, len(config.SourcePads)),
		padInfos:              config.SourcePads,
		runtime:               nr,
		parseBinElement:       parseBinElement,
		multiQueueElement:     multiQueueElement,
		parseBinIDToPadInfo:   make(map[string]core.PadInfo, len(config.SourcePads)),
		trackKeyToParseBinPad: make(map[string]string, len(config.SourcePads)),
		mqSinkPads:            make(map[gst.Pad]struct{}),
		BaseNode:              core.NewBaseNode(id, NodeTypeContainerParse, parseBin),
	}

	// Create the declared pads, so they exist before the pipeline is set to playing.
	for _, padInfo := range parseNode.padInfos {
		if _, err := parseNode.createSrcPad(padInfo); err != nil {
			_ = parseNode.Destroy()
			return nil, err
		}
		if seq := padIDSeq(padInfo.ID); seq >= parseNode.nextPadSeq {
			parseNode.nextPadSeq = seq + 1
		}
	}

	for _, padInfo := range parseNode.padInfos {
		key := selectorKeyFromProperties(padInfo.Properties)
		if key == emptySelectorKey {
			continue
		}
		parseNode.selectorToPadID[key] = padInfo.ID
	}

	//----------------------

	parseNode.padRemovedHandle = parseBinElement.ConnectPadRemoved(func(_ gst.Element, p gst.Pad) {
		removedParseBinPadId := p.GetName()
		if !strings.HasPrefix(removedParseBinPadId, "src_") {
			return
		}

		parseNode.mu.Lock()
		removedSlotPadInfo, wasBound := parseNode.parseBinIDToPadInfo[removedParseBinPadId]
		if wasBound {
			delete(parseNode.parseBinIDToPadInfo, removedParseBinPadId)
		}
		// The track is gone from the stream, so it must stop being a re-bind
		// candidate. Scan by value because the key is the track's selector, which
		// UpdatePad may have rewritten since this pad was bound.
		for selectorKey, boundParseBinPadName := range parseNode.trackKeyToParseBinPad {
			if boundParseBinPadName == removedParseBinPadId {
				delete(parseNode.trackKeyToParseBinPad, selectorKey)
			}
		}
		parseNode.mu.Unlock()
		if !wasBound {
			// This pad never carried a TAG, so no slot was ever bound to it.
			parseNode.entry.Publish(core.TrackData{
				ID:    removedParseBinPadId,
				Name:  removedParseBinPadId,
				Type:  core.TrackEventTypeError,
				Error: core.ErrGSTPadNotFound,
			})
			return
		}

		if peer := p.GetPeer(); peer != nil {
			peer.Unlink(p)
		}

		parseNode.unbindSlot(removedSlotPadInfo)
	})

	//----------------------

	parseNode.padAddedHandle = parseBinElement.ConnectPadAdded(func(_ gst.Element, p gst.Pad) {
		scratch := multiQueueElement.RequestPadSimple("sink_%u")
		if scratch == nil {
			return
		}

		if rt := p.Link(scratch); rt != gst.PadLinkOK {
			multiQueueElement.ReleaseRequestPad(scratch)
			return
		}

		var probeHandle uint32
		probeHandle = p.AddProbe(gst.PadProbeType(gst.PadProbeTypeEventDownstream), func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			event := info.GetEvent()
			if event == nil || event.GetType() != gst.EventTag {
				return gst.PadProbeOK
			}

			mediaType := ""
			if caps := p.GetCurrentCaps(); caps != nil && caps.GetSize() > 0 {
				mediaType = caps.GetStructure(0).GetName()
			}

			track := parseTrackInfo(event.ParseTag(), mediaType)
			// The first tag event only carries datetime and container-format, so a
			// track is only identified once one of its own tags shows up.
			if track.Name == "" && track.TrackID == "" {
				return gst.PadProbeOK
			}

			var isNewOutput bool

			outputPadInfo, multiQueueSinkPad, wasUnbound := parseNode.findTrackMQSinkPad(track)

			if multiQueueSinkPad == nil {
				isNewOutput = true
				parseNode.mu.Lock()
				seq := parseNode.nextPadSeq
				parseNode.nextPadSeq++
				parseNode.mu.Unlock()
				outputPadInfo = core.PadInfo{
					ID: fmt.Sprintf("%s_src_%d", parseNode.ID(), seq),
					Properties: map[string]any{
						padPropTrackName: track.Name,
						padPropTrackID:   track.TrackID,
						padPropMediaType: track.MediaType,
					},
				}
				newCreatePad, err := parseNode.createSrcPad(outputPadInfo)
				if err != nil {
					parseNode.entry.Publish(core.TrackData{
						ID:    outputPadInfo.ID,
						Name:  track.Name,
						Type:  core.TrackEventTypeError,
						Error: err,
					})
					return gst.PadProbeOK
				}
				multiQueueSinkPad = parseNode.padIDToMQSink[outputPadInfo.ID]
				// Register the new pad with the runtime, so the frontend can see it.
				// The selector travels with it: a pad without properties tells a
				// reader nothing about which track it carries, and DiffGraph compares
				// properties, so an empty map would look like a fresh update every
				// time the graph is echoed back.
				if err := nr.RegisterGstObj(core.CreateGstObjectWithProperties(
					outputPadInfo.ID, newCreatePad, outputPadInfo.Properties,
				)); err != nil {
					parseNode.entry.Publish(core.TrackData{
						ID:    outputPadInfo.ID,
						Name:  track.Name,
						Type:  core.TrackEventTypeError,
						Error: err,
					})
				}
			}

			// Unlink from scratch first; Link fails as WAS_LINKED otherwise.
			if peer := p.GetPeer(); peer != nil {
				p.Unlink(peer)
			}
			if rt := p.Link(multiQueueSinkPad); rt != gst.PadLinkOK {
				multiQueueElement.ReleaseRequestPad(scratch)
				parseNode.entry.Publish(core.TrackData{
					ID:    outputPadInfo.ID,
					Name:  track.Name,
					Type:  core.TrackEventTypeError,
					Error: core.ErrGSTLinkErr,
				})
				return gst.PadProbeOK
			}
			// The slot was unbound earlier, so its downstream is still in EOS.
			if wasUnbound {
				parseNode.rebindSlot(outputPadInfo.ID)
			}

			multiQueueElement.ReleaseRequestPad(scratch)

			parseNode.mu.Lock()
			parseNode.parseBinIDToPadInfo[p.GetName()] = outputPadInfo
			parseNode.trackKeyToParseBinPad[track.key()] = p.GetName()
			parseNode.selectorToPadID[track.key()] = outputPadInfo.ID
			if isNewOutput {
				parseNode.padInfos = append(parseNode.padInfos, outputPadInfo)
			}
			delete(parseNode.parkedEOSSlots, track.key())
			parseNode.mu.Unlock()

			parseNode.entry.Publish(core.TrackData{
				ID:        outputPadInfo.ID,
				Name:      track.Name,
				MediaType: mediaType,
				Caps:      p.GetCurrentCaps(),
				Type:      core.TrackEventTypeDiscovered,
			})

			p.RemoveProbe(probeHandle)
			return gst.PadProbeRemove
		})
	})
	//----------------------

	return parseNode, nil
}

// createSrcPad builds the outward ghost pad and the multiqueue sink pad behind
// it, and wires the ghost's target. It does not link parsebin to the sink pad.
func (n *ContainerParseNode) createSrcPad(padInfo core.PadInfo) (gst.GhostPad, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if _, exists := n.padIDToGhost[padInfo.ID]; exists {
		return nil, fmt.Errorf("%w: source pad %q already created", core.ErrInvalidConfig, padInfo.ID)
	}

	ghostPad, ok := gst.NewGhostPadNoTarget(padInfo.ID, gst.PadDirection(gst.PadSrc)).(gst.GhostPad)
	if !ok || ghostPad == nil {
		return nil, fmt.Errorf("%w: ghost pad %q", core.ErrGSTPadNotFound, padInfo.ID)
	}
	if !n.Bin().AddPad(ghostPad) {
		return nil, fmt.Errorf("%w: add ghost pad %q", core.ErrGSTPadNotFound, padInfo.ID)
	}

	mqSinkPad := n.multiQueueElement.RequestPadSimple("sink_%u")
	if mqSinkPad == nil {
		return nil, fmt.Errorf("%w: request multiqueue sink pad", core.ErrGSTPadNotFound)
	}
	mqSrcPad := n.multiQueueElement.GetStaticPad("src_" + strings.TrimPrefix(mqSinkPad.GetName(), "sink_"))
	if mqSrcPad == nil {
		n.multiQueueElement.ReleaseRequestPad(mqSinkPad)
		return nil, fmt.Errorf("%w: multiqueue src pad for %q", core.ErrGSTPadNotFound, mqSinkPad.GetName())
	}
	if !ghostPad.SetTarget(mqSrcPad) {
		n.multiQueueElement.ReleaseRequestPad(mqSinkPad)
		return nil, fmt.Errorf("%w: set target for %q", core.ErrGSTPadNotFound, padInfo.ID)
	}

	n.padIDToGhost[padInfo.ID] = ghostPad
	n.padIDToMQSink[padInfo.ID] = mqSinkPad
	n.mqSinkPads[mqSinkPad] = struct{}{}
	return ghostPad, nil
}

func (n *ContainerParseNode) findTrackMQSinkPad(track trackInfo) (core.PadInfo, gst.Pad, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	// A track that was unbound earlier still owns its slot; the slot is just no
	// longer claimed by it, and its downstream is in EOS.
	trackSelectorKey := track.key()
	boundPadID, exists := n.selectorToPadID[trackSelectorKey]
	if !exists {
		parkedPadInfo, exists := n.parkedEOSSlots[trackSelectorKey]
		if !exists {
			return core.PadInfo{}, nil, false
		}
		return parkedPadInfo, n.padIDToMQSink[parkedPadInfo.ID], true
	}

	targetMQSinkPad, exists := n.padIDToMQSink[boundPadID]
	if !exists {
		return core.PadInfo{}, nil, false
	}
	for _, padInfo := range n.padInfos {
		if padInfo.ID == boundPadID {
			return padInfo, targetMQSinkPad, false
		}
	}
	return core.PadInfo{}, nil, false
}

// unbindSlot detaches the slot from whatever track currently feeds it and parks
// it under its selector, so a later track with the same selector can reclaim it.
//
// Detaching means unlinking the parsebin pad, not just pushing EOS: nothing else
// removes that link when the selector changes, because parsebin emits no signal
// for it. Leaving the link in place would let the old track keep feeding a slot
// that has already told downstream it ended.
func (n *ContainerParseNode) unbindSlot(padInfo core.PadInfo) {
	trackSelectorKey := selectorKeyFromProperties(padInfo.Properties)

	n.mu.Lock()
	ghostPad := n.padIDToGhost[padInfo.ID]
	mqSinkPad := n.padIDToMQSink[padInfo.ID]
	delete(n.selectorToPadID, trackSelectorKey)
	n.parkedEOSSlots[trackSelectorKey] = padInfo
	n.mu.Unlock()

	// Cut the feed before pushing EOS. parsebin holds the pad's stream lock while
	// it waits for room downstream, so pushing EOS while it is still feeding
	// contends with the streaming thread; unlinking first lets that thread return
	// and go quiet.
	if mqSinkPad != nil {
		if parseBinSrcPad := mqSinkPad.GetPeer(); parseBinSrcPad != nil {
			parseBinSrcPad.Unlink(mqSinkPad)
		}
	}

	if ghostPad == nil {
		return
	}
	if ok := ghostPad.PushEvent(gst.NewEventEOS()); !ok {
		trackName, _ := padInfo.Properties[padPropTrackName].(string)
		n.entry.Publish(core.TrackData{
			ID:    padInfo.ID,
			Name:  trackName,
			Type:  core.TrackEventTypeError,
			Error: core.ErrGSTEventSendErr,
		})
	}
}

func (n *ContainerParseNode) rebindSlot(padID string) {
	n.mu.RLock()
	ghostPad := n.padIDToGhost[padID]
	n.mu.RUnlock()
	if ghostPad == nil {
		return
	}

	ghostPad.PushEvent(gst.NewEventFlushStart())
	ghostPad.PushEvent(gst.NewEventFlushStop(true))

	segment := gst.NewSegment()
	segment.Init(gst.Format(gst.FormatTime))
	ghostPad.PushEvent(gst.NewEventSegment(segment))
}

func (n *ContainerParseNode) ID() string {
	return n.BaseNode.ID()
}

func (n *ContainerParseNode) Bin() gst.Bin {
	return n.BaseNode.Bin()
}

func (n *ContainerParseNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkGhostPad, n.SinkPadID())
}

// RequestSrcPad returns a source pad. With a non-nil padInfo it returns that
// exact pad, building it when it does not exist yet. With nil the node mints a
// new pad of its own, the same way CloneNode adds a branch: a caller that has
// no ID in mind is asking for a new output, not for one of the declared pads.
func (n *ContainerParseNode) RequestSrcPad(padInfo *core.PadInfo) (gst.Pad, string, error) {
	if padInfo == nil {
		padInfo = &core.PadInfo{
			ID:         n.mintPadID(),
			NodeID:     n.ID(),
			Direction:  core.PadDirectionSrc,
			Properties: map[string]any{},
		}
	}

	n.mu.RLock()
	existingPad, exists := n.padIDToGhost[padInfo.ID]
	n.mu.RUnlock()
	if exists {
		return existingPad, padInfo.ID, nil
	}

	ghostPad, err := n.createSrcPad(*padInfo)
	if err != nil {
		return nil, "", err
	}

	// Keep the sequence counter ahead of the new pad, so a later minted pad
	// cannot collide with the ID the caller just chose.
	n.mu.Lock()
	if seq := padIDSeq(padInfo.ID); seq >= n.nextPadSeq {
		n.nextPadSeq = seq + 1
	}
	n.padInfos = append(n.padInfos, *padInfo)
	n.mu.Unlock()
	return ghostPad, padInfo.ID, nil
}

// mintPadID reserves the next pad number. The counter only ever grows, so an
// ID that was handed out is never reused for a different pad.
func (n *ContainerParseNode) mintPadID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	seq := n.nextPadSeq
	n.nextPadSeq++
	return fmt.Sprintf("%s_src_%d", n.ID(), seq)
}

func (n *ContainerParseNode) GetSrcIDs() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	ids := make([]string, 0, len(n.padInfos))
	for _, padInfo := range n.padInfos {
		ids = append(ids, padInfo.ID)
	}
	return ids
}

func (n *ContainerParseNode) GetDstIDs() []string {
	if n.sinkGhostPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *ContainerParseNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()

	if n.parseBinElement != nil && n.padAddedHandle != 0 {
		n.parseBinElement.HandlerDisconnect(n.padAddedHandle)
		n.padAddedHandle = 0
	}
	if n.parseBinElement != nil && n.padRemovedHandle != 0 {
		n.parseBinElement.HandlerDisconnect(n.padRemovedHandle)
		n.padRemovedHandle = 0
	}

	bin.SetState(gst.State(gst.StateNull))

	n.mu.Lock()
	mqSinkPads := n.mqSinkPads
	ghostPads := make([]gst.Pad, 0, len(n.padIDToGhost))
	ghostPadIDs := make([]string, 0, len(n.padIDToGhost))
	for id, pad := range n.padIDToGhost {
		ghostPads = append(ghostPads, pad)
		ghostPadIDs = append(ghostPadIDs, id)
	}
	n.padIDToGhost = make(map[string]gst.GhostPad)
	n.padIDToMQSink = make(map[string]gst.Pad)
	n.selectorToPadID = make(map[string]string)
	n.mqSinkPads = make(map[gst.Pad]struct{})
	n.parkedEOSSlots = make(map[string]core.PadInfo)
	n.trackKeyToParseBinPad = make(map[string]string)
	n.padInfos = nil
	n.mu.Unlock()

	for pad := range mqSinkPads {
		n.multiQueueElement.ReleaseRequestPad(pad)
	}
	for _, pad := range ghostPads {
		bin.RemovePad(pad)
	}
	for _, id := range ghostPadIDs {
		_ = n.runtime.UnregisterGstObj(core.GstObjectTypePad, id)
	}

	n.entry.Stop()
	if err := n.runtime.RemoveEntryById(n.entry.ID()); err != nil {
		return fmt.Errorf("%w: failed to remove entry %q: %v", core.ErrInvalidConfig, n.entry.ID(), err)
	}

	n.sinkGhostPad = nil
	n.Clear()
	return nil
}

func (n *ContainerParseNode) UpdateProperties(properties map[string]interface{}) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.BaseNode.UpdateProperties(properties)
}

var _ core.PadUpdater = (*ContainerParseNode)(nil)

// UpdatePad re-records a source pad's properties and, when they change which
// track the pad is bound to, re-evaluates that binding.
//
// This is the standalone pad-update path: the graph diffs pad properties and
// calls this once per changed pad. The pad and its external links survive — it
// is not a remove-and-recreate. Three cases:
//
//   - the slot is not bound to any track yet: only the recorded properties and
//     the parked key move, so a later track matching the new selector can claim
//     the slot;
//   - the bound track still matches the new selector: the binding is kept and
//     only its key is rewritten;
//   - the bound track no longer matches: the slot is unbound (downstream gets
//     EOS) and parked under the NEW selector, because that is what the pad now
//     wants. The old track must not reclaim it.
//
// Both the node's own maps and the pad registry are updated, because the node is
// the only one that knows which selector a pad belongs to. A snapshot reads the
// registry, so skipping it would make every echo look like a fresh update.
//
// Idempotent: applying the same PadInfo twice changes nothing, so a snapshot
// echoed back produces an empty diff.
func (n *ContainerParseNode) UpdatePad(padInfo core.PadInfo) error {
	// The previous declaration has to be read before anything is rewritten: the
	// old selector key is derived from the OLD properties, and unbindSlot()
	// recomputes the key from the PadInfo it is handed. Passing the new
	// properties there would clear a key that was never set and leave the real
	// one behind.
	n.mu.RLock()
	slotIndex := slices.IndexFunc(n.padInfos, func(pad core.PadInfo) bool {
		return pad.ID == padInfo.ID
	})
	if slotIndex < 0 {
		n.mu.RUnlock()
		return core.ErrGSTPadNotFound
	}
	previousPadInfo := n.padInfos[slotIndex]
	// The selector key the slot is currently claimed under, if any. A slot can be
	// unbound (parked in parkedEOSSlots) and still own an entry, so this is read
	// from the binding table rather than assumed from the properties.
	previousBoundSelectorKey := ""
	for selectorKey, padID := range n.selectorToPadID {
		if padID == padInfo.ID {
			previousBoundSelectorKey = selectorKey
			break
		}
	}
	// A parked slot already told downstream the stream ended, even though it is
	// not bound. That state has to survive the rewrite: the flush sequence only
	// runs when downstream is actually in EOS.
	previousParkedSelectorKey := ""
	for selectorKey, parkedPadInfo := range n.parkedEOSSlots {
		if parkedPadInfo.ID == padInfo.ID {
			previousParkedSelectorKey = selectorKey
			break
		}
	}
	n.mu.RUnlock()

	previousSelectorKey := selectorKeyFromProperties(previousPadInfo.Properties)
	newSelectorKey := selectorKeyFromProperties(padInfo.Properties)

	// One track feeds one slot, and a parsebin pad has one peer. A selector that
	// another slot already holds therefore cannot be honoured. Report it instead
	// of stealing the track: taking it over would silently stop that slot's
	// output, which looks like a decoder bug rather than a config conflict.
	if newSelectorKey != emptySelectorKey && newSelectorKey != previousBoundSelectorKey {
		n.mu.RLock()
		occupyingPadID, isOccupied := n.selectorToPadID[newSelectorKey]
		n.mu.RUnlock()
		if isOccupied && occupyingPadID != padInfo.ID {
			return fmt.Errorf("%w: selector for pad %q is already bound to pad %q",
				core.ErrInvalidConfig, padInfo.ID, occupyingPadID)
		}
	}

	// Unbind before rewriting anything, so the slot cannot be claimed by the old
	// track while its properties already describe the new one.
	didUnbind := false
	if previousBoundSelectorKey != "" && previousBoundSelectorKey != newSelectorKey {
		n.unbindSlot(previousPadInfo)
		didUnbind = true
	}

	n.mu.Lock()
	// Re-find the slot: unbindSlot only touched the maps, but the index could
	// have shifted if the slice was replaced.
	slotIndex = slices.IndexFunc(n.padInfos, func(pad core.PadInfo) bool {
		return pad.ID == padInfo.ID
	})
	if slotIndex < 0 {
		n.mu.Unlock()
		return core.ErrGSTPadNotFound
	}
	n.padInfos[slotIndex].Properties = copyPadProperties(padInfo.Properties)

	delete(n.selectorToPadID, previousSelectorKey)
	if previousBoundSelectorKey != "" && previousBoundSelectorKey == newSelectorKey {
		// Still the same track: keep the binding under its new key.
		n.selectorToPadID[newSelectorKey] = padInfo.ID
	}
	delete(n.parkedEOSSlots, previousSelectorKey)
	if previousParkedSelectorKey != "" {
		delete(n.parkedEOSSlots, previousParkedSelectorKey)
	}
	n.mu.Unlock()

	// Record the new properties in the registry, which is what Snapshot reads.
	// Failing here leaves the node updated but the published view stale, so it is
	// reported rather than ignored.
	if obj, err := n.runtime.GetGstObj(core.GstObjectTypePad, padInfo.ID); err == nil {
		// The pad may not be registered yet; the graph registers it when it creates
		// the pad, so there is nothing to keep in sync in that case.
		if err := obj.SetProperties(padInfo.Properties); err != nil {
			return fmt.Errorf("%w: record properties for pad %q: %v", core.ErrInvalidConfig, padInfo.ID, err)
		}
	}

	if newSelectorKey == emptySelectorKey || previousBoundSelectorKey == newSelectorKey {
		return nil
	}
	// Downstream is in EOS if this call just told it so, or if the slot was
	// already parked from an earlier unbind. Either way a successful re-bind has
	// to flush before data can flow again.
	downstreamInEOS := didUnbind || previousParkedSelectorKey != ""
	return n.rebindSlotToSelector(padInfo.ID, newSelectorKey, downstreamInEOS)
}

// rebindSlotToSelector points a slot at the track the selector now names.
//
// This is the manual half of pad-added. Changing a selector emits no signal:
// parsebin will not re-announce a track it is already carrying, and it will not
// remove a pad that still exists. So the node looks the track up itself, and
// only parks the slot when the track genuinely is not in the stream yet.
//
// downstreamInEOS says whether the slot's downstream was just told the stream
// ended, which decides whether the flush sequence has to run before data can
// flow again.
func (n *ContainerParseNode) rebindSlotToSelector(targetPadID, selectorKey string, downstreamInEOS bool) error {
	n.mu.RLock()
	targetMQSinkPad := n.padIDToMQSink[targetPadID]
	n.mu.RUnlock()
	if targetMQSinkPad == nil {
		return fmt.Errorf("%w: slot %q has no multiqueue sink pad", core.ErrGSTPadNotFound, targetPadID)
	}

	parseBinSrcPad, trackIsFlowing := n.findParseBinPadBySelector(selectorKey)
	if !trackIsFlowing {
		// The track is not in the stream yet. Park the slot under the selector it
		// now wants, so the pad-added probe can bind it when the track shows up.
		n.mu.Lock()
		if slotIndex := slices.IndexFunc(n.padInfos, func(pad core.PadInfo) bool {
			return pad.ID == targetPadID
		}); slotIndex >= 0 {
			n.parkedEOSSlots[selectorKey] = n.padInfos[slotIndex]
		}
		n.mu.Unlock()
		return nil
	}

	// The track is already in the stream, so bind it now rather than waiting for a
	// callback that will never come. Anything still feeding the slot has to go
	// first: Link fails on a pad that is already linked.
	if peer := targetMQSinkPad.GetPeer(); peer != nil {
		peer.Unlink(targetMQSinkPad)
	}
	if rt := parseBinSrcPad.Link(targetMQSinkPad); rt != gst.PadLinkOK {
		return fmt.Errorf("%w: link track for pad %q", core.ErrGSTLinkErr, targetPadID)
	}
	if downstreamInEOS {
		n.rebindSlot(targetPadID)
	}

	// Record the new binding. parseBinIDToPadInfo is what the pad-removed handler
	// reads to learn which slot a disappearing parsebin pad belonged to, so it has
	// to point at this slot now, not at whichever slot the pad fed before.
	n.mu.Lock()
	n.selectorToPadID[selectorKey] = targetPadID
	if slotIndex := slices.IndexFunc(n.padInfos, func(pad core.PadInfo) bool {
		return pad.ID == targetPadID
	}); slotIndex >= 0 {
		n.parseBinIDToPadInfo[parseBinSrcPad.GetName()] = n.padInfos[slotIndex]
	}
	delete(n.parkedEOSSlots, selectorKey)
	n.mu.Unlock()
	return nil
}

// findParseBinPadBySelector returns the parsebin source pad that is currently
// carrying the track matching key.
//
// trackKeyToParseBinPad is the record of which tracks are actually flowing. It
// is filled in by the pad-added probe and cleared by the pad-removed handler.
// Looking a track up here is what lets a selector change take effect without a
// signal: parsebin does not re-announce a track it is already carrying, so the
// only way to bind to it is to find the pad it already owns.
func (n *ContainerParseNode) findParseBinPadBySelector(selectorKey string) (gst.Pad, bool) {
	n.mu.RLock()
	parseBinPadName := n.trackKeyToParseBinPad[selectorKey]
	n.mu.RUnlock()
	if parseBinPadName == "" {
		return nil, false
	}

	parseBinSrcPad := n.parseBinElement.GetStaticPad(parseBinPadName)
	if parseBinSrcPad == nil {
		// The record outlived the pad. Drop it rather than handing back a pad that
		// is gone, so a later attempt re-checks instead of failing on a stale name.
		n.mu.Lock()
		if n.trackKeyToParseBinPad[selectorKey] == parseBinPadName {
			delete(n.trackKeyToParseBinPad, selectorKey)
		}
		n.mu.Unlock()
		return nil, false
	}
	return parseBinSrcPad, true
}

// copyPadProperties copies a pad property map so the node does not alias the
// caller's map.
func copyPadProperties(properties map[string]any) map[string]any {
	if properties == nil {
		return nil
	}
	copied := make(map[string]any, len(properties))
	maps.Copy(copied, properties)
	return copied
}

// RemoveSrcPad takes one output away for good. This is the PadsToRemove path:
// unlike a parsebin pad-removed, the track is not coming back, so the slot is
// retired instead of parked.
func (n *ContainerParseNode) RemoveSrcPad(id string) error {
	// Snapshot under the lock, act outside it. Every step below talks to
	// GStreamer, and unbindSlot takes the lock itself.
	n.mu.RLock()
	targetGhostPad, hasGhost := n.padIDToGhost[id]
	targetMQSinkPad, hasMQSink := n.padIDToMQSink[id]
	slotPadInfo, hasPadInfo := n.padInfoByIDLocked(id)
	n.mu.RUnlock()

	if !hasGhost || !hasMQSink {
		return core.ErrGSTPadNotFound
	}

	// "Fed by parsebin" is what separates a live slot from an idle one, not
	// whether a PadInfo exists: a declared pad always has one, bound or not.
	feedingParseBinSrcPad := targetMQSinkPad.GetPeer()
	if feedingParseBinSrcPad == nil || !hasPadInfo {
		// Nothing feeds this slot, so there is no track to unbind and no
		// downstream to notify. Retiring the plumbing is the whole job.
		return n.teardownSlot(id, targetGhostPad, targetMQSinkPad)
	}

	// Cut the data source first, then tell downstream the output ended.
	//
	// The order matters: pushing EOS while parsebin is still feeding the slot
	// makes the push contend with the streaming thread, which holds the pad's
	// stream lock while it waits for room downstream. Unlinking first lets that
	// thread return and go quiet, so the EOS push cannot deadlock against it.
	feedingParseBinSrcPad.Unlink(targetMQSinkPad)
	n.unbindSlot(slotPadInfo)

	n.mu.Lock()
	seq := n.nextPadSeq
	n.nextPadSeq++
	n.mu.Unlock()

	replacementPadInfo := core.PadInfo{
		ID: fmt.Sprintf("%s_src_%d", n.ID(), seq),
		Properties: map[string]any{
			padPropTrackName: slotPadInfo.Properties[padPropTrackName],
			padPropTrackID:   slotPadInfo.Properties[padPropTrackID],
			padPropMediaType: slotPadInfo.Properties[padPropMediaType],
		},
	}
	replacementGhostPad, err := n.createSrcPad(replacementPadInfo)
	if err != nil {
		return fmt.Errorf("%w: failed to create replacement pad for %q: %v", core.ErrInvalidConfig, id, err)
	}

	replacementMQSinkPad := n.padIDToMQSink[replacementPadInfo.ID]
	if replacementMQSinkPad == nil {
		return fmt.Errorf("%w: failed to find multiqueue sink pad for %q", core.ErrInvalidConfig, replacementPadInfo.ID)
	}
	if rt := feedingParseBinSrcPad.Link(replacementMQSinkPad); rt != gst.PadLinkOK {
		return core.ErrGSTLinkErr
	}

	replacementSelectorKey := selectorKeyFromProperties(replacementPadInfo.Properties)
	n.mu.Lock()
	delete(n.padIDToGhost, id)
	delete(n.padIDToMQSink, id)
	delete(n.mqSinkPads, targetMQSinkPad)
	delete(n.parkedEOSSlots, replacementSelectorKey)
	n.selectorToPadID[replacementSelectorKey] = replacementPadInfo.ID
	n.parseBinIDToPadInfo[feedingParseBinSrcPad.GetName()] = replacementPadInfo
	// Swap the entry in place rather than appending: the old id must stop
	// showing up in GetSrcIDs, and the slot keeps its position.
	if slotIndex := slices.IndexFunc(n.padInfos, func(padInfo core.PadInfo) bool {
		return padInfo.ID == id
	}); slotIndex >= 0 {
		n.padInfos[slotIndex] = replacementPadInfo
	} else {
		n.padInfos = append(n.padInfos, replacementPadInfo)
	}
	n.mu.Unlock()

	if peer := targetGhostPad.GetPeer(); peer != nil {
		targetGhostPad.Unlink(peer)
	}
	if !n.Bin().RemovePad(targetGhostPad) {
		return fmt.Errorf("%w: remove ghost pad %q", core.ErrGSTPadNotFound, id)
	}
	n.multiQueueElement.ReleaseRequestPad(targetMQSinkPad)
	_ = n.runtime.UnregisterGstObj(core.GstObjectTypePad, id)

	if err := n.runtime.RegisterGstObj(core.CreateGstObjectWithProperties(
		replacementPadInfo.ID, replacementGhostPad, replacementPadInfo.Properties,
	)); err != nil {
		return fmt.Errorf("%w: register pad %q: %v", core.ErrInvalidConfig, replacementPadInfo.ID, err)
	}

	replacementTrackName, _ := replacementPadInfo.Properties[padPropTrackName].(string)
	replacementMediaType, _ := replacementPadInfo.Properties[padPropMediaType].(string)
	n.entry.Publish(core.TrackData{
		ID:        replacementPadInfo.ID,
		Name:      replacementTrackName,
		MediaType: replacementMediaType,
		Caps:      replacementGhostPad.GetCurrentCaps(),
		Type:      core.TrackEventTypeDiscovered,
	})
	return nil
}

// padInfoByIDLocked returns the PadInfo for a pad id. Callers must hold n.mu.
func (n *ContainerParseNode) padInfoByIDLocked(id string) (core.PadInfo, bool) {
	for _, padInfo := range n.padInfos {
		if padInfo.ID == id {
			return padInfo, true
		}
	}
	return core.PadInfo{}, false
}

func (n *ContainerParseNode) teardownSlot(id string, ghostPad gst.GhostPad, mqSinkPad gst.Pad) error {
	if peer := ghostPad.GetPeer(); peer != nil {
		ghostPad.Unlink(peer)
	}
	if !n.Bin().RemovePad(ghostPad) {
		return fmt.Errorf("%w: remove ghost pad %q", core.ErrGSTPadNotFound, id)
	}
	n.multiQueueElement.ReleaseRequestPad(mqSinkPad)

	n.mu.Lock()
	delete(n.padIDToGhost, id)
	delete(n.padIDToMQSink, id)
	delete(n.mqSinkPads, mqSinkPad)
	n.padInfos = slices.DeleteFunc(n.padInfos, func(padInfo core.PadInfo) bool {
		return padInfo.ID == id
	})
	n.mu.Unlock()

	_ = n.runtime.UnregisterGstObj(core.GstObjectTypePad, id)
	return nil
}
