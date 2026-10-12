package render

import (
	"context"
	"io"

	"github.com/swiftpush/zebrash"
	"github.com/swiftpush/zebrash/drawers"
	"github.com/swiftpush/zebrash/elements"
)

// zebrashDrawer is the tiny surface of the library we actually use. Keeping it behind
// an interface makes the renderer swappable and the tests trivial.
type zebrashDrawer interface {
	drawPNG(ctx context.Context, labels []elements.LabelInfo, w io.Writer, o drawers.DrawerOptions) error
	drawPDF(ctx context.Context, labels []elements.LabelInfo, w io.Writer, o drawers.DrawerOptions) error
	drawSVG(ctx context.Context, labels []elements.LabelInfo, w io.Writer, o drawers.DrawerOptions) error
}

type realDrawer struct{ d *zebrash.Drawer }

func newZebrashDrawer() zebrashDrawer { return &realDrawer{d: zebrash.NewDrawer()} }

func (r *realDrawer) drawPNG(_ context.Context, labels []elements.LabelInfo, w io.Writer, o drawers.DrawerOptions) error {
	return r.d.DrawLabelAsPng(labels[0], w, o)
}

func (r *realDrawer) drawPDF(_ context.Context, labels []elements.LabelInfo, w io.Writer, o drawers.DrawerOptions) error {
	return r.d.DrawLabelAsPdf(labels[0], w, o)
}

func (r *realDrawer) drawSVG(_ context.Context, labels []elements.LabelInfo, w io.Writer, o drawers.DrawerOptions) error {
	return r.d.DrawLabelAsSvg(labels[0], w, o)
}

func parseZebrash(zpl string) ([]elements.LabelInfo, error) {
	return zebrash.NewParser().Parse([]byte(zpl))
}

func zebrushOptions(o Options) drawers.DrawerOptions {
	return drawers.DrawerOptions{
		LabelWidthMm:  o.WidthMM,
		LabelHeightMm: o.HeightMM,
		Dpmm:          o.DPMM,
		// ^POI prints the label rotated 180°; Upright suppresses that for viewers
		// who want to read the format instead of simulating the paper path.
		EnableInvertedLabels: !o.Upright,
		GrayscaleOutput:      o.Grayscale,
	}
}

// sliceWriter is an io.Writer that accumulates into a byte slice.
type sliceWriter struct{ b []byte }

func (s *sliceWriter) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}
