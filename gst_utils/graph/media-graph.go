package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/google/uuid"
)

// Aliases keep the graph package's own vocabulary (NodeInfo, PadInfo, LinkInfo)
// while the definitions live in core, which owns the wire shapes.
type (
	LinkInfo = core.LinkInfo
	NodeInfo = core.NodeInfo
	PadInfo  = core.PadInfo
)

type MediaGraphState int

const (
	MediaGraphStateNull MediaGraphState = iota
	MediaGraphStateReady
	MediaGraphStatePaused
	MediaGraphStatePlaying
	MediaGraphStateError
)

// =============================================================================
// Types
// =============================================================================

type mediaGraphImpl struct {
	pipeline gst.Pipeline
	bus      BusController
	// id is the graph's own identity. Snapshot needs it, because a DesiredGraph
	// carries the graph ID and the pipeline's name is not the same thing.
	id string

	// The event loop owns every mutation, so it is the total order for internal
	// updates. These are set once by CreateMediaGraph and never reassigned.
	// eventCtx is cancelled when the loop stops, which is how a caller tells a
	// stopped loop from a slow one. Reads do NOT come through here.
	eventChan     chan MediaGraphEvent
	loopCtx       context.Context
	loopCancel    context.CancelFunc
	isLoopStarted bool

	// mu guards every field below it. The write lock is taken by the loop while
	// it runs one message, so a reconcile is one critical section against
	// readers. Lock order: mg.mu before a node's own mutex, never the reverse.
	mu                sync.RWMutex
	currentGraphLinks []LinkInfo
	currentGraphNodes map[string]core.Node
	nodeRuntime       core.NodeRuntime
	gstObjectRegistry *core.GstObjectRegistry

	nodeChanEntries map[string]core.RegistryEntry
	trackDataChan   chan trackDataEvent
	// runtimeEventChan is the graph-to-controller boundary. It carries both
	// discovery and normalized Bus observations; internal dirty state is not
	// emitted as an event.
	runtimeEventChan chan core.RuntimeEvent

	chanRegistry          core.ChanRegistry
	chanRegistryEventChan <-chan core.ChanRegistryEvent
	busEvents             <-chan BusEvent
	stateMu               sync.RWMutex
	currentState          gst.State
	pendingState          gst.State

	applied        DesiredGraph
	appliedVersion uint64
	dirty          bool

	currentGraphState MediaGraphState
}

// trackDataEvent is the aggregate-channel envelope. The entry ID is kept next
// to the payload because a typed TrackData channel alone has no source identity.
type trackDataEvent struct {
	entryID string
	data    core.TrackData
}

// MediaGraphEventType names one kind of work the event loop can perform.
type MediaGraphEventType int

const (
	MediaGraphEventTypeReturnError MediaGraphEventType = iota
	MediaGraphEventTypeReturnSuccess
	MediaGraphEventTypeReturnResult
	// ----------------------------
	MediaGraphEventTypeApplyGraph
	MediaGraphEventTypeSetState
	MediaGraphEventTypeDestroy
)

// MediaGraphEvent is one unit of work for the event loop.
type MediaGraphEvent struct {
	Type    MediaGraphEventType
	Payload any
	// Reply receives the loop's answer. It is buffered with room for one message,
	// so the loop never blocks on a caller that stopped waiting. Nil for a message
	// the loop sends outward.
	Reply chan MediaGraphEvent
}

// ErrGraphBusy is returned instead of queueing when the event loop's queue is
// full. The caller should back off and retry; blocking would grow goroutines
// without bound, which is the failure the queue exists to prevent.
var ErrGraphBusy = errors.New("graph event loop is busy")

// ErrGraphDestroyed is returned by a command sent after the loop stopped, since
// nothing would ever read it.
var ErrGraphDestroyed = errors.New("graph is destroyed")

// DesiredGraph is the frontend's desired media graph state.
// It is reconciled against the currently applied graph by DiffGraph.
type DesiredGraph struct {
	ID         string                 `json:"id"`
	Nodes      map[string]NodeInfo    `json:"nodes"`
	Links      []LinkInfo             `json:"links"`
	Properties map[string]interface{} `json:"properties"`
}

// GraphDiff is the ordered change set that turns one graph into another.
//
// Field names are verb-first so the direction of the change reads from the name
// alone: Add*, Update*, Remove*. A field only appears in one direction; the
// inverse of an Add is a Remove, never a second field.
type GraphDiff struct {
	// Node Changes
	AddNodes              []NodeInfo
	UpdatePropertiesNodes []NodeInfo
	RemoveNodes           []NodeInfo

	// Node's Pad Changes
	//
	// AddPads carries one entry per node, with SourcePads and SinkPads holding
	// only the pads that node must gain. A pad addition is one reconcile step
	// regardless of direction, so splitting it into two lists made the caller run
	// two near-identical loops and made a node that grew both kinds appear twice.
	AddPads []NodeInfo
	// UpdatePads carries the pads whose properties changed but whose ID did not.
	//
	// A pad update is not a structural change: the pad keeps its ID, its position
	// and its external links, and only the selector it claims changes. Expressing
	// it as remove+add would tear the link down and rebuild it, which interrupts a
	// live stream, so it gets its own list.
	UpdatePads []PadInfo
	RemovePads []PadInfo

	// Link Changes
	AddLinks    []LinkInfo
	RemoveLinks []LinkInfo
}

// IsEmpty reports whether the diff would change nothing. DiffGraph returns a diff
// rather than nil even when empty, so callers ask this instead of testing for nil.
// A nil receiver counts as empty, so a caller need not check for nil first.
func (d *GraphDiff) IsEmpty() bool {
	if d == nil {
		return true
	}
	return len(d.AddNodes) == 0 &&
		len(d.UpdatePropertiesNodes) == 0 &&
		len(d.RemoveNodes) == 0 &&
		len(d.AddPads) == 0 &&
		len(d.UpdatePads) == 0 &&
		len(d.RemovePads) == 0 &&
		len(d.AddLinks) == 0 &&
		len(d.RemoveLinks) == 0
}

type sinkPadRemover interface {
	RemoveSinkPad(id string) error
}

var ErrVersionMismatch = errors.New("graph version mismatch")

// =============================================================================
// Construction
// =============================================================================

func CreateMediaGraph(id string) (*mediaGraphImpl, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("media graph id cannot be empty")
	}
	// Gst init
	pipeline := gst.NewPipeline(id).(gst.Pipeline)

	gstObjectRegistry := core.CreateGstObjectRegistry()
	chanRegistry, err := core.CreateChanRegistry(id)
	if err != nil {
		return nil, fmt.Errorf("failed to create channel registry: %w", err)
	}
	nodeRuntime, err := core.CreateNodeRuntime(id, gstObjectRegistry, chanRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to create node runtime: %w", err)
	}
	busController, err := CreateBusController(pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to create bus controller: %w", err)
	}
	busEvents, err := busController.RegisterBusEventsChan()
	if err != nil {
		busController.Stop()
		return nil, fmt.Errorf("failed to register bus events: %w", err)
	}

	eventChan := make(chan MediaGraphEvent, 5)

	eventCtx, cancel := context.WithCancel(context.Background())

	chanRegistryEventChan := chanRegistry.GetEventChan()

	mediaGraph := &mediaGraphImpl{
		pipeline:              pipeline,
		bus:                   busController,
		id:                    id,
		currentGraphNodes:     make(map[string]core.Node),
		currentGraphLinks:     make([]LinkInfo, 0),
		nodeRuntime:           nodeRuntime,
		nodeChanEntries:       make(map[string]core.RegistryEntry),
		trackDataChan:         make(chan trackDataEvent, 64),
		runtimeEventChan:      make(chan core.RuntimeEvent, 64),
		eventChan:             eventChan,
		loopCtx:               eventCtx,
		loopCancel:            cancel,
		gstObjectRegistry:     gstObjectRegistry,
		chanRegistry:          chanRegistry,
		chanRegistryEventChan: chanRegistryEventChan,
		busEvents:             busEvents,
		currentState:          gst.State(gst.StateNull),
		pendingState:          gst.State(gst.StateVoidPending),
	}

	// The event loop owns every mutation, so it must be running before the graph
	// is handed to anyone. Starting it here is what makes ApplyGraph's
	// send-and-wait safe.
	mediaGraph.runEventLoop()

	return mediaGraph, nil
}

