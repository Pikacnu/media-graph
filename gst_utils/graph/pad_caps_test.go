package graph

import (
	"testing"

	"live_tools/gst_utils/core"
)

// A live graph must be able to report what a pad can carry, because media types
// are settled by caps negotiation and are NOT declared in NodeSpec. The frontend
// needs this to know whether a connection is even possible.
func TestMediaGraphReportsPadCaps(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(testPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer func() {
		if err := mediaGraph.Destroy(); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	}()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}

	// The Output node's sink is behind a capsfilter pinned to video/x-raw, so its
	// reported caps must be that, not ANY. This is the property that makes the
	// pad the authority instead of a hand-written declaration.
	caps, err := mediaGraph.PadCaps("test_output_sink_0")
	if err != nil {
		t.Fatalf("PadCaps(test_output_sink_0): %v", err)
	}
	if caps == "" || caps == "ANY" {
		t.Errorf("PadCaps(test_output_sink_0) = %q, want the negotiated video/x-raw", caps)
	}

	if _, err := mediaGraph.PadCaps("no_such_pad"); err == nil {
		t.Error("PadCaps(unknown) = nil error, want an error")
	}
}

// CanLink is the frontend's "can I draw this wire?" check. It must reject a pair
// that Link would reject, without touching the graph.
func TestMediaGraphCanLinkReflectsReality(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(testPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer func() {
		if err := mediaGraph.Destroy(); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	}()

	// Add a Discard node with NO link, so cloner_src_0 stays free. In
	// testDesiredGraph only cloner_src_1 is wired to the Output node. This is
	// the only ApplyGraph in this test: applying twice would re-register the
	// SourceInput entry and fail.
	withDiscard := testDesiredGraph()
	withDiscard.Nodes["test_discard"] = NodeInfo{
		ID:       "test_discard",
		NodeType: "Discard",
		SinkPads: []PadInfo{{ID: "test_discard_sink_0"}},
	}
	diff, err := DiffGraph(DesiredGraph{Nodes: map[string]NodeInfo{}}, withDiscard)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}
	if diff == nil {
		t.Fatal("DiffGraph: empty diff for a non-empty desired graph")
	}
	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(withDiscard); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}

	// CanLink means "can link NOW", which is exactly what gst_pad_link checks.
	// An already-linked pair reports false, the same way Link would fail with
	// PadLinkWasLinked.
	if ok, err := mediaGraph.CanLink("test_cloner_src_1", "test_output_sink_0"); err != nil {
		t.Fatalf("CanLink(linked): %v", err)
	} else if ok {
		t.Error("CanLink(already-linked pair) = true, want false (Link would fail)")
	}

	// A compatible pair that is not yet linked reports true: the cloner's spare
	// raw-video output into the Discard node's free sink.
	if ok, err := mediaGraph.CanLink("test_cloner_src_0", "test_discard_sink_0"); err != nil {
		t.Fatalf("CanLink(free): %v", err)
	} else if !ok {
		t.Error("CanLink(cloner.src_0 -> discard.sink_0) = false, want true")
	}

	if _, err := mediaGraph.CanLink("missing_src", "test_output_sink_0"); err == nil {
		t.Error("CanLink(unknown src) = nil error, want an error")
	}
	if _, err := mediaGraph.CanLink("test_cloner_src_1", "missing_sink"); err == nil {
		t.Error("CanLink(unknown sink) = nil error, want an error")
	}
}

// A link the caps cannot satisfy must be refused BEFORE anything is linked, so a
// bad connection cannot leave the non-atomic ApplyGraph half-applied.
func TestApplyGraphRefusesIncompatibleLink(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(testPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer func() {
		if err := mediaGraph.Destroy(); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	}()

	// Build a graph whose only link is audio -> the Output node's video sink.
	bad := DesiredGraph{
		ID: testPipelineID,
		Nodes: map[string]NodeInfo{
			"bad_src": {
				ID:         "bad_src",
				NodeType:   "AudioRegulator",
				Properties: map[string]interface{}{},
				SourcePads: []PadInfo{{ID: "bad_src_src_0"}},
				SinkPads:   []PadInfo{{ID: "bad_src_sink_0"}},
			},
			"bad_out": {
				ID:         "bad_out",
				NodeType:   "Output",
				Properties: map[string]interface{}{"caps": testOutputCaps},
				SinkPads:   []PadInfo{{ID: "bad_out_sink_0"}},
			},
		},
		Links: []LinkInfo{
			{ID: "bad_link", SourcePadID: "bad_src_src_0", SinkPadID: "bad_out_sink_0"},
		},
	}

	diff, err := DiffGraph(DesiredGraph{Nodes: map[string]NodeInfo{}}, bad)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}
	if diff == nil {
		t.Fatal("DiffGraph: empty diff for a non-empty desired graph")
	}

	// validateGraph cannot catch this: both pads exist and the counts are legal.
	// The refusal must come from the CanLink pre-flight inside ApplyGraph.
	if err := mediaGraph.ApplyGraph(bad); err == nil {
		t.Fatal("ApplyGraph accepted an audio->video link, want a caps error")
	}

	// The nodes were added before the link step, so confirm the graph did not
	// record a link for the rejected connection.
	for _, link := range mediaGraph.currentGraphLinks {
		if link.ID == "bad_link" {
			t.Error("bad_link was recorded despite the caps mismatch")
		}
	}
}

// PadCapsString must be safe on a nil pad.
func TestPadCapsStringNilSafe(t *testing.T) {
	if got := core.PadCapsString(nil); got != "" {
		t.Errorf("PadCapsString(nil) = %q, want empty", got)
	}
}
