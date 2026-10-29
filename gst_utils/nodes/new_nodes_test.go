package nodes

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// newTestPipeline wraps a node bin in a real pipeline so the node's internal
// chain can be exercised against live data instead of only inspected.
func newTestPipeline(t *testing.T, id string, node core.Node) gst.Pipeline {
	t.Helper()
	pipeline := gst.NewPipeline(id).(gst.Pipeline)
	if !pipeline.Add(node.Bin()) {
		t.Fatalf("add node bin %q to pipeline", id)
	}
	return pipeline
}

// addAudioTestSource installs a finite raw-audio producer into the pipeline and
// returns its capsfilter, whose src pad is the producer's output.
//
// Both elements are added explicitly: an element that was never added to the
// pipeline never reaches PLAYING, so it emits nothing and the test hangs until
// its deadline instead of failing.
func addAudioTestSource(t *testing.T, pipeline gst.Pipeline, id string, buffers int) gst.Element {
	t.Helper()
	src := gst.ElementFactoryMake("audiotestsrc", id+"_Src")
	caps := gst.ElementFactoryMake("capsfilter", id+"_SrcCaps")
	if src == nil || caps == nil {
		t.Fatal("create audio test source")
	}
	src.SetObjectProperty("num-buffers", int32(buffers))
	caps.SetObjectProperty("caps", gst.CapsFromString("audio/x-raw,format=S16LE,rate=48000,channels=2"))
	if !pipeline.AddMany(src, caps) {
		t.Fatal("add audio test source to pipeline")
	}
	if !src.Link(caps) {
		t.Fatal("link audiotestsrc to capsfilter")
	}
	return caps
}

// runUntilEOS drives the pipeline and drains an appsink until the stream ends.
//
// EOS is detected through the bus rather than by draining forever: without it a
// passing test would still burn the whole deadline before returning.
func runUntilEOS(t *testing.T, pipeline gst.Pipeline, sink gstapp.AppSink) []byte {
	t.Helper()
	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(PLAYING) failed: %v", result)
	}

	bus := pipeline.GetBus()
	var out []byte
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		sample := sink.TryPullSample(gst.ClockTime(100 * time.Millisecond))
		if sample != nil {
			buffer := sample.GetBuffer()
			if buffer != nil {
				if mapped, ok := buffer.Map(gst.MapFlags(gst.MapRead)); ok {
					out = append(out, mapped.Data()...)
					mapped.Unmap()
				}
			}
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
	t.Logf("deadline reached without EOS, collected %d bytes", len(out))
	return out
}

// TestAudioEncodeNodeEncodesToAAC checks that the node turns raw audio into
// something a muxer will accept.
//
// The trailing aacparse is what this test is really about: without it the
// encoder emits a byte stream whose caps a muxer cannot negotiate.
func TestAudioEncodeNodeEncodesToAAC(t *testing.T) {
	gst.Init()

	node, err := CreateAudioEncodeNode(nil, AudioEncodeConfig{ID: "aenc"})
	if err != nil {
		t.Fatalf("CreateAudioEncodeNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "aenc_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sinkElement := gst.ElementFactoryMake("appsink", "aenc_Sink")
	sink, ok := sinkElement.(gstapp.AppSink)
	if !ok {
		t.Fatal("appsink cast failed")
	}
	sink.SetEmitSignals(false)
	if !pipeline.Add(sinkElement) {
		t.Fatal("add appsink to pipeline")
	}

	source := addAudioTestSource(t, pipeline, "aenc", 40)

	sinkPad, _, err := node.GetSinkPad()
	if err != nil {
		t.Fatalf("GetSinkPad: %v", err)
	}
	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if ret := source.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatalf("link source to node: %v", ret)
	}
	if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatalf("link node to sink: %v", ret)
	}

	out := runUntilEOS(t, pipeline, sink)
	if len(out) == 0 {
		t.Fatal("audio encoder produced no bytes")
	}

	caps := sink.GetStaticPad("sink").GetCurrentCaps()
	if caps == nil || caps.GetStructure(0).GetName() != "audio/mpeg" {
		t.Fatalf("encoded caps = %v, want audio/mpeg", caps)
	}
}

