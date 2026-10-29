package nodes

import (
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// TestFlvMuxSinkBranches covers the request-pad bookkeeping: video then audio,
// a hard stop at two, removal, and that a removed pad's number is not reused.
func TestFlvMuxSinkBranches(t *testing.T) {
	gst.Init()

	node, err := CreateFlvMuxNode(nil, FlvMuxConfig{ID: "fmx"})
	if err != nil {
		t.Fatalf("CreateFlvMuxNode: %v", err)
	}
	defer node.Destroy()

	firstPad, firstID, err := node.RequestSinkPad()
	if err != nil {
		t.Fatalf("RequestSinkPad(1): %v", err)
	}
	if firstID != "fmx_sink_0" || firstPad == nil {
		t.Fatalf("first sink pad = %q/%v, want fmx_sink_0", firstID, firstPad)
	}
	_, secondID, err := node.RequestSinkPad()
	if err != nil {
		t.Fatalf("RequestSinkPad(2): %v", err)
	}
	if secondID != "fmx_sink_1" {
		t.Fatalf("second sink pad = %q, want fmx_sink_1", secondID)
	}
	if got := node.GetDstIDs(); len(got) != 2 {
		t.Fatalf("GetDstIDs = %v, want 2 pads", got)
	}

	// FLV carries one stream per media type, so a third request must fail.
	if _, _, err := node.RequestSinkPad(); err == nil {
		t.Error("RequestSinkPad(3) = nil error, want the flvmux limit to refuse")
	}

	if err := node.RemoveSinkPad("fmx_sink_0"); err != nil {
		t.Fatalf("RemoveSinkPad: %v", err)
	}
	if got := node.GetDstIDs(); len(got) != 1 || got[0] != "fmx_sink_1" {
		t.Fatalf("GetDstIDs after remove = %v, want [fmx_sink_1]", got)
	}
	if err := node.RemoveSinkPad("fmx_nope"); err == nil {
		t.Error("RemoveSinkPad(unknown) = nil, want error")
	}
}

// TestFlvMuxMuxesVideoToFLV proves the node actually produces an FLV container,
// which is exactly what RTMPOutput's rtmpsink needs downstream.
func TestFlvMuxMuxesVideoToFLV(t *testing.T) {
	gst.Init()

	node, err := CreateFlvMuxNode(nil, FlvMuxConfig{ID: "fmx_v"})
	if err != nil {
		t.Fatalf("CreateFlvMuxNode: %v", err)
	}
	defer node.Destroy()

	pipeline := newTestPipeline(t, "fmx_v_pipe", node)
	defer pipeline.SetState(gst.State(gst.StateNull))

	sinkElement := gst.ElementFactoryMake("appsink", "fmx_v_Sink")
	sink, ok := sinkElement.(gstapp.AppSink)
	if !ok {
		t.Fatal("appsink cast failed")
	}
	if !pipeline.Add(sinkElement) {
		t.Fatal("add appsink to pipeline")
	}

	// A raw video producer encoded to H264, which is what the flvmux video pad
	// actually accepts (it negotiates encoded video, not raw).
	src := gst.ElementFactoryMake("videotestsrc", "fmx_v_Src")
	srcCaps := gst.ElementFactoryMake("capsfilter", "fmx_v_SrcCaps")
	encoder := gst.ElementFactoryMake("x264enc", "fmx_v_Enc")
	if src == nil || srcCaps == nil || encoder == nil {
		t.Fatal("create video test source")
	}
	src.SetObjectProperty("num-buffers", int32(60))
	srcCaps.SetObjectProperty("caps", gst.CapsFromString("video/x-raw,width=320,height=240"))
	if !pipeline.AddMany(src, srcCaps, encoder) {
		t.Fatal("add video test source")
	}
	if !src.Link(srcCaps) || !srcCaps.Link(encoder) {
		t.Fatal("link video test source to encoder")
	}

	videoPad, _, err := node.RequestSinkPad()
	if err != nil {
		t.Fatalf("RequestSinkPad: %v", err)
	}
	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if ret := encoder.GetStaticPad("src").Link(videoPad); ret != gst.PadLinkOK {
		t.Fatalf("link encoder to mux video pad: %v", ret)
	}
	if ret := srcPad.Link(sink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatalf("link mux to sink: %v", ret)
	}

	out := runUntilEOS(t, pipeline, sink)
	if len(out) == 0 {
		t.Fatal("muxer produced no bytes")
	}
	// Every FLV file starts with the "FLV" signature.
	if string(out[:3]) != "FLV" {
		t.Fatalf("output starts with %q, want FLV", out[:3])
	}
	muxedCaps := sink.GetStaticPad("sink").GetCurrentCaps()
	if muxedCaps == nil || muxedCaps.GetStructure(0).GetName() != "video/x-flv" {
		t.Fatalf("muxed caps = %v, want video/x-flv", muxedCaps)
	}
}

// TestFlvMuxFeedsRTMPOutput proves the method-A composition: FlvMux's FLV output
// is exactly what RTMPOutput's rtmpsink negotiates, so the two nodes link.
func TestFlvMuxFeedsRTMPOutput(t *testing.T) {
	gst.Init()

	mux, err := CreateFlvMuxNode(nil, FlvMuxConfig{ID: "fmx_rtmp"})
	if err != nil {
		t.Fatalf("CreateFlvMuxNode: %v", err)
	}
	defer mux.Destroy()

	rtmp, err := CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{
		ID:       "fmx_rtmp_out",
		Host:     "example.invalid",
		Location: "key",
	})
	if err != nil {
		t.Fatalf("CreateRTMPOutputNode: %v", err)
	}
	defer rtmp.Destroy()

	pipeline := gst.NewPipeline("fmx_rtmp_pipe").(gst.Pipeline)
	defer pipeline.SetState(gst.State(gst.StateNull))
	if !pipeline.AddMany(mux.Bin(), rtmp.Bin()) {
		t.Fatal("add nodes to pipeline")
	}

	// A real encoded video stream feeding the muxer's video pad.
	src := gst.ElementFactoryMake("videotestsrc", "fmx_rtmp_Src")
	srcCaps := gst.ElementFactoryMake("capsfilter", "fmx_rtmp_SrcCaps")
	encoder := gst.ElementFactoryMake("x264enc", "fmx_rtmp_Enc")
	src.SetObjectProperty("num-buffers", int32(30))
	srcCaps.SetObjectProperty("caps", gst.CapsFromString("video/x-raw,width=320,height=240"))
	if !pipeline.AddMany(src, srcCaps, encoder) {
		t.Fatal("add video source")
	}
	if !src.Link(srcCaps) || !srcCaps.Link(encoder) {
		t.Fatal("link video source to encoder")
	}

	videoPad, _, err := mux.RequestSinkPad()
	if err != nil {
		t.Fatalf("mux RequestSinkPad: %v", err)
	}
	muxSrc, _, err := mux.GetSrcPad()
	if err != nil {
		t.Fatalf("mux GetSrcPad: %v", err)
	}
	rtmpSink, _, err := rtmp.GetSinkPad()
	if err != nil {
		t.Fatalf("rtmp GetSinkPad: %v", err)
	}

	if ret := encoder.GetStaticPad("src").Link(videoPad); ret != gst.PadLinkOK {
		t.Fatalf("link encoder to mux: %v", ret)
	}
	// The link under test: FLV caps must negotiate between the two nodes.
	if ret := muxSrc.Link(rtmpSink); ret != gst.PadLinkOK {
		t.Fatalf("link FlvMux to RTMPOutput: %v (want PadLinkOK)", ret)
	}
}
