package main

import (
	"bytes"
	"testing"
)

func TestRGBToJPEG(t *testing.T) {
	data := []byte{
		255, 0, 0, 0, 255, 0,
		0, 0, 255, 255, 255, 255,
	}
	jpegData, err := rgbToJPEG(data, 2, 2)
	if err != nil {
		t.Fatalf("rgbToJPEG: %v", err)
	}
	if !bytes.HasPrefix(jpegData, []byte{0xff, 0xd8}) || !bytes.HasSuffix(jpegData, []byte{0xff, 0xd9}) {
		t.Fatalf("not a JPEG stream: %x", jpegData[:min(len(jpegData), 4)])
	}
}

func TestRGBToJPEGRejectsShortFrame(t *testing.T) {
	if _, err := rgbToJPEG([]byte{0, 0, 0}, 2, 2); err == nil {
		t.Fatal("accepted a short RGB frame")
	}
}
