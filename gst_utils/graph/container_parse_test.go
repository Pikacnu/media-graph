package graph

import (
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

const (
	containerParsePipelineID = "container_parse_pipeline"

	containerParseSourceNode = "cp_src"
	containerParseParseNode  = "cp_parse"
	containerParseOutputNode = "cp_out"

	containerParseTrackName = "main-video"
)

// buildContainerBytes produces a real Matroska stream with one tagged video
// track. The title travels inside the container, so parsebin can report it and
// the declared pad selector can match it.
func buildContainerBytes(t *testing.T) []byte {
	t.Helper()

	description := "matroskamux name=m ! appsink name=sink sync=false " +
		"videotestsrc num-buffers=30 ! video/x-raw,width=64,height=48,framerate=30/1 ! " +
		"taginject tags=\"title=" + containerParseTrackName + "\" ! queue ! m.video_0"

	element, err := gst.ParseLaunch(description)
	if err != nil {
		t.Fatalf("ParseLaunch(matroskamux): %v", err)
	}
	pipeline := element.(gst.Pipeline)
	sink, ok := pipeline.GetByName("sink").(gstapp.AppSink)
	if !ok || sink == nil {
		t.Fatal("appsink not found in mux pipeline")
	}
	sink.SetEmitSignals(false)

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("mux pipeline SetState failed: %v", result)
	}
	defer pipeline.SetState(gst.State(gst.StateNull))

	var out []byte
	for {
		sample := sink.TryPullSample(gst.ClockTime(5 * time.Second))
		if sample == nil {
			break
		}
		buffer := sample.GetBuffer()
		if buffer == nil {
			continue
		}
		mapped, ok := buffer.Map(gst.MapFlags(gst.MapRead))
		if !ok {
			continue
		}
		out = append(out, mapped.Data()...)
		mapped.Unmap()
	}

	if len(out) == 0 {
		t.Fatal("mux pipeline produced no bytes")
	}
	return out
}

func containerParseDesiredGraph() DesiredGraph {
	return DesiredGraph{
		ID: containerParsePipelineID,
		Nodes: map[string]NodeInfo{
			containerParseSourceNode: {
				ID:         containerParseSourceNode,
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: containerParseSourceNode + "_src_0"}},
			},
			containerParseParseNode: {
				ID:       containerParseParseNode,
				NodeType: "ContainerParse",
				SourcePads: []core.PadInfo{{
					ID: containerParseParseNode + "_src_0",
					Properties: map[string]interface{}{
						"track_name": containerParseTrackName,
						"track_id":   "1",
						"media_type": "video/x-raw",
					},
				}},
				SinkPads: []core.PadInfo{{ID: containerParseParseNode + "_sink_0"}},
			},
			containerParseOutputNode: {
				ID:         containerParseOutputNode,
				NodeType:   "Output",
				Properties: map[string]interface{}{"caps": "video/x-raw"},
				SinkPads:   []core.PadInfo{{ID: containerParseOutputNode + "_sink_0"}},
			},
		},
		Links: []LinkInfo{
			{
				ID:          "cp_link_source_parse",
				SourcePadID: containerParseSourceNode + "_src_0",
				SinkPadID:   containerParseParseNode + "_sink_0",
			},
			{
				ID:          "cp_link_parse_output",
				SourcePadID: containerParseParseNode + "_src_0",
				SinkPadID:   containerParseOutputNode + "_sink_0",
			},
		},
	}
}

// TestContainerParsePipeline runs the whole chain with real media:
// SourceInput -> ContainerParse(parsebin) -> Output(appsink).
// It proves that a declared, targetless ghost pad can be linked before the
// track exists, and that the pad-added callback binds the internal multiqueue
// src pad to it so bytes reach the downstream output.
func TestContainerParsePipeline(t *testing.T) {
	setupGraphTest(t)

	containerBytes := buildContainerBytes(t)
	t.Logf("container bytes = %d", len(containerBytes))
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

	outputChan, err := subscribeOutput(mediaGraph, containerParseOutputNode, "cp_test_subscriber", 256)
	if err != nil {
		t.Fatalf("subscribe output: %v", err)
	}

	if err := pushSource(mediaGraph, containerParseSourceNode, containerBytes); err != nil {
		t.Fatalf("push source: %v", err)
	}

	deadline := time.After(15 * time.Second)
	received := 0
	var firstLen int
	for received < 5 {
		select {
		case data, ok := <-outputChan:
			if !ok {
				t.Fatalf("output channel closed after %d buffers", received)
			}
			if received == 0 {
				firstLen = len(data)
			}
			received++
		case <-deadline:
			t.Fatalf("timed out: received %d buffers, want at least 5", received)
		}
	}

	if firstLen == 0 {
		t.Fatal("first output buffer was empty")
	}
	t.Logf("received %d buffers, first buffer = %d bytes", received, firstLen)
}

func subscribeOutput(mediaGraph *mediaGraphImpl, nodeID, subscriberID string, bufferSize int) (<-chan []byte, error) {
	entry, ok := mediaGraph.nodeRuntime.GetEntryById(nodeID)
	if !ok {
		return nil, core.ErrNotFound
	}
	outputEntry, ok := entry.(core.OutputRegistryEntry)
	if !ok {
		return nil, core.ErrTypeMismatch
	}
	handle, err := core.GetOutputHandle[[]byte](outputEntry, subscriberID, bufferSize)
	if err != nil {
		return nil, err
	}
	return handle.RecvChan(), nil
}

func pushSource(mediaGraph *mediaGraphImpl, nodeID string, data []byte) error {
	entry, ok := mediaGraph.nodeRuntime.GetEntryById(nodeID)
	if !ok {
		return core.ErrNotFound
	}
	inputEntry, ok := entry.(core.InputRegistryEntry)
	if !ok {
		return core.ErrTypeMismatch
	}
	handle, err := core.GetInputHandle[[]byte](inputEntry)
	if err != nil {
		return err
	}
	return handle.Send(data)
}
