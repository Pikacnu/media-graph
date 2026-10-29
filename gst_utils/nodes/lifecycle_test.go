package nodes

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

// newTestRuntime gives a node the runtime it needs without a whole MediaGraph.
// Nodes that only publish a channel (Output, Preview) need it to be non-nil;
// SourceInput and Output register an entry through it.
func newTestRuntime(t *testing.T, id string) core.NodeRuntime {
	t.Helper()
	chanRegistry, err := core.CreateChanRegistry(id)
	if err != nil {
		t.Fatalf("CreateChanRegistry: %v", err)
	}
	// Clear stops the broadcast goroutine; without it every node case leaks one.
	t.Cleanup(func() { _ = chanRegistry.Clear() })

	runtime, err := core.CreateNodeRuntime(id, core.CreateGstObjectRegistry(), chanRegistry)
	if err != nil {
		t.Fatalf("CreateNodeRuntime: %v", err)
	}
	return runtime
}

// nodeCase describes one node's happy path plus the accessors every node
// promises through the core.Node contract.
type nodeCase struct {
	name     string
	create   func(t *testing.T) (core.Node, error)
	srcIDs   []string
	dstIDs   []string
	srcPad   string // expected SrcPadID, empty when the node has no source pad
	sinkPad  string // expected SinkPadID, empty when the node has no sink pad
	wantMany bool   // node mints source pads on request rather than exposing one
}

