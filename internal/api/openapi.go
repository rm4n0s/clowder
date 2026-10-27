package api

import (
	_ "embed"
	"net/http"

	"github.com/rm4n0s/errors"
)

// openAPIDocument is openapi.json, the OpenAPI 3.1 description of the routes
// (SPEC D45). TestOpenAPIMatchesRoutes keeps it equal to them.
//
//go:embed openapi.json
var openAPIDocument []byte

// OpenAPI returns the OpenAPI document of the API as JSON. The caller must
// not change it.
func OpenAPI() []byte {
	return openAPIDocument
}

// documentRoutes are the routes of the document itself.
func (s *Server) documentRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/openapi.json", true, s.openAPI),
	}
}

// openAPI answers GET /openapi.json with the document. It needs no login.
func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(openAPIDocument); err != nil {
		appErr := errors.NewErr(tagWriteFailed, err).SetMetadata("status", http.StatusOK)
		s.logger.Debug("writing the document failed", "pattern", r.Pattern, "err", appErr)
	}
}
