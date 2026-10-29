package core

import (
	"sync"

	"github.com/go-gst/go-gst/pkg/gst"
)

const idPropertyKey = "ID"

type BaseNode struct {
	id         string
	nodeType   string
	bin        gst.Bin
	mu         sync.Mutex
	properties map[string]any
	protected  map[string]struct{}
}

// NewBaseNode builds the shared part of a node: its identity, its registered
// type name, its bin and an empty property store. A node embeds it as
// *BaseNode (never by value) because it carries a mutex.
//
// nodeType must be the same string the node is registered under. Each node file
// holds both its type constant and its registration, so the two cannot drift.
func NewBaseNode(id, nodeType string, bin gst.Bin) *BaseNode {
	return &BaseNode{
		id:         id,
		nodeType:   nodeType,
		bin:        bin,
		properties: make(map[string]any),
		protected:  make(map[string]struct{}),
	}
}

// SetBin swaps the backing bin, e.g. after a node rebuilds its internal chain.
func (b *BaseNode) SetBin(bin gst.Bin) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bin = bin
}

func (b *BaseNode) ID() string { return b.id }

// NodeType is the registered type name this node was built as.
func (b *BaseNode) NodeType() string { return b.nodeType }

func (b *BaseNode) Bin() gst.Bin {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bin
}

// SinkPadID / SrcPadID are the canonical pad IDs for a node's single fixed pad.
// Dynamic pads get their IDs from the node itself, not from here.
func (b *BaseNode) SinkPadID() string { return b.id + "_sink_0" }
func (b *BaseNode) SrcPadID() string  { return b.id + "_src_0" }

// ProtectProperties marks keys as immutable. They stay in the store so the node
// keeps the config it was built with, but a later UpdateProperties drops them
// instead of failing. That lets the graph send a node's whole property map
// (which is what NodesToUpdate does) without tripping over constructor-fixed
// config, while still refusing to let a caller change it.
func (b *BaseNode) ProtectProperties(keys ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, key := range keys {
		b.protected[key] = struct{}{}
	}
}

// UpdateProperties merges properties into the node's store.
//
// A nil value deletes the key. A protected key is dropped silently, and a key
// whose value equals the node's own ID is a no-op (it is the same node). Only an
// attempt to change the ID to a different value is an error: a node cannot
// become another node, and deleting its identity would orphan its pads.
func (b *BaseNode) UpdateProperties(properties map[string]interface{}) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for key, value := range properties {
		if key == idPropertyKey {
			if value != b.id {
				return ErrInvalidConfig
			}
			continue
		}
		if _, immutable := b.protected[key]; immutable {
			continue
		}
		if value == nil {
			delete(b.properties, key)
			continue
		}
		b.properties[key] = value
	}
	return nil
}

// Properties returns a copy of every property, protected ones included. Use it
// for a node's own bookkeeping (e.g. restoring a value after a failed update).
// To publish a node's state, use EditableProperties instead.
func (b *BaseNode) Properties() map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]interface{}, len(b.properties))
	for key, value := range b.properties {
		out[key] = value
	}
	return out
}

// EditableProperties returns a copy without the protected keys.
//
// This is the view to publish. Protected keys were fixed by the constructor and
// UpdateProperties drops them, so a caller can never change them. Reporting them
// would make an echo of this node disagree with what the caller sent (the caller
// never sent "Caps"), and DiffGraph compares properties by value, so every round
// trip would look like an update. Omitting them keeps a snapshot a fixed point:
// DiffGraph(snapshot, snapshot) has nothing to report.
func (b *BaseNode) EditableProperties() map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]interface{}, len(b.properties)-len(b.protected))
	for key, value := range b.properties {
		if _, isProtected := b.protected[key]; !isProtected {
			out[key] = value
		}
	}
	return out
}

// Destroyed lets Destroy stay idempotent.
func (b *BaseNode) Destroyed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bin == nil
}

// Clear drops the base's references. The node clears its own pads. It does not
// reset the protected set: protection is a property of the type, not of the
// instance, so a cleared node would still refuse the same keys.
func (b *BaseNode) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bin = nil
	b.properties = make(map[string]interface{})
}
