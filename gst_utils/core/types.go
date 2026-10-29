package core

import (
	"errors"
	"sync"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

type StreamType int

const (
	StreamTypeStream StreamType = iota
	StreamTypeSeekable
	StreamTypeRandomAccess
)

type GstObjectType int

const (
	GstObjectTypeUnknown GstObjectType = -1
	GstObjectTypeBin     GstObjectType = iota
	GstObjectTypePad
)

// GetGstObjTypeByType maps a payload to the bucket it belongs in.
//
// The discriminator exists because the registry is not pad-only: a bin can be
// registered too, even though nothing does that yet. Keeping the bucket means a
// future graph that wants to resolve a bin by ID does not need a second
// registry.
func GetGstObjTypeByType(obj any) GstObjectType {
	switch any(obj).(type) {
	case gst.Pad:
		return GstObjectTypePad
	case gst.Bin:
		return GstObjectTypeBin
	}
	return GstObjectTypeUnknown
}

// GstObject is something the graph knows about by ID, plus the properties
// recorded when it was created.
//
// The payload is `any` rather than gst.Pad so a bin can be stored in the same
// registry; Type() says which it is.
type GstObject interface {
	ID() string
	Type() GstObjectType
	GetObject() any
	GetProperty(propertyName string) (any, error)
	SetProperty(propertyName string, value any) error
	// SetProperties replaces the whole property set. Replacing rather than
	// merging is what makes a removed key actually disappear; a per-key Set
	// would leave keys the caller dropped behind, so a pad that stopped
	// declaring a selector field would keep matching on it forever.
	SetProperties(properties map[string]any) error
	// GetProperties returns a copy. Returning the internal map would let a
	// caller mutate the registry's state without holding its lock.
	GetProperties() map[string]any
}

// gstObjectImpl owns its property map, so it needs its own lock: a node writes
// properties from the apply path while Snapshot reads them from another
// goroutine. The registry's lock does not cover this, because it is released
// once the entry is handed out.
type gstObjectImpl struct {
	mu         sync.RWMutex
	id         string
	obj        any
	properties map[string]any
}

func (g *gstObjectImpl) ID() string {
	return g.id
}

func (g *gstObjectImpl) Type() GstObjectType {
	return GetGstObjTypeByType(g.obj)
}

func (g *gstObjectImpl) GetObject() any {
	return g.obj
}

func (g *gstObjectImpl) GetProperty(propertyName string) (any, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if value, exists := g.properties[propertyName]; exists {
		return value, nil
	}
	return nil, ErrNotFound
}

func (g *gstObjectImpl) GetProperties() map[string]any {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return copyProperties(g.properties)
}

func (g *gstObjectImpl) SetProperty(propertyName string, value any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if value == nil {
		delete(g.properties, propertyName)
	} else {
		g.properties[propertyName] = value
	}
	return nil
}

func (g *gstObjectImpl) SetProperties(properties map[string]any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.properties = copyProperties(properties)
	return nil
}

// copyProperties copies a property map. The values are scalars in practice, so
// one level is the whole depth.
func copyProperties(properties map[string]any) map[string]any {
	if properties == nil {
		return make(map[string]any)
	}
	copied := make(map[string]any, len(properties))
	for key, value := range properties {
		copied[key] = value
	}
	return copied
}

func CreateGstObject(id string, obj any) GstObject {
	return &gstObjectImpl{
		id:         id,
		obj:        obj,
		properties: make(map[string]any),
	}
}

// CreateGstObjectWithProperties copies the given properties, so the object does
// not alias the caller's map. A shared map would let the caller rewrite the
// registry's view of an object without going through SetProperties.
func CreateGstObjectWithProperties(id string, obj any, properties map[string]any) GstObject {
	return &gstObjectImpl{
		id:         id,
		obj:        obj,
		properties: copyProperties(properties),
	}
}

// TrackEventType describes why a parser node published TrackData.
type TrackEventType string

const (
	TrackEventTypeDiscovered TrackEventType = "discovered"
	TrackEventTypeError      TrackEventType = "error"
)

// TrackData is a one-way discovery/error notification, not a track-state cache.
// The node retains runtime Gst objects; they are not sent across channels.
type TrackData struct {
	ID        string
	Name      string
	MediaType string
	Caps      *gst.Caps
	Type      TrackEventType
	Error     error
}

type TrackEvent struct {
	NodeID    string         `json:"nodeId"`
	TrackID   string         `json:"trackId"`
	Type      TrackEventType `json:"type"`
	Name      string         `json:"name,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Caps      string         `json:"caps,omitempty"`
	Err       string         `json:"err,omitempty"`
}

// RuntimeEvent is the graph-to-controller boundary for runtime observations.
// It contains only serializable values; GStreamer handles and error interfaces
// stay inside the graph package.
type RuntimeEvent struct {
	Type      string      `json:"type"`
	Source    string      `json:"source,omitempty"`
	Parent    string      `json:"parent,omitempty"`
	NodeID    string      `json:"nodeId,omitempty"`
	Track     *TrackEvent `json:"track,omitempty"`
	Error     string      `json:"error,omitempty"`
	Debug     string      `json:"debug,omitempty"`
	OldState  string      `json:"oldState,omitempty"`
	NewState  string      `json:"newState,omitempty"`
	Pending   string      `json:"pending,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

const (
	RuntimeEventTrackDiscovered = "track-discovered"
	RuntimeEventTrackError      = "track-error"
	RuntimeEventBusError        = "bus-error"
	RuntimeEventBusWarning      = "bus-warning"
	RuntimeEventEOS             = "eos"
	RuntimeEventStateChanged    = "state-changed"
)

// NewTrackEvent flattens node-local values at the MediaGraph boundary.
func NewTrackEvent(nodeID string, data TrackData) TrackEvent {
	event := TrackEvent{
		NodeID:    nodeID,
		TrackID:   data.ID,
		Type:      data.Type,
		Name:      data.Name,
		MediaType: data.MediaType,
	}
	if data.Caps != nil {
		event.Caps = data.Caps.String()
	}
	if data.Error != nil {
		event.Err = data.Error.Error()
	}
	return event
}

var (
	ErrGSTNoElements        = errors.New("no elements in node")
	ErrGSTLinkErr           = errors.New("failed to link GStreamer elements")
	ErrGSTPadNotFound       = errors.New("pad not found in element")
	ErrGSTInvalidConfig     = errors.New("invalid GStreamer node configuration")
	ErrGSTNodeDestroyed     = errors.New("GStreamer node is destroyed")
	ErrGSTSyncStateErr      = errors.New("failed to sync state with parent")
	ErrUnLinkFailed         = errors.New("failed to unlink pads")
	ErrInvalidConfig        = errors.New("invalid configuration")
	ErrPadCantBeUsed        = errors.New("pad can't be used")
	ErrGSTStateChangeFailed = errors.New("failed to change GStreamer state")
	ErrEntryStopped         = errors.New("registry entry is stopped")
	ErrChanFull             = errors.New("input is full")
	ErrInputDropped         = errors.New("input dropped: buffer full")
	ErrNotFound             = errors.New("not found")
	ErrGSTEventSendErr      = errors.New("failed to send GStreamer event")
	ErrTypeMismatch         = errors.New("type mismatch")
	ErrFailed               = errors.New("operation failed")
)

// type TrackSelector struct {
// 	TrackID   string
// 	TrackName string
// 	MediaType string // known as caps
// }

// type ContainerParsePadProperties struct {
// 	QueueSinkIndex uint
// 	Selector       TrackSelector
// }
