package nodes

import (
	"strings"
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestVideoResizerCapsUpdatesLive is the video twin of the AudioRegulator test:
// a capsfilter retargets in place, so the src pad reports the new size without
// any relink.
func TestVideoResizerCapsUpdatesLive(t *testing.T) {
	gst.Init()

	const initial = "video/x-raw,width=320,height=240"
	const updated = "video/x-raw,width=640,height=480"

	node, err := CreateVideoResizerNode(nil, VideoResizerConfig{ID: "vres_upd", Caps: initial})
	if err != nil {
		t.Fatalf("CreateVideoResizerNode: %v", err)
	}
	defer node.Destroy()

	srcPad, _, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}

	if caps := srcPad.QueryCaps(nil); caps == nil || !strings.Contains(caps.String(), "width=(int)320") {
		t.Fatalf("initial caps = %v, want to contain width=320", caps)
	}

	if err := node.UpdateProperties(map[string]interface{}{"Caps": updated}); err != nil {
		t.Fatalf("UpdateProperties: %v", err)
	}
	if got := node.Properties()["Caps"]; got != updated {
		t.Errorf("stored Caps = %v, want %q", got, updated)
	}
	if caps := srcPad.QueryCaps(nil); caps == nil || !strings.Contains(caps.String(), "width=(int)640") {
		t.Fatalf("updated caps = %v, want to contain width=640", caps)
	}

	if err := node.UpdateProperties(map[string]interface{}{"Caps": 123}); err == nil {
		t.Error("UpdateProperties(123) = nil, want error")
	}
	if err := node.UpdateProperties(map[string]interface{}{"Caps": ""}); err == nil {
		t.Error(`UpdateProperties("") = nil, want error`)
	}
	if caps := srcPad.QueryCaps(nil); caps == nil || !strings.Contains(caps.String(), "width=(int)640") {
		t.Fatalf("caps after rejected updates = %v, want to still contain width=640", caps)
	}
}
