package graph

import (
	"errors"
	"sync"
	"testing"
	"time"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// newLoopTestGraph builds a graph with a live event loop and registers teardown.
func newLoopTestGraph(t *testing.T, id string) *mediaGraphImpl {
	t.Helper()
	setupGraphTest(t)
	graph, err := CreateMediaGraph(id)
	if err != nil {
		t.Fatalf("CreateMediaGraph: %v", err)
	}
	t.Cleanup(func() {
		// Destroy may already have run inside the test; that is fine.
		if err := graph.Destroy(); err != nil && !errors.Is(err, ErrGraphDestroyed) {
			t.Errorf("Destroy: %v", err)
		}
	})
	return graph
}

// TestMainLoopAppliesThroughTheLoop proves the loop is what runs the work: the
// version only moves after an event has been processed, and the caller gets the
// handler's error back through the reply channel.
func TestMainLoopAppliesThroughTheLoop(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if got := graph.Version(); got != 0 {
		t.Fatalf("fresh graph version = %d, want 0", got)
	}

	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if got := graph.Version(); got != 1 {
		t.Fatalf("after apply version = %d, want 1", got)
	}
}

// TestNoOpApplyDoesNotMoveVersion pins that re-applying the graph that is already
// applied changes nothing, so appliedVersion stays put.
//
// The version is the frontend's optimistic-concurrency token: if a request that
// changes nothing moved it, every client holding the old value would be told its
// edit was stale, and it would reload for no reason.
func TestNoOpApplyDoesNotMoveVersion(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if got := graph.Version(); got != 1 {
		t.Fatalf("after first apply version = %d, want 1", got)
	}

	for i := range 3 {
		if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
			t.Fatalf("no-op apply %d: %v", i, err)
		}
		if got := graph.Version(); got != 1 {
			t.Fatalf("after no-op apply %d version = %d, want 1", i, got)
		}
	}

	// A real change must still move it, so the guard cannot be a blanket freeze.
	// The new value must differ from testDesiredGraph's own caps, or this would
	// itself be a no-op.
	changed := testDesiredGraph()
	output := changed.Nodes[testOutputNode]
	output.Properties = map[string]any{"caps": "video/x-raw,format=I420"}
	changed.Nodes[testOutputNode] = output
	if err := graph.ApplyGraph(changed); err != nil {
		t.Fatalf("ApplyGraph with a changed node: %v", err)
	}
	if got := graph.Version(); got != 2 {
		t.Fatalf("after a changed apply version = %d, want 2", got)
	}
}

// TestGraphDiffIsEmpty pins the emptiness predicate directly, including the nil
// receiver, so a caller can ask without a nil check of its own.
func TestGraphDiffIsEmpty(t *testing.T) {
	var nilDiff *GraphDiff
	if !nilDiff.IsEmpty() {
		t.Error("nil *GraphDiff: IsEmpty() = false, want true")
	}
	if !(&GraphDiff{}).IsEmpty() {
		t.Error("zero GraphDiff: IsEmpty() = false, want true")
	}

	// Every field must be able to make it non-empty on its own.
	cases := map[string]*GraphDiff{
		"AddNodes":              {AddNodes: []NodeInfo{{ID: "a"}}},
		"UpdatePropertiesNodes": {UpdatePropertiesNodes: []NodeInfo{{ID: "a"}}},
		"RemoveNodes":           {RemoveNodes: []NodeInfo{{ID: "a"}}},
		"AddPads":               {AddPads: []NodeInfo{{ID: "a"}}},
		"UpdatePads":            {UpdatePads: []PadInfo{{ID: "p"}}},
		"RemovePads":            {RemovePads: []PadInfo{{ID: "p"}}},
		"AddLinks":              {AddLinks: []LinkInfo{{ID: "l"}}},
		"RemoveLinks":           {RemoveLinks: []LinkInfo{{ID: "l"}}},
	}
	for name, diff := range cases {
		if diff.IsEmpty() {
			t.Errorf("%s set: IsEmpty() = true, want false", name)
		}
	}
}

// TestMainLoopReturnsReplyPayload pins that the loop answers every command with
// an error payload, and that a successful command answers nil. Without the reply
// the caller would block forever, which is the failure mode the reply channel
// exists to prevent.
func TestMainLoopReturnsReplyPayload(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := graph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState(Playing): %v", err)
	}

	bad := testDesiredGraph()
	bad.ID = "some_other_graph"
	if err := graph.ApplyGraph(bad); err == nil {
		t.Fatal("ApplyGraph with a mismatched graph ID: want an error, got nil")
	}

	// The loop must still be alive after a rejected command.
	if err := graph.SetState(gst.State(gst.StateNull)); err != nil {
		t.Fatalf("SetState(Null) after a rejected apply: %v", err)
	}
}

