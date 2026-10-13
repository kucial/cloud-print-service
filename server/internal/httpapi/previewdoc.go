package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/printer"
	"github.com/kucial/cloud-print-service/server/internal/convert"
)

// previewDocument shows how a PDF or image would come out of one printer: the dots a
// receipt or label printer gets after conversion, first page only, as a PNG. Nothing
// is printed. The body is the document; its format comes from ?format= or the
// Content-Type.
func (s *Server) previewDocument(w http.ResponseWriter, r *http.Request) {
	p, ok := s.mgr.Get(r.PathValue("printer_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "printer_not_found", "unknown printer "+r.PathValue("printer_id"))
		return
	}
	name := r.URL.Query().Get("format")
	if name == "" {
		name, _, _ = strings.Cut(r.Header.Get("Content-Type"), ";")
	}
	src, ok := printer.ParseFormat(name)
	if !ok {
		writeErr(w, http.StatusUnsupportedMediaType, "invalid_request",
			"send a PDF, PNG or JPEG (Content-Type, or ?format=)")
		return
	}
	limit := s.maxPayload
	if limit <= 0 {
		limit = 64 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "cannot read body: "+err.Error())
		return
	}
	if int64(len(body)) > limit {
		writeErr(w, http.StatusUnprocessableEntity, "payload_too_large", fmt.Sprintf("payload exceeds %d bytes", limit))
		return
	}
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	img, pages, err := s.jobs.Converter().Preview(ctx, body, src, p.Spec())
	switch {
	case errors.Is(err, convert.ErrNoPDFTools):
		writeErrReason(w, http.StatusUnprocessableEntity, "invalid_request", "pdf_conversion_unavailable", err.Error())
		return
	case errors.Is(err, convert.ErrBadDocument):
		writeErrReason(w, http.StatusUnprocessableEntity, "invalid_request", "document_unreadable", err.Error())
		return
	case err != nil:
		writeErrReason(w, http.StatusUnprocessableEntity, "invalid_request", "format_not_supported", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Document-Pages", strconv.Itoa(pages))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(img)
}
