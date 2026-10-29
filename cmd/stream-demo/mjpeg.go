package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
)

func rgbToJPEG(data []byte, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 || len(data) < width*height*3 {
		return nil, fmt.Errorf("RGB frame has %d bytes; need at least %d for %dx%d", len(data), width*height*3, width, height)
	}
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			src := (y*width + x) * 3
			dst := frame.PixOffset(x, y)
			frame.Pix[dst+0] = data[src]
			frame.Pix[dst+1] = data[src+1]
			frame.Pix[dst+2] = data[src+2]
			frame.Pix[dst+3] = 0xff
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, frame, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
