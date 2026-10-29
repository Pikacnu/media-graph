package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"live_tools/gst_utils/graph"
)

func runPreview(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("preview", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("running", os.Getenv("STREAM_DEMO_ENDPOINT"), "running serve endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *endpoint == "" {
		return fmt.Errorf("-running is required or set STREAM_DEMO_ENDPOINT")
	}
	response, err := http.Get(strings.TrimRight(*endpoint, "/") + "/preview")
	if err != nil {
		return fmt.Errorf("preview running graph: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("preview returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var snapshot graph.DesiredGraph
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return err
	}
	return writePreview(stdout, snapshot)
}

func writePreview(w io.Writer, snapshot graph.DesiredGraph) error {
	fmt.Fprintf(w, "graph=%s nodes=%d links=%d\n", snapshot.ID, len(snapshot.Nodes), len(snapshot.Links))
	for id, node := range snapshot.Nodes {
		fmt.Fprintf(w, "node=%s type=%s src=%d sink=%d\n", id, node.NodeType, len(node.SourcePads), len(node.SinkPads))
	}
	for _, link := range snapshot.Links {
		fmt.Fprintf(w, "link=%s %s -> %s\n", link.ID, link.SourcePadID, link.SinkPadID)
	}
	return nil
}
