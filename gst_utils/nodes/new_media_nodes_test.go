package nodes

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// addVideoTestSource installs a finite raw-video producer and returns the
// capsfilter whose src pad is its output. Both elements are added explicitly: an
// element that was never added never reaches PLAYING, so it emits nothing and
// the test burns its deadline instead of failing.
//
// live=true makes the source pace itself against the clock instead of racing to
// EOS. Tests that gate or re-time flow need this: a non-live source pushes its
// whole buffer budget before the gate opens and then ends, leaving nothing to
// observe.
func addVideoTestSource(t *testing.T, pipeline gst.Pipeline, id string, buffers int, live bool) gst.Element {
	t.Helper()
	src := gst.ElementFactoryMake("videotestsrc", id+"_Src")
	caps := gst.ElementFactoryMake("capsfilter", id+"_SrcCaps")
	if src == nil || caps == nil {
		t.Fatal("create video test source")
	}
	src.SetObjectProperty("num-buffers", int32(buffers))
	src.SetObjectProperty("is-live", live)
	caps.SetObjectProperty("caps", gst.CapsFromString("video/x-raw,width=320,height=240,framerate=30/1"))
	if !pipeline.AddMany(src, caps) {
		t.Fatal("add video test source to pipeline")
	}
	if !src.Link(caps) {
		t.Fatal("link videotestsrc to capsfilter")
	}
	return caps
}

// addAppSink installs an appsink that only stores the last caps it negotiated,
// so a test can assert on the stream's shape as well as its bytes.
func addAppSink(t *testing.T, pipeline gst.Pipeline, id string) gstapp.AppSink {
	t.Helper()
	element := gst.ElementFactoryMake("appsink", id+"_Sink")
	sink, ok := element.(gstapp.AppSink)
	if !ok {
		t.Fatal("appsink cast failed")
	}
	sink.SetEmitSignals(false)
	sink.SetObjectProperty("sync", false)
	if !pipeline.Add(element) {
		t.Fatal("add appsink to pipeline")
	}
	return sink
}

// runCollect plays the pipeline and returns as soon as wantBytes have been
// gathered or the deadline passes. It is for sources that never EOS (a frozen
// image, a live test pattern), where runUntilEOS would always wait the full
// deadline.
func runCollect(t *testing.T, pipeline gst.Pipeline, sink gstapp.AppSink, wantBytes int, timeout time.Duration) []byte {
	t.Helper()
	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(PLAYING) failed: %v", result)
	}

	bus := pipeline.GetBus()
	var out []byte
	deadline := time.Now().Add(timeout)
	polls := 0
	for time.Now().Before(deadline) && len(out) < wantBytes {
		sample := sink.TryPullSample(gst.ClockTime(100 * time.Millisecond))
		if sample != nil {
			if buffer := sample.GetBuffer(); buffer != nil {
				if mapped, ok := buffer.Map(gst.MapFlags(gst.MapRead)); ok {
					out = append(out, mapped.Data()...)
					mapped.Unmap()
				}
			}
		}
		// Sampling the bus every turn would flood the log with the poll
		// assertion this bus shape emits, so it is checked periodically.
		polls++
		if polls%10 != 0 {
			continue
		}
		if msg := bus.TimedPop(gst.ClockTime(0)); msg != nil {
			switch msg.Type() {
			case gst.MessageEOS:
				return out
			case gst.MessageError:
				parsed, err := msg.ParseError()
				if err != nil {
					t.Fatalf("pipeline error (unparseable): %v", err)
				}
				t.Fatalf("pipeline error: %v", parsed)
			}
		}
	}
	return out
}

