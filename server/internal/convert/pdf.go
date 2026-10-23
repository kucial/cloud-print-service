package convert

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// FindTools looks for poppler's pdftoppm and pdftops: in dir when given, else on PATH.
func FindTools(dir string) Converter {
	find := func(name string) string {
		if dir != "" {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
			return ""
		}
		p, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	return Converter{PDFToPPM: find("pdftoppm"), PDFToPS: find("pdftops")}
}

// pdfTimeout bounds one external conversion: a PDF that takes longer is not a print job.
const pdfTimeout = 60 * time.Second

// run feeds data to tool on stdin-like terms: poppler reads files, so the PDF goes to
// a private temp directory that is removed afterwards.
func run(ctx context.Context, tool string, data []byte, args func(in, outDir string) []string) (string, error) {
	dir, err := os.MkdirTemp("", "cps-convert-")
	if err != nil {
		return "", err
	}
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, tool, args(in, dir)...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		msg := bytes.TrimSpace(stderr.Bytes())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("%w: %s: %v %s", ErrBadDocument, filepath.Base(tool), err, msg)
	}
	return dir, nil
}

// rasterisePDF renders every page at dpi, greyscale.
func (c Converter) rasterisePDF(ctx context.Context, data []byte, dpi int) ([]image.Image, error) {
	var pages []image.Image
	err := c.eachPDFPage(ctx, data, dpi, true, func(img image.Image) error {
		pages = append(pages, img)
		return nil
	})
	return pages, err
}

// eachPDFPage renders every page at dpi, greyscale or in colour, and hands them to fn
// one at a time: a colour page at inkjet resolution is tens of megabytes as an image.
func (c Converter) eachPDFPage(ctx context.Context, data []byte, dpi int, grey bool, fn func(image.Image) error) error {
	if c.PDFToPPM == "" {
		return ErrNoPDFTools
	}
	dir, err := run(ctx, c.PDFToPPM, data, func(in, out string) []string {
		args := []string{"-r", strconv.Itoa(dpi), "-png", "-l", strconv.Itoa(MaxPages + 1)}
		if grey {
			args = append(args, "-gray")
		}
		return append(args, in, filepath.Join(out, "page"))
	})
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	files, _ := filepath.Glob(filepath.Join(dir, "page*.png"))
	// page-1.png … page-10.png: order by the number, not the text.
	sort.Slice(files, func(i, j int) bool { return pageNo(files[i]) < pageNo(files[j]) })
	if len(files) == 0 {
		return fmt.Errorf("%w: the PDF has no pages", ErrBadDocument)
	}
	if len(files) > MaxPages {
		return fmt.Errorf("%w: more than %d pages to rasterise", ErrNoConversion, MaxPages)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return err
		}
		if err := fn(img); err != nil {
			return err
		}
	}
	return nil
}

func pageNo(path string) int {
	base := filepath.Base(path)
	i := len(base) - len(".png")
	j := i
	for j > 0 && base[j-1] >= '0' && base[j-1] <= '9' {
		j--
	}
	n, _ := strconv.Atoi(base[j:i])
	return n
}

// pdfToPS converts a PDF to PostScript, keeping it vector.
func (c Converter) pdfToPS(ctx context.Context, data []byte) ([]byte, error) {
	if c.PDFToPS == "" {
		return nil, ErrNoPDFTools
	}
	dir, err := run(ctx, c.PDFToPS, data, func(in, out string) []string {
		return []string{"-paper", "A4", in, filepath.Join(out, "out.ps")}
	})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	return os.ReadFile(filepath.Join(dir, "out.ps"))
}
