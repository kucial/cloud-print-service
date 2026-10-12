package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// logRequests emits one structured line per request (no business payload data).
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	if log == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		lvl := slog.LevelInfo
		if rec.status >= 500 {
			lvl = slog.LevelError
		} else if rec.status >= 400 {
			lvl = slog.LevelWarn
		}
		log.Log(r.Context(), lvl, "http",
			"method", r.Method,
			"route", routeLabelOf(r),
			"status", rec.status,
			"ms", time.Since(start).Milliseconds(),
		)
	})
}

func routeLabelOf(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}
