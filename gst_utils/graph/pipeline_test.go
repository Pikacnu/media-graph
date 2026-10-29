package graph

import (
	"testing"
)

func TestCreateTestPipeline(t *testing.T) {
	setupGraphTest(t)
	pipeline, err := CreateTestPipeline()
	if err != nil {
		t.Fatalf("CreateTestPipeline: %v", err)
	}
	defer func() {
		if err := pipeline.Destroy(); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	}()

	graphNodes := pipeline.graph.nodesForTest()
	for _, id := range []string{"test_input", "test_cloner", "test_output"} {
		if _, ok := graphNodes[id]; !ok {
			t.Errorf("node %q was not added to the graph", id)
		}
	}

	applied := map[string]bool{}
	for _, link := range pipeline.graph.currentGraphLinks {
		applied[link.SourcePadID+"->"+link.SinkPadID] = true
	}
	for _, want := range []string{
		"test_input_src_0->test_cloner_sink_0",
		"test_cloner_src_1->test_output_sink_0",
	} {
		if !applied[want] {
			t.Errorf("link %q was not applied; applied links: %v", want, applied)
		}
	}

	ch, err := pipeline.OutputSubscriber("test-reader", 8)
	if err != nil {
		t.Fatalf("OutputSubscriber: %v", err)
	}
	if ch == nil {
		t.Fatal("OutputSubscriber returned a nil channel")
	}
}
