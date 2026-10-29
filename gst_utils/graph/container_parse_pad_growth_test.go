package graph

import (
	"slices"
	"testing"

	"live_tools/gst_utils/core"
)

const (
	padGrowthPipelineID = "container_parse_pad_growth"

	padGrowthSourceNode = "pg_src"
	padGrowthParseNode  = "pg_parse"
)

// padGrowthAppliedGraph is the starting point: ContainerParse declares one pad.
func padGrowthAppliedGraph() DesiredGraph {
	return DesiredGraph{
		ID: padGrowthPipelineID,
		Nodes: map[string]NodeInfo{
			padGrowthSourceNode: {
				ID:         padGrowthSourceNode,
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: padGrowthSourceNode + "_src_0"}},
			},
			padGrowthParseNode: {
				ID:         padGrowthParseNode,
				NodeType:   "ContainerParse",
				SourcePads: []core.PadInfo{{ID: padGrowthParseNode + "_src_0"}},
				SinkPads:   []core.PadInfo{{ID: padGrowthParseNode + "_sink_0"}},
			},
		},
		Links: []LinkInfo{{
			ID:          "pg_link_source_parse",
			SourcePadID: padGrowthSourceNode + "_src_0",
			SinkPadID:   padGrowthParseNode + "_sink_0",
		}},
	}
}

// padGrowthDesiredGraph declares a second pad on the node that already exists.
func padGrowthDesiredGraph() DesiredGraph {
	graph := padGrowthAppliedGraph()
	node := graph.Nodes[padGrowthParseNode]
	node.SourcePads = append(node.SourcePads, core.PadInfo{ID: padGrowthParseNode + "_src_1"})
	graph.Nodes[padGrowthParseNode] = node
	return graph
}

// TestDiffGraphEmitsOnlyAddedPads guards the shape of the diff itself: the node
// already owns _src_0, so emitting it again would make ApplyGraph register a pad
// that is already registered. The diff must carry the new pad alone.
func TestDiffGraphEmitsOnlyAddedPads(t *testing.T) {
	setupGraphTest(t)

	diff, err := DiffGraph(padGrowthAppliedGraph(), padGrowthDesiredGraph())
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}
	if len(diff.AddPads) != 1 {
		t.Fatalf("AddPads = %d nodes, want 1", len(diff.AddPads))
	}

	got := make([]string, 0)
	for _, pad := range diff.AddPads[0].SourcePads {
		got = append(got, pad.ID)
	}
	want := []string{padGrowthParseNode + "_src_1"}
	if !slices.Equal(got, want) {
		t.Fatalf("diff source pads = %v, want only the added pad %v", got, want)
	}
}