// createGraphNode builds a node from its declaration using the shared catalog.
//
// The graph deliberately has no list of its own: it only knows how to ask for a
// node type, not which types exist or how they are built. That keeps the set of
// available nodes owned by the node packages, where it belongs.
func (mg *mediaGraphImpl) createGraphNode(runtime core.NodeRuntime, info NodeInfo) (core.Node, error) {
	createFunc, exists := core.LookupNodeFactory(info.NodeType)
	if !exists {
		return nil, fmt.Errorf("unsupported node type: %s", info.NodeType)
	}
	node, err := createFunc(runtime, info)
	if err != nil {
		return nil, fmt.Errorf("failed to create node %s of type %s: %w", info.ID, info.NodeType, err)
	}
	if node == nil {
		return nil, fmt.Errorf("node factory for %q returned nil", info.NodeType)
	}
	bin := node.Bin()
	if bin == nil {
		_ = node.Destroy()
		return nil, fmt.Errorf("node factory for %q returned a node with a nil bin", info.NodeType)
	}
	err = runtime.RegisterGstObj(core.CreateGstObject(info.ID, bin))
	if err != nil {
		_ = node.Destroy()
		return nil, fmt.Errorf("failed to register gst object for node %s: %w", info.ID, err)
	}
	return node, nil
}

// =============================================================================
// Diffing
// =============================================================================

// DiffGraph computes the change set that turns appliedGraph into desiredGraph.
//
// Pure, except that validateGraph consults the global node-spec/factory catalog
// (populated by each node file's init). It never reads MediaGraph state, which
// is what makes a rollback diff (desired -> previous) possible.
func DiffGraph(appliedGraph DesiredGraph, desiredGraph DesiredGraph) (*GraphDiff, error) {
	appliedGraph = normalizeGraphNodeIDs(appliedGraph)
	desiredGraph = normalizeGraphNodeIDs(desiredGraph)
	// Remove vaildation of appliedGraph and desiredGraph because we are already validating them in ApplyGraph

	// if err := validateGraph(appliedGraph); err != nil {
	// 	return nil, fmt.Errorf("invalid applied graph: %w", err)
	// }
	// if err := validateGraph(desiredGraph); err != nil {
	// 	return nil, fmt.Errorf("invalid desired graph: %w", err)
	// }
	srcNodeMap := make(map[string]string)

	nodesToAdd := make([]NodeInfo, 0)
	nodesNeedUpdate := make([]NodeInfo, 0)
	nodesAddPads := make([]NodeInfo, 0)
	nodesRemove := make([]NodeInfo, 0)
	// padsToUpdate collects pads whose selector changed. The decision of what that
	// means belongs to the node, so this list is handed over as-is.
	padsToUpdate := make([]PadInfo, 0)
	padsToRemove := make([]PadInfo, 0)

	currentNodes := make(map[string]NodeInfo)
	// [src/sink_id]node_id
	srcPadMap := make(map[string]string)
	sinkPadMap := make(map[string]string)

	for _, node := range appliedGraph.Nodes {
		srcNodeMap[node.ID] = propertiesJSON(node.Properties)
	}
	// start checking nodes
	for _, currentNode := range desiredGraph.Nodes {
		if srcProperties, exists := srcNodeMap[currentNode.ID]; exists {
			dstProperties := propertiesJSON(currentNode.Properties)

			// core.Node IDs are stable identities; changing a node type is invalid.
			if currentNode.NodeType != appliedGraph.Nodes[currentNode.ID].NodeType {
				return nil, fmt.Errorf("node %q cannot change type from %q to %q", currentNode.ID, appliedGraph.Nodes[currentNode.ID].NodeType, currentNode.NodeType)
			}

			previousNode := appliedGraph.Nodes[currentNode.ID]

			currSourceSet := padIDSet(currentNode.SourcePads)
			prevSourceSet := padIDSet(previousNode.SourcePads)
			currSinkSet := padIDSet(currentNode.SinkPads)
			prevSinkSet := padIDSet(previousNode.SinkPads)

			addedSrcPads, existingSrcPads := padsSplitByKnown(currentNode.SourcePads, prevSourceSet)
			addedSinkPads, existingSinkPads := padsSplitByKnown(currentNode.SinkPads, prevSinkSet)

			if len(addedSrcPads) > 0 || len(addedSinkPads) > 0 {
				nodeAddPads := currentNode
				nodeAddPads.SourcePads = addedSrcPads
				nodeAddPads.SinkPads = addedSinkPads
				nodesAddPads = append(nodesAddPads, nodeAddPads)
			}

			// A pad that kept its ID but changed properties needs its binding
			// re-evaluated; the pad itself and its links survive.
			//
			// Only a type that declares UpdatablePads can honour that, so the
			// check belongs here: a change the node would ignore must be rejected
			// before it reaches the graph, not accepted and then dropped during
			// apply. Rejecting here also keeps the applied graph truthful — an
			// accepted-but-ignored update would make every snapshot echo it back
			// as a fresh change.
			changedPads := append(
				padsChangedProperties(existingSrcPads, previousNode.SourcePads),
				padsChangedProperties(existingSinkPads, previousNode.SinkPads)...,
			)
			if len(changedPads) > 0 {
				spec, hasSpec := core.LookupNodeSpec(currentNode.NodeType)
				if !hasSpec {
					return nil, fmt.Errorf("node type %q has no registered spec", currentNode.NodeType)
				}
				if !spec.UpdatablePads {
					return nil, fmt.Errorf("node %q of type %q cannot update pad properties (pad %q)",
						currentNode.ID, currentNode.NodeType, changedPads[0].ID)
				}
				padsToUpdate = append(padsToUpdate, changedPads...)
			}

			// Asking from the previous side yields the pads that disappeared:
			// they are the ones the current declaration no longer knows.
			removedSrcPads, _ := padsSplitByKnown(previousNode.SourcePads, currSourceSet)
			removedSinkPads, _ := padsSplitByKnown(previousNode.SinkPads, currSinkSet)
			padsToRemove = append(padsToRemove, removedSrcPads...)
			padsToRemove = append(padsToRemove, removedSinkPads...)

			if dstProperties != srcProperties {
				nodesNeedUpdate = append(nodesNeedUpdate, currentNode)
			}
			currentNodes[currentNode.ID] = currentNode
		} else {
			nodesToAdd = append(nodesToAdd, currentNode)
			currentNodes[currentNode.ID] = currentNode
		}
		delete(srcNodeMap, currentNode.ID)
	}

	for _, node := range currentNodes {
		for _, pad := range node.SourcePads {
			srcPadMap[pad.ID] = node.ID
		}
		for _, pad := range node.SinkPads {
			sinkPadMap[pad.ID] = node.ID
		}
	}

	srcLinkMap := make(map[string]LinkInfo)

	addLinkMap := make(map[string]LinkInfo)

	for _, link := range appliedGraph.Links {
		linkID := LinkIDBuilder(link)
		srcLinkMap[linkID] = link
	}

	for _, link := range desiredGraph.Links {
		if _, exists := srcPadMap[link.SourcePadID]; !exists {
			continue
		}
		if _, exists := sinkPadMap[link.SinkPadID]; !exists {
			continue
		}
		linkID := LinkIDBuilder(link)
		if _, exists := srcLinkMap[linkID]; exists {
			delete(srcLinkMap, linkID)
		} else {
			addLinkMap[linkID] = link
		}
	}

	removeLinkMap := srcLinkMap

	for nodeId := range srcNodeMap {
		if _, exists := currentNodes[nodeId]; exists {
			continue
		}
		if _, exists := appliedGraph.Nodes[nodeId]; !exists {
			continue
		}
		nodesRemove = append(nodesRemove, appliedGraph.Nodes[nodeId])
	}

	linksToAdd := make([]LinkInfo, 0, len(addLinkMap))
	for _, link := range addLinkMap {
		linksToAdd = append(linksToAdd, link)
	}

	linksToRemove := make([]LinkInfo, 0, len(removeLinkMap))
	for _, link := range removeLinkMap {
		linksToRemove = append(linksToRemove, link)
	}

	return &GraphDiff{
		AddNodes:              nodesToAdd,
		UpdatePropertiesNodes: nodesNeedUpdate,
		RemoveNodes:           nodesRemove,
		AddPads:               nodesAddPads,
		UpdatePads:            padsToUpdate,
		RemovePads:            padsToRemove,
		AddLinks:              linksToAdd,
		RemoveLinks:           linksToRemove,
	}, nil
}

