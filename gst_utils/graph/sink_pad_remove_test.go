package graph

import (
	"errors"
	"slices"
	"testing"

	"live_tools/gst_utils/core"
)

const sinkShrinkPipelineID = "audio_combiner_sink_shrink"

// sinkShrinkAppliedGraph is the sink-side counterpart of padShrinkAppliedGraph:
// an AudioCombiner with two branches, each fed by its own source.
func sinkShrinkAppliedGraph() DesiredGraph {
	return DesiredGraph{
		ID: sinkShrinkPipelineID,
		Nodes: map[string]NodeInfo{
			"sink_shrink_src_a": {
				ID:         "sink_shrink_src_a",
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: "sink_shrink_src_a_src_0"}},
			},
			"sink_shrink_src_b": {
				ID:         "sink_shrink_src_b",
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: "sink_shrink_src_b_src_0"}},
			},
			"sink_shrink_mix": {
				ID:       "sink_shrink_mix",
				NodeType: "AudioCombiner",
				SinkPads: []core.PadInfo{
					{ID: "sink_shrink_mix_sink_0"},
					{ID: "sink_shrink_mix_sink_1"},
				},
				SourcePads: []core.PadInfo{{ID: "sink_shrink_mix_src_0"}},
			},
		},
		Links: []LinkInfo{
			{ID: "sink_shrink_link_a", SourcePadID: "sink_shrink_src_a_src_0", SinkPadID: "sink_shrink_mix_sink_0"},
			{ID: "sink_shrink_link_b", SourcePadID: "sink_shrink_src_b_src_0", SinkPadID: "sink_shrink_mix_sink_1"},
		},
	}
}

// sinkShrinkDesiredGraph drops the second branch: its node, its link, and the
// sink pad it fed.
func sinkShrinkDesiredGraph() DesiredGraph {
	graph := sinkShrinkAppliedGraph()
	mix := graph.Nodes["sink_shrink_mix"]
	mix.SinkPads = mix.SinkPads[:1]
	graph.Nodes["sink_shrink_mix"] = mix
	delete(graph.Nodes, "sink_shrink_src_b")
	graph.Links = graph.Links[:1]
	return graph
}

// TestDiffGraphEmitsRemovedSinkPad checks that a retired SINK pad travels with
// the direction set to sink, so ApplyGraph routes it to RemoveSinkPads instead
// of RemoveSrcPads. Source pads already had this covered; sink did not.
func TestDiffGraphEmitsRemovedSinkPad(t *testing.T) {
	setupGraphTest(t)

	diff, err := DiffGraph(sinkShrinkAppliedGraph(), sinkShrinkDesiredGraph())
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}

	if len(diff.RemovePads) != 1 {
		t.Fatalf("RemovePads = %d, want 1", len(diff.RemovePads))
	}
	retired := diff.RemovePads[0]
	if want := "sink_shrink_mix_sink_1"; retired.ID != want {
		t.Fatalf("retired pad = %q, want %q", retired.ID, want)
	}
	if want := "sink_shrink_mix"; retired.NodeID != want {
		t.Fatalf("retired pad NodeID = %q, want %q", retired.NodeID, want)
	}
	if retired.Direction != core.PadDirectionSink {
		t.Fatalf("retired pad Direction = %v, want sink", retired.Direction)
	}

	// The surviving branch must not be reported as anything.
	if len(diff.AddPads) != 0 {
		t.Fatalf("AddPads = %d, want 0", len(diff.AddPads))
	}
}

// TestApplyGraphRemovesSinkPad drives the whole sink-side path on a real graph.
// RemoveSinkPads was never executed by any test before this one, so the request
// pad release and the ghost pad teardown were both unverified.
func TestApplyGraphRemovesSinkPad(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(sinkShrinkPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(sinkShrinkAppliedGraph()); err != nil {
		t.Fatalf("ApplyGraph(initial): %v", err)
	}

	mixNode, ok := mediaGraph.nodesForTest()["sink_shrink_mix"].(core.ManyToOneNode)
	if !ok {
		t.Fatal("node \"sink_shrink_mix\" is not a ManyToOneNode")
	}
	retiredID := "sink_shrink_mix_sink_1"
	if got := mixNode.GetDstIDs(); !slices.Contains(got, retiredID) {
		t.Fatalf("GetDstIDs before shrink = %v, want to include %q", got, retiredID)
	}

	if err := mediaGraph.ApplyGraph(sinkShrinkDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph(shrink): %v", err)
	}

	if got := mixNode.GetDstIDs(); slices.Contains(got, retiredID) {
		t.Fatalf("GetDstIDs after shrink = %v, want %q gone", got, retiredID)
	}
	if _, ok := mediaGraph.gstObjectRegistry.GetPadSafe(retiredID); ok {
		t.Fatalf("pad %q is still registered, so a link could still resolve it", retiredID)
	}
	// The branch that was not retired must survive untouched.
	if got := mixNode.GetDstIDs(); !slices.Contains(got, "sink_shrink_mix_sink_0") {
		t.Fatalf("GetDstIDs = %v, want the surviving branch to remain", got)
	}
}

// TestRemovePadsRefusesNodesWithoutTheCapability pins the guard that is the
// whole reason sinkPadRemover exists: not every node can give a pad back, and
// asking one that cannot has to be an error rather than a silent no-op.
//
// The two helpers are checked separately because they assert against different
// capabilities: RemoveSrcPads needs OneToManyNode, RemoveSinkPads needs
// sinkPadRemover, and a node can have neither, either, or both.
func TestRemovePadsRefusesNodesWithoutTheCapability(t *testing.T) {
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

	// SourceInput is ZeroToOneNode: it has a source pad but cannot retire it.
	srcNode := mediaGraph.nodesForTest()[padGrowthSourceNode]
	err = RemoveSrcPads(srcNode, []core.PadInfo{{ID: padGrowthSourceNode + "_src_0"}})
	if err == nil {
		t.Fatal("RemoveSrcPads on a node without the capability returned nil, want an error")
	}
	if !errors.Is(err, core.ErrPadCantBeUsed) {
		t.Fatalf("RemoveSrcPads error = %v, want ErrPadCantBeUsed", err)
	}

	// ContainerParse is OneToManyNode: it retires source pads but has no
	// removable sink pad, even though it does expose one.
	parseNode := mediaGraph.nodesForTest()[padGrowthParseNode]
	err = RemoveSinkPads(parseNode, []core.PadInfo{{ID: padGrowthParseNode + "_sink_0"}})
	if err == nil {
		t.Fatal("RemoveSinkPads on a node without the capability returned nil, want an error")
	}
	if !errors.Is(err, core.ErrPadCantBeUsed) {
		t.Fatalf("RemoveSinkPads error = %v, want ErrPadCantBeUsed", err)
	}
}

// TestRemovePadsEmptySliceIsNotAnError guards the early return. A source-only
// removal leaves the sink list empty, and asserting a capability for a list
// with nothing in it would turn "no work" into a failure. This was a real bug
// when the helpers took a whole NodeInfo.
func TestRemovePadsEmptySliceIsNotAnError(t *testing.T) {
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

	srcNode := mediaGraph.nodesForTest()[padGrowthSourceNode]
	if err := RemoveSrcPads(srcNode, nil); err != nil {
		t.Fatalf("RemoveSrcPads(nil) = %v, want nil", err)
	}
	if err := RemoveSinkPads(srcNode, nil); err != nil {
		t.Fatalf("RemoveSinkPads(nil) = %v, want nil", err)
	}
}
