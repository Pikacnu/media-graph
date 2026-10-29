package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"live_tools/gst_utils/graph"
	_ "live_tools/gst_utils/nodes"

	"github.com/go-gst/go-gst/pkg/gst"
)

func main() {
	gst.Init()
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "stream-demo:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "apply":
		return runApply(args[1:], stdout, stderr)
	case "run":
		return runDemo(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "status":
		return fmt.Errorf("status requires an active in-process session; use run for the self-contained demo")
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "preview":
		return runPreview(args[1:], stdout, stderr)
	case "version":
		printVersion(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runApply(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	graphFile := flags.String("graph", "", "path to DesiredGraph JSON")
	graphID := flags.String("id", "stream-demo", "graph ID")
	runningURL := flags.String("running", os.Getenv("STREAM_DEMO_ENDPOINT"), "apply to a running serve process")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *graphFile == "" {
		return fmt.Errorf("-graph is required")
	}

	data, err := os.ReadFile(*graphFile)
	if err != nil {
		return err
	}
	var desired graph.DesiredGraph
	if err := decodeGraphFile(*graphFile, data, &desired); err != nil {
		return err
	}
	if desired.ID == "" {
		desired.ID = *graphID
	}
	if *runningURL != "" {
		return applyRunning(*runningURL, data, filepath.Ext(*graphFile), stdout)
	}

	mediaGraph, err := graph.CreateMediaGraph(*graphID)
	if err != nil {
		return err
	}
	defer mediaGraph.Destroy()
	if err := mediaGraph.ApplyGraph(desired); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "applied graph=%s version=%d nodes=%d links=%d\n", desired.ID, mediaGraph.Version(), len(desired.Nodes), len(desired.Links))
	return nil
}

func runDemo(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	graphFile := flags.String("graph", "", "path to DesiredGraph JSON/YAML; defaults to built-in demo graph")
	inputFile := flags.String("input", "", "input bytes file; defaults to stdin")
	chunkSize := flags.Int("chunk-size", 4096, "input chunk size")
	settle := flags.Duration("settle", 500*time.Millisecond, "time to allow the output sink to flush")
	if err := flags.Parse(args); err != nil {
		return err
	}

	mediaGraph, err := graph.CreateMediaGraph("stream-demo")
	if err != nil {
		return err
	}
	defer mediaGraph.Destroy()

	desired, err := loadGraph(*graphFile, "stream-demo")
	if err != nil {
		return err
	}
	if err := mediaGraph.ApplyGraph(desired); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if err := mediaGraph.SetState(gst.State(gst.StatePlaying)); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	input := io.Reader(os.Stdin)
	var file *os.File
	if *inputFile != "" {
		file, err = os.Open(*inputFile)
		if err != nil {
			return err
		}
		defer file.Close()
		input = file
	}

	output, err := subscribeOutput(mediaGraph, "demo_output", "cli", 8)
	if err != nil {
		return fmt.Errorf("subscribe output: %w", err)
	}
	sent, err := sendInput(mediaGraph, "demo_input", input, *chunkSize)
	if err != nil {
		return fmt.Errorf("send input after %d bytes: %w", sent, err)
	}
	select {
	case data, ok := <-output:
		if !ok {
			return fmt.Errorf("output stopped")
		}
		fmt.Fprintf(stdout, "applied version=%d input_bytes=%d output_bytes=%d\n", mediaGraph.Version(), sent, len(data))
	case <-time.After(*settle):
		return fmt.Errorf("timed out waiting for output")
	}
	return nil
}
