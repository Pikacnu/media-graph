package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type NodeFactory func(runtime NodeRuntime, info NodeInfo) (Node, error)

// NodeTypeEntry is everything the core knows about one node type: the pad shape
// it may declare and the constructor that builds it.
//
// The two live together on purpose. A node type is only meaningful with both:
// validateGraph needs the spec before any instance exists (it runs on a graph
// that was never built), and newGraphNode needs the factory to build one. The
// nodes package registers them in a single call, so a type can never end up
// half-registered.
type NodeTypeEntry struct {
	Spec    NodeSpec
	Factory NodeFactory
}

var (
	nodeTypesMu sync.RWMutex
	nodeTypes   = map[string]NodeTypeEntry{}
)

// RegisterNodeType is the single registration point for a node type. It is
// called once per type from that type's own file, so the catalog is populated by
// importing the nodes package and never by an explicit registration pass.
//
// A duplicate is an error rather than an overwrite: two files claiming the same
// type name would make the catalog depend on init order.
func RegisterNodeType(nodeType string, spec NodeSpec, factory NodeFactory) error {
	if strings.TrimSpace(nodeType) == "" || factory == nil {
		return fmt.Errorf("invalid node type or factory")
	}
	nodeTypesMu.Lock()
	defer nodeTypesMu.Unlock()
	if _, exists := nodeTypes[nodeType]; exists {
		return fmt.Errorf("node type %s already registered", nodeType)
	}
	nodeTypes[nodeType] = NodeTypeEntry{Spec: spec, Factory: factory}
	return nil
}

func LookupNodeType(nodeType string) (NodeTypeEntry, bool) {
	nodeTypesMu.RLock()
	defer nodeTypesMu.RUnlock()
	entry, exists := nodeTypes[nodeType]
	return entry, exists
}

// LookupNodeFactory reports just the constructor, for callers that only build.
func LookupNodeFactory(nodeType string) (NodeFactory, bool) {
	entry, exists := LookupNodeType(nodeType)
	return entry.Factory, exists
}

// LookupNodeSpec reports just the pad shape, for callers that only validate.
func LookupNodeSpec(nodeType string) (NodeSpec, bool) {
	entry, exists := LookupNodeType(nodeType)
	return entry.Spec, exists
}

// AllowedNodeTypes is derived from the catalog rather than kept beside it, so
// there is no second list that can drift out of sync. Order is unspecified.
func AllowedNodeTypes() []string {
	nodeTypesMu.RLock()
	defer nodeTypesMu.RUnlock()
	out := make([]string, 0, len(nodeTypes))
	for nodeType := range nodeTypes {
		out = append(out, nodeType)
	}
	return out
}

func IsAllowedNodeType(nodeType string) bool {
	_, exists := LookupNodeType(nodeType)
	return exists
}

func AdaptFactory[C any, N Node](creator func(NodeRuntime, C) (N, error)) NodeFactory {
	return func(runtime NodeRuntime, info NodeInfo) (Node, error) {
		config, err := DecodeNodeConfig[C](info)
		if err != nil {
			return nil, err
		}
		return creator(runtime, config)
	}
}

func DecodeNodeConfig[C any](info NodeInfo) (C, error) {
	var config C

	payload := make(map[string]any, len(info.Properties)+1)
	for key, value := range info.Properties {
		payload[key] = value
	}
	payload["id"] = info.ID

	data, err := json.Marshal(payload)
	if err != nil {
		return config, fmt.Errorf("marshal properties for node %q: %w", info.ID, err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("decode properties for node %q into %T: %w", info.ID, config, err)
	}
	return config, nil
}

func PropertyString(properties map[string]any, key, fallback string) string {
	if value, ok := properties[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func PropertyBool(properties map[string]any, key string) bool {
	value, _ := properties[key].(bool)
	return value
}

func PropertyInt(properties map[string]any, key string, fallback int) int {
	switch value := properties[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	}
	return fallback
}
