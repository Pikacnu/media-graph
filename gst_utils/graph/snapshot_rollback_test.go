package graph

import (
	"testing"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// Snapshot must be a fixed point: diffing a snapshot against itself has nothing
// to report. This is what lets a snapshot be fed back to DiffGraph, and it is
// the property that makes a false "update" impossible on an unchanged graph.
func TestSnapshotIsFixedPoint(t *testing.T) {
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

	snapshot := mediaGraph.Snapshot()
	if snapshot.ID != testPipelineID {
		t.Errorf("Snapshot().ID = %q, want %q", snapshot.ID, testPipelineID)
	}
	if len(snapshot.Nodes) == 0 {
		t.Fatal("Snapshot() reported no nodes")
	}

	settled, err := DiffGraph(snapshot, snapshot)
	if err != nil {
		t.Fatalf("DiffGraph(snapshot, snapshot): %v", err)
	}
	if len(settled.AddNodes) != 0 || len(settled.RemoveNodes) != 0 ||
		len(settled.UpdatePropertiesNodes) != 0 || len(settled.AddPads) != 0 ||
		len(settled.AddLinks) != 0 ||
		len(settled.RemoveLinks) != 0 || len(settled.RemovePads) != 0 {
		t.Errorf("DiffGraph(snapshot, snapshot) is not empty: %+v", settled)
	}
}

// Snapshot must not report constructor-fixed (protected) properties, or an echo
// of the graph would look like an update on every round trip.
func TestSnapshotOmitsProtectedProperties(t *testing.T) {
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

	// The SourceInput node protects Caps/IsLive/StreamType/BufferSize.
	snapshot := mediaGraph.Snapshot()
	for id, node := range snapshot.Nodes {
		if node.NodeType != "SourceInput" {
			continue
		}
		for _, protected := range []string{"Caps", "IsLive", "StreamType", "BufferSize"} {
			if _, exists := node.Properties[protected]; exists {
				t.Errorf("node %q snapshot includes protected key %q", id, protected)
			}
		}
	}
}

// A rejected link must not leave any new link behind, and the graph must be back
// where it started. The caps preflight runs before the first Link call, and
// ApplyGraph rolls the graph back when the preflight refuses.
func TestApplyGraphRejectedLinkLeavesGraphUnchanged(t *testing.T) {
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

	// Establish a valid baseline graph.
	if err := mediaGraph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph(baseline): %v", err)
	}
	before := mediaGraph.Snapshot()

	// Now ask for a graph with an audio source wired into a fresh video sink.
	// The sink must be free, or validateGraph would reject the duplicate
	// incoming link before caps are ever considered.
	bad := testDesiredGraph()
	bad.Nodes["bad_src"] = NodeInfo{
		ID:         "bad_src",
		NodeType:   "AudioRegulator",
		Properties: map[string]interface{}{"TargetFormat": "audio/x-raw"},
		SourcePads: []PadInfo{{ID: "bad_src_src_0"}},
		SinkPads:   []PadInfo{{ID: "bad_src_sink_0"}},
	}
	bad.Nodes["bad_out"] = NodeInfo{
		ID:         "bad_out",
		NodeType:   "Output",
		Properties: map[string]interface{}{"caps": testOutputCaps},
		SinkPads:   []PadInfo{{ID: "bad_out_sink_0"}},
	}
	bad.Links = append(bad.Links, LinkInfo{
		ID:          "bad_link",
		SourcePadID: "bad_src_src_0",
		SinkPadID:   "bad_out_sink_0",
	})

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(bad); err == nil {
		t.Fatal("ApplyGraph accepted an incompatible link, want an error")
	}

	// The graph must be exactly as it was: both new nodes and the bad link gone.
	after := mediaGraph.Snapshot()
	for _, id := range []string{"bad_src", "bad_out"} {
		if _, exists := after.Nodes[id]; exists {
			t.Errorf("node %q survived a rejected apply, want it rolled back", id)
		}
	}
	for _, link := range after.Links {
		if link.ID == "bad_link" {
			t.Errorf("bad_link survived a rejected apply: %v", link)
		}
	}
	if len(after.Nodes) != len(before.Nodes) {
		t.Errorf("node count after rollback = %d, want %d", len(after.Nodes), len(before.Nodes))
	}
	if len(after.Links) != len(before.Links) {
		t.Errorf("link count after rollback = %d, want %d", len(after.Links), len(before.Links))
	}

	// And the graph must still be usable: a second, valid apply must work.
	if err := mediaGraph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph after rollback: %v", err)
	}
	if err := mediaGraph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState after rollback: %v", err)
	}
}

