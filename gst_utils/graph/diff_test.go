package graph

import (
	"testing"

	// Blank import: the node catalog is populated by each node file's init, so
	// the graph tests need this package loaded for its side effects only.
	_ "live_tools/gst_utils/nodes"

	"github.com/go-gst/go-gst/pkg/gst"
)

func setupGraphTest(t *testing.T) {
	t.Helper()
	gst.Init()
}

func TestDiffGraphRemoveLink(t *testing.T) {
	setupGraphTest(t)

	desired := testDesiredGraph()
	desired.Nodes["test_extra"] = NodeInfo{
		ID:         "test_extra",
		NodeType:   "Clone",
		Properties: map[string]interface{}{},
		SourcePads: []PadInfo{{ID: "test_extra_src_0"}},
		SinkPads:   []PadInfo{{ID: "test_extra_sink_0"}},
	}

	applied := DesiredGraph{
		ID:    testPipelineID,
		Nodes: desired.Nodes,
		Links: append(append([]LinkInfo{}, desired.Links...), LinkInfo{
			ID:          "test_link_removed",
			SourcePadID: "test_cloner_src_0",
			SinkPadID:   "test_extra_sink_0",
		}),
	}

	diff, err := DiffGraph(applied, desired)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}

	if len(diff.AddLinks) != 0 {
		t.Errorf("AddLinks = %v, want none", diff.AddLinks)
	}
	if len(diff.RemoveLinks) != 1 {
		t.Fatalf("RemoveLinks = %v, want exactly 1", diff.RemoveLinks)
	}
	if got := diff.RemoveLinks[0].ID; got != "test_link_removed" {
		t.Errorf("RemoveLinks[0].ID = %q, want %q", got, "test_link_removed")
	}
	if len(diff.AddNodes) != 0 {
		t.Errorf("AddNodes = %v, want none", diff.AddNodes)
	}
	if len(diff.RemoveNodes) != 0 {
		t.Errorf("RemoveNodes = %v, want none", diff.RemoveNodes)
	}
}

func TestDiffGraphAddLink(t *testing.T) {
	setupGraphTest(t)

	desired := testDesiredGraph()

	applied := DesiredGraph{
		ID:    testPipelineID,
		Nodes: desired.Nodes,
		Links: []LinkInfo{
			{ID: "test_link_input_cloner", SourcePadID: "test_input_src_0", SinkPadID: "test_cloner_sink_0"},
		},
	}

	diff, err := DiffGraph(applied, desired)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}

	if len(diff.RemoveLinks) != 0 {
		t.Errorf("RemoveLinks = %v, want none", diff.RemoveLinks)
	}
	if len(diff.AddLinks) != 1 {
		t.Fatalf("AddLinks = %v, want exactly 1", diff.AddLinks)
	}
	if got := diff.AddLinks[0].ID; got != "test_link_cloner_output" {
		t.Errorf("AddLinks[0].ID = %q, want %q", got, "test_link_cloner_output")
	}
}

func TestDiffGraphRemoveNode(t *testing.T) {
	setupGraphTest(t)

	desired := testDesiredGraph()

	applied := testDesiredGraph()
	stale := DesiredGraph{
		ID:    testPipelineID,
		Nodes: make(map[string]NodeInfo, len(applied.Nodes)+1),
		Links: applied.Links,
	}
	for id, node := range applied.Nodes {
		stale.Nodes[id] = node
	}
	stale.Nodes["test_stale"] = NodeInfo{ID: "test_stale", NodeType: "Clone", SourcePads: []PadInfo{{ID: "test_stale_src_0"}}, SinkPads: []PadInfo{{ID: "test_stale_sink_0"}}}

	diff, err := DiffGraph(stale, desired)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}

	if len(diff.AddNodes) != 0 {
		t.Errorf("AddNodes = %v, want none", diff.AddNodes)
	}
	if len(diff.RemoveNodes) != 1 {
		t.Fatalf("RemoveNodes = %v, want exactly 1", diff.RemoveNodes)
	}
	if got := diff.RemoveNodes[0].ID; got != "test_stale" {
		t.Errorf("RemoveNodes[0].ID = %q, want %q", got, "test_stale")
	}
}

func TestApplyGraphRemoveNode(t *testing.T) {
	setupGraphTest(t)
	graph, err := CreateMediaGraph(testPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer func() {
		if err := graph.Destroy(); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	}()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph (add): %v", err)
	}
	if got := len(graph.nodesForTest()); got != 3 {
		t.Fatalf("nodes after add = %d, want 3", got)
	}
	if _, ok := graph.nodeRuntime.GetEntryById(testOutputNode); !ok {
		t.Fatalf("output entry missing after add")
	}

	reduced := testDesiredGraph()
	delete(reduced.Nodes, testOutputNode)
	reduced.Links = []LinkInfo{
		{ID: "test_link_input_cloner", SourcePadID: "test_input_src_0", SinkPadID: "test_cloner_sink_0"},
	}

	diff, err := DiffGraph(testDesiredGraph(), reduced)
	if err != nil {
		t.Fatalf("DiffGraph (remove): %v", err)
	}
	if len(diff.RemoveNodes) != 1 || diff.RemoveNodes[0].ID != testOutputNode {
		t.Fatalf("RemoveNodes = %v, want %q", diff.RemoveNodes, testOutputNode)
	}
	if len(diff.RemoveLinks) != 1 {
		t.Fatalf("RemoveLinks = %v, want exactly 1", diff.RemoveLinks)
	}

	if err := graph.ApplyGraph(reduced); err != nil {
		t.Fatalf("ApplyGraph (remove): %v", err)
	}

	if got := len(graph.nodesForTest()); got != 2 {
		t.Errorf("nodes after remove = %d, want 2", got)
	}
	if _, exists := graph.nodesForTest()[testOutputNode]; exists {
		t.Errorf("node %q still present after removal", testOutputNode)
	}
	for _, link := range graph.currentGraphLinks {
		if link.SinkPadID == "test_output_sink_0" {
			t.Errorf("link to removed node survived: %v", link)
		}
	}
	if _, err := graph.GetEntryByEntryID(testOutputNode); err == nil {
		t.Errorf("output entry %q still registered after node removal", testOutputNode)
	}
}
