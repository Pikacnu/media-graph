package graph

import (
	"testing"

	"live_tools/gst_utils/core"
)

// A node type with no registered spec must be rejected before construction, so
// a forgotten spec is a loud error rather than a silently unchecked node.
func TestValidateGraphRejectsUnknownSpec(t *testing.T) {
	setupGraphTest(t)

	graph := DesiredGraph{
		ID: "test_spec_pipeline",
		Nodes: map[string]NodeInfo{
			"spec_node": {ID: "spec_node", NodeType: "NoSuchType"},
		},
	}
	if err := validateGraph(graph); err == nil {
		t.Fatal("validateGraph accepted a node type with no spec, want error")
	}
}

// The spec caps how many pads of each direction a declaration may carry.
func TestValidateGraphRejectsPadCountAboveSpec(t *testing.T) {
	setupGraphTest(t)

	graph := DesiredGraph{
		ID: "test_spec_pipeline",
		Nodes: map[string]NodeInfo{
			"reg": {
				ID:       "reg",
				NodeType: "AudioRegulator",
				// OneToOneNode: two source pads is one too many.
				SourcePads: []PadInfo{{ID: "reg_src_0"}, {ID: "reg_src_1"}},
				SinkPads:   []PadInfo{{ID: "reg_sink_0"}},
			},
		},
	}
	if err := validateGraph(graph); err == nil {
		t.Fatal("validateGraph accepted two source pads on AudioRegulator, want error")
	}
}

// Zero means "none at all", not "unlimited": a sink-only node cannot declare a
// source pad.
func TestValidateGraphRejectsPadOnZeroSpec(t *testing.T) {
	setupGraphTest(t)

	graph := DesiredGraph{
		ID: "test_spec_pipeline",
		Nodes: map[string]NodeInfo{
			"sink": {
				ID:         "sink",
				NodeType:   "RTMPOutput",
				SourcePads: []PadInfo{{ID: "sink_src_0"}},
				SinkPads:   []PadInfo{{ID: "sink_sink_0"}},
			},
		},
	}
	if err := validateGraph(graph); err == nil {
		t.Fatal("validateGraph accepted a source pad on RTMPOutput, want error")
	}
}

// Unlimited is -1: Clone may fan out to any number of source pads, including
// several at once.
func TestValidateGraphAcceptsUnlimitedSrcPads(t *testing.T) {
	setupGraphTest(t)

	graph := DesiredGraph{
		ID: "test_spec_pipeline",
		Nodes: map[string]NodeInfo{
			"cloner": {
				ID:       "cloner",
				NodeType: "Clone",
				SourcePads: []PadInfo{
					{ID: "cloner_src_0"},
					{ID: "cloner_src_1"},
					{ID: "cloner_src_2"},
				},
				SinkPads: []PadInfo{{ID: "cloner_sink_0"}},
			},
		},
	}
	if err := validateGraph(graph); err != nil {
		t.Fatalf("validateGraph rejected unlimited source pads on Clone: %v", err)
	}
}

// PadCount.Allows is the single place the -1 / 0 / n semantics live.
func TestPadCountAllows(t *testing.T) {
	cases := []struct {
		name  string
		max   core.PadCount
		count int
		want  bool
	}{
		{"unlimited accepts zero", core.UnlimitedPads, 0, true},
		{"unlimited accepts many", core.UnlimitedPads, 7, true},
		{"zero accepts none", 0, 0, true},
		{"zero rejects one", 0, 1, false},
		{"exactly one accepts one", 1, 1, true},
		{"exactly one rejects two", 1, 2, false},
		{"at most two accepts two", 2, 2, true},
		{"at most two rejects three", 2, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.max.Allows(tc.count); got != tc.want {
				t.Errorf("Allows(%d) with max=%d = %v, want %v", tc.count, tc.max, got, tc.want)
			}
		})
	}
}
