package main

import "live_tools/gst_utils/graph"

func DemoGraph() graph.DesiredGraph {
	return graph.DesiredGraph{
		ID: "stream-demo",
		Nodes: map[string]graph.NodeInfo{
			"demo_input": {
				ID: "demo_input", NodeType: "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-raw,format=RGB,width=2,height=2,framerate=1/1", "is_live": true, "buffer_size": 64},
				SourcePads: []graph.PadInfo{{ID: "demo_input_src_0"}},
			},
			"demo_output": {
				ID: "demo_output", NodeType: "Output",
				Properties: map[string]interface{}{"caps": "video/x-raw,format=RGB,width=2,height=2,framerate=1/1", "buffer_size": 8},
				SinkPads:   []graph.PadInfo{{ID: "demo_output_sink_0"}},
			},
		},
		Links: []graph.LinkInfo{{
			ID: "demo_input_output", SourcePadID: "demo_input_src_0", SinkPadID: "demo_output_sink_0",
		}},
	}
}
