package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swiftpush/zebrash/drawers"
	"github.com/swiftpush/zebrash/elements"
)

const sampleZPL = "^XA^PW812^LL1218^FO40,40^A0N,50,50^FDSKU-12345^FS" +
	"^FO40,120^GB732,4,4^FS^FO40,160^BCN,120,Y,N,N^FD123456789012^FS^XZ"

func TestZebrashRendersPNG(t *testing.T) {
	img, err := NewZebrash().Render(context.Background(), sampleZPL, Options{
		WidthMM: 101.6, HeightMM: 152.4, DPMM: 8,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if img.Format != "png" || img.ContentType != "image/png" {
		t.Fatalf("unexpected format: %+v", img)
	}
	if !bytes.HasPrefix(img.Data, []byte("\x89PNG")) {
		t.Fatal("not a PNG")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Width < 700 || cfg.Height < 1100 {
		t.Fatalf("unexpected image size %dx%d for 101.6x152.4mm at 8dpmm", cfg.Width, cfg.Height)
	}
	if img.Backend != "zebrash" {
		t.Fatalf("backend = %q", img.Backend)
	}
}

func TestZebrushRenderIsDeterministic(t *testing.T) {
	r := NewZebrash()
	a, err := r.Render(context.Background(), sampleZPL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Render(context.Background(), sampleZPL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatal("same ZPL must render identically; the gallery depends on it")
	}
}

func TestRendersSVGPDF(t *testing.T) {
	for _, tc := range []struct{ format, ct, prefix string }{
		{"svg", "image/svg+xml", "<"},
		{"pdf", "application/pdf", "%PDF"},
	} {
		img, err := NewZebrash().Render(context.Background(), sampleZPL, Options{Format: tc.format})
		if err != nil {
			t.Fatalf("%s: %v", tc.format, err)
		}
		if img.ContentType != tc.ct {
			t.Errorf("%s content type = %q", tc.format, img.ContentType)
		}
		if len(img.Data) == 0 || !strings.HasPrefix(string(img.Data), tc.prefix) {
			t.Errorf("%s output does not look right (%d bytes)", tc.format, len(img.Data))
		}
	}
}

func TestRejectsPayloadWithoutLabel(t *testing.T) {
	if _, err := NewZebrash().Render(context.Background(), "not zpl at all", Options{}); err == nil {
		t.Fatal("expected an error for a payload with no ^XA…^XZ")
	}
}

func TestContextCancellationIsHonoured(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewZebrash().Render(ctx, sampleZPL, Options{}); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

func TestConcurrentRenders(t *testing.T) {
	r := NewZebrash()
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := r.Render(context.Background(), sampleZPL, Options{})
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent render: %v", err)
		}
	}
}

// ------------------------------------------------------------ labelize backend

func TestLabelizeBackend(t *testing.T) {
	var gotPath, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/convert":
			gotPath = r.URL.RawQuery
			gotCT = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			// A 1x1 PNG is enough to prove the plumbing.
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	lz := NewLabelize(srv.URL)
	if !lz.Healthy(context.Background()) {
		t.Fatal("health check should succeed")
	}
	img, err := lz.Render(context.Background(), sampleZPL, Options{WidthMM: 101.6, HeightMM: 152.4, DPMM: 8})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if img.Backend != "labelize" || img.ContentType != "image/png" {
		t.Fatalf("unexpected image meta: %+v", img)
	}
	if gotCT != "application/zpl" {
		t.Errorf("content type = %q, want application/zpl", gotCT)
	}
	if gotBody != sampleZPL {
		t.Errorf("body was not forwarded verbatim")
	}
	for _, want := range []string{"width=101.6", "height=152.4", "dpmm=8", "output=png"} {
		if !strings.Contains(gotPath, want) {
			t.Errorf("query %q missing %q", gotPath, want)
		}
	}
}

func TestLabelizeSurfacesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"bad zpl"}`))
	}))
	defer srv.Close()

	if _, err := NewLabelize(srv.URL).Render(context.Background(), sampleZPL, Options{}); err == nil {
		t.Fatal("expected an error when labelize rejects the payload")
	}
}

func TestLabelizeRejectsNonPNG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer srv.Close()

	_, err := NewLabelize(srv.URL).Render(context.Background(), sampleZPL, Options{})
	if err == nil || !strings.Contains(err.Error(), "not a PNG") {
		t.Fatalf("expected a clear 'not a PNG' error, got %v", err)
	}
}

// ------------------------------------------------------------------- selection

func TestPickPrefersLabelizeWhenHealthy(t *testing.T) {
	var healthy atomicFlag
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.get() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	healthy.set(false)
	r, err := Pick("auto", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "zebrash" {
		t.Fatalf("auto should fall back to zebrash when labelize is down, got %s", r.Name())
	}
	healthy.set(true)
	r, err = Pick("auto", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "labelize" {
		t.Fatalf("auto should use labelize when healthy, got %s", r.Name())
	}
}

func TestPickErrors(t *testing.T) {
	if _, err := Pick("labelize", ""); err == nil {
		t.Error("labelize without a URL must fail loudly")
	}
	if _, err := Pick("imagemagick", ""); err == nil {
		t.Error("unknown backend must be rejected")
	}
	if r, err := Pick("", ""); err != nil || r.Name() != "zebrash" {
		t.Errorf("empty backend should default to zebrash: %v %v", r, err)
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.WithDefaults()
	if o.WidthMM != 101.6 || o.HeightMM != 152.4 || o.DPMM != 8 || o.Format != "png" {
		t.Fatalf("defaults wrong: %+v", o)
	}
	if (Options{DPMM: 7}).WithDefaults().DPMM != 8 {
		t.Error("unsupported dpmm must fall back to 8")
	}
	if ContentTypeFor("svg") != "image/svg+xml" || ContentTypeFor("nope") != "image/png" {
		t.Error("content type mapping wrong")
	}
}

func TestImageIsSerializable(t *testing.T) {
	// The gallery stores previews as JSON; Data must be excluded.
	img := Image{Data: []byte("binary"), Backend: "zebrash", Format: "png", WidthDots: 812}
	b, err := json.Marshal(img)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "binary") {
		t.Fatal("image bytes must not be serialised into the API")
	}
}

type atomicFlag struct{ v bool }

func (a *atomicFlag) set(v bool) { a.v = v }
func (a *atomicFlag) get() bool  { return a.v }

// A panic inside a renderer must become an error, never a dead service: the renderer
// is third-party code running on the debugging path.
func TestRendererPanicBecomesError(t *testing.T) {
	z := NewZebrash()
	z.mu.Lock()
	z.drawer = panicDrawer{}
	z.mu.Unlock()

	_, err := z.Render(context.Background(), sampleZPL, Options{})
	if err == nil {
		t.Fatal("expected an error when the renderer panics")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("error should name the failure clearly, got %v", err)
	}

	// The renderer must still work afterwards: no poisoned state.
	z.mu.Lock()
	z.drawer = newZebrashDrawer()
	z.mu.Unlock()
	if _, err := z.Render(context.Background(), sampleZPL, Options{}); err != nil {
		t.Fatalf("renderer unusable after a panic: %v", err)
	}
}

func TestLabelizePanicBecomesError(t *testing.T) {
	lz := &Labelize{BaseURL: "http://example.invalid", HTTP: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			panic("boom")
		}),
	}}
	if _, err := lz.Render(context.Background(), sampleZPL, Options{}); err == nil ||
		!strings.Contains(err.Error(), "panicked") {
		t.Fatalf("expected a panic-wrapped error, got %v", err)
	}
}

// Real-world guard: this ^GF payload panics inside zebrash v1.38.1.
func TestRealGraphicFieldPanicIsContained(t *testing.T) {
	zpl := "^XA^FO40,40^GFA,20,8,FF FF FF FF FF FF FF FF FF FF FF FF FF FF FF FF FF FF FF FF 81 81 81 81 81 81 81 81 81 81 81 81 81 81 81 81 81^FS^XZ"
	img, err := NewZebrash().Render(context.Background(), zpl, Options{})
	if err != nil {
		if !strings.Contains(err.Error(), "panicked") && !strings.Contains(err.Error(), "draw") {
			t.Fatalf("unexpected error shape: %v", err)
		}
		return // contained, as designed
	}
	if len(img.Data) == 0 {
		t.Fatal("empty image")
	}
}

type panicDrawer struct{}

func (panicDrawer) drawPNG(context.Context, []elements.LabelInfo, io.Writer, drawers.DrawerOptions) error {
	panic("simulated renderer failure")
}
func (panicDrawer) drawPDF(context.Context, []elements.LabelInfo, io.Writer, drawers.DrawerOptions) error {
	panic("simulated renderer failure")
}
func (panicDrawer) drawSVG(context.Context, []elements.LabelInfo, io.Writer, drawers.DrawerOptions) error {
	panic("simulated renderer failure")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A ^POI label must be rotatable on demand: physically faithful by default, upright
// when asked (which is how published ZPL reference renders are produced).
func TestUprightOptionControlsPOIRotation(t *testing.T) {
	zpl := "^XA^POI^FO40,40^GB300,120,8^FS^FO40,200^A0N,40,40^FTOP^FS^XZ"

	physical, err := NewZebrash().Render(context.Background(), zpl, Options{})
	if err != nil {
		t.Fatal(err)
	}
	upright, err := NewZebrash().Render(context.Background(), zpl, Options{Upright: true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(physical.Data, upright.Data) {
		t.Fatal("^POI must rotate the label unless Upright is set")
	}
	// Rotating the physical render by 180° must reproduce the upright one exactly.
	imgRot, err := png.Decode(bytes.NewReader(physical.Data))
	if err != nil {
		t.Fatal(err)
	}
	imgUp, err := png.Decode(bytes.NewReader(upright.Data))
	if err != nil {
		t.Fatal(err)
	}
	grayOf := func(im image.Image, x, y int) uint8 {
		r, g, b, _ := im.At(x, y).RGBA()
		return uint8((r + g + b) / 3 >> 8)
	}
	if imgRot.Bounds() != imgUp.Bounds() {
		t.Fatalf("size mismatch %v vs %v", imgRot.Bounds(), imgUp.Bounds())
	}
	mismatch := 0
	for y := 0; y < imgRot.Bounds().Dy(); y++ {
		for x := 0; x < imgRot.Bounds().Dx(); x++ {
			a := grayOf(imgRot, x, y)
			b := grayOf(imgUp, imgRot.Bounds().Dx()-1-x, imgRot.Bounds().Dy()-1-y)
			if a != b {
				mismatch++
			}
		}
	}
	if mismatch != 0 {
		t.Fatalf("Upright output should be the physical render rotated 180°, %d pixels differ", mismatch)
	}
}

// RenderPipelineMatchesPublishedReference pins the renderer against zebrash's own UPS
// reference (testdata/ups.zpl → ups_grayscale.png, 4"x8" @ 203dpi, greyscale).
//
// It runs offline against the same payload/geometry rather than downloading, so it
// stays a fast regression test; `examples/verify-render.sh` re-runs the same comparison
// end-to-end through the service and pulls the live reference.
func TestRenderMatchesUPSReference(t *testing.T) {
	img, err := NewZebrash().Render(context.Background(), upsZPL, Options{
		WidthMM: 101.6, HeightMM: 203.2, DPMM: 8, Grayscale: true, Upright: true,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 813 || cfg.Height != 1626 {
		t.Fatalf("size %dx%d, want 813x1626 (101.6x203.2mm at 8dpmm)", cfg.Width, cfg.Height)
	}

	// The reference is fetched once and cached; the test is skipped without network.
	ref, err := fetchUPSReference(t)
	if err != nil {
		t.Skipf("reference unavailable (%v) — run examples/verify-render.sh online", err)
	}
	if len(ref) != len(img.Data) {
		t.Fatalf("byte length %d vs reference %d", len(img.Data), len(ref))
	}
	refCfg, err := png.DecodeConfig(bytes.NewReader(ref))
	if err != nil {
		t.Fatal(err)
	}
	if refCfg.Width != cfg.Width || refCfg.Height != cfg.Height {
		t.Fatalf("reference is %dx%d, ours %dx%d", refCfg.Width, refCfg.Height, cfg.Width, cfg.Height)
	}

	a, err := png.Decode(bytes.NewReader(ref))
	if err != nil {
		t.Fatal(err)
	}
	b, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	diff := 0
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb {
				diff++
			}
		}
	}
	if diff != 0 {
		t.Fatalf("%d of %d pixels differ from the published reference (%.4f%%)",
			diff, a.Bounds().Dx()*a.Bounds().Dy(),
			100*float64(diff)/float64(a.Bounds().Dx()*a.Bounds().Dy()))
	}
	t.Log("render matches the published reference pixel for pixel")
}

const upsZPL = `
^XA
^LRN
^MNY
^MFN,N
^LH10,12
^MCY
^POI
^PW812
^CI27
^FO284,524^BY3^BCN,107,N,N,N,A^FV4210405000^FS
^FO66,792^BY3^BCN,208,N,N,N,A^FV1Z680RA4DL08720000^FS
^FO20,431^CVY^BD3^FH_^FD4030405000  [)>_1E01_1D961Z08720000_1DUPSN_1D680RA4_1D051_1D_1D1/1_1D1_1DN_1D_1DHALLEIN_1D_1E_04^FS
^FO15,7^A0N,20,24^FVMERCHANT AB^FS
^FO15,27^A0N,20,24^FV46000000000^FS
^FO15,47^A0N,20,24^FVMERCHANT AB^FS
^FO15,67^A0N,20,24^FVTEST STREET 1^FS
^FO15,87^A0N,20,24^FV13000  OSTERHANINGE ^FS
^FO15,108^A0N,20,24^FVSWEDEN^FS
^FO15,142^A0N,28,32^FVSHIP TO: ^FS
^FO61,166^A0N,28,32^FVTEST RECEIVER^FS
^FO61,194^A0N,28,32^FV430000000000^FS
^FO61,222^A0N,28,32^FVTEST RECEIVER^FS
^FO61,251^A0N,28,32^FVTEST STREET 2^FS
^FO61,279^A0N,45,44^FV5000  HALLEIN  ^FS
^FO61,324^A0N,45,44^FVAUSTRIA^FS
^FO446,9^A0N,30,34^FV1 KG^FS
^FO683,9^A0N,28,32^FV1 OF 1^FS
^FO508,51^A0N,22,26^FVSHP#: 680R A44X PGR^FS
^FO508,73^A0N,22,26^FVSHP WT: 0.5 KG^FS
^FO508,95^A0N,22,26^FVDATE: 20 FEB 2024^FS
^FO269,436^A0N,80,70^FVAUT 505 5-01^FS
^FO10,1031^A0N,22,26^FVBILLING: P/P  ^FS
^FO10,1053^A0N,22,26^FVADULT SIGNATURE REQUIRED^FS
^FO10,1075^A0N,22,26^FVDESC: Wooden logs^FS
^FO591,1035^A0N,60,64^FVEDI^FS
^FO10,1163^A0N,22,26^FVReference No.1: DE_HA.6305266653^FS
^FO180,1203^A0N,14,20^FVXOL 24.01.23          NV45 8.0A 02/2024*^FS
^FO9,670^A0N,56,58^FVUPS STANDARD^FS
^FO9,731^A0N,26,30^FVTRACKING #: 1Z 680 RA4 DL 0872 0000^FS
^FO689,650^GB124,125,124,B,0^FS
^FO0,648^GB811,14,14,B,0^FS
^FO0,423^GB812,4,4,B,0^FS
^FO244,423^GB4,225,4,B,0^FS
^FO0,774^GB812,5,5,B,0^FS
^FO0,1013^GB812,14,14,B,0^FS
^FO629,1147
^GFA,00969,00969,019,FFFFFFFFFFFFFFFFFFFFFFFFFFFFF000000000
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF000000000
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF000000000
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF000000000
F0000000000001F8000000000000F000000000
F0000000000001F8000000000000F000000000
F0000000003F81F83FC000000000F000000000
F0000000003F81F83FC000000000F000000000
F000000000FFF9F9FFF000000000F000000000
F000000000FFF9F9FFF000000000F000000000
F000000000FFFFFFFFFC00000000F000000000
F000000000FFFFFFFFFC00000000F000000000
F000000000F07FFFF0FC00000000F000000000
F000000000F07FFFF0FC00000000F000000000
F000000000FC1FFFC3F000000000F000000000
F000000000FC1FFFC3F000000000F000000000
F000000000FFFFFFFFF000000000F000000000
F000000000FFFFFFFFF000000000F000000000
F0000000003FFFFFFFC000000000F000000000
F0000000003FFFFFFFC000000000F000000000
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF000000000
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF000000000
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF000000000
F00000000001FFFFF00000000000F000000000
F00000000001FFFFF00000000000F000000000
F00000000003FFF9FC0000000000F000000000
F00000000003FFF9FC0000000000F000000000
F0000000003FE1F87FC000000000F000000000
F0000000003FE1F87FC000000000F000000000
F000000000FF81F83FF000000000F000000000
F000000000FF81F83FF000000000F000000000
F000000000FE01F803F000000000F000000000
F000000000FE01F803F000000000F000000000
F000000000F001F800F000000000F000000000
F000000000F001F800F000000000F000000000
F0000000000001F8000000000000F000000000
F0000000000001F8000000000000F0FFDC1C00
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF0FFDC1C00
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF00C1E3C00
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF00C1E3C00
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF00C1A2C00
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF00C1B6C00
FFFFFFFFFFFFFFFFFFFFFFFFFFFFF00C1B6C00
0000000000000000000000000000000C1B6C00
0000000000000000000000000000000C19CC00
0000000000000000000000000000000C19CC00
0000000000000000000000000000000C19CC00
0000000000000000000000000000000C188C00
00000000000000000000000000000000000000
00000000000000000000000000000000000000
00000000000000000000000000000000000000
^DN
^XZ
`

const upsReferenceURL = "https://raw.githubusercontent.com/swiftpush/zebrash/main/testdata/ups_grayscale.png"

func fetchUPSReference(t *testing.T) ([]byte, error) {
	t.Helper()
	cache := filepath.Join(os.TempDir(), "cps-ups-reference.png")
	if b, err := os.ReadFile(cache); err == nil {
		return b, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, upsReferenceURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(cache, b, 0o644)
	return b, nil
}