// TestAudioEncodeFeedsFLVMuxToFileSink composes the new nodes the way the
// target topology does: encode audio, mux it to FLV, write the container.
//
// A node can look correct in isolation and still fail to compose, so this is
// the test that proves the muxer gap is actually closed end to end.
func TestAudioEncodeFeedsFLVMuxToFileSink(t *testing.T) {
	gst.Init()

	location := filepath.Join(t.TempDir(), "out.flv")

	encode, err := CreateAudioEncodeNode(nil, AudioEncodeConfig{ID: "flv_aenc"})
	if err != nil {
		t.Fatalf("CreateAudioEncodeNode: %v", err)
	}
	defer encode.Destroy()

	fileSink, err := CreateFileSinkNode(nil, FileSinkConfig{ID: "flv_sink", Location: location})
	if err != nil {
		t.Fatalf("CreateFileSinkNode: %v", err)
	}
	defer fileSink.Destroy()

	pipeline := gst.NewPipeline("flv_pipe").(gst.Pipeline)
	defer pipeline.SetState(gst.State(gst.StateNull))

	muxer := gst.ElementFactoryMake("flvmux", "flv_Mux")
	if muxer == nil {
		t.Fatal("create flvmux")
	}
	// streamable avoids requiring a seekable sink, which a live graph does not have.
	muxer.SetObjectProperty("streamable", true)

	if !pipeline.AddMany(muxer, encode.Bin(), fileSink.Bin()) {
		t.Fatal("add elements to pipeline")
	}
	source := addAudioTestSource(t, pipeline, "flv", 60)

	encodeSink, _, err := encode.GetSinkPad()
	if err != nil {
		t.Fatalf("encode GetSinkPad: %v", err)
	}
	encodeSrc, _, err := encode.GetSrcPad()
	if err != nil {
		t.Fatalf("encode GetSrcPad: %v", err)
	}
	fileSinkPad, _, err := fileSink.GetSinkPad()
	if err != nil {
		t.Fatalf("file sink GetSinkPad: %v", err)
	}

	if ret := source.GetStaticPad("src").Link(encodeSink); ret != gst.PadLinkOK {
		t.Fatalf("link source to encoder: %v", ret)
	}
	if ret := encodeSrc.Link(muxer.RequestPadSimple("audio")); ret != gst.PadLinkOK {
		t.Fatalf("link encoder to flvmux audio: %v", ret)
	}
	if ret := muxer.GetStaticPad("src").Link(fileSinkPad); ret != gst.PadLinkOK {
		t.Fatalf("link flvmux to file sink: %v", ret)
	}

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(PLAYING) failed: %v", result)
	}

	// Wait for EOS so the muxer has written the container tail.
	bus := pipeline.GetBus()
	sawEOS := false
	deadline := time.Now().Add(30 * time.Second)
	for !sawEOS && time.Now().Before(deadline) {
		msg := bus.TimedPop(gst.ClockTime(200 * time.Millisecond))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			sawEOS = true
		case gst.MessageError:
			parsed, err := msg.ParseError()
			if err != nil {
				t.Fatalf("pipeline error (unparseable): %v", err)
			}
			t.Fatalf("pipeline error: %v", parsed)
		}
	}
	if !sawEOS {
		t.Fatal("pipeline never reached EOS")
	}

	// NULL is what makes filesink flush and finalise the container.
	if result := pipeline.SetState(gst.State(gst.StateNull)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(NULL) failed: %v", result)
	}

	data, err := os.ReadFile(location)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("output file is empty")
	}
	if !bytes.HasPrefix(data, []byte("FLV")) {
		t.Fatalf("output is not an FLV container, first bytes: %q", data[:min(16, len(data))])
	}
}

