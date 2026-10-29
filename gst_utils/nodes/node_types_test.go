package nodes

import (
	"testing"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// nodeTypeBuilders maps each registered type to a way to build a minimal
// instance. It is deliberately built from the NodeType* constants, so a
// constant that stops matching the registry is caught below.
var nodeTypeBuilders = map[string]func(t *testing.T) core.Node{
	NodeTypeSourceInput: func(t *testing.T) core.Node {
		n, err := CreateSourceInput(newTestRuntime(t, "nt_input"), SourceInputConfig{ID: "nt_input", StreamType: core.StreamTypeStream})
		if err != nil {
			t.Fatalf("CreateSourceInput: %v", err)
		}
		return n
	},
	NodeTypeClone: func(t *testing.T) core.Node {
		n, err := CreateCloneNode(nil, CloneConfig{ID: "nt_clone"})
		if err != nil {
			t.Fatalf("CreateCloneNode: %v", err)
		}
		return n
	},
	NodeTypeAudioCombiner: func(t *testing.T) core.Node {
		n, err := CreateAudioCombinerNode(nil, AudioCombinerConfig{ID: "nt_mix"})
		if err != nil {
			t.Fatalf("CreateAudioCombinerNode: %v", err)
		}
		return n
	},
	NodeTypeAudioRegulator: func(t *testing.T) core.Node {
		n, err := CreateAudioRegulatorNode(nil, AudioRegulatorConfig{ID: "nt_reg", TargetFormat: "audio/x-raw"})
		if err != nil {
			t.Fatalf("CreateAudioRegulatorNode: %v", err)
		}
		return n
	},
	NodeTypeAudioTrack: func(t *testing.T) core.Node {
		n, err := CreateAudioTrackNode(nil, AudioTrackConfig{ID: "nt_track"})
		if err != nil {
			t.Fatalf("CreateAudioTrackNode: %v", err)
		}
		return n
	},
	NodeTypeVideoResizer: func(t *testing.T) core.Node {
		n, err := CreateVideoResizerNode(nil, VideoResizerConfig{ID: "nt_rsz", Caps: "video/x-raw"})
		if err != nil {
			t.Fatalf("CreateVideoResizerNode: %v", err)
		}
		return n
	},
	NodeTypeVideoTranscode: func(t *testing.T) core.Node {
		n, err := CreateVideoTranscodeNode(nil, VideoTranscodeConfig{ID: "nt_tc", Caps: "video/x-raw"})
		if err != nil {
			t.Fatalf("CreateVideoTranscodeNode: %v", err)
		}
		return n
	},
	NodeTypeAudioEncode: func(t *testing.T) core.Node {
		n, err := CreateAudioEncodeNode(nil, AudioEncodeConfig{ID: "nt_aenc", Caps: "audio/x-raw"})
		if err != nil {
			t.Fatalf("CreateAudioEncodeNode: %v", err)
		}
		return n
	},
	NodeTypeFlvMux: func(t *testing.T) core.Node {
		n, err := CreateFlvMuxNode(nil, FlvMuxConfig{ID: "nt_mux"})
		if err != nil {
			t.Fatalf("CreateFlvMuxNode: %v", err)
		}
		return n
	},
	NodeTypeFileOutput: func(t *testing.T) core.Node {
		n, err := CreateFileOutputNode(nil, FileOutputConfig{ID: "nt_fout", FilePath: t.TempDir() + "/o.bin"})
		if err != nil {
			t.Fatalf("CreateFileOutputNode: %v", err)
		}
		return n
	},
	NodeTypeFileSink: func(t *testing.T) core.Node {
		n, err := CreateFileSinkNode(nil, FileSinkConfig{ID: "nt_fsink", Location: t.TempDir() + "/o.flv"})
		if err != nil {
			t.Fatalf("CreateFileSinkNode: %v", err)
		}
		return n
	},
	NodeTypeDiscard: func(t *testing.T) core.Node {
		n, err := CreateDiscardNode(nil, DiscardConfig{ID: "nt_disc"})
		if err != nil {
			t.Fatalf("CreateDiscardNode: %v", err)
		}
		return n
	},
	NodeTypeRTMPOutput: func(t *testing.T) core.Node {
		n, err := CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{ID: "nt_rtmp", Host: "h", Location: "/live", Application: "a", Scheme: "rtmp"})
		if err != nil {
			t.Fatalf("CreateRTMPOutputNode: %v", err)
		}
		return n
	},
	NodeTypeOutput: func(t *testing.T) core.Node {
		n, err := CreateOutputNode(newTestRuntime(t, "nt_out"), OutputConfig{ID: "nt_out", Caps: "video/x-raw"})
		if err != nil {
			t.Fatalf("CreateOutputNode: %v", err)
		}
		return n
	},
	NodeTypePreview: func(t *testing.T) core.Node {
		n, err := CreatePreviewNode(newTestRuntime(t, "nt_prev"), PreviewConfig{ID: "nt_prev"})
		if err != nil {
			t.Fatalf("CreatePreviewNode: %v", err)
		}
		return n
	},
	NodeTypeCompositor: func(t *testing.T) core.Node {
		n, err := CreateCompositorNode(nil, CompositorConfig{ID: "nt_comp", Width: 640, Height: 360})
		if err != nil {
			t.Fatalf("CreateCompositorNode: %v", err)
		}
		return n
	},
	NodeTypeTransition: func(t *testing.T) core.Node {
		n, err := CreateTransitionNode(nil, TransitionConfig{ID: "nt_trans"})
		if err != nil {
			t.Fatalf("CreateTransitionNode: %v", err)
		}
		return n
	},
	NodeTypeVolume: func(t *testing.T) core.Node {
		n, err := CreateVolumeNode(nil, VolumeConfig{ID: "nt_vol"})
		if err != nil {
			t.Fatalf("CreateVolumeNode: %v", err)
		}
		return n
	},
	NodeTypeTestSource: func(t *testing.T) core.Node {
		n, err := CreateTestSourceNode(nil, TestSourceConfig{ID: "nt_test"})
		if err != nil {
			t.Fatalf("CreateTestSourceNode: %v", err)
		}
		return n
	},
	NodeTypeImageSource: func(t *testing.T) core.Node {
		n, err := CreateImageSourceNode(nil, ImageSourceConfig{ID: "nt_img", Location: t.TempDir() + "/logo.png"})
		if err != nil {
			t.Fatalf("CreateImageSourceNode: %v", err)
		}
		return n
	},
	NodeTypeHLSOutput: func(t *testing.T) core.Node {
		dir := t.TempDir()
		n, err := CreateHLSOutputNode(nil, HLSOutputConfig{
			ID:               "nt_hls",
			Location:         dir + "/seg%05d.ts",
			PlaylistLocation: dir + "/index.m3u8",
		})
		if err != nil {
			t.Fatalf("CreateHLSOutputNode: %v", err)
		}
		return n
	},
	NodeTypeOutputControl: func(t *testing.T) core.Node {
		n, err := CreateOutputControlNode(nil, OutputControlConfig{ID: "nt_gate"})
		if err != nil {
			t.Fatalf("CreateOutputControlNode: %v", err)
		}
		return n
	},
	NodeTypeThumbnail: func(t *testing.T) core.Node {
		n, err := CreateThumbnailNode(newTestRuntime(t, "nt_thumb"), ThumbnailConfig{ID: "nt_thumb"})
		if err != nil {
			t.Fatalf("CreateThumbnailNode: %v", err)
		}
		return n
	},
}

