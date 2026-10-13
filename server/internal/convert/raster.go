package convert

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
)

// bitmap is a 1-bit image: true is a dot (black). Rows are packed MSB first, which is
// what ESC/POS GS v 0, ZPL ^GF and PCL raster all expect.
type bitmap struct {
	w, h int
	rows [][]byte // len h, each ceil(w/8) bytes
}

func (b *bitmap) rowBytes() int { return (b.w + 7) / 8 }

// fitWidth scales img to exactly width dots wide, keeping its aspect ratio. A picture
// narrower than the paper is centred at its own size rather than blown up past 2x, so
// a small logo stays sharp.
func fitWidth(img image.Image, width int) *image.Gray {
	src := img.Bounds()
	scale := float64(width) / float64(src.Dx())
	if scale > 2 {
		scale = 2
	}
	w := int(math.Round(float64(src.Dx()) * scale))
	h := int(math.Round(float64(src.Dy()) * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	// Composite onto white first: transparent pixels are paper, not black.
	flat := image.NewRGBA(src)
	draw.Draw(flat, src, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, src, img, src.Min, draw.Over)

	scaled := image.NewGray(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), flat, src, xdraw.Src, nil)
	if w == width {
		return scaled
	}
	out := image.NewGray(image.Rect(0, 0, width, h))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(out, image.Rect((width-w)/2, 0, (width-w)/2+w, h), scaled, image.Point{}, draw.Src)
	return out
}

// fitBox scales img to fit inside width x height dots (keeping its aspect ratio) and
// centres it on a white box of exactly that size: a label is a fixed piece of paper.
func fitBox(img image.Image, width, height int) *image.Gray {
	src := img.Bounds()
	w := width
	if h := int(math.Round(float64(src.Dy()) * float64(width) / float64(src.Dx()))); h > height {
		w = int(math.Round(float64(src.Dx()) * float64(height) / float64(src.Dy())))
	}
	g := fitWidth(img, w)
	out := image.NewGray(image.Rect(0, 0, width, height))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	gb := g.Bounds()
	x0, y0 := (width-gb.Dx())/2, (height-gb.Dy())/2
	draw.Draw(out, image.Rect(x0, y0, x0+gb.Dx(), y0+gb.Dy()), g, image.Point{}, draw.Src)
	return out
}

// dither turns a grey image into dots with Floyd–Steinberg error diffusion, so photos
// and grey logos keep their tones on a printer that has only black.
func dither(g *image.Gray) *bitmap {
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	errs := make([][]float64, 2)
	errs[0], errs[1] = make([]float64, w+2), make([]float64, w+2)
	bm := &bitmap{w: w, h: h, rows: make([][]byte, h)}
	for y := 0; y < h; y++ {
		row := make([]byte, bm.rowBytes())
		cur, next := errs[y%2], errs[(y+1)%2]
		for i := range next {
			next[i] = 0
		}
		for x := 0; x < w; x++ {
			v := float64(g.GrayAt(b.Min.X+x, b.Min.Y+y).Y) + cur[x+1]
			black := v < 128
			var e float64
			if black {
				row[x/8] |= 0x80 >> (x % 8)
				e = v
			} else {
				e = v - 255
			}
			cur[x+2] += e * 7 / 16
			next[x] += e * 3 / 16
			next[x+1] += e * 5 / 16
			next[x+2] += e * 1 / 16
		}
		bm.rows[y] = row
		for i := range cur {
			cur[i] = 0
		}
	}
	return bm
}

// trimBottom drops trailing blank rows: a receipt should end where the content does.
func (b *bitmap) trimBottom() {
	for b.h > 1 {
		blank := true
		for _, c := range b.rows[b.h-1] {
			if c != 0 {
				blank = false
				break
			}
		}
		if !blank {
			return
		}
		b.h--
		b.rows = b.rows[:b.h]
	}
}

// image draws the dots back as a black-and-white picture.
func (b *bitmap) image() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, b.w, b.h))
	for y := 0; y < b.h; y++ {
		for x := 0; x < b.w; x++ {
			if b.rows[y][x/8]&(0x80>>(x%8)) == 0 {
				img.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	return img
}