func normalizeGraphNodeIDs(graph DesiredGraph) DesiredGraph {
	nodes := make(map[string]NodeInfo, len(graph.Nodes))
	for mapID, node := range graph.Nodes {
		if node.ID == "" {
			if mapID != "" {
				node.ID = mapID
			} else {
				node.ID = uuid.NewString()
			}
		}
		// Copy the pad slices before writing to them. NodeInfo holds slice
		// headers, so assigning to an element in place would reach through to
		// the caller's DesiredGraph. DiffGraph must not do that: a caller keeps
		// the previous graph to re-diff against it on rollback, and rewriting
		// that baseline would silently corrupt the rollback target.
		sourcePads := make([]PadInfo, len(node.SourcePads))
		copy(sourcePads, node.SourcePads)
		for index := range sourcePads {
			if sourcePads[index].ID == "" {
				sourcePads[index].ID = canonicalPadID(node, "src", index)
			}
			sourcePads[index].NodeID = node.ID
			sourcePads[index].Direction = core.PadDirectionSrc
		}
		sinkPads := make([]PadInfo, len(node.SinkPads))
		copy(sinkPads, node.SinkPads)
		for index := range sinkPads {
			if sinkPads[index].ID == "" {
				sinkPads[index].ID = canonicalPadID(node, "sink", index)
			}
			sinkPads[index].NodeID = node.ID
			sinkPads[index].Direction = core.PadDirectionSink
		}
		node.SourcePads = sourcePads
		node.SinkPads = sinkPads
		nodes[node.ID] = node
	}
	graph.Nodes = nodes
	// Fill in missing link IDs from the endpoints, and copy the slice so DiffGraph
	// does not write through to the caller. Without the fill, DiffGraph would
	// report a link to remove under an empty key while currentGraphLinks holds
	// the minted one, so the removal would miss and a dead link would survive.
	links := make([]LinkInfo, len(graph.Links))
	copy(links, graph.Links)
	for index := range links {
		if links[index].ID == "" {
			links[index].ID = LinkIDFromEndpoints(links[index].SourcePadID, links[index].SinkPadID)
		}
	}
	graph.Links = links
	return graph
}

func canonicalPadID(node NodeInfo, direction string, index int) string {
	return fmt.Sprintf("%s_%s_%d", node.ID, direction, index)
}

func isCanonicalPadID(nodeID, direction, padID string) bool {
	prefix := nodeID + "_" + direction + "_"
	if !strings.HasPrefix(padID, prefix) {
		return false
	}
	index := strings.TrimPrefix(padID, prefix)
	if index == "" {
		return false
	}
	value, err := strconv.Atoi(index)
	return err == nil && value >= 0
}

// LinkIDBuilder keys a link by its ID and both endpoints. Two links with the
// same endpoints are one link regardless of their (optional) IDs, so the
// endpoint half is what makes the key stable across a frontend echo.
func LinkIDBuilder(link LinkInfo) string {
	return link.ID + "\x00" + link.SourcePadID + "\x00" + link.SinkPadID
}

// LinkIDFromEndpoints mints a link ID from its two pads, so the same wire gets
// the same ID on every apply. The separator is NUL because canonical pad IDs
// contain "_" and a node ID may contain any printable character.
func LinkIDFromEndpoints(sourcePadID, sinkPadID string) string {
	return sourcePadID + "\x00" + sinkPadID
}

// padsSplitByKnown partitions pads into those the known set does not contain
// (new) and those it does (already declared). The known set is a pad-ID set.
func padsSplitByKnown(pads []core.PadInfo, known map[string]struct{}) (newPads []core.PadInfo, knownPads []core.PadInfo) {
	newPads = make([]core.PadInfo, 0, len(pads))
	knownPads = make([]core.PadInfo, 0, len(pads))
	for _, pad := range pads {
		if _, exists := known[pad.ID]; !exists {
			newPads = append(newPads, pad)
		} else {
			knownPads = append(knownPads, pad)
		}
	}
	return newPads, knownPads
}

func padIDSet(pads []core.PadInfo) map[string]struct{} {
	set := make(map[string]struct{}, len(pads))
	for _, pad := range pads {
		set[pad.ID] = struct{}{}
	}
	return set
}

// padsChangedProperties reports the pads in current whose properties differ
// from the pad of the same ID in previous.
//
// Properties are compared as JSON so two maps with the same content compare
// equal regardless of key order or numeric type (an int decoded from a request
// and a float64 decoded from a stored graph describe the same value). Only pads
// present in both lists are considered: a pad that appeared or vanished is an
// addition or a removal, not an update.
func padsChangedProperties(current, previous []core.PadInfo) []core.PadInfo {
	previousProperties := make(map[string]string, len(previous))
	for _, pad := range previous {
		previousProperties[pad.ID] = padPropertiesJSON(pad.Properties)
	}

	changed := make([]core.PadInfo, 0, len(current))
	for _, pad := range current {
		before, exists := previousProperties[pad.ID]
		if !exists {
			continue
		}
		if padPropertiesJSON(pad.Properties) != before {
			changed = append(changed, pad)
		}
	}
	return changed
}

// padPropertiesJSON renders a pad property map as a JSON string for equality
// comparison. An unmarshalable value falls back to its fmt form rather than
// being treated as equal to everything else.
//
// An empty map and a nil map are the same thing here: neither declares any
// property. They must compare equal, or a pad the caller declared with no
// properties would look different from the same pad read back out of the
// registry (which reports an empty map), and every round trip would produce a
// phantom update.
func padPropertiesJSON(properties map[string]any) string {
	if len(properties) == 0 {
		return ""
	}
	data, err := json.Marshal(properties)
	if err != nil {
		return fmt.Sprintf("%v", properties)
	}
	return string(data)
}

// propertiesJSON renders a node property map for equality comparison, with the
// same nil/empty equivalence as padPropertiesJSON.
//
// Without it, a node whose properties were never set compares as null against
// the empty map a snapshot publishes, so DiffGraph reports an update on every
// round trip and ApplyGraph re-applies it forever.
func propertiesJSON(properties map[string]any) string {
	if len(properties) == 0 {
		return ""
	}
	data, err := json.Marshal(properties)
	if err != nil {
		return fmt.Sprintf("%v", properties)
	}
	return string(data)
}

// =============================================================================
// Validation
// =============================================================================

func validateGraph(graph DesiredGraph) error { return ValidateGraph(graph) }

func ValidateGraph(graph DesiredGraph) error {
	srcPads := make(map[string]string)
	sinkPads := make(map[string]string)

	for mapID, node := range graph.Nodes {
		if strings.TrimSpace(mapID) == "" || mapID != node.ID {
			return fmt.Errorf("node map key and node ID must match and be non-empty: %q/%q", mapID, node.ID)
		}
		if !core.IsAllowedNodeType(node.NodeType) {
			return fmt.Errorf("unsupported node type %q", node.NodeType)
		}
		spec, hasSpec := core.LookupNodeSpec(node.NodeType)
		if !hasSpec {
			return fmt.Errorf("node type %q has no registered spec", node.NodeType)
		}
		if !spec.Sink.Allows(len(node.SinkPads)) {
			return fmt.Errorf("node %q of type %q declares %d sink pads, spec allows at most %d",
				node.ID, node.NodeType, len(node.SinkPads), spec.Sink)
		}
		if !spec.Src.Allows(len(node.SourcePads)) {
			return fmt.Errorf("node %q of type %q declares %d source pads, spec allows at most %d",
				node.ID, node.NodeType, len(node.SourcePads), spec.Src)
		}
		for _, padInfo := range node.SourcePads {
			padId := padInfo.ID

			if strings.TrimSpace(padId) == "" {
				return fmt.Errorf("node %q has an empty source pad ID", node.ID)
			}
			if !isCanonicalPadID(node.ID, "src", padId) {
				return fmt.Errorf("invalid source pad ID %q for node %q", padId, node.ID)
			}
			if _, exists := srcPads[padId]; exists {
				return fmt.Errorf("duplicate source pad ID: %s", padId)
			}
			if _, exists := sinkPads[padId]; exists {
				return fmt.Errorf("pad ID used as both source and sink: %s", padId)
			}
			srcPads[padId] = node.ID
		}
		for _, padInfo := range node.SinkPads {
			padId := padInfo.ID
			if strings.TrimSpace(padId) == "" {
				return fmt.Errorf("node %q has an empty sink pad ID", node.ID)
			}
			if !isCanonicalPadID(node.ID, "sink", padId) {
				return fmt.Errorf("invalid sink pad ID %q for node %q", padId, node.ID)
			}
			if _, exists := sinkPads[padId]; exists {
				return fmt.Errorf("duplicate sink pad ID: %s", padId)
			}
			if _, exists := srcPads[padId]; exists {
				return fmt.Errorf("pad ID used as both source and sink: %s", padId)
			}
			sinkPads[padId] = node.ID
		}
	}

	linkIDs := make(map[string]struct{}, len(graph.Links))
	endpointIDs := make(map[string]struct{}, len(graph.Links))
	sourceLinkIDs := make(map[string]struct{}, len(graph.Links))
	sinkLinkIDs := make(map[string]struct{}, len(graph.Links))
	for _, link := range graph.Links {
		if _, exists := linkIDs[link.ID]; exists {
			if link.ID != "" {
				return fmt.Errorf("duplicate link ID: %s", link.ID)
			}
		} else if link.ID != "" {
			linkIDs[link.ID] = struct{}{}
		}
		srcNode, exists := srcPads[link.SourcePadID]
		if !exists {
			return fmt.Errorf("link %q references unknown source pad %q", link.ID, link.SourcePadID)
		}
		dstNode, exists := sinkPads[link.SinkPadID]
		if !exists {
			return fmt.Errorf("link %q references unknown sink pad %q", link.ID, link.SinkPadID)
		}
		if srcNode == dstNode {
			return fmt.Errorf("link %q cannot connect a node to itself", link.ID)
		}
		if _, exists := sourceLinkIDs[link.SourcePadID]; exists {
			return fmt.Errorf("source pad %q may only have one outgoing link", link.SourcePadID)
		}
		sourceLinkIDs[link.SourcePadID] = struct{}{}
		if _, exists := sinkLinkIDs[link.SinkPadID]; exists {
			return fmt.Errorf("sink pad %q may only have one incoming link", link.SinkPadID)
		}
		sinkLinkIDs[link.SinkPadID] = struct{}{}
		endpointID := link.SourcePadID + "\x00" + link.SinkPadID
		if _, exists := endpointIDs[endpointID]; exists {
			return fmt.Errorf("duplicate link endpoints: %s -> %s", link.SourcePadID, link.SinkPadID)
		}
		endpointIDs[endpointID] = struct{}{}
	}
	return nil
}