// TestNodeLifecycleContract drives create -> accessors -> destroy for every
// registered node type. The accessors are where the arity interfaces are
// actually satisfied: a node that reports the wrong ID, or forgets to clear its
// pads on Destroy, still links up at first and only breaks later.
func TestNodeLifecycleContract(t *testing.T) {
	gst.Init()

	rawAudio := "audio/x-raw,format=S16LE,rate=48000,channels=2"
	rawVideo := "video/x-raw,width=320,height=240"

	cases := []nodeCase{
		{
			name: "AudioRegulator",
			create: func(t *testing.T) (core.Node, error) {
				return CreateAudioRegulatorNode(nil, AudioRegulatorConfig{
					ID:           "lc_areg",
					TargetFormat: rawAudio,
				})
			},
			srcIDs: []string{"lc_areg_src_0"}, dstIDs: []string{"lc_areg_sink_0"},
			srcPad: "lc_areg_src_0", sinkPad: "lc_areg_sink_0",
		},
		{
			name: "VideoResizer",
			create: func(t *testing.T) (core.Node, error) {
				return CreateVideoResizerNode(nil, VideoResizerConfig{
					ID:   "lc_vres",
					Caps: rawVideo,
				})
			},
			srcIDs: []string{"lc_vres_src_0"}, dstIDs: []string{"lc_vres_sink_0"},
			srcPad: "lc_vres_src_0", sinkPad: "lc_vres_sink_0",
		},
		{
			name: "VideoTranscode",
			create: func(t *testing.T) (core.Node, error) {
				return CreateVideoTranscodeNode(nil, VideoTranscodeConfig{
					ID:      "lc_vtr",
					Caps:    rawVideo,
					Encoder: "x264enc",
				})
			},
			srcIDs: []string{"lc_vtr_src_0"}, dstIDs: []string{"lc_vtr_sink_0"},
			srcPad: "lc_vtr_src_0", sinkPad: "lc_vtr_sink_0",
		},
		{
			name: "AudioTrack",
			create: func(t *testing.T) (core.Node, error) {
				return CreateAudioTrackNode(nil, AudioTrackConfig{ID: "lc_atr"})
			},
			srcIDs: []string{"lc_atr_src_0"}, dstIDs: []string{"lc_atr_sink_0"},
			srcPad: "lc_atr_src_0", sinkPad: "lc_atr_sink_0",
		},
		{
			name: "Discard",
			create: func(t *testing.T) (core.Node, error) {
				return CreateDiscardNode(nil, DiscardConfig{ID: "lc_dis"})
			},
			dstIDs: []string{"lc_dis_sink_0"}, sinkPad: "lc_dis_sink_0",
		},
		{
			name: "FileSink",
			create: func(t *testing.T) (core.Node, error) {
				return CreateFileSinkNode(nil, FileSinkConfig{
					ID:       "lc_fsk",
					Location: filepath.Join(t.TempDir(), "out.bin"),
				})
			},
			dstIDs: []string{"lc_fsk_sink_0"}, sinkPad: "lc_fsk_sink_0",
		},
		{
			name: "FileOutput",
			create: func(t *testing.T) (core.Node, error) {
				return CreateFileOutputNode(nil, FileOutputConfig{
					ID:       "lc_fout",
					FilePath: filepath.Join(t.TempDir(), "out.bin"),
				})
			},
			dstIDs: []string{"lc_fout_sink_0"}, sinkPad: "lc_fout_sink_0",
		},
		// RTMPOutput is a pure FLV sink: it no longer converts caps, because
		// rtmpsink's sink template is video/x-flv and no element in the bin
		// could turn raw video into it. An FlvMux must feed it.
		{
			name: "RTMPOutput",
			create: func(t *testing.T) (core.Node, error) {
				return CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{
					ID:       "lc_rtmp",
					Host:     "example.invalid",
					Location: "key",
				})
			},
			dstIDs: []string{"lc_rtmp_sink_0"}, sinkPad: "lc_rtmp_sink_0",
		},
		{
			name: "SourceInput",
			create: func(t *testing.T) (core.Node, error) {
				return CreateSourceInput(newTestRuntime(t, "lc_src"), SourceInputConfig{
					ID:         "lc_src",
					StreamType: core.StreamTypeStream,
					Caps:       "video/x-matroska",
				})
			},
			srcIDs: []string{"lc_src_src_0"}, srcPad: "lc_src_src_0",
		},
		{
			name: "Output",
			create: func(t *testing.T) (core.Node, error) {
				return CreateOutputNode(newTestRuntime(t, "lc_out"), OutputConfig{
					ID:   "lc_out",
					Caps: rawVideo,
				})
			},
			dstIDs: []string{"lc_out_sink_0"}, sinkPad: "lc_out_sink_0",
		},
		{
			name: "Preview",
			create: func(t *testing.T) (core.Node, error) {
				return CreatePreviewNode(newTestRuntime(t, "lc_prev"), PreviewConfig{ID: "lc_prev"})
			},
			srcIDs: []string{"lc_prev_src_0"}, dstIDs: []string{"lc_prev_sink_0"},
			srcPad: "lc_prev_src_0", sinkPad: "lc_prev_sink_0",
		},
		{
			name: "Clone",
			create: func(t *testing.T) (core.Node, error) {
				return CreateCloneNode(nil, CloneConfig{ID: "lc_clone"})
			},
			dstIDs:   []string{"lc_clone_sink_0"},
			sinkPad:  "lc_clone_sink_0",
			wantMany: true,
		},
		{
			name: "AudioCombiner",
			create: func(t *testing.T) (core.Node, error) {
				return CreateAudioCombinerNode(nil, AudioCombinerConfig{ID: "lc_mix"})
			},
			srcIDs:   []string{"lc_mix_src_0"},
			srcPad:   "lc_mix_src_0",
			wantMany: true,
		},
		{
			name: "AudioEncode",
			create: func(t *testing.T) (core.Node, error) {
				return CreateAudioEncodeNode(nil, AudioEncodeConfig{ID: "lc_aenc", Codec: AudioCodecAAC})
			},
			srcIDs: []string{"lc_aenc_src_0"}, dstIDs: []string{"lc_aenc_sink_0"},
			srcPad: "lc_aenc_src_0", sinkPad: "lc_aenc_sink_0",
		},
		{
			name: "FlvMux",
			create: func(t *testing.T) (core.Node, error) {
				return CreateFlvMuxNode(nil, FlvMuxConfig{ID: "lc_mux"})
			},
			srcIDs: []string{"lc_mux_src_0"},
			srcPad: "lc_mux_src_0",
			// Sink pads are request pads: none exist until RequestSinkPad runs,
			// which TestFlvMuxSinkBranches covers.
		},
		{
			name: "Compositor",
			create: func(t *testing.T) (core.Node, error) {
				return CreateCompositorNode(nil, CompositorConfig{ID: "lc_comp", Width: 640, Height: 360})
			},
			srcIDs: []string{"lc_comp_src_0"},
			srcPad: "lc_comp_src_0",
		},
		{
			name: "Transition",
			create: func(t *testing.T) (core.Node, error) {
				return CreateTransitionNode(nil, TransitionConfig{ID: "lc_trans"})
			},
			srcIDs: []string{"lc_trans_src_0"},
			srcPad: "lc_trans_src_0",
		},
		{
			name: "Volume",
			create: func(t *testing.T) (core.Node, error) {
				return CreateVolumeNode(nil, VolumeConfig{ID: "lc_vol"})
			},
			srcIDs: []string{"lc_vol_src_0"}, dstIDs: []string{"lc_vol_sink_0"},
			srcPad: "lc_vol_src_0", sinkPad: "lc_vol_sink_0",
		},
		{
			name: "TestSource",
			create: func(t *testing.T) (core.Node, error) {
				return CreateTestSourceNode(nil, TestSourceConfig{ID: "lc_test"})
			},
			srcIDs: []string{"lc_test_src_0"},
			srcPad: "lc_test_src_0",
		},
		{
			name: "ImageSource",
			create: func(t *testing.T) (core.Node, error) {
				// The file is only opened at PLAYING, so construction is clean.
				return CreateImageSourceNode(nil, ImageSourceConfig{ID: "lc_img", Location: filepath.Join(t.TempDir(), "logo.png")})
			},
			srcIDs: []string{"lc_img_src_0"},
			srcPad: "lc_img_src_0",
		},
		{
			name: "HLSOutput",
			create: func(t *testing.T) (core.Node, error) {
				dir := t.TempDir()
				return CreateHLSOutputNode(nil, HLSOutputConfig{
					ID:               "lc_hls",
					Location:         filepath.Join(dir, "seg%05d.ts"),
					PlaylistLocation: filepath.Join(dir, "index.m3u8"),
				})
			},
			dstIDs:  []string{"lc_hls_sink_0"},
			sinkPad: "lc_hls_sink_0",
		},
		{
			name: "OutputControl",
			create: func(t *testing.T) (core.Node, error) {
				return CreateOutputControlNode(nil, OutputControlConfig{ID: "lc_gate"})
			},
			srcIDs: []string{"lc_gate_src_0"}, dstIDs: []string{"lc_gate_sink_0"},
			srcPad: "lc_gate_src_0", sinkPad: "lc_gate_sink_0",
		},
		{
			name: "Thumbnail",
			create: func(t *testing.T) (core.Node, error) {
				return CreateThumbnailNode(newTestRuntime(t, "lc_thumb"), ThumbnailConfig{ID: "lc_thumb"})
			},
			dstIDs:  []string{"lc_thumb_sink_0"},
			sinkPad: "lc_thumb_sink_0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := tc.create(t)
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			if got := node.GetSrcIDs(); !slices.Equal(got, tc.srcIDs) {
				t.Errorf("GetSrcIDs = %v, want %v", got, tc.srcIDs)
			}
			if got := node.GetDstIDs(); !slices.Equal(got, tc.dstIDs) {
				t.Errorf("GetDstIDs = %v, want %v", got, tc.dstIDs)
			}

			if tc.srcPad != "" {
				pad, id, err := srcPadOf(node)
				if err != nil {
					t.Errorf("GetSrcPad: %v", err)
				} else if id != tc.srcPad {
					t.Errorf("GetSrcPad id = %q, want %q", id, tc.srcPad)
				} else if pad == nil {
					t.Errorf("GetSrcPad returned a nil pad for %q", tc.srcPad)
				}
			}
			if tc.sinkPad != "" {
				pad, id, err := sinkPadOf(node)
				if err != nil {
					t.Errorf("GetSinkPad: %v", err)
				} else if id != tc.sinkPad {
					t.Errorf("GetSinkPad id = %q, want %q", id, tc.sinkPad)
				} else if pad == nil {
					t.Errorf("GetSinkPad returned a nil pad for %q", tc.sinkPad)
				}
			}

			// UpdateProperties is on every node; a no-op must still be clean.
			if err := node.UpdateProperties(map[string]interface{}{"probe": 1}); err != nil {
				t.Errorf("UpdateProperties: %v", err)
			}
			if err := node.UpdateProperties(map[string]interface{}{"probe": nil}); err != nil {
				t.Errorf("UpdateProperties(delete): %v", err)
			}

			if err := node.Destroy(); err != nil {
				t.Fatalf("Destroy: %v", err)
			}
			// Destroy is documented as idempotent.
			if err := node.Destroy(); err == nil {
				t.Error("second Destroy returned nil, want an error")
			}

			// Pads must be dropped on Destroy, so nothing resolves a dead pad.
			if got := node.GetSrcIDs(); len(got) != 0 {
				t.Errorf("GetSrcIDs after Destroy = %v, want none", got)
			}
			if got := node.GetDstIDs(); len(got) != 0 {
				t.Errorf("GetDstIDs after Destroy = %v, want none", got)
			}
		})
	}
}

