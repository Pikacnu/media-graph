package nodes

import (
	"strings"
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestAudioRegulatorTargetFormatUpdatesLive pins the node that can change its
// output format while running. A capsfilter retargets in place, so the src pad
// reports the new caps without any relink.
func TestAudioRegulatorTargetFormatUpdatesLive(t *testing.T) {
	gst.Init()

	const initial = "audio/x-raw,format=S16LE,rate=48000,channels=2"
	const updated = "audio/x-raw,format=F32LE,rate=44100,channels=1"

	node, err := CreateAudioRegulatorNode(nil, AudioRegulatorConfig{ID: "areg_upd", TargetFormat: initial})
	if err != nil {
		t.Fatalf("CreateAudioRegulatorNode: %v", err)
	}
	defer node.Destroy()

	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}

	if caps := srcPad.QueryCaps(nil); caps == nil || !strings.Contains(caps.String(), "S16LE") {
		t.Fatalf("initial caps = %v, want to contain S16LE", caps)
	}

	if err := node.UpdateProperties(map[string]interface{}{"TargetFormat": updated}); err != nil {
		t.Fatalf("UpdateProperties: %v", err)
	}
	if got := node.Properties()["TargetFormat"]; got != updated {
		t.Errorf("stored TargetFormat = %v, want %q", got, updated)
	}
	if caps := srcPad.QueryCaps(nil); caps == nil || !strings.Contains(caps.String(), "F32LE") {
		t.Fatalf("updated caps = %v, want to contain F32LE", caps)
	}

	// A bad format is rejected without touching the capsfilter.
	if err := node.UpdateProperties(map[string]interface{}{"TargetFormat": 123}); err == nil {
		t.Error("UpdateProperties(123) = nil, want error")
	}
	if err := node.UpdateProperties(map[string]interface{}{"TargetFormat": ""}); err == nil {
		t.Error(`UpdateProperties("") = nil, want error`)
	}
	if caps := srcPad.QueryCaps(nil); caps == nil || !strings.Contains(caps.String(), "F32LE") {
		t.Fatalf("caps after rejected updates = %v, want to still contain F32LE", caps)
	}
}
