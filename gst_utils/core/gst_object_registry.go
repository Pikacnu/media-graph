package core

import (
	"reflect"
	"sync"

	"github.com/go-gst/go-gst/pkg/gst"
)

type GstObjectRegistry struct {
	objects map[GstObjectType]map[string]GstObject
	mu      sync.RWMutex
}

func CreateGstObjectRegistry() *GstObjectRegistry {
	return &GstObjectRegistry{
		objects: make(map[GstObjectType]map[string]GstObject),
	}
}

// Register records a GstObject under its ID.
//
// Registering an object that is already present is a no-op when it is the same
// object, and an error when it is a different object under the same ID. The
// no-op case is a normal flow, not a mistake: a node registers a pad it
// discovers, and the same pad is registered again when a client echoes a graph
// that already contains it. Treating that as an error would make a legitimate
// round trip fail. A different object under a taken ID would make the registry
// ambiguous, so that stays an error.
func (r *GstObjectRegistry) Register(obj GstObject) error {
	if obj == nil {
		return ErrInvalidConfig
	}
	if obj.Type() == GstObjectTypeUnknown {
		return ErrInvalidConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.objects[obj.Type()]; !exists {
		r.objects[obj.Type()] = make(map[string]GstObject)
	}
	if existing, exists := r.objects[obj.Type()][obj.ID()]; exists {
		if sameGstObject(existing.GetObject(), obj.GetObject()) {
			return nil
		}
		return ErrInvalidConfig
	}
	r.objects[obj.Type()][obj.ID()] = obj
	return nil
}

// sameGstObject reports whether two GstObject payloads are the same underlying
// object. The values are interfaces holding pointers (a gst.Pad, a gst.Bin), so
// pointer identity is the right comparison; comparing interfaces directly would
// panic on a non-comparable dynamic type, which is why this checks the kind
// first.
func sameGstObject(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.Kind() != reflect.Ptr || bv.Kind() != reflect.Ptr {
		return false
	}
	return av.Pointer() == bv.Pointer()
}

func (r *GstObjectRegistry) Get(objType GstObjectType, id string) (GstObject, error) {
	defer r.mu.RUnlock()
	r.mu.RLock()
	if objMap, exists := r.objects[objType]; exists {
		if obj, exists := objMap[id]; exists {
			return obj, nil
		}
	}
	return nil, ErrNotFound
}

func (r *GstObjectRegistry) GetSafe(objType GstObjectType, id string) (GstObject, bool) {
	defer r.mu.RUnlock()
	r.mu.RLock()
	if objMap, exists := r.objects[objType]; exists {
		if obj, exists := objMap[id]; exists {
			return obj, true
		}
	}
	return nil, false
}

func (r *GstObjectRegistry) GetPadSafe(id string) (gst.Pad, bool) {
	obj, exists := r.GetSafe(GstObjectTypePad, id)
	if !exists {
		return nil, false
	}
	pad, ok := obj.GetObject().(gst.Pad)
	return pad, ok
}

func (r *GstObjectRegistry) GetPadByProperty(properties map[string]any) (gst.Pad, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(properties) == 0 {
		return nil, ErrInvalidConfig
	}
	for _, gstObj := range r.objects[GstObjectTypePad] {
		isTarget := true
		for k, v := range properties {
			propValue, err := gstObj.GetProperty(k)
			if err != nil || !reflect.DeepEqual(propValue, v) {
				isTarget = false
				break
			}
		}
		if isTarget {
			pad, ok := gstObj.GetObject().(gst.Pad)
			if !ok {
				return nil, ErrTypeMismatch
			}
			return pad, nil
		}
	}
	return nil, ErrNotFound
}

func (r *GstObjectRegistry) Unregister(objType GstObjectType, id string) error {
	defer r.mu.Unlock()
	r.mu.Lock()
	if objMap, exists := r.objects[objType]; exists {
		if _, exists := objMap[id]; exists {
			delete(objMap, id)
			return nil
		}
	}
	return ErrNotFound
}

func (r *GstObjectRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.objects = make(map[GstObjectType]map[string]GstObject)
}
