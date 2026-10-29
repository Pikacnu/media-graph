package core

import (
	"github.com/go-gst/go-gst/pkg/gst"
)

type nodeRuntime struct {
	Registry    ChanRegistry
	GstRegistry *GstObjectRegistry
	ID          string
}

type NodeRuntime interface {
	RegisterEntry(entry RegistryEntry) error
	RemoveEntryById(id string) error
	GetEntryById(id string) (RegistryEntry, bool)
	RegisterGstObj(obj GstObject) error
	GetGstObj(objType GstObjectType, id string) (GstObject, error)
	UnregisterGstObj(objType GstObjectType, id string) error
	GetPadInfoByTrackName(trackName string) (gst.Pad, error)
}

func CreateNodeRuntime(id string, gstObjectRegistry *GstObjectRegistry, chanRegistry ChanRegistry) (NodeRuntime, error) {
	return &nodeRuntime{
		Registry:    chanRegistry,
		GstRegistry: gstObjectRegistry,
		ID:          id,
	}, nil
}

func (g *nodeRuntime) RegisterEntry(entry RegistryEntry) error {
	return g.Registry.RegisterEntry(entry)
}

func (g *nodeRuntime) RemoveEntryById(id string) error {
	return g.Registry.RemoveEntryById(id)
}

func (g *nodeRuntime) GetEntryById(id string) (RegistryEntry, bool) {
	return g.Registry.GetEntryById(id)
}

func (g *nodeRuntime) RegisterGstObj(obj GstObject) error {
	return g.GstRegistry.Register(obj)
}

func (g *nodeRuntime) GetGstObj(objType GstObjectType, id string) (GstObject, error) {
	return g.GstRegistry.Get(objType, id)
}

func (g *nodeRuntime) UnregisterGstObj(objType GstObjectType, id string) error {
	return g.GstRegistry.Unregister(objType, id)
}

func (g *nodeRuntime) GetPadInfoByTrackName(trackName string) (gst.Pad, error) {
	return g.GstRegistry.GetPadByProperty(map[string]any{
		"track_name": trackName,
	})
}

// func (g *nodeRuntime) GetPadInfoByTrackSelector(selector TrackSelector) (gst.Pad, error) {
// 	searchProperties := make(map[string]any)
// 	if selector.TrackName != "" {
// 		searchProperties["TrackName"] = selector.TrackName
// 	}
// 	if selector.TrackID != "" {
// 		searchProperties["TrackID"] = selector.TrackID
// 	}
// 	if selector.MediaType != "" {
// 		searchProperties["MediaType"] = selector.MediaType
// 	}
// 	if len(slices.Collect(maps.Keys(searchProperties))) == 0 {
// 		return nil, ErrInvalidConfig
// 	}
// 	return g.GstRegistry.GetPadByProperty(searchProperties)
// }
