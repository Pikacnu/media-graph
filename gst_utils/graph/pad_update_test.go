package graph

import (
	"testing"

	"live_tools/gst_utils/core"
)

// padUpdateGraph is a two-node graph whose parser pad already carries a track
// selector, so changing that selector is a pad update the node has to reconcile.
func padUpdateGraph(id, trackName string) DesiredGraph {
	return DesiredGraph{
		ID: id,
		Nodes: map[string]NodeInfo{
			"pu_src": {
				ID:         "pu_src",
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: "pu_src_src_0"}},
			},
			"pu_parse": {
				ID:       "pu_parse",
				NodeType: "ContainerParse",
				SourcePads: []core.PadInfo{{
					ID: "pu_parse_src_0",
					Properties: map[string]any{
						"track_name": trackName,
						"track_id":   "",
						"media_type": "video/x-h264",
					},
				}},
				SinkPads: []core.PadInfo{{ID: "pu_parse_sink_0"}},
			},
		},
		Links: []LinkInfo{{
			ID:          "pu_link",
			SourcePadID: "pu_src_src_0",
			SinkPadID:   "pu_parse_sink_0",
		}},
	}
}

func newPadUpdateTestGraph(t *testing.T, id string) *mediaGraphImpl {
	t.Helper()
	mediaGraph, err := CreateMediaGraph(id)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	t.Cleanup(func() { _ = mediaGraph.Destroy() })
	return mediaGraph
}

// TestDiffGraphEmitsUpdatedPadProperties is the shape test for the standalone
// pad-update path: a pad that kept its ID but changed its selector goes into
// UpdatePads, and the node it belongs to is NOT reported as a node update.
func TestDiffGraphEmitsUpdatedPadProperties(t *testing.T) {
	setupGraphTest(t)

	diff, err := DiffGraph(padUpdateGraph("pu_diff", "Camera A"), padUpdateGraph("pu_diff", "Camera B"))
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}

	if len(diff.UpdatePads) != 1 {
		t.Fatalf("expected exactly one pad update, got %d: %+v", len(diff.UpdatePads), diff.UpdatePads)
	}
	updated := diff.UpdatePads[0]
	if updated.ID != "pu_parse_src_0" {
		t.Fatalf("expected pad pu_parse_src_0, got %q", updated.ID)
	}
	if updated.NodeID != "pu_parse" {
		t.Fatalf("pad update must name its owning node, got %q", updated.NodeID)
	}
	if got, _ := updated.Properties["track_name"].(string); got != "Camera B" {
		t.Fatalf("pad update must carry the NEW properties, got track_name=%q", got)
	}
	// A pad update is not a structural change, so no node needs re-property-ing.
	if len(diff.UpdatePropertiesNodes) != 0 {
		t.Fatalf("pad change must not also update node properties: %+v", diff.UpdatePropertiesNodes)
	}
	// And it is not an add/remove either: the pad keeps its identity.
	if len(diff.AddPads) != 0 || len(diff.RemovePads) != 0 {
		t.Fatalf("pad change must not add or remove pads: add=%+v remove=%+v",
			diff.AddPads, diff.RemovePads)
	}
}

// TestDiffGraphIgnoresIdenticalPadProperties guards idempotence: echoing the
// same declaration back must produce an empty diff.
func TestDiffGraphIgnoresIdenticalPadProperties(t *testing.T) {
	setupGraphTest(t)

	diff, err := DiffGraph(padUpdateGraph("pu_same", "Camera A"), padUpdateGraph("pu_same", "Camera A"))
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}
	if len(diff.UpdatePads) != 0 {
		t.Fatalf("identical pad properties must not produce an update: %+v", diff.UpdatePads)
	}
}

// TestDiffGraphTreatsNilAndEmptyPadPropertiesAsEqual covers the asymmetry that
// makes a never-touched pad look changed: a caller declares a pad with no
// properties, the registry reads back an empty map, and a naive comparison
// reports a difference on every round trip.
func TestDiffGraphTreatsNilAndEmptyPadPropertiesAsEqual(t *testing.T) {
	setupGraphTest(t)

	withEmpty := padUpdateGraph("pu_nil_empty", "Camera A")
	node := withEmpty.Nodes["pu_parse"]
	node.SourcePads = []core.PadInfo{{ID: "pu_parse_src_0", Properties: map[string]any{}}}
	withEmpty.Nodes["pu_parse"] = node

	withNil := padUpdateGraph("pu_nil_empty", "Camera A")
	node = withNil.Nodes["pu_parse"]
	node.SourcePads = []core.PadInfo{{ID: "pu_parse_src_0"}}
	withNil.Nodes["pu_parse"] = node

	diff, err := DiffGraph(withEmpty, withNil)
	if err != nil {
		t.Fatalf("DiffGraph: %v", err)
	}
	if len(diff.UpdatePads) != 0 {
		t.Fatalf("nil and empty pad properties must compare equal, got %d updates", len(diff.UpdatePads))
	}
}

