package graph

import (
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

const (
	padRebindPipelineID = "container_parse_rebind"

	padRebindSourceNode = "pr_src"
	padRebindParseNode  = "pr_parse"
	padRebindOutputNode = "pr_out"

	padRebindAbsentName = "no-such-track"
)

// padRebindDesiredGraph declares one parser pad whose selector matches nothing in
// the stream, so the tracks that do arrive are given pads of their own. That
// leaves the declared pad free for a later UpdatePad to aim somewhere.
func padRebindDesiredGraph() DesiredGraph {
	return DesiredGraph{
		ID: padRebindPipelineID,
		Nodes: map[string]NodeInfo{
			padRebindSourceNode: {
				ID:         padRebindSourceNode,
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: padRebindSourceNode + "_src_0"}},
			},
			padRebindParseNode: {
				ID:       padRebindParseNode,
				NodeType: "ContainerParse",
				SourcePads: []core.PadInfo{{
					ID:         padRebindParseNode + "_src_0",
					Properties: map[string]interface{}{"track_name": padRebindAbsentName},
				}},
				SinkPads: []core.PadInfo{{ID: padRebindParseNode + "_sink_0"}},
			},
			padRebindOutputNode: {
				ID:         padRebindOutputNode,
				NodeType:   "Output",
				Properties: map[string]interface{}{"caps": "video/x-raw"},
				SinkPads:   []core.PadInfo{{ID: padRebindOutputNode + "_sink_0"}},
			},
		},
		Links: []LinkInfo{
			{
				ID:          "pr_link_source_parse",
				SourcePadID: padRebindSourceNode + "_src_0",
				SinkPadID:   padRebindParseNode + "_sink_0",
			},
			{
				ID:          "pr_link_parse_out",
				SourcePadID: padRebindParseNode + "_src_0",
				SinkPadID:   padRebindOutputNode + "_sink_0",
			},
		},
	}
}

// padRebindWithSelector returns the same graph with the declared parser pad
// aimed at trackName instead.
func padRebindWithSelector(trackName string) DesiredGraph {
	graph := padRebindDesiredGraph()
	node := graph.Nodes[padRebindParseNode]
	pads := make([]core.PadInfo, len(node.SourcePads))
	copy(pads, node.SourcePads)
	pads[0].Properties = map[string]interface{}{"track_name": trackName}
	node.SourcePads = pads
	graph.Nodes[padRebindParseNode] = node
	return graph
}

