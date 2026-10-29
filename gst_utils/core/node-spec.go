package core

import (
	"fmt"
)

// PadCount is how many pads of one direction a node type may expose.
//
//	Unlimited (-1): any number, including zero
//	0             : none at all
//	n > 0         : at most n
//
// It deliberately says nothing about the media a pad carries: that is decided by
// caps negotiation between real elements, not by a declaration, so the graph
// must not pretend to know it ahead of time.
type PadCount int

const UnlimitedPads PadCount = -1

// Allows reports whether a declaration with the given pad count is legal.
func (c PadCount) Allows(count int) bool {
	if c == UnlimitedPads {
		return true
	}
	return count <= int(c)
}

// NodeSpec declares how many pads of each direction a node type accepts. It is
// the type-level counterpart of NodeInfo: NodeInfo says what one instance wants,
// NodeSpec says what the type permits, so the graph can reject an impossible
// declaration before building anything.
type NodeSpec struct {
	Sink PadCount
	Src  PadCount

	// UpdatablePads declares whether a pad's properties carry runtime meaning.
	//
	// Most nodes only record pad properties as metadata, so changing them would
	// be a change with no effect: the registry would report the new value while
	// the node kept behaving as before. A type that can act on the change (a
	// parser re-evaluating which track a pad is bound to) sets this true.
	//
	// The flag is checked in DiffGraph, because a pad update that cannot be
	// honoured must be rejected before it reaches the graph, not silently
	// accepted and then ignored during apply.
	UpdatablePads bool
}

// The arity-shaped specs, so a node type only states its shape once.
var (
	SpecZeroToOne  = NodeSpec{Sink: 0, Src: 1}
	SpecOneToOne   = NodeSpec{Sink: 1, Src: 1}
	SpecOneToZero  = NodeSpec{Sink: 1, Src: 0}
	SpecOneToMany  = NodeSpec{Sink: 1, Src: UnlimitedPads}
	SpecManyToOne  = NodeSpec{Sink: UnlimitedPads, Src: 1}
	SpecManyToZero = NodeSpec{Sink: UnlimitedPads, Src: 0}
)

// The type-level registry (spec + factory per type) lives in node-factory.go as
// NodeTypeEntry. A spec is never stored on its own, so a type cannot be
// described without also being constructible.

// NodeSpecOf reports the pad shape a built node actually honours, derived from
// the arity interfaces. It is the runtime counterpart of NodeSpec: the registry
// says what a type may declare, this says what the node can carry out.
//
// Order matters: the arity interfaces are structural subsets of one another
// (e.g. OneToOneNode also satisfies ZeroToOneNode), so the most capable shape
// must be tested first. ok is false when the node implements no arity interface.
func NodeSpecOf(node Node) (NodeSpec, bool) {
	switch node.(type) {
	case OneToManyNode:
		return SpecOneToMany, true
	case ManyToOneNode:
		return SpecManyToOne, true
	case ManyToZeroNode:
		return SpecManyToZero, true
	case OneToOneNode:
		return SpecOneToOne, true
	case ZeroToOneNode:
		return SpecZeroToOne, true
	case OneToZeroNode:
		return SpecOneToZero, true
	default:
		return NodeSpec{}, false
	}
}

// HasSrcPad / HasSinkPad answer the direction question without the caller
// having to know which arity interface a node implements.
func (s NodeSpec) HasSrcPad() bool  { return s.Src != 0 }
func (s NodeSpec) HasSinkPad() bool { return s.Sink != 0 }

// ValidNode is an optional extension. A node that needs to check state no
// descriptor can express (pads actually negotiated, entries actually bound)
// implements it; everything else is fully described by its NodeSpec.
//
// It is called after construction and before any mutation, so a failure costs
// nothing: the caller can discard the node and keep the old graph.
type ValidNode interface {
	Valid() error
}

// PadUpdater is an optional extension for a node whose pad properties carry
// runtime meaning. A pad update is a PadInfo, not a property map, so it cannot
// travel through UpdateProperties: the node needs the pad identity to know which
// slot to re-evaluate.
//
// A type declares support with NodeSpec.UpdatablePads, which DiffGraph checks
// before the update ever reaches the graph. This interface is the runtime half:
// the graph asserts it and lets the node decide what the change means.
type PadUpdater interface {
	// UpdatePad re-records a pad's properties and, if the new properties change
	// what the pad is bound to, re-evaluates that binding. The pad and its
	// external links survive: this is not a remove-and-recreate.
	UpdatePad(padInfo PadInfo) error
}

// ValidateNode reports why a built node cannot carry a declaration, or nil.
// Direction is checked against the arity interfaces rather than the registered
// spec, so a node whose constructor and declaration disagree is caught here
// instead of failing later inside a pad request.
func ValidateNode(node Node, srcCount, sinkCount int) error {
	spec, ok := NodeSpecOf(node)
	if !ok {
		return fmt.Errorf("%w: node %q implements no arity interface", ErrPadCantBeUsed, node.ID())
	}
	if srcCount > 0 && !spec.HasSrcPad() {
		return fmt.Errorf("%w: node %q has no source pad", ErrGSTPadNotFound, node.ID())
	}
	if sinkCount > 0 && !spec.HasSinkPad() {
		return fmt.Errorf("%w: node %q has no sink pad", ErrGSTPadNotFound, node.ID())
	}
	if spec.Src != UnlimitedPads && srcCount > int(spec.Src) {
		return fmt.Errorf("%w: node %q accepts at most %d source pads, got %d",
			ErrPadCantBeUsed, node.ID(), spec.Src, srcCount)
	}
	if spec.Sink != UnlimitedPads && sinkCount > int(spec.Sink) {
		return fmt.Errorf("%w: node %q accepts at most %d sink pads, got %d",
			ErrPadCantBeUsed, node.ID(), spec.Sink, sinkCount)
	}
	if v, ok := node.(ValidNode); ok {
		if err := v.Valid(); err != nil {
			return fmt.Errorf("node %q is not valid: %w", node.ID(), err)
		}
	}
	return nil
}