// TestDiffGraphRejectsPadUpdateOnUnsupportedType pins the guard: a node type
// that never declared UpdatablePads cannot silently accept a pad change.
func TestDiffGraphRejectsPadUpdateOnUnsupportedType(t *testing.T) {
	setupGraphTest(t)

	applied := padUpdateGraph("pu_reject", "Camera A")
	desired := padUpdateGraph("pu_reject", "Camera B")
	// Clone does not declare UpdatablePads.
	node := desired.Nodes["pu_parse"]
	node.NodeType = "Clone"
	desired.Nodes["pu_parse"] = node
	appliedNode := applied.Nodes["pu_parse"]
	appliedNode.NodeType = "Clone"
	applied.Nodes["pu_parse"] = appliedNode

	if _, err := DiffGraph(applied, desired); err == nil {
		t.Fatal("expected DiffGraph to reject a pad update on a type without UpdatablePads")
	}
}

// TestApplyGraphUpdatesPadProperties proves the apply path really reaches the
// node: the pad's selector is re-recorded in the registry (what Snapshot reads)
// and the pad's external link survives.
func TestApplyGraphUpdatesPadProperties(t *testing.T) {
	setupGraphTest(t)
	mediaGraph := newPadUpdateTestGraph(t, "pu_apply")

	if err := mediaGraph.ApplyGraph(padUpdateGraph("pu_apply", "Camera A")); err != nil {
		t.Fatalf("ApplyGraph(initial): %v", err)
	}
	if err := mediaGraph.ApplyGraph(padUpdateGraph("pu_apply", "Camera B")); err != nil {
		t.Fatalf("ApplyGraph(pad change): %v", err)
	}

	snapshot := mediaGraph.Snapshot()
	pad, ok := findPadByID(snapshot.Nodes["pu_parse"].SourcePads, "pu_parse_src_0")
	if !ok {
		t.Fatalf("pad pu_parse_src_0 missing from snapshot")
	}
	if got, _ := pad.Properties["track_name"].(string); got != "Camera B" {
		t.Fatalf("snapshot reports track_name=%q, want %q", got, "Camera B")
	}
	if _, ok := findLinkByEndpoints(snapshot.Links, "pu_src_src_0", "pu_parse_sink_0"); !ok {
		t.Fatalf("pad update must not drop the link: %+v", snapshot.Links)
	}
}

// TestApplyGraphPadUpdateIsNotReportedAgain is the anti-oscillation guard: after
// applying a pad change, diffing the graph against its own applied state must be
// empty. Without the registry write in UpdatePad, the snapshot would keep
// reporting the old selector and every round trip would re-apply the update.
func TestApplyGraphPadUpdateIsNotReportedAgain(t *testing.T) {
	setupGraphTest(t)
	mediaGraph := newPadUpdateTestGraph(t, "pu_settle")

	if err := mediaGraph.ApplyGraph(padUpdateGraph("pu_settle", "Camera A")); err != nil {
		t.Fatalf("ApplyGraph(initial): %v", err)
	}
	if err := mediaGraph.ApplyGraph(padUpdateGraph("pu_settle", "Camera B")); err != nil {
		t.Fatalf("ApplyGraph(pad change): %v", err)
	}

	settled, err := DiffGraph(mediaGraph.Snapshot(), mediaGraph.GetAppliedGraph())
	if err != nil {
		t.Fatalf("DiffGraph(settled): %v", err)
	}
	if len(settled.UpdatePads) != 0 {
		t.Fatalf("pad change is still reported after apply: %+v", settled.UpdatePads)
	}
}

func findPadByID(pads []core.PadInfo, id string) (core.PadInfo, bool) {
	for _, pad := range pads {
		if pad.ID == id {
			return pad, true
		}
	}
	return core.PadInfo{}, false
}

func findLinkByEndpoints(links []LinkInfo, sourcePadID, sinkPadID string) (LinkInfo, bool) {
	for _, link := range links {
		if link.SourcePadID == sourcePadID && link.SinkPadID == sinkPadID {
			return link, true
		}
	}
	return LinkInfo{}, false
}
