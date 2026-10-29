package graph

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

type Pipeline struct {
	graph      *mediaGraphImpl
	sourceNode string
	outputNode string
}

const (
	testPipelineID = "test_Pipeline"
	testSourceNode = "test_input"
	testOutputNode = "test_output"

	// testOutputCaps is the concrete format the test Output node's capsfilter
	// negotiates. It must be non-empty: CapsFromString("") yields EMPTY caps,
	// which the capsfilter cannot negotiate.
	testOutputCaps = "video/x-raw"
)

func testDesiredGraph() DesiredGraph {
	return DesiredGraph{
		ID: testPipelineID,
		Nodes: map[string]NodeInfo{
			"test_input":  {ID: "test_input", NodeType: "SourceInput", Properties: map[string]interface{}{}, SourcePads: []core.PadInfo{{ID: "test_input_src_0"}}},
			"test_cloner": {ID: "test_cloner", NodeType: "Clone", Properties: map[string]interface{}{}, SourcePads: []core.PadInfo{{ID: "test_cloner_src_0"}, {ID: "test_cloner_src_1"}}, SinkPads: []core.PadInfo{{ID: "test_cloner_sink_0"}}},
			"test_output": {ID: "test_output", NodeType: "Output", Properties: map[string]interface{}{"caps": testOutputCaps}, SinkPads: []core.PadInfo{{ID: "test_output_sink_0"}}},
		},
		Links: []LinkInfo{
			{ID: "test_link_input_cloner", SourcePadID: "test_input_src_0", SinkPadID: "test_cloner_sink_0"},
			{ID: "test_link_cloner_output", SourcePadID: "test_cloner_src_1", SinkPadID: "test_output_sink_0"},
		},
	}
}

func CreateTestPipeline() (*Pipeline, error) {
	graph, err := CreateMediaGraph(testPipelineID)
	if err != nil {
		return nil, err
	}

	// ApplyGraph diffs internally, so the caller sends the desired graph.
	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		return nil, fmt.Errorf("failed to apply graph: %w", err)
	}
	if err := graph.SetState(gst.State(gst.StatePlaying)); err != nil {
		return nil, err
	}

	return &Pipeline{
		graph:      graph,
		sourceNode: testSourceNode,
		outputNode: testOutputNode,
	}, nil
}

func (p *Pipeline) Push(data []byte) error {
	entry, ok := p.graph.nodeRuntime.GetEntryById(p.sourceNode)
	if !ok {
		return fmt.Errorf("source entry %q not found", p.sourceNode)
	}
	inputEntry, ok := entry.(core.InputRegistryEntry)
	if !ok {
		return fmt.Errorf("entry %q does not accept input", p.sourceNode)
	}
	handle, err := core.GetInputHandle[[]byte](inputEntry)
	if err != nil {
		return err
	}
	return handle.Send(data)
}

func (p *Pipeline) OutputSubscriber(id string, bufferSize int) (<-chan []byte, error) {
	entry, ok := p.graph.nodeRuntime.GetEntryById(p.outputNode)
	if !ok {
		return nil, fmt.Errorf("output entry %q not found", p.outputNode)
	}
	outputEntry, ok := entry.(core.OutputRegistryEntry)
	if !ok {
		return nil, fmt.Errorf("entry %q does not publish buffers (%T)", p.outputNode, entry)
	}
	handle, err := core.GetOutputHandle[[]byte](outputEntry, id, bufferSize)
	if err != nil {
		return nil, err
	}
	return handle.RecvChan(), nil
}

func (p *Pipeline) Destroy() error {
	return p.graph.Destroy()
}
