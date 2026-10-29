package chan_registry

import "live_tools/gst_utils/core"

func NewByteOutputEntry(id string, bufferSize int, dropType core.ChannelDropType) (*OutputEntry[[]byte], error) {
	return NewOutputEntry[[]byte](id, bufferSize, dropType)
}
