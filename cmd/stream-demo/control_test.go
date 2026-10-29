package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"live_tools/gst_utils/core"
	"live_tools/gst_utils/graph"
)

type fakeRunningGraph struct {
	snapshot graph.DesiredGraph
	version  uint64
}

func (f *fakeRunningGraph) ApplyGraph(desired graph.DesiredGraph) error {
	f.snapshot = desired
	f.version++
	return nil
}
func (f *fakeRunningGraph) Snapshot() graph.DesiredGraph { return f.snapshot }
func (f *fakeRunningGraph) Version() uint64              { return f.version }
func (f *fakeRunningGraph) GetEntryByEntryID(string) (core.RegistryEntry, error) {
	return nil, nil
}

func TestServeHandlerHealthPreviewAndApply(t *testing.T) {
	fake := &fakeRunningGraph{snapshot: graph.DesiredGraph{ID: "demo"}, version: 1}
	server := httptest.NewServer(serveHandler(fake, "missing", 2, 2))
	defer server.Close()

	response, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d", response.StatusCode)
	}

	request := httptest.NewRequest(http.MethodPost, "/graph", strings.NewReader("id: updated\nnodes: {}\nlinks: []\n"))
	request.Header.Set("Content-Type", "application/yaml")
	recorder := httptest.NewRecorder()
	serveHandler(fake, "missing", 2, 2).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || fake.snapshot.ID != "updated" {
		t.Fatalf("apply status=%d body=%q id=%q", recorder.Code, recorder.Body.String(), fake.snapshot.ID)
	}
}
