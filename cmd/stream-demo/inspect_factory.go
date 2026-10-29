package main

import (
	"reflect"
	"runtime"

	"live_tools/gst_utils/core"
)

func factoryName(factory core.NodeFactory) string {
	if factory == nil {
		return "<nil>"
	}
	value := reflect.ValueOf(factory)
	if value.Kind() != reflect.Func {
		return value.Type().String()
	}
	if function := runtime.FuncForPC(value.Pointer()); function != nil {
		return function.Name()
	}
	return value.Type().String()
}
