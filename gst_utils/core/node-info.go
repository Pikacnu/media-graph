package core

// LinkInfo identifies a connection between two pads.
type LinkInfo struct {
	ID          string `json:"id"`
	SourcePadID string `json:"source_pad_id"`
	SinkPadID   string `json:"sink_pad_id"`
}

// NodeInfo is the declaration of one node in a graph. It describes what to
// build and how it is wired, not how the node is implemented: the node type
// plus its properties are enough for a NodeFactory to construct it.
type NodeInfo struct {
	ID         string         `json:"id"`
	NodeType   string         `json:"node_type"`
	Properties map[string]any `json:"properties"`
	SourcePads []PadInfo      `json:"source_pad_ids"`
	SinkPads   []PadInfo      `json:"sink_pad_ids"`
}

type PadInfo struct {
	ID         string         `json:"id"`
	NodeID     string         `json:"node_id"`
	Direction  PadDirection   `json:"direction"`
	Properties map[string]any `json:"properties"`
}

type PadDirection int

const (
	PadDirectionSrc PadDirection = iota
	PadDirectionSink
)

func (d PadDirection) String() string {
	switch d {
	case PadDirectionSrc:
		return "src"
	case PadDirectionSink:
		return "sink"
	default:
		return ""
	}
}