// TestCompositorComposesTwoBranchesOnOneCanvas is the Scene/Mixer contract: two
// independent video branches become one stream whose geometry is the canvas, not
// either input.
func TestCompositorComposesTwoBranchesOnOneCanvas(t *testing.T) {
	gst.Init()

	node, err := CreateCompositorNode(nil, CompositorConfig{
		ID:     "comp",
		Width:  640,
		Height: 360,
	})
	if err != nil {
		t.Fatalf("CreateCompositorNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "comp_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sink := addAppSink(t, pipeline, "comp")

	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if srcPad.Link(sink.GetStaticPad("sink")) != gst.PadLinkOK {
		t.Fatal("link compositor to appsink")
	}

	for index, branchID := range []string{"a", "b"} {
		branchPad, _, err := node.RequestSinkPad()
		if err != nil {
			t.Fatalf("RequestSinkPad %d: %v", index, err)
		}
		source := addVideoTestSource(t, pipeline, branchID, 30, false)
		if source.GetStaticPad("src").Link(branchPad) != gst.PadLinkOK {
			t.Fatalf("link branch %d into compositor", index)
		}
	}

	// Placement arrives as a property update, which is how the graph delivers it.
	if err := node.UpdateProperties(map[string]interface{}{
		"Pads": []map[string]any{
			{"X": 0, "Y": 0, "Width": 320, "Height": 180, "ZOrder": 1, "Alpha": 1},
			{"X": 200, "Y": 100, "Width": 320, "Height": 180, "ZOrder": 2, "Alpha": 0.5},
		},
	}); err != nil {
		t.Fatalf("UpdateProperties(Pads): %v", err)
	}

	out := runCollect(t, pipeline, sink, 1, 20*time.Second)
	if len(out) == 0 {
		t.Fatal("compositor produced no bytes")
	}

	caps := sink.GetStaticPad("sink").GetCurrentCaps()
	if caps == nil {
		t.Fatal("compositor output negotiated no caps")
	}
	structure := caps.GetStructure(0)
	width := int(propertyNumber(structure.GetValue("width"), -1))
	height := int(propertyNumber(structure.GetValue("height"), -1))
	if width != 640 || height != 360 {
		t.Fatalf("compositor canvas = %dx%d, want 640x360", width, height)
	}
}

// TestCompositorPadLayoutIsApplied pins that placement really lands on the
// compositor's own pad. A layout that is accepted but never written would look
// identical from the outside until the picture is wrong.
func TestCompositorPadLayoutIsApplied(t *testing.T) {
	gst.Init()

	node, err := CreateCompositorNode(nil, CompositorConfig{
		ID: "comp_layout", Width: 1280, Height: 720,
		Pads: []CompositorPadLayout{{X: 10, Y: 20, ZOrder: 3, Alpha: 0.25}},
	})
	if err != nil {
		t.Fatalf("CreateCompositorNode: %v", err)
	}
	defer node.Destroy()

	if _, _, err := node.RequestSinkPad(); err != nil {
		t.Fatalf("RequestSinkPad: %v", err)
	}

	pad := node.sinkLeases[0].requestPad
	if got := pad.ObjectProperty("xpos"); got != int32(10) {
		t.Errorf("xpos = %v, want 10", got)
	}
	if got := pad.ObjectProperty("ypos"); got != int32(20) {
		t.Errorf("ypos = %v, want 20", got)
	}
	if got := pad.ObjectProperty("zorder"); got != uint32(3) {
		t.Errorf("zorder = %v, want 3", got)
	}
	if got, _ := pad.ObjectProperty("alpha").(float64); got != 0.25 {
		t.Errorf("alpha = %v, want 0.25", got)
	}

	// A second branch plus a live layout update must move that branch.
	if _, _, err := node.RequestSinkPad(); err != nil {
		t.Fatalf("RequestSinkPad: %v", err)
	}
	if err := node.UpdateProperties(map[string]interface{}{
		"Pads": []map[string]any{{"X": 1, "Y": 1}, {"X": 400, "Y": 300, "Width": 100, "Height": 50}},
	}); err != nil {
		t.Fatalf("UpdateProperties(Pads): %v", err)
	}
	second := node.sinkLeases[1].requestPad
	if got := second.ObjectProperty("xpos"); got != int32(400) {
		t.Errorf("moved xpos = %v, want 400", got)
	}
	if got := second.ObjectProperty("width"); got != int32(100) {
		t.Errorf("moved width = %v, want 100", got)
	}
}

// TestTransitionWipesTwoBranchesIntoOne checks the two-in/one-out shape against
// real media, and that the wipe settings reach the element.
func TestTransitionWipesTwoBranchesIntoOne(t *testing.T) {
	gst.Init()

	node, err := CreateTransitionNode(nil, TransitionConfig{ID: "trans", DurationMs: 400})
	if err != nil {
		t.Fatalf("CreateTransitionNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "trans_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sink := addAppSink(t, pipeline, "trans")
	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if srcPad.Link(sink.GetStaticPad("sink")) != gst.PadLinkOK {
		t.Fatal("link transition to appsink")
	}

	for index, branchID := range []string{"x", "y"} {
		branchPad, _, err := node.RequestSinkPad()
		if err != nil {
			t.Fatalf("RequestSinkPad %d: %v", index, err)
		}
		source := addVideoTestSource(t, pipeline, branchID, 40, false)
		if source.GetStaticPad("src").Link(branchPad) != gst.PadLinkOK {
			t.Fatalf("link branch %d into transition", index)
		}
	}

	out := runCollect(t, pipeline, sink, 1, 20*time.Second)
	if len(out) == 0 {
		t.Fatal("transition produced no bytes")
	}

	// A third branch must be refused: the wipe has exactly two inputs.
	if _, _, err := node.RequestSinkPad(); err == nil {
		t.Error("RequestSinkPad on a full transition returned no error")
	}
}

// TestTestSourcePublishesConfiguredGeometry proves the placeholder source
// honours the canvas it was asked for rather than its own default.
func TestTestSourcePublishesConfiguredGeometry(t *testing.T) {
	gst.Init()

	node, err := CreateTestSourceNode(nil, TestSourceConfig{
		ID: "test_src", Width: 480, Height: 270, Framerate: "25/1", Pattern: "ball",
	})
	if err != nil {
		t.Fatalf("CreateTestSourceNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "test_src_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sink := addAppSink(t, pipeline, "test_src")
	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if srcPad.Link(sink.GetStaticPad("sink")) != gst.PadLinkOK {
		t.Fatal("link test source to appsink")
	}

	if len(runCollect(t, pipeline, sink, 1, 15*time.Second)) == 0 {
		t.Fatal("test source produced no bytes")
	}

	structure := sink.GetStaticPad("sink").GetCurrentCaps().GetStructure(0)
	width := int(propertyNumber(structure.GetValue("width"), -1))
	height := int(propertyNumber(structure.GetValue("height"), -1))
	if width != 480 || height != 270 {
		t.Fatalf("test source geometry = %dx%d, want 480x270", width, height)
	}
}

// TestVolumeAppliesGainLive is the volume slider's contract, checked by its
// effect: unity gain leaves the source audible and zero gain silences it.
func TestVolumeAppliesGainLive(t *testing.T) {
	gst.Init()

	unity := 1.0
	silence := 0.0
	for _, tc := range []struct {
		name       string
		gain       *float64
		wantSilent bool
	}{
		{name: "unity is not silent", gain: &unity},
		{name: "zero is silent", gain: &silence, wantSilent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, err := CreateVolumeNode(nil, VolumeConfig{ID: "vol_" + tc.name[:4], Volume: tc.gain})
			if err != nil {
				t.Fatalf("CreateVolumeNode: %v", err)
			}
			defer node.Destroy()

			pipeline := newTestPipeline(t, "vol_pipe", node)
			defer pipeline.SetState(gst.State(gst.StateNull))

			sink := addAppSink(t, pipeline, "vol")
			srcPad, _, err := node.GetSrcPad()
			if err != nil {
				t.Fatalf("GetSrcPad: %v", err)
			}
			if srcPad.Link(sink.GetStaticPad("sink")) != gst.PadLinkOK {
				t.Fatal("link volume to appsink")
			}

			source := addAudioTestSource(t, pipeline, "vol", 25)
			sinkPad, _, err := node.GetSinkPad()
			if err != nil {
				t.Fatalf("GetSinkPad: %v", err)
			}
			if source.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
				t.Fatal("link audio source to volume")
			}

			out := runUntilEOS(t, pipeline, sink)
			if len(out) == 0 {
				t.Fatal("volume produced no bytes")
			}
			loud := false
			for _, b := range out {
				if b != 0 {
					loud = true
					break
				}
			}
			if tc.wantSilent && loud {
				t.Error("zero gain still produced non-zero samples")
			}
			if !tc.wantSilent && !loud {
				t.Error("unity gain produced silence")
			}
		})
	}
}

// TestImageSourceFreezesAStillImageIntoVideo is the logo/slate path: a single
// PNG must become a continuous stream instead of one buffer and EOS.
func TestImageSourceFreezesAStillImageIntoVideo(t *testing.T) {
	gst.Init()

	imagePath := filepath.Join(t.TempDir(), "logo.png")
	if !writeTestPNG(t, imagePath) {
		t.Skip("no pngenc/filesink available to author a test image")
	}

	node, err := CreateImageSourceNode(nil, ImageSourceConfig{ID: "img_src", Location: imagePath})
	if err != nil {
		t.Fatalf("CreateImageSourceNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "img_src_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sink := addAppSink(t, pipeline, "img_src")
	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if srcPad.Link(sink.GetStaticPad("sink")) != gst.PadLinkOK {
		t.Fatal("link image source to appsink")
	}

	// One 320x240 RGB frame is 230400 bytes, so two frames' worth proves the
	// freeze is repeating rather than passing a single buffer through.
	out := runCollect(t, pipeline, sink, 2*320*240, 20*time.Second)
	if len(out) < 2*320*240/2 {
		t.Fatalf("image source produced %d bytes, expected repeated frames", len(out))
	}
}

// writeTestPNG authors a small still image with GStreamer itself.
func writeTestPNG(t *testing.T, path string) bool {
	t.Helper()
	pipeline := gst.NewPipeline("pnggen").(gst.Pipeline)
	src := gst.ElementFactoryMake("videotestsrc", "pnggen_Src")
	caps := gst.ElementFactoryMake("capsfilter", "pnggen_Caps")
	enc := gst.ElementFactoryMake("pngenc", "pnggen_Enc")
	sink := gst.ElementFactoryMake("filesink", "pnggen_Sink")
	if src == nil || caps == nil || enc == nil || sink == nil {
		return false
	}
	src.SetObjectProperty("num-buffers", int32(1))
	caps.SetObjectProperty("caps", gst.CapsFromString("video/x-raw,width=320,height=240"))
	sink.SetObjectProperty("location", path)

	if !pipeline.AddMany(src, caps, enc, sink) {
		t.Fatal("add png generator elements")
	}
	if !src.Link(caps) || !caps.Link(enc) || !enc.Link(sink) {
		t.Fatal("build png generator chain")
	}

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("png generator SetState(PLAYING) failed: %v", result)
	}
	bus := pipeline.GetBus()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if msg := bus.TimedPop(gst.ClockTime(100 * time.Millisecond)); msg != nil {
			switch msg.Type() {
			case gst.MessageEOS:
				pipeline.SetState(gst.State(gst.StateNull))
				info, err := os.Stat(path)
				return err == nil && info.Size() > 0
			case gst.MessageError:
				parsed, _ := msg.ParseError()
				t.Fatalf("no png could be written: %v", parsed)
			}
		}
	}
	pipeline.SetState(gst.State(gst.StateNull))
	return false
}

// TestThumbnailPublishesJPEGFrames checks the node a UI card will use: the bytes
// a subscriber receives must really be a JPEG, not raw video.
func TestThumbnailPublishesJPEGFrames(t *testing.T) {
	gst.Init()

	runtime := newTestRuntime(t, "thumb")
	node, err := CreateThumbnailNode(runtime, ThumbnailConfig{ID: "thumb", Width: 160, Height: 90})
	if err != nil {
		t.Fatalf("CreateThumbnailNode: %v", err)
	}
	defer node.Destroy()

	entry, ok := runtime.GetEntryById("thumb")
	if !ok {
		t.Fatal("thumbnail did not register its entry")
	}
	outputEntry, ok := entry.(core.OutputRegistryEntry)
	if !ok {
		t.Fatal("thumbnail entry is not an output registry entry")
	}
	handle, err := core.GetOutputHandle[[]byte](outputEntry, "thumb_test", 8)
	if err != nil {
		t.Fatalf("GetOutputHandle: %v", err)
	}

	pipeline := newTestPipeline(t, "thumb_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	source := addVideoTestSource(t, pipeline, "thumb", 30, false)
	sinkPad, _, err := node.GetSinkPad()
	if err != nil {
		t.Fatalf("GetSinkPad: %v", err)
	}
	if source.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
		t.Fatal("link source to thumbnail")
	}
	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(PLAYING) failed: %v", result)
	}

	deadline := time.After(20 * time.Second)
	for {
		select {
		case frame, open := <-handle.RecvChan():
			if !open {
				t.Fatal("thumbnail entry closed before publishing a frame")
			}
			if len(frame) < 4 {
				t.Fatalf("thumbnail frame is %d bytes, too short to be a JPEG", len(frame))
			}
			if frame[0] != 0xFF || frame[1] != 0xD8 || frame[2] != 0xFF {
				t.Fatalf("thumbnail frame does not start with the JPEG marker: % x", frame[:3])
			}
			return
		case <-deadline:
			t.Fatal("thumbnail published no frame within the deadline")
		}
	}
}

// TestOutputControlGatesFlowLive is the "go live / stop" contract, checked by
// effect: a closed gate passes nothing, opening it starts flow, and closing it
// again stops flow without the pipeline ever leaving PLAYING.
//
// This is the property that makes it a publish switch rather than a topology
// change: no rebuild, no state change, no dropped connection.
func TestOutputControlGatesFlowLive(t *testing.T) {
	gst.Init()

	node, err := CreateOutputControlNode(nil, OutputControlConfig{ID: "gate", Publishing: false})
	if err != nil {
		t.Fatalf("CreateOutputControlNode: %v", err)
	}
	defer node.Destroy()

	if node.Publishing() {
		t.Fatal("node reports publishing while constructed closed")
	}

	pipeline := newTestPipeline(t, "gate_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sink := addAppSink(t, pipeline, "gate")
	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if srcPad.Link(sink.GetStaticPad("sink")) != gst.PadLinkOK {
		t.Fatal("link gate to appsink")
	}

	source := addVideoTestSource(t, pipeline, "gate", 1000, true)
	sinkPad, _, err := node.GetSinkPad()
	if err != nil {
		t.Fatalf("GetSinkPad: %v", err)
	}
	if source.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
		t.Fatal("link source to gate")
	}

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(PLAYING) failed: %v", result)
	}

	// A closed gate must not stop the pipeline from reaching PLAYING, or a
	// branch that starts closed would wedge the whole graph.
	if state, _, _ := pipeline.GetState(gst.ClockTime(2 * time.Second)); state != gst.StatePlaying {
		t.Fatalf("pipeline state with a closed gate = %v, want PLAYING", state)
	}
	if got := drainAppSink(sink, 500*time.Millisecond); got != 0 {
		t.Fatalf("closed gate passed %d buffers, want 0", got)
	}

	node.SetPublishing(true)
	if got := drainAppSink(sink, 500*time.Millisecond); got == 0 {
		t.Fatal("opened gate passed no buffers")
	}

	node.SetPublishing(false)
	if state, _, _ := pipeline.GetState(gst.ClockTime(0)); state != gst.StatePlaying {
		t.Fatalf("pipeline left PLAYING when the gate closed: %v", state)
	}
	if got := drainAppSink(sink, 500*time.Millisecond); got != 0 {
		t.Fatalf("re-closed gate passed %d buffers, want 0", got)
	}

	// The property path must be equivalent to the named method.
	if err := node.UpdateProperties(map[string]interface{}{"Publishing": true}); err != nil {
		t.Fatalf("UpdateProperties(Publishing): %v", err)
	}
	if !node.Publishing() {
		t.Fatal("UpdateProperties did not open the gate")
	}
	if got := drainAppSink(sink, 500*time.Millisecond); got == 0 {
		t.Fatal("gate reopened through UpdateProperties passed no buffers")
	}
}

// TestOutputControlAppliesBufferPolicyLive proves the branch's buffering — its
// latency ceiling — is configured at construction and adjustable while running,
// which is what separates this node from a bare gate.
func TestOutputControlAppliesBufferPolicyLive(t *testing.T) {
	gst.Init()

	node, err := CreateOutputControlNode(nil, OutputControlConfig{
		ID: "buf_gate",
		Buffer: OutputBufferConfig{
			MaxSizeBuffers: 8,
			MaxSizeTime:    uint64(500 * time.Millisecond),
			Leaky:          queueLeakyDownstream,
		},
	})
	if err != nil {
		t.Fatalf("CreateOutputControlNode: %v", err)
	}
	defer node.Destroy()

	if got := objectInt(node.queue.ObjectProperty("max-size-buffers")); got != 8 {
		t.Fatalf("max-size-buffers = %d, want 8", got)
	}
	if got := objectInt(node.queue.ObjectProperty("leaky")); got != 2 {
		t.Fatalf("leaky = %d, want 2 (downstream)", got)
	}

	// The buffer policy is live, like the gate.
	if err := node.UpdateProperties(map[string]interface{}{
		"MaxSizeBuffers": 64,
		"Leaky":          queueLeakyUpstream,
	}); err != nil {
		t.Fatalf("UpdateProperties(buffer): %v", err)
	}
	if got := objectInt(node.queue.ObjectProperty("max-size-buffers")); got != 64 {
		t.Fatalf("max-size-buffers after update = %d, want 64", got)
	}
	if got := objectInt(node.queue.ObjectProperty("leaky")); got != 1 {
		t.Fatalf("leaky after update = %d, want 1 (upstream)", got)
	}

	if err := node.UpdateProperties(map[string]interface{}{"Leaky": "sideways"}); err == nil {
		t.Fatal("unknown leaky mode was accepted")
	}
}

// objectInt reads a numeric GObject property as an integer regardless of the
// exact Go width gobject hands back.
func objectInt(value any) int64 {
	switch number := value.(type) {
	case int:
		return int64(number)
	case int32:
		return int64(number)
	case int64:
		return int64(number)
	case uint:
		return int64(number)
	case uint32:
		return int64(number)
	case uint64:
		return int64(number)
	case float64:
		return int64(number)
	default:
		return -1
	}
}

// drainAppSink counts the buffers an appsink receives inside a window.
func drainAppSink(sink gstapp.AppSink, window time.Duration) int {
	count := 0
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if sink.TryPullSample(gst.ClockTime(50*time.Millisecond)) != nil {
			count++
		}
	}
	return count
}

// TestHLSOutputWritesPlaylistAndSegments proves the output really produces a
// playable HLS set, which is the only thing that distinguishes a working muxer
// chain from one that links but never writes.
func TestHLSOutputWritesPlaylistAndSegments(t *testing.T) {
	gst.Init()

	dir := t.TempDir()
	segmentPattern := filepath.Join(dir, "seg%05d.ts")
	playlistPath := filepath.Join(dir, "index.m3u8")

	node, err := CreateHLSOutputNode(nil, HLSOutputConfig{
		ID:               "hls",
		Location:         segmentPattern,
		PlaylistLocation: playlistPath,
		TargetDuration:   1,
		MaxFiles:         5,
		PlaylistLength:   3,
	})
	if err != nil {
		t.Fatalf("CreateHLSOutputNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "hls_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	src := gst.ElementFactoryMake("videotestsrc", "hls_Src")
	caps := gst.ElementFactoryMake("capsfilter", "hls_Caps")
	enc := gst.ElementFactoryMake("x264enc", "hls_Enc")
	parse := gst.ElementFactoryMake("h264parse", "hls_Parse")
	if !pipeline.AddMany(src, caps, enc, parse) {
		t.Fatal("add HLS producer elements")
	}
	src.SetObjectProperty("num-buffers", int32(120))
	caps.SetObjectProperty("caps", gst.CapsFromString("video/x-raw,width=320,height=240,framerate=30/1"))
	// tune and speed-preset are enums, so they need GStreamer's string setter.
	gst.UtilSetObjectArg(enc, "tune", "zerolatency")
	gst.UtilSetObjectArg(enc, "speed-preset", "ultrafast")
	enc.SetObjectProperty("key-int-max", uint32(30))
	if !src.Link(caps) || !caps.Link(enc) || !enc.Link(parse) {
		t.Fatal("build HLS producer chain")
	}

	sinkPad, _, err := node.GetSinkPad()
	if err != nil {
		t.Fatalf("GetSinkPad: %v", err)
	}
	if parse.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
		t.Fatal("link producer to HLS output")
	}

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(PLAYING) failed: %v", result)
	}

	// A segment closes on a keyframe boundary, so this waits for the writer
	// rather than assuming a fixed duration is enough.
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if playlist, err := os.ReadFile(playlistPath); err == nil && len(playlist) > 0 {
			if segments, _ := filepath.Glob(filepath.Join(dir, "seg*.ts")); len(segments) > 0 {
				if info, err := os.Stat(segments[0]); err == nil && info.Size() > 0 {
					return
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("HLS output wrote no playlist/segment within the deadline (dir %s)", dir)
}
