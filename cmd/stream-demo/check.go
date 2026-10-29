package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"live_tools/gst_utils/graph"
)

func runCheck(args []string, stdout, stderr io.Writer) error {
	if len(args) != 2 || args[0] != "-graph" {
		return fmt.Errorf("usage: stream-demo check -graph <file.json|file.yaml>")
	}
	desired, err := loadGraph(args[1], "stream-check")
	if err != nil {
		return err
	}

	if err := graph.ValidateGraph(desired); err != nil {
		return fmt.Errorf("graph is invalid: %w", err)
	}

	fmt.Fprintf(stdout, "valid graph=%s nodes=%d links=%d\n", desired.ID, len(desired.Nodes), len(desired.Links))
	for _, warning := range graphWarnings(desired) {
		fmt.Fprintf(stdout, "warning: %s\n", warning)
	}
	return nil
}

func graphWarnings(desired graph.DesiredGraph) []string {
	warnings := make([]string, 0, 2)
	hasSource, hasParse := false, false
	for _, node := range desired.Nodes {
		switch node.NodeType {
		case "SourceInput", "ImageSource", "TestSource":
			hasSource = true
		case "ContainerParse":
			hasParse = true
		}
	}
	if !hasSource {
		warnings = append(warnings, "graph has no source node (SourceInput, ImageSource, or TestSource)")
	}
	if !hasParse {
		warnings = append(warnings, "graph has no ContainerParse node; input is not being demuxed")
	}

	knownPads := make(map[string]struct{})
	for id, node := range desired.Nodes {
		for _, pad := range node.SourcePads {
			knownPads[pad.ID] = struct{}{}
		}
		for _, pad := range node.SinkPads {
			knownPads[pad.ID] = struct{}{}
		}
		if id != node.ID {
			warnings = append(warnings, fmt.Sprintf("node map key %q differs from node id %q", id, node.ID))
		}
	}
	for _, link := range desired.Links {
		if _, ok := knownPads[link.SourcePadID]; !ok {
			warnings = append(warnings, fmt.Sprintf("link %q references unknown source pad %q", link.ID, link.SourcePadID))
		}
		if _, ok := knownPads[link.SinkPadID]; !ok {
			warnings = append(warnings, fmt.Sprintf("link %q references unknown sink pad %q", link.ID, link.SinkPadID))
		}
	}
	sort.Strings(warnings)
	return uniqueWarnings(warnings)
}

func uniqueWarnings(warnings []string) []string {
	seen := make(map[string]struct{}, len(warnings))
	out := warnings[:0]
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == "" {
			continue
		}
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		out = append(out, warning)
	}
	return out
}
