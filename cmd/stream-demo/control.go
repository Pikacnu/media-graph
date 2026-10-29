package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"live_tools/gst_utils/core"
	"live_tools/gst_utils/graph"
)

func applyRunning(baseURL string, data []byte, extension string, stdout io.Writer) error {
	contentType := "application/json"
	if strings.EqualFold(extension, ".yaml") || strings.EqualFold(extension, ".yml") {
		contentType = "application/yaml"
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/graph", bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", contentType)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("apply to running graph: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("running graph returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	_, err = fmt.Fprintln(stdout, strings.TrimSpace(string(body)))
	return err
}

type runningGraph interface {
	ApplyGraph(graph.DesiredGraph) error
	Snapshot() graph.DesiredGraph
	Version() uint64
}

type outputSubscriber interface {
	GetEntryByEntryID(string) (core.RegistryEntry, error)
}

func serveHandler(mediaGraph interface {
	runningGraph
	outputSubscriber
}, outputNode string, width, height int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprintf(writer, "graph=%s version=%d\n", mediaGraph.Snapshot().ID, mediaGraph.Version())
	})
	mux.HandleFunc("/preview", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(mediaGraph.Snapshot())
	})
	mux.HandleFunc("/stream", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		output, err := subscribeOutput(mediaGraph, outputNode, "mjpeg", 2)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		writer.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		flusher, ok := writer.(http.Flusher)
		if !ok {
			http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		for {
			select {
			case <-request.Context().Done():
				return
			case data, ok := <-output:
				if !ok {
					return
				}
				frame, err := rgbToJPEG(data, width, height)
				if err != nil {
					http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
					return
				}
				fmt.Fprintf(writer, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
				if _, err := writer.Write(frame); err != nil {
					return
				}
				fmt.Fprint(writer, "\r\n")
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/graph", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		var desired graph.DesiredGraph
		path := "graph.json"
		if strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "yaml") {
			path = "graph.yaml"
		}
		if err := decodeGraphFile(path, body, &desired); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if err := mediaGraph.ApplyGraph(desired); err != nil {
			http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		fmt.Fprintf(writer, "applied graph=%s version=%d nodes=%d links=%d\n", desired.ID, mediaGraph.Version(), len(desired.Nodes), len(desired.Links))
	})
	return mux
}
