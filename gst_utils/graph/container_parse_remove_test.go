package graph

import (
	"slices"
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestContainerParseRemoveSrcPadRetiresIdleSlot drives the PadsToRemove path on
// a declared pad that no track ever claimed. There is nothing to unbind and
// nothing to notify, so the pad must simply stop being reported: GetSrcIDs is
// what the diff is built from, and a retired id left in the list would make the
// graph try to link a pad that is gone.
func TestContainerParseRemoveSrcPadRetiresIdleSlot(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(containerParsePipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(containerParseDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}

	// No bytes are pushed, so the declared pad is idle: parsebin has no src pad
	// for it yet.
	parseNode, ok := mediaGraph.nodesForTest()[containerParseParseNode].(core.OneToManyNode)
	if !ok {
		t.Fatalf("node %q is not a OneToManyNode", containerParseParseNode)
	}

	declaredID := containerParseParseNode + "_src_0"
	if got := parseNode.GetSrcIDs(); !slices.Contains(got, declaredID) {
		t.Fatalf("GetSrcIDs = %v, want to include the declared pad %q", got, declaredID)
	}

	if err := parseNode.RemoveSrcPad(declaredID); err != nil {
		t.Fatalf("RemoveSrcPad(%q): %v", declaredID, err)
	}
	if got := parseNode.GetSrcIDs(); slices.Contains(got, declaredID) {
		t.Fatalf("GetSrcIDs = %v, want the retired pad %q to be gone", got, declaredID)
	}
	if err := parseNode.RemoveSrcPad(declaredID); err == nil {
		t.Fatal("second RemoveSrcPad returned nil, want an error")
	}
}

// TestContainerParseRemoveSrcPadUnknownID checks that retiring an id the node
// never handed out is reported rather than silently ignored.
func TestContainerParseRemoveSrcPadUnknownID(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(containerParsePipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(containerParseDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}

	parseNode, ok := mediaGraph.nodesForTest()[containerParseParseNode].(core.OneToManyNode)
	if !ok {
		t.Fatalf("node %q is not a OneToManyNode", containerParseParseNode)
	}
	if err := parseNode.RemoveSrcPad("cp_parse_src_99"); err == nil {
		t.Fatal("RemoveSrcPad(unknown) returned nil, want an error")
	}
}

// TestContainerParseRemoveSrcPadSwapsBoundSlot is the full PadsToRemove path
// with real media: the declared pad is bound to a track, then retired. The
// replacement keeps the track's selector, so the same track can bind to it and
// the graph can re-link from the diff.
//
// Retiring a bound slot pushes EOS, and PushEvent is synchronous. That is why
// RemoveSrcPad unlinks parsebin before pushing: while parsebin still feeds the
// slot, the push contends with the streaming thread and never returns. The test
// runs the call on a goroutine and fails on timeout, so a regression in that
// ordering shows up as a failure instead of wedging the suite.
func TestContainerParseRemoveSrcPadSwapsBoundSlot(t *testing.T) {
	setupGraphTest(t)
	mediaGraph, err := CreateMediaGraph(containerParsePipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(containerParseDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := mediaGraph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	// Subscribe before pushing: the announcement is published once.
	trackSub, err := subscribeTrackData(mediaGraph, containerParseParseNode, "cpr_sub", 32)
	if err != nil {
		t.Fatalf("subscribe track data: %v", err)
	}
	outputSub, err := subscribeOutput(mediaGraph, containerParseOutputNode, "cpr_out", 256)
	if err != nil {
		t.Fatalf("subscribe output: %v", err)
	}
	go func() {
		for range outputSub {
		}
	}()

	if err := pushSource(mediaGraph, containerParseSourceNode, buildContainerBytes(t)); err != nil {
		t.Fatalf("push source: %v", err)
	}

	var bound core.TrackData
	deadline := time.After(15 * time.Second)
wait:
	for {
		select {
		case data, ok := <-trackSub:
			if !ok {
				t.Fatal("track data channel closed before a track was bound")
			}
			if data.Error != nil {
				t.Fatalf("track data error: %v", data.Error)
			}
			if data.ID != "" {
				bound = data
				break wait
			}
		case <-deadline:
			t.Fatal("timed out waiting for a bound track")
		}
	}
	t.Logf("bound pad = %q", bound.ID)

	parseNode, ok := mediaGraph.nodesForTest()[containerParseParseNode].(core.OneToManyNode)
	if !ok {
		t.Fatalf("node %q is not a OneToManyNode", containerParseParseNode)
	}
	if got := parseNode.GetSrcIDs(); !slices.Contains(got, bound.ID) {
		t.Fatalf("GetSrcIDs = %v, want to include the bound pad %q", got, bound.ID)
	}

	done := make(chan error, 1)
	go func() { done <- parseNode.RemoveSrcPad(bound.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RemoveSrcPad(%q): %v", bound.ID, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RemoveSrcPad did not return: the EOS push blocked, so parsebin was not unlinked first")
	}

	after := parseNode.GetSrcIDs()
	if slices.Contains(after, bound.ID) {
		t.Fatalf("GetSrcIDs = %v, want the retired pad %q to be gone", after, bound.ID)
	}
	if len(after) == 0 {
		t.Fatal("GetSrcIDs is empty after the swap, want a replacement pad")
	}
	t.Logf("retired %q, now %v", bound.ID, after)
}