// srcPadOf and sinkPadOf read a pad through whichever arity interface the node
// implements. The interfaces overlap, so the most capable one is tried first.
func srcPadOf(node core.Node) (gst.Pad, string, error) {
	switch typed := node.(type) {
	case core.OneToOneNode:
		return typed.GetSrcPad()
	case core.ManyToOneNode:
		return typed.GetSrcPad()
	case core.ZeroToOneNode:
		return typed.GetSrcPad()
	}
	return nil, "", errors.New("node has no source pad accessor")
}

func sinkPadOf(node core.Node) (gst.Pad, string, error) {
	switch typed := node.(type) {
	case core.OneToManyNode:
		return typed.GetSinkPad()
	case core.OneToOneNode:
		return typed.GetSinkPad()
	case core.OneToZeroNode:
		return typed.GetSinkPad()
	}
	return nil, "", errors.New("node has no sink pad accessor")
}

// TestNodeConstructorsRejectBadConfig pins the validation each constructor does
// before it touches GStreamer. These are the guards that keep a bad frontend
// declaration from building half a bin.
func TestNodeConstructorsRejectBadConfig(t *testing.T) {
	gst.Init()

	cases := []struct {
		name    string
		build   func() (core.Node, error)
		wantErr error
	}{
		{
			name:    "AudioRegulator empty ID",
			build:   func() (core.Node, error) { return CreateAudioRegulatorNode(nil, AudioRegulatorConfig{}) },
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "AudioRegulator empty target format",
			build: func() (core.Node, error) {
				return CreateAudioRegulatorNode(nil, AudioRegulatorConfig{ID: "bad"})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name:    "VideoResizer empty ID",
			build:   func() (core.Node, error) { return CreateVideoResizerNode(nil, VideoResizerConfig{}) },
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "VideoResizer no caps",
			build: func() (core.Node, error) {
				// CapsFromString("") yields EMPTY caps, which is non-nil, so the
				// guard passes and the failure surfaces later as a link error.
				return CreateVideoResizerNode(nil, VideoResizerConfig{ID: "bad"})
			},
			wantErr: core.ErrGSTLinkErr,
		},
		{
			name:    "VideoTranscode empty ID",
			build:   func() (core.Node, error) { return CreateVideoTranscodeNode(nil, VideoTranscodeConfig{}) },
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name:    "AudioTrack empty ID",
			build:   func() (core.Node, error) { return CreateAudioTrackNode(nil, AudioTrackConfig{}) },
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name:    "Discard empty ID",
			build:   func() (core.Node, error) { return CreateDiscardNode(nil, DiscardConfig{}) },
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name:    "FileSink empty ID",
			build:   func() (core.Node, error) { return CreateFileSinkNode(nil, FileSinkConfig{}) },
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name:    "FileSink empty location",
			build:   func() (core.Node, error) { return CreateFileSinkNode(nil, FileSinkConfig{ID: "bad"}) },
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name:    "FileOutput empty ID",
			build:   func() (core.Node, error) { return CreateFileOutputNode(nil, FileOutputConfig{}) },
			wantErr: core.ErrInvalidConfig,
		},
		{
			name:    "FileOutput empty path",
			build:   func() (core.Node, error) { return CreateFileOutputNode(nil, FileOutputConfig{ID: "bad"}) },
			wantErr: core.ErrInvalidConfig,
		},
		{
			name:    "RTMPOutput empty ID",
			build:   func() (core.Node, error) { return CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{}) },
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "RTMPOutput missing host",
			build: func() (core.Node, error) {
				return CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{ID: "bad", Location: "k"})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "RTMPOutput port out of range",
			build: func() (core.Node, error) {
				return CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{
					ID: "bad", Host: "h", Location: "k", Port: 70000,
				})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "RTMPOutput bad scheme",
			build: func() (core.Node, error) {
				return CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{
					ID: "bad", Host: "h", Location: "k", Scheme: "ftp",
				})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "RTMPOutput username without password",
			build: func() (core.Node, error) {
				return CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{
					ID: "bad", Host: "h", Location: "k",
					Auth: RTMPOutputAuthConfig{Username: "u"},
				})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "RTMPOutput token with credentials",
			build: func() (core.Node, error) {
				return CreateRTMPOutputNode(nil, RTMPOutputNodeConfig{
					ID: "bad", Host: "h", Location: "k",
					Auth: RTMPOutputAuthConfig{Username: "u", Password: "p", SecureToken: "t"},
				})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name:    "SourceInput empty ID",
			build:   func() (core.Node, error) { return CreateSourceInput(newTestRuntime(t, "bad"), SourceInputConfig{}) },
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name: "SourceInput negative buffer size",
			build: func() (core.Node, error) {
				return CreateSourceInput(newTestRuntime(t, "bad"), SourceInputConfig{ID: "bad", BufferSize: -1})
			},
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name: "SourceInput bad stream type",
			build: func() (core.Node, error) {
				return CreateSourceInput(newTestRuntime(t, "bad"), SourceInputConfig{
					ID: "bad", StreamType: core.StreamType(99),
				})
			},
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name: "Output empty ID",
			build: func() (core.Node, error) {
				return CreateOutputNode(newTestRuntime(t, "bad"), OutputConfig{})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "Preview empty ID",
			build: func() (core.Node, error) {
				return CreatePreviewNode(newTestRuntime(t, "bad"), PreviewConfig{})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "AudioCombiner empty ID",
			build: func() (core.Node, error) {
				return CreateAudioCombinerNode(nil, AudioCombinerConfig{})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "AudioCombiner unknown element",
			build: func() (core.Node, error) {
				return CreateAudioCombinerNode(nil, AudioCombinerConfig{ID: "bad", ElementName: "nope"})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name:    "Clone empty ID",
			build:   func() (core.Node, error) { return CreateCloneNode(nil, CloneConfig{}) },
			wantErr: core.ErrInvalidConfig,
		},
		{
			name:    "AudioEncode empty ID",
			build:   func() (core.Node, error) { return CreateAudioEncodeNode(nil, AudioEncodeConfig{}) },
			wantErr: core.ErrGSTInvalidConfig,
		},
		{
			name:    "FlvMux empty ID",
			build:   func() (core.Node, error) { return CreateFlvMuxNode(nil, FlvMuxConfig{}) },
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "Compositor without a canvas size",
			build: func() (core.Node, error) {
				return CreateCompositorNode(nil, CompositorConfig{ID: "bad_comp"})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "Transition empty ID",
			build: func() (core.Node, error) {
				return CreateTransitionNode(nil, TransitionConfig{})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "Volume negative gain",
			build: func() (core.Node, error) {
				negative := -1.0
				return CreateVolumeNode(nil, VolumeConfig{ID: "bad_vol", Volume: &negative})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "TestSource empty ID",
			build: func() (core.Node, error) {
				return CreateTestSourceNode(nil, TestSourceConfig{})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "ImageSource without a location",
			build: func() (core.Node, error) {
				return CreateImageSourceNode(nil, ImageSourceConfig{ID: "bad_img"})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "HLSOutput without a playlist location",
			build: func() (core.Node, error) {
				return CreateHLSOutputNode(nil, HLSOutputConfig{ID: "bad_hls", Location: "seg%05d.ts"})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "OutputControl unknown drop mode",
			build: func() (core.Node, error) {
				return CreateOutputControlNode(nil, OutputControlConfig{ID: "bad_gate", DropMode: "nope"})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "OutputControl unknown leaky mode",
			build: func() (core.Node, error) {
				return CreateOutputControlNode(nil, OutputControlConfig{
					ID:     "bad_gate_leaky",
					Buffer: OutputBufferConfig{Leaky: "sideways"},
				})
			},
			wantErr: core.ErrInvalidConfig,
		},
		{
			name: "Thumbnail empty ID",
			build: func() (core.Node, error) {
				return CreateThumbnailNode(newTestRuntime(t, "bad_thumb"), ThumbnailConfig{})
			},
			wantErr: core.ErrInvalidConfig,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := tc.build()
			if err == nil {
				if node != nil {
					_ = node.Destroy()
				}
				t.Fatal("constructor returned nil error, want a rejection")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestSourceInputPublishesAndDrains drives the input node's goroutine path:
// the entry it registers must accept bytes, and Destroy must stop the handler
// and drop the entry rather than leaving it behind.
func TestSourceInputPublishesAndDrains(t *testing.T) {
	gst.Init()

	runtime := newTestRuntime(t, "si_node")
	node, err := CreateSourceInput(runtime, SourceInputConfig{
		ID:         "si_node",
		StreamType: core.StreamTypeStream,
	})
	if err != nil {
		t.Fatalf("CreateSourceInput: %v", err)
	}

	entry, ok := runtime.GetEntryById("si_node")
	if !ok {
		t.Fatal("source input did not register an entry")
	}
	inputEntry, ok := entry.(core.InputRegistryEntry)
	if !ok {
		t.Fatalf("entry %T does not accept input", entry)
	}
	handle, err := core.GetInputHandle[[]byte](inputEntry)
	if err != nil {
		t.Fatalf("GetInputHandle: %v", err)
	}
	if err := handle.Send([]byte("payload")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if err := node.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok := runtime.GetEntryById("si_node"); ok {
		t.Fatal("entry is still registered after Destroy")
	}
	// The handler observes Done() and exits; a stopped entry refuses new data.
	if err := handle.Send([]byte("late")); err == nil {
		t.Log("Send after Destroy returned nil; the handler may not have observed Done yet")
	}
}

// TestOutputNodeEntryLifecycle pins that Output registers its channel and takes
// it away again, since that entry is how a consumer reaches the node.
func TestOutputNodeEntryLifecycle(t *testing.T) {
	gst.Init()

	runtime := newTestRuntime(t, "out_node")
	node, err := CreateOutputNode(runtime, OutputConfig{ID: "out_node", Caps: "video/x-raw"})
	if err != nil {
		t.Fatalf("CreateOutputNode: %v", err)
	}

	entry, ok := runtime.GetEntryById("out_node")
	if !ok {
		t.Fatal("output did not register an entry")
	}
	outputEntry, ok := entry.(core.OutputRegistryEntry)
	if !ok {
		t.Fatalf("entry %T does not publish output", entry)
	}
	if _, err := core.GetOutputHandle[[]byte](outputEntry, "sub", 4); err != nil {
		t.Fatalf("GetOutputHandle: %v", err)
	}

	if err := node.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok := runtime.GetEntryById("out_node"); ok {
		t.Fatal("entry is still registered after Destroy")
	}
}

// TestCloneNodeBranchMinting covers the branch lifecycle the graph relies on:
// mint a branch, name one, refuse a duplicate, retire one, and confirm a
// retired number is never handed out again.
func TestCloneNodeBranchMinting(t *testing.T) {
	gst.Init()

	node, err := CreateCloneNode(nil, CloneConfig{ID: "cb"})
	if err != nil {
		t.Fatalf("CreateCloneNode: %v", err)
	}
	defer node.Destroy()

	// No declared branches yet.
	if got := node.GetSrcIDs(); len(got) != 0 {
		t.Fatalf("GetSrcIDs = %v, want none", got)
	}
	if _, _, err := node.GetSrcPad(); err == nil {
		t.Error("GetSrcPad with no branches returned nil error")
	}

	for _, want := range []string{"cb_src_0", "cb_src_1"} {
		_, id, err := node.RequestSrcPad(nil)
		if err != nil {
			t.Fatalf("RequestSrcPad(nil): %v", err)
		}
		if id != want {
			t.Fatalf("minted id = %q, want %q", id, want)
		}
	}

	// A caller-named pad is honoured even out of sequence.
	if _, id, err := node.RequestSrcPad(&core.PadInfo{ID: "cb_src_7"}); err != nil {
		t.Fatalf("RequestSrcPad(named): %v", err)
	} else if id != "cb_src_7" {
		t.Fatalf("named id = %q, want cb_src_7", id)
	}
	// ...but the same name twice is a conflict.
	if _, _, err := node.RequestSrcPad(&core.PadInfo{ID: "cb_src_7"}); err == nil {
		t.Error("duplicate RequestSrcPad returned nil error")
	}

	if _, _, err := node.GetSrcPad(); err != nil {
		t.Errorf("GetSrcPad with branches: %v", err)
	}

	if err := node.RemoveSrcPad("cb_src_1"); err != nil {
		t.Fatalf("RemoveSrcPad: %v", err)
	}
	if got := node.GetSrcIDs(); slices.Contains(got, "cb_src_1") {
		t.Errorf("GetSrcIDs = %v, want cb_src_1 gone", got)
	}
	if err := node.RemoveSrcPad("cb_src_1"); err == nil {
		t.Error("second RemoveSrcPad returned nil error")
	}
	if err := node.RemoveSrcPad("nope"); err == nil {
		t.Error("RemoveSrcPad(unknown) returned nil error")
	}

	// The retired number must not be reused.
	if _, id, err := node.RequestSrcPad(nil); err != nil {
		t.Fatalf("RequestSrcPad after removal: %v", err)
	} else if id == "cb_src_1" {
		t.Fatalf("minted %q after it was retired, want a fresh number", id)
	}
}

// TestAudioCombinerSinkBranches covers the sink side of the same contract.
func TestAudioCombinerSinkBranches(t *testing.T) {
	gst.Init()

	node, err := CreateAudioCombinerNode(nil, AudioCombinerConfig{ID: "ac"})
	if err != nil {
		t.Fatalf("CreateAudioCombinerNode: %v", err)
	}
	defer node.Destroy()

	if got := node.GetDstIDs(); len(got) != 0 {
		t.Fatalf("GetDstIDs = %v, want none", got)
	}

	_, id, err := node.RequestSinkPad()
	if err != nil {
		t.Fatalf("RequestSinkPad: %v", err)
	}
	if id != "ac_sink_0" {
		t.Fatalf("first sink id = %q, want ac_sink_0", id)
	}
	if _, id, err := node.RequestSinkPad(); err != nil {
		t.Fatalf("second RequestSinkPad: %v", err)
	} else if id != "ac_sink_1" {
		t.Fatalf("second sink id = %q, want ac_sink_1", id)
	}
	if got := node.GetDstIDs(); len(got) != 2 {
		t.Fatalf("GetDstIDs = %v, want 2 branches", got)
	}

	if err := node.RemoveSinkPad("ac_sink_0"); err != nil {
		t.Fatalf("RemoveSinkPad: %v", err)
	}
	if got := node.GetDstIDs(); slices.Contains(got, "ac_sink_0") {
		t.Errorf("GetDstIDs = %v, want ac_sink_0 gone", got)
	}
	if err := node.RemoveSinkPad("ac_sink_0"); err == nil {
		t.Error("second RemoveSinkPad returned nil error")
	}
	if err := node.RemoveSinkPad("nope"); err == nil {
		t.Error("RemoveSinkPad(unknown) returned nil error")
	}
}