func startPadRebindGraph(t *testing.T) *mediaGraphImpl {
	t.Helper()
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(padRebindPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	t.Cleanup(func() { _ = mediaGraph.Destroy() })

	if err := mediaGraph.ApplyGraph(padRebindDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := mediaGraph.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	return mediaGraph
}

// containerBytesForRebind muxes two named video tracks.
func containerBytesForRebind(t *testing.T) []byte {
	t.Helper()
	return buildUntaggedMultiTrackBytes(t)
}

// waitForPadCount waits until the parser reports want source pads, so a test can
// assert on binding state without racing the pad-added probe.
func waitForPadCount(t *testing.T, mediaGraph *mediaGraphImpl, want int, timeout time.Duration) []string {
	t.Helper()
	parseNode, ok := mediaGraph.nodesForTest()[padRebindParseNode].(core.OneToManyNode)
	if !ok {
		t.Fatalf("node %q is not a OneToManyNode", padRebindParseNode)
	}
	deadline := time.Now().Add(timeout)
	var ids []string
	for time.Now().Before(deadline) {
		ids = parseNode.GetSrcIDs()
		if len(ids) >= want {
			return ids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("parser reported %d pads (%v), want at least %d", len(ids), ids, want)
	return ids
}

// TestUpdatePadRejectsSelectorHeldByAnotherPad covers the case where two slots
// would be told to carry the same track. A parsebin pad has one peer, so the
// second request cannot be honoured; it must be reported rather than silently
// stealing the track and stopping the other slot's output.
func TestUpdatePadRejectsSelectorHeldByAnotherPad(t *testing.T) {
	mediaGraph := startPadRebindGraph(t)

	if err := pushSource(mediaGraph, padRebindSourceNode, containerBytesForRebind(t)); err != nil {
		t.Fatalf("push source: %v", err)
	}

	// The declared pad matches nothing, so each real track gets its own pad.
	ids := waitForPadCount(t, mediaGraph, 3, 15*time.Second)
	t.Logf("parser pads after streaming: %v", ids)

	// Read back the selector of a pad that a real track claimed, then aim the
	// declared pad at the same track. A selector is the whole (name, track id,
	// media type) triple, so all three have to be copied for the keys to collide.
	snapshot := mediaGraph.Snapshot()
	var occupied core.PadInfo
	for _, pad := range snapshot.Nodes[padRebindParseNode].SourcePads {
		if pad.ID == padRebindParseNode+"_src_0" {
			continue
		}
		if len(pad.Properties) > 0 {
			occupied = pad
			break
		}
	}
	if occupied.ID == "" {
		t.Fatalf("no track-claimed pad found in %+v", snapshot.Nodes[padRebindParseNode].SourcePads)
	}
	t.Logf("pad %s carries %v", occupied.ID, occupied.Properties)

	desired := padRebindDesiredGraph()
	node := desired.Nodes[padRebindParseNode]
	pads := make([]core.PadInfo, len(node.SourcePads))
	copy(pads, node.SourcePads)
	pads[0].Properties = map[string]interface{}{}
	for key, value := range occupied.Properties {
		pads[0].Properties[key] = value
	}
	node.SourcePads = pads
	desired.Nodes[padRebindParseNode] = node

	err := mediaGraph.ApplyGraph(desired)
	if err == nil {
		t.Fatalf("ApplyGraph accepted a selector already bound to pad %q; want an error", occupied.ID)
	}
	t.Logf("rejected as expected: %v", err)
}

// TestUpdatePadUnbindsThenRebindsToFlowingTrack covers the manual half of
// pad-added. Changing a selector emits no GStreamer signal, so the node has to
// look the track up itself; if the track is still in the stream it must be
// re-linked immediately, not parked waiting for a callback that never comes.
func TestUpdatePadUnbindsThenRebindsToFlowingTrack(t *testing.T) {
	mediaGraph := startPadRebindGraph(t)

	if err := pushSource(mediaGraph, padRebindSourceNode, containerBytesForRebind(t)); err != nil {
		t.Fatalf("push source: %v", err)
	}
	ids := waitForPadCount(t, mediaGraph, 3, 15*time.Second)
	t.Logf("parser pads after streaming: %v", ids)

	// Aim the declared pad at a track that does not exist. That unbinds nothing
	// (it was never bound) and parks it.
	if err := mediaGraph.ApplyGraph(padRebindWithSelector(padRebindAbsentName + "-2")); err != nil {
		t.Fatalf("ApplyGraph(park): %v", err)
	}

	// Now aim it at a track that IS flowing. A pad-added callback will not fire,
	// so this only works if UpdatePad resolves the track itself.
	if err := mediaGraph.ApplyGraph(padRebindWithSelector(dynamicPadFirstName)); err != nil {
		t.Fatalf("ApplyGraph(rebind to flowing track): %v", err)
	}

	// The binding must be recorded, and it must be the declared pad that holds it.
	snapshot := mediaGraph.Snapshot()
	parseNodeInfo, ok := snapshot.Nodes[padRebindParseNode]
	if !ok {
		t.Fatalf("snapshot missing node %q", padRebindParseNode)
	}
	found := false
	for _, pad := range parseNodeInfo.SourcePads {
		if pad.ID != padRebindParseNode+"_src_0" {
			continue
		}
		found = true
		if got, _ := pad.Properties["track_name"].(string); got != dynamicPadFirstName {
			t.Fatalf("declared pad selector = %q, want %q", got, dynamicPadFirstName)
		}
	}
	if !found {
		t.Fatalf("snapshot has no declared pad: %+v", parseNodeInfo.SourcePads)
	}

	// A second identical apply must be a no-op, or every echo would re-diff.
	if err := mediaGraph.ApplyGraph(padRebindWithSelector(dynamicPadFirstName)); err != nil {
		t.Fatalf("ApplyGraph(idempotent): %v", err)
	}
}
