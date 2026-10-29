package main

import (
	"bufio"
	"fmt"
	"io"

	"live_tools/gst_utils/core"
)

func sendInput(mediaGraph interface {
	GetEntryByEntryID(string) (core.RegistryEntry, error)
}, nodeID string, reader io.Reader, chunkSize int) (uint64, error) {
	if chunkSize <= 0 {
		return 0, fmt.Errorf("chunk size must be positive")
	}
	entry, err := mediaGraph.GetEntryByEntryID(nodeID)
	if err != nil {
		return 0, err
	}
	inputEntry, ok := entry.(core.InputRegistryEntry)
	if !ok {
		return 0, fmt.Errorf("entry %q does not accept byte input", nodeID)
	}
	handle, err := core.GetInputHandle[[]byte](inputEntry)
	if err != nil {
		return 0, err
	}

	buffer := make([]byte, chunkSize)
	reader = bufio.NewReaderSize(reader, chunkSize)
	var sent uint64
	for {
		n, readErr := io.ReadFull(reader, buffer)
		if n > 0 {
			if err := handle.Send(append([]byte(nil), buffer[:n]...)); err != nil {
				return sent, err
			}
			sent += uint64(n)
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return sent, nil
		}
		if readErr != nil {
			return sent, readErr
		}
	}
}

func subscribeOutput(mediaGraph interface {
	GetEntryByEntryID(string) (core.RegistryEntry, error)
}, nodeID, subscriberID string, bufferSize int) (<-chan []byte, error) {
	if bufferSize <= 0 {
		return nil, fmt.Errorf("buffer size must be positive")
	}
	entry, err := mediaGraph.GetEntryByEntryID(nodeID)
	if err != nil {
		return nil, err
	}
	outputEntry, ok := entry.(core.OutputRegistryEntry)
	if !ok {
		return nil, fmt.Errorf("entry %q does not publish byte output", nodeID)
	}
	handle, err := core.GetOutputHandle[[]byte](outputEntry, subscriberID, bufferSize)
	if err != nil {
		return nil, err
	}
	return handle.RecvChan(), nil
}
