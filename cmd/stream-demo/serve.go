package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"live_tools/gst_utils/graph"

	"github.com/go-gst/go-gst/pkg/gst"
)

func runServe(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	graphFile := flags.String("graph", "", "path to DesiredGraph JSON/YAML")
	graphID := flags.String("id", "stream-demo", "graph ID")
	listen := flags.String("listen", os.Getenv("STREAM_DEMO_LISTEN"), "local HTTP control address")
	outputNode := flags.String("output-node", "demo_output", "Output node used by /stream")
	width := flags.Int("width", 2, "raw RGB frame width for /stream")
	height := flags.Int("height", 2, "raw RGB frame height for /stream")
	if *listen == "" {
		*listen = "127.0.0.1:8091"
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *graphFile == "" {
		return fmt.Errorf("-graph is required")
	}

	desired, err := loadGraph(*graphFile, *graphID)
	if err != nil {
		return err
	}
	mediaGraph, err := graph.CreateMediaGraph(*graphID)
	if err != nil {
		return err
	}
	defer mediaGraph.Destroy()
	if err := mediaGraph.ApplyGraph(desired); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if err := mediaGraph.SetState(gst.State(gst.StatePlaying)); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server := &http.Server{
		Addr: *listen, Handler: serveHandler(mediaGraph, *outputNode, *width, *height),
		BaseContext: func(net.Listener) context.Context { return requests },
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "serving graph=%s version=%d control=http://%s; press Ctrl+C to stop\n", desired.ID, mediaGraph.Version(), *listen)
	select {
	case <-ctx.Done():
		fmt.Fprintln(stdout, "stopping graph")
	case err := <-serverErr:
		if err != http.ErrServerClosed {
			return fmt.Errorf("control server: %w", err)
		}
	}
	stop()
	cancelRequests()
	return shutdownServer(server)
}

func shutdownServer(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		// A slow stream client may block a write even after request cancellation.
		if closeErr := server.Close(); closeErr != nil {
			return fmt.Errorf("close control server: %w", closeErr)
		}
		if err != context.DeadlineExceeded {
			return fmt.Errorf("shutdown control server: %w", err)
		}
	}
	return nil
}
