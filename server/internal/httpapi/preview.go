package httpapi

import (
	"net/http"
	"strings"

	"github.com/kucial/cloud-print-service/pkg/render"
	"github.com/kucial/cloud-print-service/server/internal/domain"
)

// SetRenderer wires the ZPL preview engine used by POST /api/v1/admin/preview.
//
// The renderer is optional: without it the preview endpoint answers 501 and every other
// endpoint is unaffected. Keeping it a setter means the printer/queue core does not
// depend on a rendering engine being present in the binary.
func (s *Server) SetRenderer(r render.Renderer) { s.renderer = r }

// previewZPL renders arbitrary ZPL so an operator can check a template before sending it
// to a real printer. Admin only, and it never touches a printer.
func (s *Server) previewZPL(w http.ResponseWriter, r *http.Request) {
	if s.renderer == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured",
			"no render engine configured; set render.backend in config")
		return
	}
	var body struct {
		ZPL            string  `json:"zpl"`
		WidthMM        float64 `json:"width_mm"`
		HeightMM       float64 `json:"height_mm"`
		PaperSize      string  `json:"paper_size"`
		DPI            int     `json:"dpi"`
		DPMM           int     `json:"dpmm"`
		Orientation    string  `json:"orientation"`
		PrintDirection string  `json:"print_direction"`
		Grayscale      bool    `json:"grayscale"`
		Upright        bool    `json:"upright"`
		Format         string  `json:"format"` // png (default) | svg | pdf
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if len(body.ZPL) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", "zpl is required")
		return
	}
	if int64(len(body.ZPL)) > s.maxPayload {
		writeErr(w, http.StatusUnprocessableEntity, "payload_too_large", "zpl payload too large")
		return
	}
	if body.PaperSize != "" && body.WidthMM == 0 && body.HeightMM == 0 {
		wMM, hMM, _, ok := domain.ParsePaperSize(body.PaperSize)
		if ok {
			body.WidthMM = wMM
			body.HeightMM = hMM
		}
	}
	if body.DPMM == 0 && body.DPI > 0 {
		switch body.DPI {
		case 203:
			body.DPMM = 8
		case 300:
			body.DPMM = 12
		case 600:
			body.DPMM = 24
		default:
			body.DPMM = int(float64(body.DPI)/25.4 + 0.5)
		}
	}
	if !body.Upright && (body.Orientation == "normal" || body.Orientation == "portrait" || body.PrintDirection == "top_first") {
		body.Upright = true
	}
	opts := render.Options{
		WidthMM: body.WidthMM, HeightMM: body.HeightMM, DPMM: body.DPMM,
		Grayscale: body.Grayscale, Upright: body.Upright, Format: body.Format,
		Label: "preview",
	}
	img, err := s.renderer.Render(r.Context(), body.ZPL, opts)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "render_failed", err.Error())
		return
	}
	// The body is the image itself, so render metadata travels in headers and the panel
	// can show it without a JSON envelope.
	w.Header().Set("Content-Type", img.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Render-Backend", img.Backend)
	w.Header().Set("X-Render-Format", img.Format)
	w.Header().Set("X-Render-Width-Dots", itoa(img.WidthDots))
	w.Header().Set("X-Render-Height-Dots", itoa(img.HeightDots))
	w.Header().Set("X-Render-Duration-Ms", itoa(int(img.DurationMS)))
	if len(img.Warnings) > 0 {
		w.Header().Set("X-Render-Warnings", strings.Join(img.Warnings, "; "))
	}
	w.Header().Set("Content-Length", itoa(len(img.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Data)
}
