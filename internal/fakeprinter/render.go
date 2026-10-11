package fakeprinter

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/kongkx/cloud-print-service/internal/render"
)

// RenderOpts configures label previews.
type RenderOpts struct {
	// Backend selects the renderer: auto|zebrash|labelize (see render.Pick).
	Backend string
	// LabelizeURL enables the optional labelize microservice backend.
	LabelizeURL string
	// Dir writes each rendered label as <dir>/<id>_NNNN.png. Empty = memory only.
	Dir string
	// Options override label geometry (mm, dpmm, grayscale).
	Options render.Options
	// Timeout bounds one render; rendering never delays the print itself.
	Timeout time.Duration
}

// EnableRendering turns on ZPL -> image previews for received labels.
func (p *Printer) EnableRendering(ctx context.Context, o RenderOpts) error {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	r, err := render.Pick(o.Backend, o.LabelizeURL)
	if err != nil {
		return err
	}
	o.Options = o.Options.WithDefaults()
	if o.Dir != "" {
		if err := os.MkdirAll(o.Dir, 0o755); err != nil {
			return fmt.Errorf("render dir: %w", err)
		}
	}
	p.mu.Lock()
	p.renderer = r
	p.renderOn = true
	p.renderDir = o.Dir
	p.renderSize = o.Options
	if p.images == nil {
		p.images = map[int][]byte{}
	}
	p.mu.Unlock()
	return nil
}

// DisableRendering stops producing previews; existing ones stay browsable.
func (p *Printer) DisableRendering() {
	p.mu.Lock()
	p.renderOn = false
	p.mu.Unlock()
}

// RendererName reports the active backend ("none" when disabled).
func (p *Printer) RendererName() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.renderOn || p.renderer == nil {
		return "none"
	}
	return p.renderer.Name()
}

// Image returns the rendered bytes for a label index.
func (p *Printer) Image(index int) ([]byte, string, bool) { // data, contentType, ok
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.images[index]
	if !ok {
		return nil, "", false
	}
	return b, render.ContentTypeFor(p.renderSize.Format), true
}

// renderLabel produces the preview for a received label. It is called off the print
// path: a failure annotates the label, it never blocks or fails the print.
func (p *Printer) renderLabel(ctx context.Context, index int, zpl string) {
	p.mu.Lock()
	on, r, dir := p.renderOn, p.renderer, p.renderDir
	opts := p.renderSize
	p.mu.Unlock()
	if !on || r == nil {
		return
	}

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), renderTimeout)
	defer cancel()

	img, err := r.Render(rctx, zpl, opts)
	pv := &Preview{Format: opts.Format}
	if err != nil {
		pv.Error = err.Error()
	} else {
		pv.Available = true
		pv.Format = img.Format
		pv.Backend = img.Backend
		pv.WidthDots, pv.HeightDots = img.WidthDots, img.HeightDots
		pv.DurationMS = img.DurationMS
		pv.Bytes = len(img.Data)
		pv.Warnings = img.Warnings
		if dir != "" {
			name := filepath.Join(dir, fmt.Sprintf("%s_%04d.%s", p.renderName(), index, img.Format))
			if werr := os.WriteFile(name, img.Data, 0o644); werr == nil {
				pv.File = name
			}
		}
		p.mu.Lock()
		p.images[index] = img.Data
		p.mu.Unlock()
	}

	p.mu.Lock()
	for i := range p.labels {
		if p.labels[i].Index == index {
			p.labels[i].Image = pv
		}
	}
	p.mu.Unlock()
}

// renderTimeout bounds a single render so a wedged backend cannot pile up goroutines.
const renderTimeout = 8 * time.Second

// renderName is the filesystem-friendly stem for rendered filenames: the printer id
// when we have one (a bind address like "::" is useless as a name).
func (p *Printer) renderName() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name := sanitize(p.renderSize.Label); name != "" {
		return name
	}
	if p.ID != "" {
		return sanitize(p.ID)
	}
	h, _, err := net.SplitHostPort(p.addr)
	if err != nil || h == "" {
		return "printer"
	}
	return sanitize(h)
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
