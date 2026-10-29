package graph

import (
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

const (
	dynamicPadPipelineID = "container_parse_dynamic"

	dynamicPadSourceNode = "dp_src"
	dynamicPadParseNode  = "dp_parse"

	dynamicPadFirstName  = "dyn-track-a"
	dynamicPadSecondName = "dyn-track-b"

	// Neither declared pad matches a real track, so every track is given a new
	// pad. Those pads have to be numbered past the declared _src_0 and _src_1.
	dynamicPadAbsentName = "declared-but-absent"
)

// buildUntaggedMultiTrackBytes produces a Matroska with two named video tracks
// whose titles match no declared pad.
func buildUntaggedMultiTrackBytes(t *testing.T) []byte {
	t.Helper()

	description := "matroskamux name=m ! appsink name=sink sync=false " +
		"videotestsrc num-buffers=30 ! video/x-raw,width=64,height=48,framerate=30/1 ! " +
		"taginject tags=\"title=" + dynamicPadFirstName + "\" ! queue ! m.video_0 " +
		"videotestsrc num-buffers=30 ! video/x-raw,width=96,height=72,framerate=30/1 ! " +
		"taginject tags=\"title=" + dynamicPadSecondName + "\" ! queue ! m.video_1"

	element, err := gst.ParseLaunch(description)
	if err != nil {
		t.Fatalf("ParseLaunch(matroskamux dynamic): %v", err)
	}
	pipeline := element.(gst.Pipeline)
	sink, ok := pipeline.GetByName("sink").(gstapp.AppSink)
	if !ok || sink == nil {
		t.Fatal("appsink not found in dynamic mux pipeline")
	}
	sink.SetEmitSignals(false)

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("dynamic mux SetState failed: %v", result)
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
		t.Fatal("dynamic mux produced no bytes")
	}
	return out
}

func dynamicPadDesiredGraph() DesiredGraph {
	return DesiredGraph{
		ID: dynamicPadPipelineID,
		Nodes: map[string]NodeInfo{
			dynamicPadSourceNode: {
				ID:         dynamicPadSourceNode,
				NodeType:   "SourceInput",
				Properties: map[string]interface{}{"caps": "video/x-matroska"},
				SourcePads: []core.PadInfo{{ID: dynamicPadSourceNode + "_src_0"}},
			},
			dynamicPadParseNode: {
				ID:       dynamicPadParseNode,
				NodeType: "ContainerParse",
				SourcePads: []core.PadInfo{
					{
						ID:         dynamicPadParseNode + "_src_0",
						Properties: map[string]interface{}{"track_name": dynamicPadAbsentName},
					},
					{
						ID:         dynamicPadParseNode + "_src_1",
						Properties: map[string]interface{}{"track_name": dynamicPadAbsentName + "-2"},
					},
				},
				SinkPads: []core.PadInfo{{ID: dynamicPadParseNode + "_sink_0"}},
			},
		},
		Links: []LinkInfo{
			{
				ID:          "dp_link_source_parse",
				SourcePadID: dynamicPadSourceNode + "_src_0",
				SinkPadID:   dynamicPadParseNode + "_sink_0",
			},
		},
	}
}

func subscribeTrackData(mediaGraph *mediaGraphImpl, nodeID, subscriberID string, bufferSize int) (<-chan core.TrackData, error) {
	entry, ok := mediaGraph.nodeRuntime.GetEntryById(nodeID)
	if !ok {
		return nil, core.ErrNotFound
	}
	outputEntry, ok := entry.(core.OutputRegistryEntry)
	if !ok {
		return nil, core.ErrTypeMismatch
	}
	handle, err := core.GetOutputHandle[core.TrackData](outputEntry, subscriberID, bufferSize)
	if err != nil {
		return nil, err
	}
	return handle.RecvChan(), nil
}

// TestContainerParseDynamicPadIDs covers the case where a discovered track fits
// no declared pad. Every such track gets a new pad, and those pads must not
// reuse an index a declared pad already holds, or creation fails and the track
// is silently dropped.
func TestContainerParseDynamicPadIDs(t *testing.T) {
	setupGraphTest(t)

	containerBytes := buildUntaggedMultiTrackBytes(t)
	t.Logf("container bytes = %d", len(containerBytes))
	mediaGraph, err := CreateMediaGraph(dynamicPadPipelineID)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	defer mediaGraph.Destroy()

	// ApplyGraph diffs against the applied graph, so the caller sends intent.
	if err := mediaGraph.ApplyGraph(dynamicPadDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := mediaGraph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	trackData, err := subscribeTrackData(mediaGraph, dynamicPadParseNode, "dp_sub", 16)
	if err != nil {
		t.Fatalf("subscribe track data: %v", err)
	}

	if err := pushSource(mediaGraph, dynamicPadSourceNode, containerBytes); err != nil {
		t.Fatalf("push source: %v", err)
	}

	seen := map[string]bool{}
	deadline := time.After(15 * time.Second)
	for len(seen) < 2 {
		select {
		case data, ok := <-trackData:
			if !ok {
				t.Fatalf("track data channel closed after %d tracks", len(seen))
			}
			if data.Type != core.TrackEventTypeDiscovered {
				t.Fatalf("track %q has type %q, want discovered", data.Name, data.Type)
			}
			if seen[data.ID] {
				t.Fatalf("track id %q was handed out twice", data.ID)
			}
			seen[data.ID] = true
			t.Logf("track id = %q, name = %q", data.ID, data.Name)
		case <-deadline:
			t.Fatalf("timed out after %d tracks, want 2", len(seen))
		}
	}

	// The declared pads own index 0 and 1, so the dynamic pads must start at 2.
	for _, want := range []string{
		dynamicPadParseNode + "_src_2",
		dynamicPadParseNode + "_src_3",
	} {
		if !seen[want] {
			t.Fatalf("track ids = %v, want to include %q", seen, want)
		}
	}
}
