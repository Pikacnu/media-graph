package graph

import (
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

const (
	multiTrackPipelineID = "container_parse_multi"

	multiTrackSourceNode = "mt_src"
	multiTrackParseNode  = "mt_parse"
	multiTrackOutputA    = "mt_out_a"
	multiTrackOutputB    = "mt_out_b"

	multiTrackFirstName = "main-video"
)

// buildMultiTrackContainerBytes produces a Matroska with two video tracks of
// different resolutions. Matroska only propagates tags for the first stream, so
// the second track is discovered without a title; the second declared pad is
// therefore selected by media type rather than by name.
func buildMultiTrackContainerBytes(t *testing.T) []byte {
	t.Helper()

	description := "matroskamux name=m ! appsink name=sink sync=false " +
		"videotestsrc num-buffers=30 ! video/x-raw,width=64,height=48,framerate=30/1 ! " +
		"taginject tags=\"title=" + multiTrackFirstName + "\" ! queue ! m.video_0 " +
		"videotestsrc num-buffers=30 ! video/x-raw,width=96,height=72,framerate=30/1 ! queue ! m.video_1"

	element, err := gst.ParseLaunch(description)
	if err != nil {
		t.Fatalf("ParseLaunch(matroskamux multi): %v", err)
	}
	pipeline := element.(gst.Pipeline)
	sink, ok := pipeline.GetByName("sink").(gstapp.AppSink)
	if !ok || sink == nil {
		t.Fatal("appsink not found in multi-track mux pipeline")
	}
	sink.SetEmitSignals(false)

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("multi-track mux SetState failed: %v", result)
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
		t.Fatal("multi-track mux produced no bytes")
	}
	return out
}

func multiTrackDesiredGraph() DesiredGraph {
	return DesiredGraph{
		ID: multiTrackPipelineID,
		Nodes: map[string]NodeInfo{
			multiTrackSourceNode: {
				ID:         multiTrackSourceNode,
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: multiTrackSourceNode + "_src_0"}},
			},
			multiTrackParseNode: {
				ID:       multiTrackParseNode,
				NodeType: "ContainerParse",
				SourcePads: []core.PadInfo{
					{
						ID: multiTrackParseNode + "_src_0",
						Properties: map[string]interface{}{
							"track_name": multiTrackFirstName,
							"track_id":   "1",
							"media_type": "video/x-raw",
						},
					},
					{
						ID: multiTrackParseNode + "_src_1",
						Properties: map[string]interface{}{
							"track_id":   "2",
							"media_type": "video/x-raw",
						},
					},
				},
				SinkPads: []core.PadInfo{{ID: multiTrackParseNode + "_sink_0"}},
			},
			multiTrackOutputA: {
				ID:         multiTrackOutputA,
				NodeType:   "Output",
				Properties: map[string]interface{}{"caps": "video/x-raw"},
				SinkPads:   []core.PadInfo{{ID: multiTrackOutputA + "_sink_0"}},
			},
			multiTrackOutputB: {
				ID:         multiTrackOutputB,
				NodeType:   "Output",
				Properties: map[string]interface{}{"caps": "video/x-raw"},
				SinkPads:   []core.PadInfo{{ID: multiTrackOutputB + "_sink_0"}},
			},
		},
		Links: []LinkInfo{
			{
				ID:          "mt_link_source_parse",
				SourcePadID: multiTrackSourceNode + "_src_0",
				SinkPadID:   multiTrackParseNode + "_sink_0",
			},
			{
				ID:          "mt_link_parse_out_a",
				SourcePadID: multiTrackParseNode + "_src_0",
				SinkPadID:   multiTrackOutputA + "_sink_0",
			},
			{
				ID:          "mt_link_parse_out_b",
				SourcePadID: multiTrackParseNode + "_src_1",
				SinkPadID:   multiTrackOutputB + "_sink_0",
			},
		},
	}
}

// TestContainerParseMultiTrack proves that two tracks discovered from one
// container are bound to two distinct declared pads, and that both branches
// deliver buffers downstream.
func TestContainerParseMultiTrack(t *testing.T) {
	setupGraphTest(t)

	containerBytes := buildMultiTrackContainerBytes(t)
	t.Logf("container bytes = %d", len(containerBytes))
	mediaGraph, err := CreateMediaGraph(multiTrackPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(multiTrackDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := mediaGraph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	chanA, err := subscribeOutput(mediaGraph, multiTrackOutputA, "mt_sub_a", 256)
	if err != nil {
		t.Fatalf("subscribe output A: %v", err)
	}
	chanB, err := subscribeOutput(mediaGraph, multiTrackOutputB, "mt_sub_b", 256)
	if err != nil {
		t.Fatalf("subscribe output B: %v", err)
	}

	if err := pushSource(mediaGraph, multiTrackSourceNode, containerBytes); err != nil {
		t.Fatalf("push source: %v", err)
	}

	receivedA := waitForBuffers(t, chanA, 3, 15*time.Second, "branch A")
	receivedB := waitForBuffers(t, chanB, 3, 15*time.Second, "branch B")
	t.Logf("branch A buffers = %d, branch B buffers = %d", receivedA, receivedB)
}

func waitForBuffers(t *testing.T, ch <-chan []byte, want int, timeout time.Duration, label string) int {
	t.Helper()
	deadline := time.After(timeout)
	received := 0
	for received < want {
		select {
		case data, ok := <-ch:
			if !ok {
				t.Fatalf("%s: channel closed after %d buffers", label, received)
			}
			if len(data) == 0 {
				t.Fatalf("%s: received an empty buffer", label)
			}
			received++
		case <-deadline:
			t.Fatalf("%s: timed out after %d buffers, want %d", label, received, want)
		}
	}
	return received
}
