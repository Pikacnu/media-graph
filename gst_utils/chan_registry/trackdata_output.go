package chan_registry

import "live_tools/gst_utils/core"

func NewTrackDataOutputEntry(id string, bufferSize int, dropType core.ChannelDropType) (*OutputEntry[core.TrackData], error) {
	return NewOutputEntry[core.TrackData](id, bufferSize, dropType)
}
