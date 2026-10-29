// Package nodes builds one gst.Bin per node and exposes it through ghost pads.
//
// Pad construction order is fixed: bin.Add(elements) -> link the internal chain
// -> create the ghost pads. gst_ghost_pad_new links the ghost pad to its target,
// so a ghost pad created first would make the chain endpoint look already
// linked and the following Link call would fail. Nodes reference this rule as
// "add, link, then ghost".
package nodes

import (
	"fmt"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// registerNode is the single place a node type is declared. Every node file
// calls it from init, so adding a node type means adding one file: its
// constructor, its type constant, its pad spec and its registration all live
// together, and no shared table has to be edited.
//
// A duplicate registration is a programming error rather than a runtime
// condition, so it panics: init cannot report an error, and a clash would mean
// two files claiming the same type name.
func registerNode(nodeType string, spec core.NodeSpec, factory core.NodeFactory) {
	if err := core.RegisterNodeType(nodeType, spec, factory); err != nil {
		panic(fmt.Sprintf("register node type %q: %v", nodeType, err))
	}
}

// padOrErr returns the pad with its canonical ID. A nil pad means the node has
// no pad in that direction.
func padOrErr(pad gst.Pad, padID string) (gst.Pad, string, error) {
	if pad == nil {
		return nil, "", core.ErrGSTPadNotFound
	}
	return pad, padID, nil
}

// propertyNumber coerces a numeric property to float64. A value that came from a
// JSON graph is float64 while one a constructor stored is int, and both reach
// the same lookup, so the store must be read through one shape.
func propertyNumber(raw any, fallback float64) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int32:
		return float64(value)
	case int64:
		return float64(value)
	case uint32:
		return float64(value)
	case uint64:
		return float64(value)
	}
	return fallback
}