// TestDiscardNodeDrainsBranch checks the node keeps a branch flowing without
// pacing the graph, which is what makes it usable on an unused tee branch.
func TestDiscardNodeDrainsBranch(t *testing.T) {
	gst.Init()

	node, err := CreateDiscardNode(nil, DiscardConfig{ID: "disc"})
	if err != nil {
		t.Fatalf("CreateDiscardNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "disc_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	source := addAudioTestSource(t, pipeline, "disc", 100)

	sinkPad, _, err := node.GetSinkPad()
	if err != nil {
		t.Fatalf("GetSinkPad: %v", err)
	}
	if ret := source.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatalf("link source to discard: %v", ret)
	}

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(PLAYING) failed: %v", result)
	}

	// A branch that blocks or errors would surface on the bus within this window.
	bus := pipeline.GetBus()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(gst.ClockTime(200 * time.Millisecond))
		if msg == nil {
			continue
		}
		if msg.Type() == gst.MessageError {
			parsed, err := msg.ParseError()
			if err != nil {
				t.Fatalf("discard branch error (unparseable): %v", err)
			}
			t.Fatalf("discard branch reported an error: %v", parsed)
		}
	}
}

// TestNewNodesRejectBadConfig guards the constructor contracts.
func TestNewNodesRejectBadConfig(t *testing.T) {
	gst.Init()

	if _, err := CreateFileSinkNode(nil, FileSinkConfig{ID: "fs"}); err == nil {
		t.Fatal("expected an error for an empty location")
	}
	if _, err := CreateDiscardNode(nil, DiscardConfig{}); err == nil {
		t.Fatal("expected an error for an empty node id")
	}
	if _, err := CreateAudioEncodeNode(nil, AudioEncodeConfig{}); err == nil {
		t.Fatal("expected an error for an empty node id")
	}
	if _, err := CreateAudioEncodeNode(nil, AudioEncodeConfig{ID: "ae", Codec: "bogus"}); err == nil {
		t.Fatal("expected an error for an unknown codec")
	}
}

// TestAudioCodecChainsAreResolvable checks that every codec the registry names
// can actually be built on this machine: each has at least one candidate
// encoder+parser pair whose elements are installed. A codec listed but
// unresolvable would only fail at pipeline build time, which is much harder to
// diagnose.
func TestAudioCodecChainsAreResolvable(t *testing.T) {
	gst.Init()

	for _, codec := range audioCodecNames() {
		candidates, known := audioCodecCandidatesFor(AudioCodec(codec))
		if !known {
			t.Fatalf("codec %q is listed but has no candidates", codec)
		}
		chain, ok := resolveAudioCodecChain(AudioCodec(codec))
		if !ok {
			t.Errorf("codec %q has no installed encoder; tried %s",
				codec, formatCodecCandidates(candidates))
			continue
		}
		t.Logf("codec %q resolves to %s + %s", codec, chain.Encoder, chain.Parser)
	}
}

// TestAudioEncodeNodeEncodesOpus proves the codec mapping actually switches the
// chain: the parser is chosen together with the encoder, so a codec other than
// AAC must not be stuck with aacparse.
func TestAudioEncodeNodeEncodesOpus(t *testing.T) {
	gst.Init()

	node, err := CreateAudioEncodeNode(nil, AudioEncodeConfig{ID: "oenc", Codec: AudioCodecOpus})
	if err != nil {
		t.Fatalf("CreateAudioEncodeNode(opus): %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "oenc_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sinkElement := gst.ElementFactoryMake("appsink", "oenc_Sink")
	sink, ok := sinkElement.(gstapp.AppSink)
	if !ok {
		t.Fatal("appsink cast failed")
	}
	sink.SetEmitSignals(false)
	if !pipeline.Add(sinkElement) {
		t.Fatal("add appsink to pipeline")
	}

	source := addAudioTestSource(t, pipeline, "oenc", 40)

	sinkPad, _, err := node.GetSinkPad()
	if err != nil {
		t.Fatalf("GetSinkPad: %v", err)
	}
	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if ret := source.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatalf("link source to node: %v", ret)
	}
	if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatalf("link node to sink: %v", ret)
	}

	out := runUntilEOS(t, pipeline, sink)
	if len(out) == 0 {
		t.Fatal("opus encoder produced no bytes")
	}

	caps := sink.GetStaticPad("sink").GetCurrentCaps()
	if caps == nil {
		t.Fatal("no caps negotiated on the opus output")
	}
	if name := caps.GetStructure(0).GetName(); name != "audio/x-opus" {
		t.Fatalf("encoded caps = %v, want audio/x-opus", name)
	}
}