// =============================================================================
// Applying
// =============================================================================

// ApplyGraph applies a desired graph to the live pipeline.
//
// It diffs against the last applied graph, so a caller sends intent rather than
// an operation list. Not atomic: everything checkable without GStreamer runs
// first (validateGraph, ValidateNode, preflightLinks), and a later failure rolls
// back to the entry state. The rollback is best-effort; if it also fails, both
// errors are returned and the caller should rebuild the graph.
//
// The apply runs on the event loop, so applies are ordered rather than racing.
func (mg *mediaGraphImpl) ApplyGraph(desiredGraph DesiredGraph) error {
	return mg.request(MediaGraphEventTypeApplyGraph, &applyRequest{Graph: desiredGraph})
}

// ApplyGraphIfVersion applies desiredGraph only if the caller's view is still
// current, and is how an optimistic client submits an edit.
//
// baseVersion is the Version() the caller last saw. The comparison happens inside
// the event loop's critical section, so a stale apply cannot slip in between the
// check and the write; that check-then-act atomicity is the whole reason the loop
// exists. A mismatch returns ErrVersionMismatch and changes nothing, and the
// caller should re-read the graph and re-submit.
func (mg *mediaGraphImpl) ApplyGraphIfVersion(desiredGraph DesiredGraph, baseVersion uint64) error {
	return mg.request(MediaGraphEventTypeApplyGraph, &applyRequest{Graph: desiredGraph, BaseVersion: &baseVersion})
}

// applyRequest is the apply event's payload. BaseVersion is optional: nil means
// an unconditional apply, which is what an internal caller with no stale-state
// risk (a rollback, a test) wants.
type applyRequest struct {
	Graph       DesiredGraph
	BaseVersion *uint64
}

// applyGraphLocked is the body of an apply. The loop calls it while holding
// mg.mu, so it must not take the lock itself.
//
// baseVersion is checked here rather than in the caller so the compare and the
// write are one critical section; checking outside the loop would leave a window
// where another apply lands between the check and this call.
func (mg *mediaGraphImpl) applyGraphLocked(desiredGraph DesiredGraph, baseVersion *uint64) error {
	if baseVersion != nil && *baseVersion != mg.appliedVersion {
		return fmt.Errorf("%w: base version %d, current %d", ErrVersionMismatch, *baseVersion, mg.appliedVersion)
	}

	if desiredGraph.ID != mg.id {
		return errors.Join(core.ErrInvalidConfig, fmt.Errorf("graph ID mismatch: %q != %q", desiredGraph.ID, mg.id))
	}

	if err := ValidateGraph(desiredGraph); err != nil {
		return errors.Join(core.ErrInvalidConfig, fmt.Errorf("graph validation failed: %w", err))
	}

	diff, err := DiffGraph(mg.applied, desiredGraph)
	if err != nil {
		return errors.Join(core.ErrFailed, fmt.Errorf("diff failed: %w", err))
	}
	// A diff that changes nothing must not move appliedVersion. DiffGraph always
	// returns a diff, so emptiness is asked of the diff rather than tested for nil.
	if diff.IsEmpty() {
		return nil
	}

	before := CopyGraph(mg.applied)
	if err := mg.applyGstGraph(diff, false); err != nil {
		// Invert the diff to undo it. The inverse comes from the intent, not a
		// live snapshot: a snapshot carries runtime-discovered pads the applied
		// graph never declared, so it would report every one of them as a removal.
		// Apply the inverse tolerantly, since the forward pass may have stopped
		// before reaching some of the operations it names.
		rollback, diffErr := DiffGraph(desiredGraph, before)
		if diffErr != nil {
			return errors.Join(err, fmt.Errorf("rollback diff: %w", diffErr))
		}
		if rollbackErr := mg.applyGstGraph(rollback, true); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return err
	}
	// Store a canonical copy. Normalizing first means the applied graph carries
	// the same link IDs and pad ownership the apply path recorded, so it stays a
	// truthful description of the state the diff baseline names.
	mg.applied = CopyGraph(normalizeGraphNodeIDs(desiredGraph))
	mg.appliedVersion++
	mg.dirty = false
	return nil
}

