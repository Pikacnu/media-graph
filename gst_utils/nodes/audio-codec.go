package nodes

import (
	"slices"
	"strings"
	"sync"

	"github.com/go-gst/go-gst/pkg/gst"
)

type AudioCodec string

const (
	AudioCodecAAC    AudioCodec = "aac"
	AudioCodecOpus   AudioCodec = "opus"
	AudioCodecMP3    AudioCodec = "mp3"
	AudioCodecVorbis AudioCodec = "vorbis"
	AudioCodecFLAC   AudioCodec = "flac"
)

const defaultAudioCodec = AudioCodecAAC

type codecChain struct {
	Encoder string
	Parser  string
}

// audioCodecCandidates lists, per codec, the encoder+parser pairs to try in
// preference order.
//
// A codec is a format, not an element, and several plugins can provide the same
// one: AAC ships as voaacenc (gst-plugins-bad), fdkaacenc, faac, or mfaacenc
// depending on the platform's plugin set. Pinning one element name makes the
// codec unusable wherever that particular plugin is absent, and the failure only
// surfaces at pipeline build time. Listing the alternatives keeps a codec usable
// wherever any one of them is installed.
var audioCodecCandidates = map[AudioCodec][]codecChain{
	AudioCodecAAC: {
		{Encoder: "voaacenc", Parser: "aacparse"},
		{Encoder: "fdkaacenc", Parser: "aacparse"},
		{Encoder: "faac", Parser: "aacparse"},
		{Encoder: "mfaacenc", Parser: "aacparse"},
	},
	AudioCodecOpus:   {{Encoder: "opusenc", Parser: "opusparse"}},
	AudioCodecMP3:    {{Encoder: "lamemp3enc", Parser: "mpegaudioparse"}},
	AudioCodecVorbis: {{Encoder: "vorbisenc", Parser: "vorbisparse"}},
	AudioCodecFLAC:   {{Encoder: "flacenc", Parser: "flacparse"}},
}

func audioCodecNames() []string {
	names := make([]string, 0, len(audioCodecCandidates))
	for codec := range audioCodecCandidates {
		names = append(names, string(codec))
	}
	slices.Sort(names)
	return names
}

// audioCodecCandidatesFor returns the candidate chains for a codec. known is
// false when the codec itself is not recognised, which is different from a
// recognised codec having nothing installed.
func audioCodecCandidatesFor(codec AudioCodec) ([]codecChain, bool) {
	candidates, known := audioCodecCandidates[codec]
	return candidates, known
}

// resolveAudioCodecChain picks the first candidate pair whose elements are both
// installed. ok is false when the codec is unknown or when no candidate can be
// built on this machine.
//
// The lookup walks the plugin registry, so it cannot run in init(): a factory
// find before gst.Init() returns nil for every element regardless of what is
// installed, which would mark every codec unavailable. Instead the answer is
// computed once, lazily, on the first construction.
func resolveAudioCodecChain(codec AudioCodec) (codecChain, bool) {
	resolvedAudioCodecChainsOnce.Do(func() {
		// Ensure GStreamer is up before reading the registry, or the walk below
		// would record "nothing is installed" and freeze that answer for the
		// process. The check keeps this from being a redundant call when the
		// caller (main, or a test's gst.Init) already initialised: gst_init is
		// safe to repeat and to race — verified with 200 sequential and 50
		// concurrent calls under -race — but there is no reason to rely on that
		// when gst_is_initialized answers the question directly.
		if !gst.IsInitialized() {
			gst.Init()
		}

		resolvedAudioCodecChains = make(map[AudioCodec]codecChain, len(audioCodecCandidates))
		for codec, candidates := range audioCodecCandidates {
			for _, candidate := range candidates {
				if elementInstalled(candidate.Encoder) && elementInstalled(candidate.Parser) {
					resolvedAudioCodecChains[codec] = candidate
					break
				}
			}
		}
	})
	chain, ok := resolvedAudioCodecChains[codec]
	return chain, ok
}

// resolvedAudioCodecChainsOnce guards the one-time registry walk above.
// resolvedAudioCodecChains holds the codecs that resolved and deliberately omits
// those that did not, so a missing codec stays missing without re-walking.
var (
	resolvedAudioCodecChainsOnce sync.Once
	resolvedAudioCodecChains     map[AudioCodec]codecChain
)

// elementInstalled reports whether a factory is registered for the element. It
// looks the factory up rather than constructing an element: building one just to
// check would allocate a GObject that has to be unreffed again.
func elementInstalled(name string) bool {
	return gst.ElementFactoryFind(name) != nil
}

// formatCodecCandidates renders the candidate pairs for an error message, so a
// build failure names every element combination that was tried.
func formatCodecCandidates(candidates []codecChain) string {
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		parts = append(parts, candidate.Encoder+" + "+candidate.Parser)
	}
	return strings.Join(parts, ", ")
}
