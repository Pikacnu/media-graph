package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"live_tools/gst_utils/graph"

	"github.com/goccy/go-yaml"
)

func decodeGraphFile(path string, data []byte, desired *graph.DesiredGraph) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		jsonData, err := yaml.YAMLToJSON(data)
		if err != nil {
			return fmt.Errorf("decode graph YAML: %w", err)
		}
		if err := json.Unmarshal(jsonData, desired); err != nil {
			return fmt.Errorf("decode graph YAML: %w", err)
		}
	default:
		if err := json.Unmarshal(data, desired); err != nil {
			return fmt.Errorf("decode graph JSON: %w", err)
		}
	}
	return nil
}

func loadGraph(path, graphID string) (graph.DesiredGraph, error) {
	if path == "" {
		desired := DemoGraph()
		desired.ID = graphID
		return desired, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return graph.DesiredGraph{}, err
	}
	var desired graph.DesiredGraph
	if err := decodeGraphFile(path, data, &desired); err != nil {
		return graph.DesiredGraph{}, err
	}
	if desired.ID == "" {
		desired.ID = graphID
	}
	return desired, nil
}

func runInspect(args []string, stdout, stderr io.Writer) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: stream-demo inspect [node-type]")
	}
	if len(args) == 1 {
		detail, err := InspectNode(args[0])
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, detail)
		return err
	}
	_, err := io.WriteString(stdout, InspectSummary())
	return err
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  stream-demo apply -graph <file.json|file.yaml> [-id graph-id]
  stream-demo run [-graph file.json|file.yaml] [-input file] [-chunk-size bytes]
  stream-demo serve -graph <file.json|file.yaml> [-id graph-id] [-listen 127.0.0.1:8091] [-output-node demo_output]
  stream-demo inspect [node-type]
  stream-demo check -graph <file.json|file.yaml>
  stream-demo preview [-running http://127.0.0.1:8091]
  stream-demo help

Commands:
  apply    Load a DesiredGraph from JSON or YAML and apply it once.
  run      Build a graph, send input bytes, read output bytes, and exit.
  serve    Keep an applied graph running with a local HTTP control endpoint.
  inspect  List registered node types, or inspect one node type.
  check    Validate a graph and report source/parse/link warnings.
  preview  Show the current graph snapshot from a running serve process.

The running server also exposes GET /stream as an MJPEG preview for the default raw RGB demo output.

Examples:
  stream-demo inspect
  stream-demo inspect SourceInput
  stream-demo apply -graph examples/graph.yaml
  stream-demo run -input sample.raw`)
}