// applyGstGraph reconciles the live pipeline to the state the diff describes.
//
// The caller MUST hold mg.mu for writing; the lock is not taken here because
// ApplyGraph holds it across the forward apply and the rollback.
//
// tolerant changes what a missing target means. The forward pass is strict: a
// missing pad or a failed unlink means applied and runtime disagree. The
// rollback is tolerant, because the inverse diff was computed against the fully
// applied intent but runs against a partly changed graph, so "already gone" and
// "never linked" are expected rather than failures.
func (mg *mediaGraphImpl) applyGstGraph(diff *GraphDiff, tolerant bool) error {
	// Teardown first, then build: unlink -> remove pads -> remove nodes ->
	// add sink pads -> update nodes -> add src pads -> preflight -> link.
	// Removing before adding frees pad names (source_src_0) for reuse.
	var blockProbes []uint32
	var blockProbePads []gst.Pad

	defer func() {
		for i, pad := range blockProbePads {
			pad.RemoveProbe(blockProbes[i])
		}
	}()

	for _, link := range diff.RemoveLinks {
		fromPad, ok := mg.gstObjectRegistry.GetPadSafe(link.SourcePadID)
		if fromPad == nil || !ok {
			if tolerant {
				continue
			}
			return fmt.Errorf("failed to get pad: %s", link.SourcePadID)
		}
		toPad, ok := mg.gstObjectRegistry.GetPadSafe(link.SinkPadID)
		if toPad == nil || !ok {
			if tolerant {
				continue
			}
			return fmt.Errorf("failed to get pad: %s", link.SinkPadID)
		}
		id := fromPad.AddProbe(gst.PadProbeType(gst.PadProbeTypeBlockDownstream), func(pad gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			return gst.PadProbeOK
		})
		blockProbes = append(blockProbes, id)
		blockProbePads = append(blockProbePads, fromPad)
		if !fromPad.Unlink(toPad) {
			// Unlink is false when the pads are not linked. On a rollback that
			// means the forward apply never got to this link, so there is nothing
			// to undo and the rest of the rollback must still run.
			if tolerant {
				continue
			}
			return fmt.Errorf("failed to unlink pads: %s -> %s", link.SourcePadID, link.SinkPadID)
		}
	}

	// Retire pads only after their links are gone: the unlink loop above returns
	// an error when a pad it expects is missing, so the pad has to outlive its
	// last link.
	for _, pad := range diff.RemovePads {
		existingNode, exists := mg.currentGraphNodes[pad.NodeID]
		if !exists {
			if tolerant {
				continue
			}
			return fmt.Errorf("node with ID %s does not exist for pad removal", pad.NodeID)
		}
		var err error
		switch pad.Direction {
		case core.PadDirectionSrc:
			err = RemoveSrcPads(existingNode, []core.PadInfo{pad})
		case core.PadDirectionSink:
			err = RemoveSinkPads(existingNode, []core.PadInfo{pad})
		default:
			err = fmt.Errorf("%w: pad %q has an unknown direction", core.ErrPadCantBeUsed, pad.ID)
		}
		if err != nil {
			if tolerant {
				continue
			}
			return fmt.Errorf("failed to remove pad %s from node %s: %w", pad.ID, pad.NodeID, err)
		}
		_ = mg.nodeRuntime.UnregisterGstObj(core.GstObjectTypePad, pad.ID)
	}

	for _, info := range diff.RemoveNodes {
		node, exists := mg.currentGraphNodes[info.ID]
		if !exists {
			// A node already in the target state, which a rollback re-add path can
			// legitimately hit. Only an absent node is an error.
			if tolerant {
				continue
			}
			return fmt.Errorf("node with ID %s does not exist for removal", info.ID)
		}
		mg.pipeline.Remove(node.Bin())
		mg.gstObjectRegistry.Unregister(core.GstObjectTypeBin, node.ID())
		node.Destroy()
		delete(mg.currentGraphNodes, node.ID())
	}

	for _, node := range diff.AddPads {
		existingNode, exists := mg.currentGraphNodes[node.ID]
		if !exists {
			if tolerant {
				continue
			}
			return fmt.Errorf("node with ID %s does not exist for pad addition", node.ID)
		}
		// Source pads first, then sink pads. The two directions come from
		// different node capabilities, so a node that grew both runs both here
		// rather than appearing in two lists.
		if len(node.SourcePads) > 0 {
			pads, err := CreateSrcPads(existingNode, node.SourcePads)
			if err != nil {
				if tolerant {
					continue
				}
				return fmt.Errorf("failed to create source pads for node %s: %w", node.ID, err)
			}
			if err := mg.registerGstObjs(pads); err != nil {
				if tolerant {
					continue
				}
				return fmt.Errorf("failed to register source pads for node %s: %w", node.ID, err)
			}
		}
		if len(node.SinkPads) > 0 {
			pads, err := CreateSinkPads(existingNode, node.SinkPads)
			if err != nil {
				if tolerant {
					continue
				}
				return fmt.Errorf("failed to create sink pads for node %s: %w", node.ID, err)
			}
			if err := mg.registerGstObjs(pads); err != nil {
				if tolerant {
					continue
				}
				return fmt.Errorf("failed to register sink pads for node %s: %w", node.ID, err)
			}
		}
	}

	// Pads are updated after they exist and before node properties: a pad update
	// only re-evaluates which track a pad carries, so it needs the pad in place
	// but nothing else.
	for _, pad := range diff.UpdatePads {
		existingNode, exists := mg.currentGraphNodes[pad.NodeID]
		if !exists {
			if tolerant {
				continue
			}
			return fmt.Errorf("node with ID %s does not exist for pad update", pad.NodeID)
		}
		// DiffGraph already rejected an update on a type that cannot honour it, so
		// a node that does not implement PadUpdater here means the declaration and
		// the implementation disagree. The node owns its pads, so it records the
		// new properties itself; the graph must not write to the registry behind
		// the node's back, or the two would drift apart.
		updater, ok := existingNode.(core.PadUpdater)
		if !ok {
			if tolerant {
				continue
			}
			return fmt.Errorf("%w: node %q of type %q declares updatable pads but does not implement PadUpdater",
				core.ErrPadCantBeUsed, pad.NodeID, existingNode.NodeType())
		}
		if err := updater.UpdatePad(pad); err != nil {
			if tolerant {
				continue
			}
			return fmt.Errorf("failed to update pad %s on node %s: %w", pad.ID, pad.NodeID, err)
		}
	}

	for _, node := range diff.UpdatePropertiesNodes {
		existingNode, exists := mg.currentGraphNodes[node.ID]
		if !exists {
			if tolerant {
				continue
			}
			return fmt.Errorf("node with ID %s does not exist for update", node.ID)
		}
		err := existingNode.UpdateProperties(node.Properties)
		if err != nil {
			if tolerant {
				continue
			}
			return fmt.Errorf("failed to update properties for node %s: %v", node.ID, err)
		}
	}

	for i := range diff.AddNodes {
		node := &diff.AddNodes[i]
		if node.ID == "" {
			node.ID = uuid.NewString()
		}
		if tolerant {
			// A rollback re-adds what the forward apply removed. If the forward
			// apply stopped before removing it, the node is still live and there is
			// nothing to rebuild.
			if _, exists := mg.currentGraphNodes[node.ID]; exists {
				continue
			}
		}
		newNode, err := mg.createGraphNode(mg.nodeRuntime, *node)
		if err != nil {
			return err
		}
		// Every step below can fail after the node has already taken resources
		// (its bin, its registry entry, the pads registered in the object
		// registry), so a failure unwinds them here. The rollback cannot do it:
		// it is computed from mg.applied, and a half-built node never reached
		// currentGraphNodes, so the diff has no idea it exists.
		if err := mg.buildNode(newNode, *node); err != nil {
			mg.discardNode(newNode)
			return err
		}
		mg.currentGraphNodes[node.ID] = newNode
	}

	// Preflight every link before creating any of them. This is the last point a
	// bad connection can be rejected for free: all pads exist and rewired pads
	// are already unlinked, so CanLink's "linked means false" rule does not fire
	// on a legitimate rewire. It also means a caps failure leaves zero new links.
	if err := mg.preflightLinks(diff.AddLinks); err != nil {
		return err
	}

	for i := range diff.AddLinks {
		link := &diff.AddLinks[i]
		if link.ID == "" {
			link.ID = LinkIDFromEndpoints(link.SourcePadID, link.SinkPadID)
		}
		fromPad, ok := mg.gstObjectRegistry.GetPadSafe(link.SourcePadID)
		if fromPad == nil || !ok {
			return fmt.Errorf("failed to get pad: %s", link.SourcePadID)
		}
		toPad, ok := mg.gstObjectRegistry.GetPadSafe(link.SinkPadID)
		if toPad == nil || !ok {
			return fmt.Errorf("failed to get pad: %s", link.SinkPadID)
		}
		if result := fromPad.Link(toPad); result != gst.PadLinkOK {
			return fmt.Errorf("failed to link pads %s -> %s: %v", link.SourcePadID, link.SinkPadID, result)
		}
	}

	currentLinks := make(map[string]LinkInfo, 0)
	for _, link := range diff.AddLinks {
		currentLinks[link.ID] = link
	}
	for _, link := range mg.currentGraphLinks {
		currentLinks[link.ID] = link
	}
	for _, link := range diff.RemoveLinks {
		delete(currentLinks, link.ID)
	}
	mg.currentGraphLinks = make([]LinkInfo, 0, len(currentLinks))
	for _, link := range currentLinks {
		mg.currentGraphLinks = append(mg.currentGraphLinks, link)
	}

	currentNodes := make(map[string]core.Node, 0)
	for _, node := range diff.AddNodes {
		currentNodes[node.ID] = mg.currentGraphNodes[node.ID]
	}
	for _, node := range mg.currentGraphNodes {
		currentNodes[node.ID()] = node
	}
	for _, node := range diff.RemoveNodes {
		delete(currentNodes, node.ID)
	}
	mg.currentGraphNodes = currentNodes
	return nil
}

// buildNode validates a freshly constructed node, materialises its declared
// pads, registers them, and attaches its bin to the pipeline.
//
// Split out of the apply loop so the caller can unwind the whole thing on any
// failure; on error the node has already taken some resources, so discardNode
// must run.
func (mg *mediaGraphImpl) buildNode(node core.Node, info NodeInfo) error {
	if err := core.ValidateNode(node, len(info.SourcePads), len(info.SinkPads)); err != nil {
		return fmt.Errorf("invalid node %s: %w", info.ID, err)
	}
	srcPads, err := CreateSrcPads(node, info.SourcePads)
	if err != nil {
		return fmt.Errorf("failed to create source pads for node %s: %w", info.ID, err)
	}
	sinkPads, err := CreateSinkPads(node, info.SinkPads)
	if err != nil {
		return fmt.Errorf("failed to create sink pads for node %s: %w", info.ID, err)
	}
	if err := mg.registerGstObjs(srcPads); err != nil {
		return fmt.Errorf("failed to register source pads for node %s: %w", info.ID, err)
	}
	if err := mg.registerGstObjs(sinkPads); err != nil {
		return fmt.Errorf("failed to register sink pads for node %s: %w", info.ID, err)
	}
	if !mg.pipeline.Add(node.Bin()) {
		return fmt.Errorf("failed to add node %s", info.ID)
	}
	if !node.Bin().SyncStateWithParent() {
		return fmt.Errorf("failed to sync node %s", info.ID)
	}
	return nil
}

// discardNode unwinds a node that never became part of the graph: its pads leave
// the object registry, its bin leaves the pipeline, and Destroy releases its
// registry entry and request pads. Without this a failed apply leaks all three,
// and the leak is invisible to the rollback diff.
//
// Removal is best-effort and its errors are deliberately dropped: this runs on a
// failure path, so the caller's original error is the one worth reporting.
func (mg *mediaGraphImpl) discardNode(node core.Node) {
	for _, padID := range node.GetSrcIDs() {
		_ = mg.nodeRuntime.UnregisterGstObj(core.GstObjectTypePad, padID)
	}
	for _, padID := range node.GetDstIDs() {
		_ = mg.nodeRuntime.UnregisterGstObj(core.GstObjectTypePad, padID)
	}
	mg.pipeline.Remove(node.Bin())
	_ = node.Destroy()
}