// preflightLinks must not reject a legitimate rewire: moving a src pad from one
// sink to another means the pad is still linked when the preflight runs, and
// CanLink reports false for any linked pad.
func TestApplyGraphAllowsRewire(t *testing.T) {
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

	// Establish a valid baseline graph.
	if err := mediaGraph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph(baseline): %v", err)
	}

	// Add a Discard node and move the cloner's spare output onto it, then move
	// that same output to a second Discard. The second move rewires a pad that
	// is linked at preflight time.
	rewired := testDesiredGraph()
	rewired.Nodes["rewire_a"] = NodeInfo{
		ID:       "rewire_a",
		NodeType: "Discard",
		SinkPads: []PadInfo{{ID: "rewire_a_sink_0"}},
	}
	rewired.Links = append(rewired.Links, LinkInfo{
		SourcePadID: "test_cloner_src_0",
		SinkPadID:   "rewire_a_sink_0",
	})
	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(rewired); err != nil {
		t.Fatalf("ApplyGraph(step1): %v", err)
	}

	// Same src pad, different sink: this is the rewire case.
	rewired.Nodes["rewire_b"] = NodeInfo{
		ID:       "rewire_b",
		NodeType: "Discard",
		SinkPads: []PadInfo{{ID: "rewire_b_sink_0"}},
	}
	for i := range rewired.Links {
		if rewired.Links[i].SourcePadID == "test_cloner_src_0" {
			rewired.Links[i].SinkPadID = "rewire_b_sink_0"
		}
	}
	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(rewired); err != nil {
		t.Fatalf("ApplyGraph(rewire) = %v, want success", err)
	}

	final := mediaGraph.Snapshot()
	found := false
	for _, link := range final.Links {
		if link.SourcePadID == "test_cloner_src_0" {
			found = true
			if link.SinkPadID != "rewire_b_sink_0" {
				t.Errorf("cloner_src_0 -> %s, want rewire_b_sink_0", link.SinkPadID)
			}
		}
	}
	if !found {
		t.Error("cloner_src_0 has no link after the rewire")
	}
}

// Snapshot must deep-copy, so a caller cannot mutate the live graph through it.
func TestSnapshotIsIndependentCopy(t *testing.T) {
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

	snapshot := mediaGraph.Snapshot()
	delete(snapshot.Nodes, testOutputNode)
	snapshot.Links = nil
	for id, node := range snapshot.Nodes {
		node.Properties["Injected"] = true
		snapshot.Nodes[id] = node
	}

	second := mediaGraph.Snapshot()
	if _, exists := second.Nodes[testOutputNode]; !exists {
		t.Error("deleting from a snapshot removed a live node")
	}
	if len(second.Links) == 0 {
		t.Error("clearing snapshot links cleared the live links")
	}
	for id, node := range second.Nodes {
		if _, exists := node.Properties["Injected"]; exists {
			t.Errorf("node %q picked up a property written into a snapshot", id)
		}
	}
}

// The old entry-lookup helper is gone; the replacement must still find a live
// entry and reject an unknown one.
func TestGetEntryByEntryID(t *testing.T) {
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

	entry, err := mediaGraph.GetEntryByEntryID(testSourceNode)
	if err != nil {
		t.Fatalf("GetEntryByEntryID(%q): %v", testSourceNode, err)
	}
	inputEntry, ok := entry.(core.InputRegistryEntry)
	if !ok {
		t.Fatalf("entry %q is %T, want core.InputRegistryEntry", testSourceNode, entry)
	}
	handle, err := core.GetInputHandle[[]byte](inputEntry)
	if err != nil {
		t.Fatalf("GetInputHandle: %v", err)
	}
	if handle.DropType() != core.DropNone {
		t.Errorf("handle DropType = %v, want DropNone (the entry's policy)", handle.DropType())
	}

	if _, err := mediaGraph.GetEntryByEntryID("no_such_entry"); err == nil {
		t.Error("GetEntryByEntryID(unknown) = nil error, want an error")
	}
}
