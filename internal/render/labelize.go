package render

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Labelize renders through a self-hosted labelize instance
// (https://github.com/GOODBOY008/labelize).
//
// Use it when you want the widest command coverage and Labelary-calibrated output;
// the default in-process backend needs no container.
type Labelize struct {
	BaseURL string
	HTTP    *http.Client
}

func NewLabelize(baseURL string) *Labelize {
	return &Labelize{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (l *Labelize) Name() string { return "labelize" }

// Healthy reports whether the service answers /health (used by Pick for auto mode).
func (l *Labelize) Healthy(ctx context.Context) bool {
	if l.BaseURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.BaseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// Render posts the ZPL to /convert and returns the produced image.
func (l *Labelize) Render(ctx context.Context, zpl string, opts Options) (img Image, err error) {
	defer guard(l.Name(), &err)
	opts = opts.withDefaults()
	if l.BaseURL == "" {
		return Image{}, fmt.Errorf("labelize: no base url configured")
	}
	q := url.Values{}
	q.Set("width", strconv.FormatFloat(opts.WidthMM, 'f', -1, 64))
	q.Set("height", strconv.FormatFloat(opts.HeightMM, 'f', -1, 64))
	q.Set("dpmm", strconv.Itoa(opts.DPMM))
	q.Set("output", opts.Format)
	if opts.Grayscale {
		q.Set("antialias", "true")
	}

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.BaseURL+"/convert?"+q.Encode(), bytes.NewReader([]byte(zpl)))
	if err != nil {
		return Image{}, err
	}
	req.Header.Set("Content-Type", "application/zpl")

	resp, err := l.HTTP.Do(req)
	if err != nil {
		return Image{}, fmt.Errorf("labelize render failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Image{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Image{}, fmt.Errorf("labelize returned %d: %s",
			resp.StatusCode, truncate(string(body), 200))
	}

	res := Image{
		Data:        body,
		ContentType: ContentTypeFor(opts.Format),
		Format:      opts.Format,
		Backend:     l.Name(),
		Source:      zpl,
		DurationMS:  time.Since(start).Milliseconds(),
		RenderedAt:  time.Now().UTC(),
	}
	if opts.Format == "png" && !bytes.HasPrefix(body, []byte("\x89PNG")) {
		return Image{}, fmt.Errorf("labelize returned %s, not a PNG", http.DetectContentType(body))
	}
	res.WidthDots = int(opts.WidthMM * float64(opts.DPMM))
	res.HeightDots = int(opts.HeightMM * float64(opts.DPMM))
	return res, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
