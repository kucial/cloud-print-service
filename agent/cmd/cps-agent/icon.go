package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Tray icon colours by phase, the same as the dots in the panel.
var phaseColors = map[string]color.NRGBA{
	phaseConnected:  {0x34, 0xc7, 0x59, 0xff},
	phaseStarting:   {0xff, 0x9f, 0x0a, 0xff},
	phaseNoPrinters: {0xff, 0x9f, 0x0a, 0xff},
	phaseError:      {0xff, 0x3b, 0x30, 0xff},
	phaseBlocked:    {0xff, 0x3b, 0x30, 0xff},
	phaseNeedsSetup: {0x8e, 0x8e, 0x93, 0xff},
}

// statusDot draws an anti-aliased filled circle with a thin dark ring, readable on
// both light and dark taskbars.
func statusDot(size int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	r := float64(size)/2 - 1
	ring := math.Max(1, float64(size)/16)
	centre := float64(size) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)+0.5-centre, float64(y)+0.5-centre)
			alpha := math.Min(1, math.Max(0, r-d+0.5)) // edge coverage
			if alpha == 0 {
				continue
			}
			px := c
			if d > r-ring {
				px = color.NRGBA{c.R / 2, c.G / 2, c.B / 2, 0xff}
			}
			px.A = uint8(alpha * 255)
			img.SetNRGBA(x, y, px)
		}
	}
	return img
}

// iconPNG is the tray icon for macOS and Linux.
func iconPNG(phase string) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, statusDot(64, colorFor(phase)))
	return buf.Bytes()
}

// iconICO is the tray icon for Windows: an .ico with 16 and 32 px images as classic
// 32-bit DIBs, which every Windows version loads (PNG entries are not always accepted).
func iconICO(phase string) []byte {
	sizes := []int{16, 32}
	var images [][]byte
	for _, s := range sizes {
		images = append(images, dib(statusDot(s, colorFor(phase))))
	}
	var buf bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&buf, le, [3]uint16{0, 1, uint16(len(sizes))}) // reserved, type=icon, count
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		_ = binary.Write(&buf, le, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{uint8(s), uint8(s), 0, 0, 1, 32, uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, b := range images {
		buf.Write(b)
	}
	return buf.Bytes()
}

// dib encodes img as an icon image: BITMAPINFOHEADER (double height, for the mask),
// bottom-up BGRA pixels, then an all-zero AND mask (alpha does the masking).
func dib(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	maskRow := ((w + 31) / 32) * 4
	var buf bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&buf, le, struct {
		Size                  uint32
		Width, Height         int32
		Planes, BitCount      uint16
		Compression, Image    uint32
		XPels, YPels          int32
		ClrUsed, ClrImportant uint32
	}{40, int32(w), int32(2 * h), 1, 32, 0, uint32(w*h*4 + maskRow*h), 0, 0, 0, 0})
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	buf.Write(make([]byte, maskRow*h))
	return buf.Bytes()
}

func colorFor(phase string) color.NRGBA {
	if c, ok := phaseColors[phase]; ok {
		return c
	}
	return phaseColors[phaseNeedsSetup]
}