// preflightLinks checks every link's caps before any of them is created, so a
// rejected connection leaves no new link behind.
//
// Must run after the unlinks: gst_pad_can_link is false whenever either pad is
// already linked, so a rewire would be wrongly rejected while the old link still
// exists.
func (mg *mediaGraphImpl) preflightLinks(links []LinkInfo) error {
	for i := range links {
		link := &links[i]
		if link.ID == "" {
			link.ID = LinkIDFromEndpoints(link.SourcePadID, link.SinkPadID)
		}
		fromPad, ok := mg.gstObjectRegistry.GetPadSafe(link.SourcePadID)
		if fromPad == nil || !ok {
			return fmt.Errorf("failed to get pad: %s", link.SourcePadID)
		}
		toPad, ok := mg.gstObjectRegistry.GetPadSafe(link.SinkPadID)
		if toPad == nil || !ok {
			return fmt.Errorf("failed to get pad: %s", link.SinkPadID)
		}
		// CanLinkPads is the exact predicate Link uses internally.
		if !core.CanLinkPads(fromPad, toPad) {
			return fmt.Errorf("incompatible link %s (%s) -> %s (%s)",
				link.SourcePadID, core.PadCapsString(fromPad),
				link.SinkPadID, core.PadCapsString(toPad))
		}
	}
	return nil
}

// CreateSrcPads materialises the node's source pads for the requested IDs.
// Pads come from the arity interface, not from bin lookup: a bin's ghost pads
// are not reachable through GetByName (it only walks child elements).
func CreateSrcPads(node core.Node, srcPads []core.PadInfo) ([]core.GstObject, error) {
	var newPads []core.GstObject
	for _, srcInfo := range srcPads {
		switch typedNode := node.(type) {
		case core.OneToManyNode:
			newPad, generatedID, err := typedNode.RequestSrcPad(&srcInfo)
			if err != nil {
				return nil, err
			}
			if generatedID != srcInfo.ID {
				return nil, fmt.Errorf("source pad ID %q does not match expected ID %q", generatedID, srcInfo.ID)
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, srcInfo.Properties))
		case core.OneToOneNode:
			newPad, generatedID, err := typedNode.GetSrcPad()
			if err != nil {
				return nil, err
			}
			if generatedID != srcInfo.ID {
				return nil, fmt.Errorf("source pad ID %q does not match expected ID %q", generatedID, srcInfo.ID)
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, srcInfo.Properties))
		case core.ManyToOneNode:
			newPad, generatedID, err := typedNode.GetSrcPad()
			if err != nil {
				return nil, err
			}
			if generatedID != srcInfo.ID {
				return nil, fmt.Errorf("source pad ID %q does not match expected ID %q", generatedID, srcInfo.ID)
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, srcInfo.Properties))
		case core.ZeroToOneNode:
			newPad, generatedID, err := typedNode.GetSrcPad()
			if err != nil {
				return nil, err
			}
			if generatedID != srcInfo.ID {
				return nil, fmt.Errorf("source pad ID %q does not match expected ID %q", generatedID, srcInfo.ID)
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, srcInfo.Properties))
		case core.OneToZeroNode:
			// Direction is validated up front by core.ValidateNode; reaching here
			// means the node's arity and its declaration disagree.
			return nil, fmt.Errorf("%w: node %q has no source pad", core.ErrGSTPadNotFound, node.ID())
		case core.ManyToZeroNode:
			return nil, fmt.Errorf("%w: node %q has no source pad", core.ErrGSTPadNotFound, node.ID())
		default:
			return nil, fmt.Errorf("%w: node %q", core.ErrPadCantBeUsed, node.ID())
		}
	}
	return newPads, nil
}

func CreateSinkPads(node core.Node, sinkPads []core.PadInfo) ([]core.GstObject, error) {
	var newPads []core.GstObject
	for _, sinkInfo := range sinkPads {
		sinkId := sinkInfo.ID
		// Most specific arity first; see CreateSrcPads for why the order matters.
		switch typedNode := node.(type) {
		case core.ManyToOneNode:
			newPad, generatedID, err := typedNode.RequestSinkPad()
			if err != nil {
				return nil, err
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, sinkInfo.Properties))
			if generatedID != sinkId {
				return nil, fmt.Errorf("generated sink pad ID %q does not match expected ID %q", generatedID, sinkId)
			}
		case core.ManyToZeroNode:
			// After ManyToOneNode on purpose: a node exposing both arities has a
			// source pad, so it is the more capable shape and wins.
			newPad, generatedID, err := typedNode.RequestSinkPad()
			if err != nil {
				return nil, err
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, sinkInfo.Properties))
			if generatedID != sinkId {
				return nil, fmt.Errorf("generated sink pad ID %q does not match expected ID %q", generatedID, sinkId)
			}
		case core.OneToManyNode:
			newPad, generatedID, err := typedNode.GetSinkPad()
			if err != nil {
				return nil, err
			}
			if generatedID != sinkId {
				return nil, fmt.Errorf("sink pad ID %q does not match expected ID %q", generatedID, sinkId)
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, sinkInfo.Properties))
		case core.OneToOneNode:
			newPad, generatedID, err := typedNode.GetSinkPad()
			if err != nil {
				return nil, err
			}
			if generatedID != sinkId {
				return nil, fmt.Errorf("sink pad ID %q does not match expected ID %q", generatedID, sinkId)
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, sinkInfo.Properties))
		case core.OneToZeroNode:
			newPad, generatedID, err := typedNode.GetSinkPad()
			if err != nil {
				return nil, err
			}
			if generatedID != sinkId {
				return nil, fmt.Errorf("sink pad ID %q does not match expected ID %q", generatedID, sinkId)
			}
			newPads = append(newPads, core.CreateGstObjectWithProperties(generatedID, newPad, sinkInfo.Properties))
		case core.ZeroToOneNode:
			// A source-only node has no sink pad, so this request is invalid.
			return nil, fmt.Errorf("%w: node %q has no sink pad", core.ErrGSTPadNotFound, node.ID())
		default:
			return nil, fmt.Errorf("%w: node %q", core.ErrPadCantBeUsed, node.ID())
		}
	}

	return newPads, nil
}

