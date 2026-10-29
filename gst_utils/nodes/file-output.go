package nodes

import (
	"fmt"
	"log"
	"os"
	"sync"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

type FileOutputNode struct {
	*core.BaseNode
	sinkPad  gst.Pad
	filePath string
	// The sink handler runs on a streaming thread and Destroy runs on the
	// caller's, so the writer is guarded by mu and closed under it.
	mu           sync.Mutex
	file         *os.File
	appSink      gstapp.AppSink
	sampleHandle gobject.SignalHandle
}

var _ core.OneToZeroNode = (*FileOutputNode)(nil)

// NodeTypeFileOutput is this node's registered type name. It lives beside the
// constructor so adding a node type never needs a shared table edited.
const NodeTypeFileOutput = "FileOutput"

func init() {
	registerNode(NodeTypeFileOutput, core.SpecOneToZero, core.AdaptFactory(CreateFileOutputNode))
}

type FileOutputConfig struct {
	ID       string
	FilePath string
}

func CreateFileOutputNode(_ core.NodeRuntime, config FileOutputConfig) (*FileOutputNode, error) {
	id := config.ID
	filePath := config.FilePath
	if id == "" {
		return nil, fmt.Errorf("%w: node id is empty", core.ErrInvalidConfig)
	}
	if filePath == "" {
		return nil, fmt.Errorf("%w: file path is empty", core.ErrInvalidConfig)
	}
	fileOutputBin := gst.NewBin(id).(gst.Bin)
	appSinkElement := gst.ElementFactoryMake("appsink", fmt.Sprintf("%s_AppSink", id)).(gstapp.AppSink)
	queueElement := gst.ElementFactoryMake("queue", fmt.Sprintf("%s_Queue", id))

	if appSinkElement == nil || queueElement == nil {
		return nil, fmt.Errorf("%w: create file output elements", core.ErrGSTNoElements)
	}

	sinkPad := gst.NewGhostPad(fmt.Sprintf("%s_sink_0", id), queueElement.GetStaticPad("sink"))
	if sinkPad == nil {
		return nil, core.ErrGSTPadNotFound
	}

	fileOutputBin.AddMany(appSinkElement, queueElement)

	if !queueElement.Link(appSinkElement) {
		return nil, fmt.Errorf("%w: failed to link queue to appsink", core.ErrGSTLinkErr)
	}

	fileOutputBin.AddPad(sinkPad)

	appSinkElement.SetObjectProperties(map[string]any{
		"drop": true,
		"sync": false,
	})

	appSinkElement.SetEmitSignals(true)

	// Opened once and appended to: os.WriteFile truncates, so writing per buffer
	// would leave only the last one on disk.
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("%w: open %q: %v", core.ErrInvalidConfig, filePath, err)
	}

	node := &FileOutputNode{
		sinkPad:  sinkPad,
		filePath: filePath,
		file:     file,
		appSink:  appSinkElement,
	}
	node.sampleHandle = appSinkElement.Connect("new-sample", func(sink gstapp.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowOK
		}
		buffer := sample.GetBuffer()
		if buffer == nil {
			return gst.FlowOK
		}
		mappedBuffer, ok := buffer.Map(gst.MapFlags(gst.MapRead))
		if !ok {
			return gst.FlowOK
		}

		data := mappedBuffer.Data()
		copiedData := make([]byte, len(data))
		copy(copiedData, data)
		mappedBuffer.Unmap()

		node.mu.Lock()
		file := node.file
		node.mu.Unlock()
		// Destroy clears the handle; stop the stream instead of writing to a
		// closed descriptor.
		if file == nil {
			return gst.FlowEOS
		}

		if _, err := file.Write(copiedData); err != nil {
			log.Printf("FileOutputNode %s: write %q: %v", id, filePath, err)
		}
		return gst.FlowOK
	})

	queueElement.SyncStateWithParent()
	appSinkElement.SyncStateWithParent()

	node.BaseNode = core.NewBaseNode(id, NodeTypeFileOutput, fileOutputBin)
	if err := node.BaseNode.UpdateProperties(map[string]any{"FilePath": filePath}); err != nil {
		return nil, err
	}
	node.BaseNode.ProtectProperties("FilePath")
	return node, nil
}

func (n *FileOutputNode) GetSinkPad() (gst.Pad, string, error) {
	return padOrErr(n.sinkPad, n.SinkPadID())
}

func (n *FileOutputNode) GetSrcIDs() []string { return nil }

func (n *FileOutputNode) GetDstIDs() []string {
	if n.sinkPad == nil {
		return nil
	}
	return []string{n.SinkPadID()}
}

func (n *FileOutputNode) Destroy() error {
	if n.Destroyed() {
		return fmt.Errorf("%w: node already destroyed", core.ErrGSTNodeDestroyed)
	}
	bin := n.Bin()

	if n.appSink != nil && n.sampleHandle != 0 {
		n.appSink.HandlerDisconnect(n.sampleHandle)
		n.sampleHandle = 0
	}

	bin.SetState(gst.State(gst.StateNull))

	n.mu.Lock()
	file := n.file
	n.file = nil
	n.mu.Unlock()
	if file != nil {
		if err := file.Close(); err != nil {
			log.Printf("FileOutputNode %s: close %q: %v", n.ID(), n.filePath, err)
		}
	}

	n.sinkPad = nil
	n.filePath = ""
	n.appSink = nil
	n.Clear()
	return nil
}
