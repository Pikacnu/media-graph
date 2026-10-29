package main

import (
	"fmt"
	"sort"
	"strings"

	"live_tools/gst_utils/core"
	_ "live_tools/gst_utils/nodes"
)

func InspectNodeTypes() []string {
	types := core.AllowedNodeTypes()
	sort.Strings(types)
	return types
}

func InspectNode(nodeType string) (string, error) {
	entry, ok := core.LookupNodeType(nodeType)
	if !ok {
		return "", fmt.Errorf("unknown node type %q", nodeType)
	}
	return fmt.Sprintf("Node: \t %s\nFactory symbol: \t %s\nSink pads: \t %s\nSource pads: \t %s\nUpdatable pads: \t %t\n\n\n", nodeType, factoryName(entry.Factory), formatPadCount(entry.Spec.Sink), formatPadCount(entry.Spec.Src), entry.Spec.UpdatablePads), nil
}

func formatPadCount(count core.PadCount) string {
	if count == core.UnlimitedPads {
		return "unlimited"
	}
	return fmt.Sprintf("%d", count)
}

func InspectSummary() string {
	var b strings.Builder
	for _, nodeType := range InspectNodeTypes() {
		detail, err := InspectNode(nodeType)
		if err == nil {
			b.WriteString(detail)
		}
	}
	return b.String()
}