// A node must report the type it was built as, because a runtime snapshot uses
// NodeType() to describe a node without consulting the applied graph.
func TestNodeReportsItsNodeType(t *testing.T) {
	gst.Init()

	for wantType, build := range nodeTypeBuilders {
		t.Run(wantType, func(t *testing.T) {
			node := build(t)
			defer func() { _ = node.Destroy() }()

			if got := node.NodeType(); got != wantType {
				t.Errorf("NodeType() = %q, want %q", got, wantType)
			}
		})
	}
}

// The constant a node is built with must equal the string it is registered
// under, or a snapshot would report a type the graph cannot look up. This is the
// drift guard between node-types.go and registry.go.
func TestNodeTypeConstantsMatchRegistry(t *testing.T) {
	gst.Init()

	for wantType, build := range nodeTypeBuilders {
		t.Run(wantType, func(t *testing.T) {
			if !core.IsAllowedNodeType(wantType) {
				t.Fatalf("%q is not a registered node type", wantType)
			}
			if _, ok := core.LookupNodeSpec(wantType); !ok {
				t.Fatalf("%q has no registered spec", wantType)
			}

			node := build(t)
			defer func() { _ = node.Destroy() }()

			// The built node must be creatable via the factory under the SAME
			// string, which is what ties the constant to the registry entry.
			if node.NodeType() != wantType {
				t.Errorf("built node reports %q, constant says %q", node.NodeType(), wantType)
			}
		})
	}
}
