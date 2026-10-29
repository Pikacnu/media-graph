package graph

import (
	"slices"
	"testing"

	"live_tools/gst_utils/core"
)

const padShrinkPipelineID = "container_parse_pad_shrink"

// padShrinkAppliedGraph is the starting point: ContainerParse declares two pads.
func padShrinkAppliedGraph() DesiredGraph {
	graph := padGrowthAppliedGraph()
	graph.ID = padShrinkPipelineID
	node := graph.Nodes[padGrowthParseNode]
	node.SourcePads = append(node.SourcePads, core.PadInfo{ID: padGrowthParseNode + "_src_1"})
	graph.Nodes[padGrowthParseNode] = node
	return graph
}

// padShrinkDesiredGraph drops the second declared pad.
func padShrinkDesiredGraph() DesiredGraph {
	graph := padShrinkAppliedGraph()
	node := graph.Nodes[padGrowthParseNode]
	node.SourcePads = node.SourcePads[:1]
	graph.Nodes[padGrowthParseNode] = node
	return graph
}

// TestDiffGraphEmitsRemovedPads covers the direction that used to be rejected
// outright: a desired graph that declares FEWER pads than the applied one. The
// retired pads travel in PadsToRemove on their own, without a node around them;
// ApplyGraph looks up the owner when it applies them.
func TestDiffGraphEmitsRemovedPads(t *testing.T) {
	setupGraphTest(t)

	diff, err := DiffGraph(padShrinkAppliedGraph(), padShrinkDesiredGraph())
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}

	got := make([]string, 0, len(diff.RemovePads))
	for _, pad := range diff.RemovePads {
		got = append(got, pad.ID)
	}
	if want := []string{padGrowthParseNode + "_src_1"}; !slices.Equal(got, want) {
		t.Fatalf("retired pads = %v, want %v", got, want)
	}

	// The surviving pad must not also be reported as added.
	if len(diff.AddPads) != 0 {
		t.Fatalf("AddPads = %d, want 0 (nothing was added)", len(diff.AddPads))
	}
}

// TestApplyGraphRemovesSrcPad drives the whole path on a real graph: the pad is
// unregistered so no link can resolve it, and the node stops reporting it.
func TestApplyGraphRemovesSrcPad(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(padShrinkPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(padShrinkAppliedGraph()); err != nil {
		t.Fatalf("ApplyGraph(initial): %v", err)
	}

	parseNode, ok := mediaGraph.nodesForTest()[padGrowthParseNode].(core.OneToManyNode)
	if !ok {
		t.Fatalf("node %q is not a OneToManyNode", padGrowthParseNode)
	}
	retiredID := padGrowthParseNode + "_src_1"
	if got := parseNode.GetSrcIDs(); !slices.Contains(got, retiredID) {
		t.Fatalf("GetSrcIDs before shrink = %v, want to include %q", got, retiredID)
	}

	if err := mediaGraph.ApplyGraph(padShrinkDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph(shrink): %v", err)
	}

	if got := parseNode.GetSrcIDs(); slices.Contains(got, retiredID) {
		t.Fatalf("GetSrcIDs after shrink = %v, want %q gone", got, retiredID)
	}
	if _, ok := mediaGraph.gstObjectRegistry.GetPadSafe(retiredID); ok {
		t.Fatalf("pad %q is still registered, so a link could still resolve it", retiredID)
	}
	// The pad that was not retired must survive untouched.
	if got := parseNode.GetSrcIDs(); !slices.Contains(got, padGrowthParseNode+"_src_0") {
		t.Fatalf("GetSrcIDs = %v, want the surviving pad to remain", got)
	}
}

// TestDiffGraphShrinkAndGrowAtOnce pins that the two directions are computed
// independently: replacing one pad with another is a removal AND an addition,
// not a no-op.
func TestDiffGraphShrinkAndGrowAtOnce(t *testing.T) {
	setupGraphTest(t)

	desired := padShrinkAppliedGraph()
	node := desired.Nodes[padGrowthParseNode]
	node.SourcePads = []core.PadInfo{{ID: padGrowthParseNode + "_src_9"}}
	desired.Nodes[padGrowthParseNode] = node

	diff, err := DiffGraph(padShrinkAppliedGraph(), desired)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}

	if len(diff.RemovePads) != 2 {
		t.Fatalf("RemovePads = %d, want 2", len(diff.RemovePads))
	}
	removed := make([]string, 0, len(diff.RemovePads))
	for _, pad := range diff.RemovePads {
		removed = append(removed, pad.ID)
	}
	if want := []string{padGrowthParseNode + "_src_0", padGrowthParseNode + "_src_1"}; !slices.Equal(removed, want) {
		t.Fatalf("retired pads = %v, want %v", removed, want)
	}

	if len(diff.AddPads) != 1 {
		t.Fatalf("AddPads = %d, want 1", len(diff.AddPads))
	}
	added := make([]string, 0)
	for _, pad := range diff.AddPads[0].SourcePads {
		added = append(added, pad.ID)
	}
	if want := []string{padGrowthParseNode + "_src_9"}; !slices.Equal(added, want) {
		t.Fatalf("added pads = %v, want %v", added, want)
	}
}

// TestDiffGraphPadOrderDoesNotMatter guards the set-based comparison: the same
// pads declared in a different order are the same pads, so no diff is produced.
func TestDiffGraphPadOrderDoesNotMatter(t *testing.T) {
	setupGraphTest(t)

	reordered := padShrinkAppliedGraph()
	node := reordered.Nodes[padGrowthParseNode]
	node.SourcePads = []core.PadInfo{node.SourcePads[1], node.SourcePads[0]}
	reordered.Nodes[padGrowthParseNode] = node

	diff, err := DiffGraph(padShrinkAppliedGraph(), reordered)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}
	if len(diff.AddPads) != 0 {
		t.Fatalf("AddPads = %d, want 0 for a pure reorder", len(diff.AddPads))
	}
	if len(diff.RemovePads) != 0 {
		t.Fatalf("RemovePads = %d, want 0 for a pure reorder", len(diff.RemovePads))
	}
}