// TestApplyGraphAddsSrcPadToExistingNode drives the path that had no coverage:
// a pad added to a node that is already in the graph. The node mints pad IDs
// internally, so the graph has to be able to ask for one by ID rather than
// taking whatever the node happens to hand out next.
func TestApplyGraphAddsSrcPadToExistingNode(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(padGrowthPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(padGrowthAppliedGraph()); err != nil {
		t.Fatalf("ApplyGraph(initial): %v", err)
	}

	parseNode, ok := mediaGraph.nodesForTest()[padGrowthParseNode].(core.OneToManyNode)
	if !ok {
		t.Fatalf("node %q is not a OneToManyNode", padGrowthParseNode)
	}
	if got := parseNode.GetSrcIDs(); len(got) != 1 {
		t.Fatalf("GetSrcIDs before growth = %v, want 1 pad", got)
	}

	if err := mediaGraph.ApplyGraph(padGrowthDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph(growth): %v", err)
	}

	newPadID := padGrowthParseNode + "_src_1"
	if got := parseNode.GetSrcIDs(); !slices.Contains(got, newPadID) {
		t.Fatalf("GetSrcIDs after growth = %v, want to include %q", got, newPadID)
	}

	// The graph has to be able to reach the new pad by ID, since that is the ID
	// a link will name.
	if _, ok := mediaGraph.gstObjectRegistry.GetPadSafe(newPadID); !ok {
		t.Fatalf("pad %q was not registered, so no link could ever resolve it", newPadID)
	}

	// Diffing against the graph we just applied must be a no-op: the pad is now
	// part of the applied state, so it is not new any more. A diff that still
	// carried it would ask ApplyGraph to register a pad that already exists.
	settled, err := DiffGraph(padGrowthDesiredGraph(), padGrowthDesiredGraph())
	if err != nil {
		t.Fatalf("DiffGraph(settled): %v", err)
	}
	if len(settled.AddPads) != 0 {
		t.Fatalf("AddPads after settling = %d, want 0", len(settled.AddPads))
	}
}

// TestContainerParseRequestSrcPadWithoutID covers the nil-padInfo path. A
// caller with no ID in mind is asking for a NEW output, not for one of the
// declared pads, so the node mints an ID past everything it already owns and
// builds that pad. The declared pad must not be handed out again.
func TestContainerParseRequestSrcPadWithoutID(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(padGrowthPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(padGrowthAppliedGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}

	parseNode, ok := mediaGraph.nodesForTest()[padGrowthParseNode].(core.OneToManyNode)
	if !ok {
		t.Fatalf("node %q is not a OneToManyNode", padGrowthParseNode)
	}

	// One declared pad exists, so the minted ID has to be past it.
	_, id, err := parseNode.RequestSrcPad(nil)
	if err != nil {
		t.Fatalf("RequestSrcPad(nil): %v", err)
	}
	if want := padGrowthParseNode + "_src_1"; id != want {
		t.Fatalf("RequestSrcPad(nil) = %q, want a new pad %q", id, want)
	}

	// A second call mints a second pad rather than running out.
	_, second, err := parseNode.RequestSrcPad(nil)
	if err != nil {
		t.Fatalf("second RequestSrcPad(nil): %v", err)
	}
	if want := padGrowthParseNode + "_src_2"; second != want {
		t.Fatalf("second RequestSrcPad(nil) = %q, want %q", second, want)
	}

	got := parseNode.GetSrcIDs()
	if !slices.Contains(got, padGrowthParseNode+"_src_0") {
		t.Fatalf("GetSrcIDs = %v, want the declared pad to survive", got)
	}
	if len(got) != 3 {
		t.Fatalf("GetSrcIDs = %v, want 3 pads (1 declared + 2 minted)", got)
	}
}

// TestCloneNodeRefusesForeignPadID pins the contract for a node that mints its
// TestCloneNodeAcceptsCallerPadID pins the contract that made CloneNode match
// ContainerParseNode: the pad id is the caller's business, while the tee and
// multiqueue indexes stay internal. A caller-named pad must be honoured even
// when its number does not line up with the branch order.
func TestCloneNodeAcceptsCallerPadID(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(padGrowthPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	node := padGrowthDesiredGraph().Nodes[padGrowthParseNode]
	node.NodeType = "Clone"
	node.SourcePads = nil
	node.SinkPads = nil

	clone, err := mediaGraph.createGraphNode(mediaGraph.nodeRuntime, node)
	if err != nil {
		t.Fatalf("createGraphNode(Clone): %v", err)
	}
	defer clone.Destroy()

	oneToMany, ok := clone.(core.OneToManyNode)
	if !ok {
		t.Fatalf("CloneNode is not a OneToManyNode")
	}

	// A number that does not match the branch order must still be honoured: the
	// branch index is internal, the pad id is not.
	named := padGrowthParseNode + "_src_7"
	_, id, err := oneToMany.RequestSrcPad(&core.PadInfo{ID: named})
	if err != nil {
		t.Fatalf("RequestSrcPad(%q): %v", named, err)
	}
	if id != named {
		t.Fatalf("RequestSrcPad returned %q, want the requested %q", id, named)
	}

	// A second pad with the same id would be a duplicate branch.
	if _, _, err := oneToMany.RequestSrcPad(&core.PadInfo{ID: named}); err == nil {
		t.Fatal("RequestSrcPad accepted a duplicate pad id, want an error")
	}

	// Removing the named pad must not consume a minted number, so the next
	// unnamed request still starts at _src_0.
	if err := oneToMany.RemoveSrcPad(named); err != nil {
		t.Fatalf("RemoveSrcPad(%q): %v", named, err)
	}
	_, minted, err := oneToMany.RequestSrcPad(nil)
	if err != nil {
		t.Fatalf("RequestSrcPad(nil): %v", err)
	}
	if want := padGrowthParseNode + "_src_0"; minted != want {
		t.Fatalf("minted id = %q, want %q", minted, want)
	}
}
