package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"live_tools/gst_utils/graph"

	"github.com/go-gst/go-gst/pkg/gst"
)

func TestHelpListsGraphInput(t *testing.T) {
	var stdout bytes.Buffer
	printUsage(&stdout)
	for _, want := range []string{"-graph file.json|file.yaml", "preview", "serve"} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Fatalf("help missing %q: %q", want, stdout.String())
		}
	}
}

func TestDecodeGraphFileJSONAndYAML(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		data []byte
	}{
		{name: "yaml", path: "graph.yaml", data: []byte("id: yaml-demo\nnodes: {}\nlinks: []\n")},
		{name: "json", path: "graph.json", data: []byte(`{"id":"json-demo","nodes":{},"links":[]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var graphValue graph.DesiredGraph
			if err := decodeGraphFile(test.path, test.data, &graphValue); err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := test.name + "-demo"
			if graphValue.ID != want {
				t.Fatalf("id = %q, want %q", graphValue.ID, want)
			}
		})
	}
}

func TestRunInspect(t *testing.T) {
	var stdout bytes.Buffer
	if err := runInspect([]string{"SourceInput"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, want := range []string{"SourceInput", "Sink pads", "Source pads"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("inspect missing %q: %q", want, stdout.String())
		}
	}
}

func TestRunCheckValidGraph(t *testing.T) {
	path := writeGraphFile(t, `id: check-demo
nodes:
  source:
    id: source
    node_type: TestSource
    source_pad_ids:
      - id: source_src_0
  output:
    id: output
    node_type: Output
    properties:
      caps: video/x-raw,format=RGB,width=2,height=2,framerate=1/1
    sink_pad_ids:
      - id: output_sink_0
links:
  - id: source_output
    source_pad_id: source_src_0
    sink_pad_id: output_sink_0
`, ".yaml")
	var stdout bytes.Buffer
	if err := runCheck([]string{"-graph", path}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(stdout.String(), "valid graph=check-demo nodes=2 links=1") || !strings.Contains(stdout.String(), "no ContainerParse") {
		t.Fatalf("unexpected check output: %q", stdout.String())
	}
}

func TestGraphWarningForMissingSourceAndParse(t *testing.T) {
	warnings := graphWarnings(graph.DesiredGraph{ID: "empty", Nodes: map[string]graph.NodeInfo{}})
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "no source node") || !strings.Contains(joined, "no ContainerParse") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestRunCheckRejectsUnknownNode(t *testing.T) {
	path := writeGraphFile(t, `id: bad-demo
nodes:
  bad:
    id: bad
    node_type: DoesNotExist
links: []
`, ".yaml")
	if err := runCheck([]string{"-graph", path}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("check accepted an unknown node type")
	}
}

func TestRunApply(t *testing.T) {
	gst.Init()
	path := writeGraphFile(t, `{"id":"stream-demo","nodes":{},"links":[]}`, ".json")
	var stdout bytes.Buffer
	if err := runApply([]string{"-graph", path}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(stdout.String(), "applied graph=stream-demo version=0") {
		t.Fatalf("unexpected apply output: %q", stdout.String())
	}
}

func TestRunDemoMovesGStreamerBytes(t *testing.T) {
	gst.Init()
	inputPath := filepath.Join(t.TempDir(), "frame.raw")
	if err := os.WriteFile(inputPath, []byte{0, 0, 0, 255, 255, 0, 0, 255, 0, 0, 255, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := runDemo([]string{"-input", inputPath, "-settle", "2s"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "input_bytes=12 output_bytes=12") {
		t.Fatalf("unexpected run output: %q", stdout.String())
	}
}

func TestVersionReportsBuildMetadata(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"version"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("version: %v", err)
	}
	for _, want := range []string{"stream-demo dev", "commit none", "gstreamer "} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("version missing %q: %q", want, stdout.String())
		}
	}
}

func writeGraphFile(t *testing.T, content, extension string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph"+extension)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