// TestGetStateUsesPipelineBusState proves that GetState reads MediaGraph's
// cached pipeline state and observes the asynchronous transition.
func TestGetStateUsesPipelineBusState(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := graph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState(Playing): %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, pending, err := graph.GetState()
		if err != nil {
			t.Fatalf("GetState while alive: %v", err)
		}
		if current == gst.StatePaused || current == gst.StatePlaying || pending == gst.StatePlaying {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, pending, _ := graph.GetState()
	t.Fatalf("GetState = (%v, %v), want PAUSED/PLAYING or pending PLAYING", current, pending)
}

func TestRuntimeEventForwardingAndMarkDirtyBoundary(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)
	runtimeEvents := graph.RuntimeEvents()
	bus := graph.bus.(*busControllerImpl)

	version := graph.Version()
	bus.publish(BusEvent{
		Type:   BusEventTypeError,
		Source: "decoder",
		Error:  errors.New("decoder failed"),
	})

	select {
	case event := <-runtimeEvents:
		if event.Type != core.RuntimeEventBusError {
			t.Fatalf("runtime event type = %q, want %q", event.Type, core.RuntimeEventBusError)
		}
		if event.Error != "decoder failed" {
			t.Fatalf("runtime event error = %q, want decoder failed", event.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for normalized bus error")
	}

	bus.publish(BusEvent{
		Type:     BusEventTypeStateChanged,
		Source:   graph.id,
		OldState: gst.StateReady,
		NewState: gst.StatePlaying,
		Pending:  gst.StateVoidPending,
	})
	select {
	case event := <-runtimeEvents:
		if event.Type != core.RuntimeEventStateChanged {
			t.Fatalf("runtime state event type = %q, want %q", event.Type, core.RuntimeEventStateChanged)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for normalized state event")
	}
	current, pending, err := graph.GetState()
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if current != gst.StatePlaying || pending != gst.StateVoidPending {
		t.Fatalf("GetState = (%v, %v), want (PLAYING, VOID_PENDING)", current, pending)
	}

	bus.publish(BusEvent{Type: BusEventTypeStateDirty, Source: graph.id})
	deadline := time.Now().Add(time.Second)
	for !graph.IsDirty() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !graph.IsDirty() {
		t.Fatal("StateDirty did not mark the graph dirty")
	}
	if got := graph.Version(); got != version {
		t.Fatalf("dirty runtime observation changed applied version to %d, want %d", got, version)
	}
	select {
	case event := <-runtimeEvents:
		t.Fatalf("StateDirty leaked runtime event %q", event.Type)
	default:
	}
}

// TestMainLoopSurvivesSetStateAndDestroyOrdering pins that commands are applied
// in queue order: a state change sent before the destroy is executed before it,
// and the destroy is what ends the loop.
func TestMainLoopSurvivesSetStateAndDestroyOrdering(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}
	if err := graph.SetState(gst.State(gst.StatePlaying)); err != nil {
		t.Fatalf("SetState(Playing): %v", err)
	}
	if err := graph.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// A second Destroy must not block: the loop has exited, so it answers from
	// the stopped loop's context instead of sending a message nobody would read.
	if err := graph.Destroy(); !errors.Is(err, ErrGraphDestroyed) {
		t.Fatalf("second Destroy = %v, want ErrGraphDestroyed", err)
	}
}

// TestCommandsAfterDestroyDoNotBlock pins the destroyed guard for every command,
// not just Destroy. A caller that lost the race with teardown must get an error
// rather than a hang.
func TestCommandsAfterDestroyDoNotBlock(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if err := graph.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if err := graph.ApplyGraph(testDesiredGraph()); !errors.Is(err, ErrGraphDestroyed) {
		t.Fatalf("ApplyGraph after Destroy = %v, want ErrGraphDestroyed", err)
	}
	if err := graph.SetState(gst.State(gst.StatePlaying)); !errors.Is(err, ErrGraphDestroyed) {
		t.Fatalf("SetState after Destroy = %v, want ErrGraphDestroyed", err)
	}
	if err := graph.Destroy(); !errors.Is(err, ErrGraphDestroyed) {
		t.Fatalf("Destroy after Destroy = %v, want ErrGraphDestroyed", err)
	}
}

// TestMediaGraphOwnsBusSubscriptionLifecycle pins that a bus subscription is
// owned by the graph and is closed after graph teardown, rather than leaving a
// listener waiting on a pipeline that no longer exists.
func TestMediaGraphOwnsBusSubscriptionLifecycle(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	busEvents, err := graph.RegisterBusEventsChan()
	if err != nil {
		t.Fatalf("RegisterBusEventsChan: %v", err)
	}
	if busEvents == nil {
		t.Fatal("RegisterBusEventsChan returned a nil channel")
	}

	if err := graph.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	select {
	case _, ok := <-busEvents:
		if ok {
			t.Fatal("bus event channel remained open after graph destruction")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bus event channel to close")
	}

	if _, err := graph.RegisterBusEventsChan(); !errors.Is(err, ErrGraphDestroyed) {
		t.Fatalf("RegisterBusEventsChan after Destroy = %v, want ErrGraphDestroyed", err)
	}
}

// TestConcurrentAppliesAreSerialized runs many commands at once. The point is not
// which one wins but that every call returns (no lost reply) and the race
// detector sees no unsynchronised access to the graph state.
func TestConcurrentAppliesAreSerialized(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if err := graph.ApplyGraph(testDesiredGraph()); err != nil {
		t.Fatalf("ApplyGraph: %v", err)
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker sends a state change: cheap, ordered, and it still has
			// to travel through the queue and come back.
			results[i] = graph.SetState(gst.State(gst.StatePlaying))
		}()
	}
	wg.Wait()

	for i, err := range results {
		switch {
		case err == nil:
		case errors.Is(err, ErrGraphBusy):
			// Acceptable: the queue is bounded on purpose.
		default:
			t.Fatalf("worker %d: unexpected error %v", i, err)
		}
	}

	// Readers must still work while commands flow.
	if got := graph.Version(); got != 1 {
		t.Fatalf("version after concurrent commands = %d, want 1", got)
	}
}

// TestApplyGraphIfVersionRejectsStaleBase pins the optimistic-concurrency
// contract: an apply whose base is not the current version is refused, and the
// refusal must leave the graph untouched so the caller can re-read and retry.
func TestApplyGraphIfVersionRejectsStaleBase(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	if err := graph.ApplyGraphIfVersion(testDesiredGraph(), 0); err != nil {
		t.Fatalf("ApplyGraphIfVersion with a current base: %v", err)
	}
	if got := graph.Version(); got != 1 {
		t.Fatalf("version after a successful apply = %d, want 1", got)
	}

	// Base 0 is now stale: the graph has moved on.
	changed := testDesiredGraph()
	output := changed.Nodes[testOutputNode]
	output.Properties = map[string]any{"caps": "video/x-raw,format=I420"}
	changed.Nodes[testOutputNode] = output

	err := graph.ApplyGraphIfVersion(changed, 0)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("ApplyGraphIfVersion with a stale base = %v, want ErrVersionMismatch", err)
	}
	if got := graph.Version(); got != 1 {
		t.Fatalf("version after a rejected apply = %d, want 1 (rejection must not move it)", got)
	}

	// The rejected change must not have landed, so re-diffing reports it again.
	settled, diffErr := DiffGraph(graph.GetAppliedGraph(), changed)
	if diffErr != nil {
		t.Fatalf("DiffGraph: %v", diffErr)
	}
	if settled.IsEmpty() {
		t.Error("the rejected apply was partially applied: diff against applied is empty")
	}

	// The same edit succeeds once the caller uses the version it just read.
	if err := graph.ApplyGraphIfVersion(changed, graph.Version()); err != nil {
		t.Fatalf("ApplyGraphIfVersion with a fresh base: %v", err)
	}
	if got := graph.Version(); got != 2 {
		t.Fatalf("version after the retried apply = %d, want 2", got)
	}
}

// TestFailedApplyLeavesNoNodeBehind pins that a node rejected during construction
// is unwound. A half-built node never reaches currentGraphNodes, so the rollback
// diff cannot see it; only the apply loop itself can clean it up.
//
// An unsupported node type fails before the node exists, so it cannot test this.
// A pad count above the type's spec gets past construction and fails in
// ValidateNode, which is the first step that has resources to release.
func TestFailedApplyLeavesNoNodeBehind(t *testing.T) {
	graph := newLoopTestGraph(t, testPipelineID)

	before := len(graph.nodesForTest())

	bad := testDesiredGraph()
	// SourceInput is 0->1; a second source pad is outside its spec.
	extra := bad.Nodes[testSourceNode]
	extra.SourcePads = append(extra.SourcePads, PadInfo{ID: testSourceNode + "_src_1"})
	bad.Nodes[testSourceNode] = extra

	if err := graph.ApplyGraph(bad); err == nil {
		t.Fatal("ApplyGraph with an over-spec node: want an error, got nil")
	}

	if after := len(graph.nodesForTest()); after != before {
		t.Fatalf("node count after a failed apply = %d, want %d (the rejected node leaked)", after, before)
	}
	if got := graph.Version(); got != 0 {
		t.Fatalf("version after a failed apply = %d, want 0", got)
	}
}
