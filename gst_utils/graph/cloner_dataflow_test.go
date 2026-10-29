package graph

import (
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

const (
	clonerBranchPipelineID = "cloner_branch_pipeline"
	clonerNodeID           = "cloner_node"
	clonerOutAID           = "cloner_out_a"
	clonerOutBID           = "cloner_out_b"
)

// clonerBranchDesiredGraph is the proven ContainerParse chain with a Clone node
// on the parser's declared pad, fanning the decoded track into two outputs.
//
// The parser's own Output node and its link are removed: a pad may only have one
// outgoing link, so the cloner takes that place.
func clonerBranchDesiredGraph() DesiredGraph {
	graph := containerParseDesiredGraph()
	graph.ID = clonerBranchPipelineID
	delete(graph.Nodes, containerParseOutputNode)

	links := graph.Links[:0]
	for _, link := range graph.Links {
		if link.ID != "cp_link_parse_output" {
			links = append(links, link)
		}
	}
	graph.Links = links

	graph.Nodes[clonerNodeID] = NodeInfo{
		ID:       clonerNodeID,
		NodeType: "Clone",
		SourcePads: []core.PadInfo{
			{ID: clonerNodeID + "_src_0"},
			{ID: clonerNodeID + "_src_1"},
		},
		SinkPads: []core.PadInfo{{ID: clonerNodeID + "_sink_0"}},
	}
	for _, outID := range []string{clonerOutAID, clonerOutBID} {
		graph.Nodes[outID] = NodeInfo{
			ID:         outID,
			NodeType:   "Output",
			Properties: map[string]interface{}{"caps": "video/x-raw"},
			SinkPads:   []core.PadInfo{{ID: outID + "_sink_0"}},
		}
	}

	graph.Links = append(graph.Links,
		LinkInfo{ID: "l_parse_clone", SourcePadID: containerParseParseNode + "_src_0", SinkPadID: clonerNodeID + "_sink_0"},
		LinkInfo{ID: "l_clone_a", SourcePadID: clonerNodeID + "_src_0", SinkPadID: clonerOutAID + "_sink_0"},
		LinkInfo{ID: "l_clone_b", SourcePadID: clonerNodeID + "_src_1", SinkPadID: clonerOutBID + "_sink_0"},
	)
	return graph
}

// TestCloneNodeFansOutRealMedia fans a real decoded track through the cloner
// into two outputs.
//
// This is what validates the cloner's internal wiring, which no unit test can:
// the tee pad has to reach the multiqueue sink pad, and each branch ghost pad
// has to reach the paired multiqueue source pad. If either link is missing the
// bytes never arrive.
//
// Both branches are linked. An unlinked branch is a different case: its
// multiqueue queue has no consumer, so it fills up and the tee blocks upstream.
func TestCloneNodeFansOutRealMedia(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(clonerBranchPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(clonerBranchDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := mediaGraph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	subA, err := subscribeOutput(mediaGraph, clonerOutAID, "cloner_a", 256)
	if err != nil {
		t.Fatalf("subscribe output A: %v", err)
	}
	subB, err := subscribeOutput(mediaGraph, clonerOutBID, "cloner_b", 256)
	if err != nil {
		t.Fatalf("subscribe output B: %v", err)
	}

	if err := pushSource(mediaGraph, containerParseSourceNode, buildContainerBytes(t)); err != nil {
		t.Fatalf("push source: %v", err)
	}

	gotA, gotB := 0, 0
	deadline := time.After(20 * time.Second)
	for gotA == 0 || gotB == 0 {
		select {
		case data, ok := <-subA:
			if !ok {
				t.Fatalf("branch A channel closed (A=%d B=%d)", gotA, gotB)
			}
			if len(data) > 0 {
				gotA++
			}
		case data, ok := <-subB:
			if !ok {
				t.Fatalf("branch B channel closed (A=%d B=%d)", gotA, gotB)
			}
			if len(data) > 0 {
				gotB++
			}
		case <-deadline:
			t.Fatalf("timed out: branch A got %d buffers, branch B got %d", gotA, gotB)
		}
	}
	t.Logf("both branches received data (A=%d B=%d)", gotA, gotB)
}
