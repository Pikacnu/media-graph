package nodes

import (
	"fmt"
	"slices"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

type RTMPOutputNode struct {
	*core.BaseNode
	sinkPad gst.Pad
}

var _ core.OneToZeroNode = (*RTMPOutputNode)(nil)

// NodeTypeRTMPOutput is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeRTMPOutput = "RTMPOutput"

func init() {
	registerNode(NodeTypeRTMPOutput, core.SpecOneToZero, core.AdaptFactory(CreateRTMPOutputNode))
}

type RTMPOutputNodeConfig struct {
	ID          string
	Host        string
	Location    string
	Port        int
	Application string
	Delay       int
	DownTimeout int
	Auth        RTMPOutputAuthConfig
	Scheme      string
}

var AllowedRTMPSchemes = []string{"rtmp", "rtmps"}

type RTMPOutputAuthConfig struct {
	Username    string
	Password    string
	SecureToken string
}

func CreateRTMPOutputNode(_ core.NodeRuntime, config RTMPOutputNodeConfig) (*RTMPOutputNode, error) {

	id := config.ID
	if id == "" {
		return nil, fmt.Errorf("%w: node id is empty", core.ErrInvalidConfig)
	}
	if config.Host == "" || config.Location == "" {
		return nil, fmt.Errorf("%w: host and location are required", core.ErrInvalidConfig)
	}
	if config.Port < 0 || config.Port > 65535 {
		return nil, fmt.Errorf("%w: port %d out of range", core.ErrInvalidConfig, config.Port)
	}
	// Normalise defaults BEFORE validating, so "unset" is not mistaken for
	// "invalid".
	if config.Application == "" {
		config.Application = "live"
	}
	if config.Scheme == "" {
		config.Scheme = "rtmp"
	}
	if config.Delay < 0 {
		config.Delay = 0
	}
	if config.DownTimeout <= 0 {
		config.DownTimeout = 5000000
	}
	if !slices.Contains(AllowedRTMPSchemes, config.Scheme) {
		return nil, fmt.Errorf("%w: unsupported scheme %q", core.ErrInvalidConfig, config.Scheme)
	}
	if config.Auth.Username != "" && config.Auth.Password == "" {
		return nil, fmt.Errorf("%w: username set without password", core.ErrInvalidConfig)
	}
	if config.Auth.SecureToken != "" && (config.Auth.Username != "" || config.Auth.Password != "") {
		return nil, fmt.Errorf("%w: secure token cannot be combined with username/password", core.ErrInvalidConfig)
	}

	resizerBin := gst.NewBin(id).(gst.Bin)
	queueElement := gst.ElementFactoryMake("queue", fmt.Sprintf("%s_Queue", id))
	rtmpSinkElement := gst.ElementFactoryMake("rtmpsink", fmt.Sprintf("%s_RTMPSink", id))

	if queueElement == nil || rtmpSinkElement == nil {
		return nil, fmt.Errorf("%w: create rtmp output elements", core.ErrGSTNoElements)
	}

	// rtmpsink's sink template accepts only video/x-flv, so this node is a pure
	// FLV sink: an FlvMux node must feed it. It deliberately does NOT convert or
	// filter caps itself, because no element in this bin could turn raw video
	// into the FLV caps rtmpsink negotiates.
	resizerBin.Add(queueElement)
	resizerBin.Add(rtmpSinkElement)

	if !queueElement.Link(rtmpSinkElement) {
		return nil, fmt.Errorf("%w: failed to link queue to rtmpsink", core.ErrGSTLinkErr)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), queueElement.GetStaticPad("sink"))
	if sinkPad == nil {
		return nil, core.ErrGSTPadNotFound
	}

	resizerBin.AddPad(sinkPad)

	rtmpSinkElement.SetObjectProperties(map[string]any{
		// connection related properties
		"host":        config.Host,
		"location":    config.Location,
		"port":        int32(config.Port),
		"application": config.Application, // The application name on the RTMP server
		// time related properties
		"processing-deadline": "buffering-time",
		// GObject rejects plain int, so numeric properties widen to int32.
		"render-delay": int32(config.Delay),       // Set to 0 to minimize latency should be adjust
		"timeout":      int32(config.DownTimeout), // Timeout in seconds for network operations
		// auth
		"username":     config.Auth.Username, // Username for authentication if required
		"password":     config.Auth.Password,
		"secure-token": config.Auth.SecureToken, // Authorization token if required by the RTMP server
		"scheme":       config.Scheme,           // or "rtmps" for secure connections
	})

	queueElement.SyncStateWithParent()
	rtmpSinkElement.SyncStateWithParent()

	base := core.NewBaseNode(id, NodeTypeRTMPOutput, resizerBin)
	// The connection target is baked into rtmpsink at build time, so it is
	// recorded and locked rather than silently changeable.
	if err := base.UpdateProperties(map[string]any{
		"Host":        config.Host,
		"Location":    config.Location,
		"Scheme":      config.Scheme,
		"Application": config.Application,
	}); err != nil {
		return nil, err
	}
	base.ProtectProperties("Host", "Location", "Scheme", "Application")

	return &RTMPOutputNode{
		BaseNode: base,
		sinkPad:  sinkPad,
	}, nil
}

func (n *RTMPOutputNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *RTMPOutputNode) GetSrcIDs() []string { return nil }

func (n *RTMPOutputNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *RTMPOutputNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()

	bin.SetState(gst.State(gst.StateNull))

	n.sinkPad = nil
	n.Clear()
	return nil
}
