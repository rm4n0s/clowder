package metrics

import (
	"bytes"
	"log/slog"
	"net/http"
)

// Path is the path the metrics are served at.
const Path = "/metrics"

// ContentType is the media type of the Prometheus text format, version 0.0.4.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler serves the registry at GET Path and nothing else: any other path is
// 404 and any other method 405. The answer has the content type of the text
// format and Cache-Control: no-store. It asks for no login: the listener it
// belongs to is for a private network (SPEC D60). A nil logger discards the
// log.
func (r *Registry) Handler(logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if req.URL.Path != Path {
			http.NotFound(w, req)
			return
		}
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		var b bytes.Buffer
		if _, err := r.WriteTo(&b); err != nil {
			logger.Error("writing the metrics failed", "err", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		if _, err := w.Write(b.Bytes()); err != nil {
			logger.Debug("sending the metrics failed", "err", err)
		}
	})
}