// RemoveSrcPads retires the named source pads from an existing node and drops
// their registrations, so a link can no longer resolve them.
func RemoveSrcPads(node core.Node, srcPads []core.PadInfo) error {
	if len(srcPads) == 0 {
		return nil
	}

	switch typedNode := node.(type) {
	case core.OneToManyNode:
		for _, pad := range srcPads {
			if err := typedNode.RemoveSrcPad(pad.ID); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: node %q has no removable source pads", core.ErrPadCantBeUsed, node.ID())
	}
	return nil
}

// RemoveSinkPads retires the named sink pads from an existing node and drops
// their registrations.
func RemoveSinkPads(node core.Node, sinkPads []core.PadInfo) error {
	if len(sinkPads) == 0 {
		return nil
	}
	remover, ok := node.(sinkPadRemover)
	if !ok {
		return fmt.Errorf("%w: node %q has no removable sink pads", core.ErrPadCantBeUsed, node.ID())
	}
	for _, pad := range sinkPads {
		if err := remover.RemoveSinkPad(pad.ID); err != nil {
			return err
		}
	}
	return nil
}

// =============================================================================
// Observation
// =============================================================================

// Snapshot observes the live graph as a DesiredGraph.
//
// It reports the runtime, not the intent: dynamic pads exist only in the built
// graph, so pad lists come from the nodes. Returning DesiredGraph lets a
// snapshot be fed back to DiffGraph, giving DiffGraph(snapshot, snapshot) ==
// empty. Node properties come from EditableProperties (protected keys omitted,
// so an echo is not an update); pad properties come from the registry, which
// recorded them at creation, because a rollback needs them to rebuild a pad.
//
// The read lock makes the snapshot one consistent observation.
func (mg *mediaGraphImpl) Snapshot() DesiredGraph {
	mg.mu.RLock()
	defer mg.mu.RUnlock()

	nodes := make(map[string]NodeInfo, len(mg.currentGraphNodes))
	for id, node := range mg.currentGraphNodes {
		nodes[id] = NodeInfo{
			ID:         id,
			NodeType:   node.NodeType(),
			Properties: node.EditableProperties(),
			SourcePads: mg.snapshotPads(id, core.PadDirectionSrc, node.GetSrcIDs()),
			SinkPads:   mg.snapshotPads(id, core.PadDirectionSink, node.GetDstIDs()),
		}
	}
	links := make([]LinkInfo, len(mg.currentGraphLinks))
	copy(links, mg.currentGraphLinks)
	return DesiredGraph{
		ID:    mg.id,
		Nodes: nodes,
		Links: links,
	}
}

// snapshotPads turns a node's pad IDs into declarations with the properties
// each pad was registered with. A pad the registry does not know is reported
// with no properties rather than dropped: the node says it exists.
func (mg *mediaGraphImpl) snapshotPads(nodeID string, direction core.PadDirection, padIDs []string) []PadInfo {
	if len(padIDs) == 0 {
		return nil
	}
	pads := make([]PadInfo, 0, len(padIDs))
	for _, padID := range padIDs {
		properties := map[string]any{}
		if obj, ok := mg.gstObjectRegistry.GetSafe(core.GstObjectTypePad, padID); ok && obj != nil {
			for key, value := range obj.GetProperties() {
				properties[key] = value
			}
		}
		pads = append(pads, PadInfo{
			ID:         padID,
			NodeID:     nodeID,
			Direction:  direction,
			Properties: properties,
		})
	}
	return pads
}

// PadCaps reports the caps a live pad can carry, as a GStreamer caps string.
//
// Media types are not declared anywhere, because only negotiation knows them, so
// the pad is asked on demand (QueryCaps proxies through a ghost pad to its
// target). An unknown pad ID is an error, not an empty string.
func (mg *mediaGraphImpl) PadCaps(padID string) (string, error) {
	pad, ok := mg.gstObjectRegistry.GetPadSafe(padID)
	if pad == nil || !ok {
		return "", fmt.Errorf("%w: pad %q", core.ErrGSTPadNotFound, padID)
	}
	return core.PadCapsString(pad), nil
}

// CanLink reports whether two live pads could be linked right now, without
// linking them. It is the same predicate Link uses internally, so it answers the
// frontend's "can I draw this wire?" question without mutating the graph.
func (mg *mediaGraphImpl) CanLink(sourcePadID, sinkPadID string) (bool, error) {
	fromPad, ok := mg.gstObjectRegistry.GetPadSafe(sourcePadID)
	if fromPad == nil || !ok {
		return false, fmt.Errorf("%w: pad %q", core.ErrGSTPadNotFound, sourcePadID)
	}
	toPad, ok := mg.gstObjectRegistry.GetPadSafe(sinkPadID)
	if toPad == nil || !ok {
		return false, fmt.Errorf("%w: pad %q", core.ErrGSTPadNotFound, sinkPadID)
	}
	return core.CanLinkPads(fromPad, toPad), nil
}

func (mg *mediaGraphImpl) Version() uint64 {
	mg.mu.RLock()
	defer mg.mu.RUnlock()
	return mg.appliedVersion
}

// GetState returns the last pipeline state reported by the bus listener. It is
// deliberately not implemented with pipeline.GetState: that call can block
// while GStreamer completes an asynchronous state transition.
func (mg *mediaGraphImpl) GetState() (gst.State, gst.State, error) {
	select {
	case <-mg.loopCtx.Done():
		return gst.State(gst.StateNull), gst.State(gst.StateVoidPending), ErrGraphDestroyed
	default:
	}
	mg.stateMu.RLock()
	defer mg.stateMu.RUnlock()
	return mg.currentState, mg.pendingState, nil
}

// GetAppliedGraph returns the last successfully applied graph.
//
// It returns a copy. Handing out the internal value would let a caller rewrite
// the diff baseline through it, which is the failure CopyGraph exists to stop.
func (mg *mediaGraphImpl) GetAppliedGraph() DesiredGraph {
	mg.mu.RLock()
	defer mg.mu.RUnlock()
	return CopyGraph(mg.applied)
}

// =============================================================================
// Copying
// =============================================================================

// CopyGraph deep-copies a DesiredGraph, including every nested map.
//
// An applied graph is a diff baseline the caller must not reach into: a shared
// Properties map would let a later request change the baseline without moving
// appliedVersion, so the version would stop describing the state it names.
func CopyGraph(graph DesiredGraph) DesiredGraph {
	copied := DesiredGraph{
		ID:         graph.ID,
		Nodes:      make(map[string]NodeInfo, len(graph.Nodes)),
		Links:      make([]LinkInfo, len(graph.Links)),
		Properties: copyProperties(graph.Properties),
	}
	for id, node := range graph.Nodes {
		node.Properties = copyProperties(node.Properties)
		node.SourcePads = copyPads(node.SourcePads)
		node.SinkPads = copyPads(node.SinkPads)
		copied.Nodes[id] = node
	}
	copy(copied.Links, graph.Links)
	return copied
}

// copyPads copies a pad list and the properties map each pad carries. LinkInfo
// and PadInfo hold only scalars apart from that map, so one level is the whole
// depth.
func copyPads(pads []PadInfo) []PadInfo {
	if pads == nil {
		return nil
	}
	copied := make([]PadInfo, len(pads))
	copy(copied, pads)
	for index := range copied {
		copied[index].Properties = copyProperties(copied[index].Properties)
	}
	return copied
}

func copyProperties(properties map[string]any) map[string]any {
	if properties == nil {
		return nil
	}
	copied := make(map[string]any, len(properties))
	for key, value := range properties {
		copied[key] = value
	}
	return copied
}

// =============================================================================
// Lifecycle
// =============================================================================

// SetState changes the pipeline state. It goes through the loop so a state change
// cannot interleave with a reconcile.
func (mg *mediaGraphImpl) SetState(state gst.State) error {
	return mg.request(MediaGraphEventTypeSetState, &state)
}

// RegisterBusEventsChan subscribes to runtime messages emitted by the graph's
// pipeline. Bus events are observational: they do not mutate the desired graph
// or participate in reconciliation. The graph owns the subscription lifecycle
// and closes registered channels during Destroy.
func (mg *mediaGraphImpl) RegisterBusEventsChan() (<-chan BusEvent, error) {
	select {
	case <-mg.loopCtx.Done():
		return nil, ErrGraphDestroyed
	default:
	}
	busEvents, err := mg.bus.RegisterBusEventsChan()
	if err != nil {
		select {
		case <-mg.loopCtx.Done():
			return nil, ErrGraphDestroyed
		default:
		}
	}
	return busEvents, err
}

// Destroy tears the graph down and stops the main loop.
//
// It is the loop's terminator: the teardown runs as the loop's last message, so
// it cannot interleave with an in-flight apply. The loop stops itself once the
// reply has been delivered, so a later call returns ErrGraphDestroyed.
func (mg *mediaGraphImpl) Destroy() error {
	return mg.request(MediaGraphEventTypeDestroy, nil)
}

// destroyLocked runs the teardown. The loop calls it while holding mg.mu.
func (mg *mediaGraphImpl) destroyLocked() error {
	mg.bus.Stop()
	mg.pipeline.SetState(gst.State(gst.StateNull))
	var errs []error
	for _, node := range mg.currentGraphNodes {
		if err := node.Destroy(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := mg.chanRegistry.Clear(); err != nil {
		errs = append(errs, err)
	}
	mg.gstObjectRegistry.Clear()
	mg.currentGraphNodes = make(map[string]core.Node)
	mg.currentGraphLinks = make([]LinkInfo, 0)
	mg.nodeChanEntries = make(map[string]core.RegistryEntry)
	mg.pipeline = nil
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// =============================================================================
// Data access
// =============================================================================

// GetEntryByEntryID looks up the registry entry for an entry ID.
//
// Entry IDs are node IDs, so this is also how a caller reaches a node's channel.
// The entry is returned untyped because a method cannot carry its own type
// parameter, so the assertion lives at the call site:
//
//	entry, err := mg.GetEntryByEntryID("source_1")
//	inputEntry, ok := entry.(core.InputRegistryEntry)
//	handle, err := core.GetInputHandle[[]byte](inputEntry)
func (mg *mediaGraphImpl) GetEntryByEntryID(id string) (core.RegistryEntry, error) {
	entry, ok := mg.nodeRuntime.GetEntryById(id)
	if !ok {
		return nil, fmt.Errorf("%w: entry with ID %q not found", core.ErrInvalidConfig, id)
	}
	return entry, nil
}

// TrackEvents returns the graph's normalized discovery/error stream.
// Consumers own the transport wrapper and must keep reading until the graph
// context ends; the channel is intentionally not a track-state cache.
func (mg *mediaGraphImpl) RuntimeEvents() <-chan core.RuntimeEvent {
	return mg.runtimeEventChan
}

// TrackEvents is kept as a compatibility name for the graph-to-controller
// stream. The channel now contains normalized RuntimeEvent values, not only
// discovery events.
func (mg *mediaGraphImpl) TrackEvents() <-chan core.RuntimeEvent {
	return mg.runtimeEventChan
}

// MarkDirty records that runtime observation may no longer match the applied
// intent. It is internal graph state: it does not publish an event and does
// not advance the frontend intent version.
func (mg *mediaGraphImpl) MarkDirty() {
	mg.mu.Lock()
	mg.dirty = true
	mg.mu.Unlock()
}

// IsDirty reports whether runtime observation has been marked divergent from
// the last applied intent.
func (mg *mediaGraphImpl) IsDirty() bool {
	mg.mu.RLock()
	defer mg.mu.RUnlock()
	return mg.dirty
}

// registerGstObjs records freshly created pads so links can resolve them.
func (mg *mediaGraphImpl) registerGstObjs(objs []core.GstObject) error {
	for _, obj := range objs {
		if err := mg.nodeRuntime.RegisterGstObj(obj); err != nil {
			return fmt.Errorf("pad %q: %w", obj.ID(), err)
		}
	}
	return nil
}

// =============================================================================
// Event loop
// =============================================================================

// request sends one command to the event loop and waits for its answer.
//
// It never blocks: a full queue returns ErrGraphBusy, and a stopped loop returns
// ErrGraphDestroyed rather than waiting for a reply that can never come. Both are
// decided by the loop's own context, so no separate "destroyed" flag is needed.
func (mg *mediaGraphImpl) request(messageType MediaGraphEventType, payload any) error {
	reply := make(chan MediaGraphEvent, 1)
	msg := MediaGraphEvent{Type: messageType, Payload: payload, Reply: reply}

	select {
	case <-mg.loopCtx.Done():
		return ErrGraphDestroyed
	case mg.eventChan <- msg:
	default:
		return ErrGraphBusy
	}

	select {
	case <-mg.loopCtx.Done():
		return ErrGraphDestroyed
	case response := <-reply:
		errPointer, ok := response.Payload.(*error)
		if !ok {
			return fmt.Errorf("%w: reply payload is %T", core.ErrTypeMismatch, response.Payload)
		}
		return *errPointer
	}
}

// readTrackData forwards one typed entry into the aggregate channel. Passing
// the handle and entry ID explicitly keeps the goroutine independent of the
// subscription setup closure.
func (mg *mediaGraphImpl) readTrackData(entryID string, handle *core.OutputHandle[core.TrackData]) {
	for {
		trackData, ok := handle.Recv()
		if !ok {
			return
		}
		select {
		case mg.trackDataChan <- trackDataEvent{entryID: entryID, data: trackData}:
		case <-mg.loopCtx.Done():
			return
		}
	}
}

func (mg *mediaGraphImpl) runEventLoop() {
	mg.mu.Lock()
	if mg.isLoopStarted {
		mg.mu.Unlock()
		return
	}
	mg.isLoopStarted = true
	mg.mu.Unlock()

	// Main Loop
	// to update / change Graph Itself By Order.
	// Control by Outside
	go func() {
		for {
			select {
			case <-mg.loopCtx.Done():
				return
			case msg := <-mg.eventChan:
				mg.handleEvent(msg)
				// Stop after a destroy, once its reply has been delivered. Doing it
				// here rather than inside destroyLocked keeps the caller from racing
				// its own shutdown: the reply is sent first, then the context ends.
				if msg.Type == MediaGraphEventTypeDestroy {
					mg.loopCancel()
					return
				}
			}
		}
	}()

	// Sub Loop
	// to Update / Listen Data And Update Status Of Graph.
	// Control By Inside
	go func() {
		// The aggregate channel keeps the loop's select set fixed. If the number
		// of entry types grows, this can be replaced by one aggregate channel per
		// type plus a single switch over a tagged event; reflect.Select is only
		// needed when the set of channels itself must be dynamic.
		trackSubscriptions := make(map[string]core.OutputRegistryEntry)
		startTrackReader := func(entryID string, entry core.RegistryEntry) {
			if _, exists := trackSubscriptions[entryID]; exists {
				return
			}
			// Entry registration is intentionally node-type agnostic. Any node
			// may expose a typed output in the future; TrackData is simply the
			// first aggregate branch. Failed type probes are normal for entries
			// carrying another payload and are cleaned up below.
			outputEntry, ok := entry.(core.OutputRegistryEntry)
			if !ok {
				return
			}
			const bufferSize = 32
			subscriberID := fmt.Sprintf("mediagraph:%s:%s", mg.id, entryID)
			handle, err := core.GetOutputHandle[core.TrackData](outputEntry, subscriberID, bufferSize)
			if err != nil {
				// GetOutputHandle may create the subscriber before discovering a
				// concrete channel type mismatch. Do not leave that probe behind.
				_ = outputEntry.RemoveOutput(subscriberID)
				return
			}
			trackSubscriptions[entryID] = outputEntry
			go mg.readTrackData(entryID, handle)
		}

		for {
			select {
			case <-mg.loopCtx.Done():
				for entryID, outputEntry := range trackSubscriptions {
					_ = outputEntry.RemoveOutput(fmt.Sprintf("mediagraph:%s:%s", mg.id, entryID))
				}
				return
			case event, ok := <-mg.chanRegistryEventChan:
				if !ok {
					return
				}
				entryID := event.Payload.ChanEntryId
				switch event.Type {
				case core.ChanRegistryEventTypeEntryRegistered:
					entry, exists := mg.chanRegistry.GetEntryById(entryID)
					if !exists {
						continue
					}
					mg.mu.Lock()
					mg.nodeChanEntries[entryID] = entry
					mg.mu.Unlock()
					startTrackReader(entryID, entry)
				case core.ChanRegistryEventTypeEntryRemoved:
					if outputEntry, exists := trackSubscriptions[entryID]; exists {
						_ = outputEntry.RemoveOutput(fmt.Sprintf("mediagraph:%s:%s", mg.id, entryID))
						delete(trackSubscriptions, entryID)
					}
					mg.mu.Lock()
					delete(mg.nodeChanEntries, entryID)
					mg.mu.Unlock()
				}
			case event := <-mg.trackDataChan:
				if _, subscribed := trackSubscriptions[event.entryID]; !subscribed {
					continue
				}
				track := core.NewTrackEvent(event.entryID, event.data)
				runtimeEvent := core.RuntimeEvent{
					Type:      core.RuntimeEventTrackDiscovered,
					NodeID:    event.entryID,
					Track:     &track,
					Timestamp: time.Now(),
				}
				if event.data.Type == core.TrackEventTypeError {
					runtimeEvent.Type = core.RuntimeEventTrackError
				}
				select {
				case mg.runtimeEventChan <- runtimeEvent:
				case <-mg.loopCtx.Done():
					return
				}
			}
		}
	}()

	// Read Bus Event
	go func() {
		for {
			select {
			case <-mg.loopCtx.Done():
				return
			case event, ok := <-mg.busEvents:
				if !ok {
					return
				}
				if event.Type == BusEventTypeStateDirty {
					mg.MarkDirty()
					continue
				}
				if event.Type == BusEventTypeStateChanged {
					if event.Source == mg.id || event.Parent == mg.id {
						mg.stateMu.Lock()
						mg.currentState = event.NewState
						mg.pendingState = event.Pending
						mg.stateMu.Unlock()
					}
					mg.publishBusRuntimeEvent(event)
					continue
				}
				if event.Type == BusEventTypeError || event.Type == BusEventTypeWarning || event.Type == BusEventTypeEOS {
					mg.publishBusRuntimeEvent(event)
				}
			}
		}
	}()

}

func (mg *mediaGraphImpl) publishBusRuntimeEvent(event BusEvent) {
	runtimeEvent := core.RuntimeEvent{
		Source:    event.Source,
		Parent:    event.Parent,
		Error:     errorString(event.Error),
		Debug:     event.Debug,
		Timestamp: event.Timestamp,
	}
	switch event.Type {
	case BusEventTypeError:
		runtimeEvent.Type = core.RuntimeEventBusError
	case BusEventTypeWarning:
		runtimeEvent.Type = core.RuntimeEventBusWarning
	case BusEventTypeEOS:
		runtimeEvent.Type = core.RuntimeEventEOS
	case BusEventTypeStateChanged:
		runtimeEvent.Type = core.RuntimeEventStateChanged
		runtimeEvent.OldState = event.OldState.String()
		runtimeEvent.NewState = event.NewState.String()
		runtimeEvent.Pending = event.Pending.String()
	default:
		return
	}
	select {
	case mg.runtimeEventChan <- runtimeEvent:
	case <-mg.loopCtx.Done():
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// handleEvent runs one event under the write lock and answers its reply.
//
// The recover covers a panicking command: the loop must survive one bad event
// instead of stopping, because stopping would hang every later caller waiting on
// a reply that can never come.
func (mg *mediaGraphImpl) handleEvent(msg MediaGraphEvent) {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in graph main loop: %v", r)
			}
		}()

		mg.mu.Lock()
		defer mg.mu.Unlock()

		// The type assertions are the payload validation.
		switch msg.Type {
		case MediaGraphEventTypeApplyGraph:
			request, ok := msg.Payload.(*applyRequest)
			if !ok {
				err = fmt.Errorf("%w: apply-graph payload is %T", core.ErrTypeMismatch, msg.Payload)
				return
			}
			err = mg.applyGraphLocked(request.Graph, request.BaseVersion)
		case MediaGraphEventTypeSetState:
			state, ok := msg.Payload.(*gst.State)
			if !ok {
				err = fmt.Errorf("%w: set-state payload is %T", core.ErrTypeMismatch, msg.Payload)
				return
			}
			if result := mg.pipeline.SetState(*state); result == gst.StateChangeFailure {
				err = core.ErrGSTStateChangeFailed
			}
		case MediaGraphEventTypeDestroy:
			err = mg.destroyLocked()
		default:
			err = fmt.Errorf("%w: unknown message type %v", core.ErrInvalidConfig, msg.Type)
		}
	}()

	if msg.Reply != nil {
		msg.Reply <- MediaGraphEvent{Type: MediaGraphEventTypeReturnError, Payload: &err}
	}
}
