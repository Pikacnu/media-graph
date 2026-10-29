package graph

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// buildProbePipeline returns a minimal appsrc -> queue -> appsink chain plus
// helpers, so each subtest can exercise one event sequence in isolation.
func buildProbePipeline(t *testing.T) (gstapp.AppSrc, gstapp.AppSink, gst.Pad, gst.Pipeline) {
	t.Helper()

	element, err := gst.ParseLaunch(
		"appsrc name=src is-live=false do-timestamp=true ! queue name=q ! appsink name=sink sync=false",
	)
	if err != nil {
		t.Fatalf("ParseLaunch: %v", err)
	}
	pipeline, ok := element.(gst.Pipeline)
	if !ok || pipeline == nil {
		t.Fatal("parsed element is not a pipeline")
	}
	src, ok := pipeline.GetByName("src").(gstapp.AppSrc)
	if !ok || src == nil {
		t.Fatal("appsrc not found")
	}
	sink, ok := pipeline.GetByName("sink").(gstapp.AppSink)
	if !ok || sink == nil {
		t.Fatal("appsink not found")
	}
	queue := pipeline.GetByName("q")
	if queue == nil {
		t.Fatal("queue not found")
	}
	sink.SetEmitSignals(false)
	src.SetObjectProperties(map[string]any{
		"caps": gst.CapsFromString("video/x-raw,format=RGB,width=64,height=48,framerate=30/1"),
	})

	if result := pipeline.SetState(gst.State(gst.StatePlaying)); result == gst.StateChangeFailure {
		t.Fatalf("SetState(Playing): %v", result)
	}
	t.Cleanup(func() { pipeline.SetState(gst.State(gst.StateNull)) })

	probePad := queue.GetStaticPad("src")
	if probePad == nil {
		t.Fatal("queue src pad not found")
	}
	return src, sink, probePad, pipeline
}

func probePush(src gstapp.AppSrc, count int) int {
	pushed := 0
	for i := 0; i < count; i++ {
		buffer := gst.NewBufferAllocate(nil, probeFrameBytes, nil)
		mapped, ok := buffer.Map(gst.MapFlags(gst.MapWrite))
		if !ok {
			return pushed
		}
		mapped.Unmap()
		if result := src.PushBuffer(buffer); result != gst.FlowOK {
			return pushed
		}
		pushed++
	}
	return pushed
}

func probeDrain(sink gstapp.AppSink) int {
	received := 0
	for {
		sample := sink.TryPullSample(gst.ClockTime(200 * time.Millisecond))
		if sample == nil {
			break
		}
		received++
	}
	return received
}

// TestProbeFlushSequenceIsolation finds the MINIMAL event sequence that brings
// a downstream back out of EOS. §7.3.3 assumed FLUSH_START + FLUSH_STOP; this
// checks whether both are actually required.
func TestProbeFlushSequenceIsolation(t *testing.T) {
	setupGraphTest(t)

	cases := []struct {
		name  string
		flush func(pad gst.Pad)
	}{
		{
			name:  "no flush",
			flush: func(gst.Pad) {},
		},
		{
			name:  "flush_stop only",
			flush: func(pad gst.Pad) { pad.PushEvent(gst.NewEventFlushStop(true)) },
		},
		{
			name:  "flush_start only",
			flush: func(pad gst.Pad) { pad.PushEvent(gst.NewEventFlushStart()) },
		},
		{
			name: "flush_start + flush_stop",
			flush: func(pad gst.Pad) {
				pad.PushEvent(gst.NewEventFlushStart())
				pad.PushEvent(gst.NewEventFlushStop(true))
			},
		},
		{
			name: "flush_start + flush_stop(reset=false)",
			flush: func(pad gst.Pad) {
				pad.PushEvent(gst.NewEventFlushStart())
				pad.PushEvent(gst.NewEventFlushStop(false))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, sink, probePad, _ := buildProbePipeline(t)

			probePush(src, 5)
			baseline := probeDrain(sink)
			if baseline != 5 {
				t.Fatalf("baseline expected 5, got %d", baseline)
			}

			probePad.PushEvent(gst.NewEventEOS())
			probeDrain(sink)
			if !sink.IsEOS() {
				t.Fatal("expected IsEOS after EOS event")
			}

			tc.flush(probePad)

			probePush(src, 5)
			recovered := probeDrain(sink)
			t.Logf("recovered=%d IsEOS=%v", recovered, sink.IsEOS())
		})
	}
}

// TestProbeSegmentAfterFlush checks whether a SEGMENT event is required after
// FLUSH_STOP before data is accepted cleanly. The first probe run emitted
// "Got data flow before segment event", so this pins down the correct sequence.
func TestProbeSegmentAfterFlush(t *testing.T) {
	setupGraphTest(t)

	cases := []struct {
		name         string
		segmentAfter bool
	}{
		{name: "no segment after flush", segmentAfter: false},
		{name: "segment after flush", segmentAfter: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, sink, probePad, _ := buildProbePipeline(t)

			probePush(src, 5)
			if got := probeDrain(sink); got != 5 {
				t.Fatalf("baseline expected 5, got %d", got)
			}

			probePad.PushEvent(gst.NewEventEOS())
			probeDrain(sink)

			probePad.PushEvent(gst.NewEventFlushStart())
			probePad.PushEvent(gst.NewEventFlushStop(true))

			if tc.segmentAfter {
				segment := gst.NewSegment()
				segment.Init(gst.Format(gst.FormatTime))
				if !probePad.PushEvent(gst.NewEventSegment(segment)) {
					t.Logf("PushEvent(SEGMENT) returned false")
				}
			}

			probePush(src, 5)
			recovered := probeDrain(sink)
			t.Logf("recovered=%d IsEOS=%v", recovered, sink.IsEOS())
		})
	}
}
