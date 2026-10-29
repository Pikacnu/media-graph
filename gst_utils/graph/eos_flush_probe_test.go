package graph

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// probeFrameBytes matches the caps below: 64x48 RGB.
const probeFrameBytes = 64 * 48 * 3

// TestProbeEOSThenFlushRecovery answers the questions left open in
// docs/design/container-parse-next-steps.md §7.3.3:
//
//  1. After EOS, does the downstream really refuse further data?
//  2. Which direction must FLUSH_START / FLUSH_STOP be pushed?
//  3. Can FLUSH_STOP bring the downstream back out of EOS?
//
// It uses a bare appsrc -> queue -> appsink chain. No ContainerParseNode is
// involved because the question is pure GStreamer event semantics: the node
// does not exist yet, and the answer decides whether it can exist.
func TestProbeEOSThenFlushRecovery(t *testing.T) {
	setupGraphTest(t)

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
	defer pipeline.SetState(gst.State(gst.StateNull))

	// The queue's src pad stands in for the ghost pad: it is an output pad
	// whose downstream we are probing.
	probePad := queue.GetStaticPad("src")
	if probePad == nil {
		t.Fatal("queue src pad not found")
	}

	// pushFrames reports a failure instead of aborting, because after EOS the
	// push itself is one of the things under test.
	pushFrames := func(count int) int {
		pushed := 0
		for i := 0; i < count; i++ {
			buffer := gst.NewBufferAllocate(nil, probeFrameBytes, nil)
			mapped, ok := buffer.Map(gst.MapFlags(gst.MapWrite))
			if !ok {
				t.Logf("push: map buffer %d failed", i)
				return pushed
			}
			mapped.Unmap()
			if result := src.PushBuffer(buffer); result != gst.FlowOK {
				t.Logf("push: PushBuffer %d returned %v", i, result)
				return pushed
			}
			pushed++
		}
		return pushed
	}

	drain := func(label string) int {
		received := 0
		for {
			sample := sink.TryPullSample(gst.ClockTime(200 * time.Millisecond))
			if sample == nil {
				break
			}
			received++
		}
		t.Logf("%-18s received=%d IsEOS=%v", label, received, sink.IsEOS())
		return received
	}

	// --- 1. baseline ----------------------------------------------------
	pushFrames(5)
	if got := drain("baseline"); got != 5 {
		t.Fatalf("baseline expected 5 buffers, got %d", got)
	}

	// --- 2. EOS downstream ----------------------------------------------
	t.Logf("pushing EOS on %s", probePad.GetName())
	if !probePad.PushEvent(gst.NewEventEOS()) {
		t.Fatal("PushEvent(EOS) returned false")
	}
	drain("after EOS")

	// --- 3. does data still flow after EOS? -----------------------------
	pushedAfterEOS := pushFrames(5)
	afterEOS := drain("data after EOS")
	t.Logf("after EOS: pushed=%d delivered=%d", pushedAfterEOS, afterEOS)

	// --- 4. flush downstream --------------------------------------------
	t.Logf("pushing FLUSH_START + FLUSH_STOP on %s", probePad.GetName())
	if !probePad.PushEvent(gst.NewEventFlushStart()) {
		t.Logf("PushEvent(FLUSH_START) returned false")
	}
	if !probePad.PushEvent(gst.NewEventFlushStop(true)) {
		t.Logf("PushEvent(FLUSH_STOP) returned false")
	}
	drain("after flush")

	// --- 5. does data flow again? ---------------------------------------
	pushedAfterFlush := pushFrames(5)
	afterFlush := drain("data after flush")
	t.Logf("after flush: pushed=%d delivered=%d", pushedAfterFlush, afterFlush)

	t.Logf("SUMMARY afterEOS=%d afterFlush=%d IsEOS=%v", afterEOS, afterFlush, sink.IsEOS())
}
