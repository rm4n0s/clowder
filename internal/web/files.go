package web

import (
	"io"
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
)

const (
	// downloadCSP is the Content-Security-Policy of a download (SPEC §7.5,
	// §17); the headers are set by files.ServeDownload, which the API shares.
	downloadCSP = files.DownloadCSP
	// downloadCacheControl lets the browser of the reader keep a file for a
	// day; shared caches never see it.
	downloadCacheControl = files.DownloadCacheControl
)

// fileDownload answers GET /files/{id}/{name} with the file of an attachment,
// for a person who may read the room of its post; anyone else, a missing
// attachment and one of a deleted post get the 404 page. The name in the path
// is only there so that a saved file gets a good name: the ID decides.
//
// Only the image types of SPEC §7.5 are served inline, with their own type;
// everything else is application/octet-stream with Content-Disposition
// attachment. All answers carry X-Content-Type-Options: nosniff and a strict
// Content-Security-Policy. http.ServeContent handles ranges and
// If-Modified-Since (the time the file was attached).
func (s *Server) fileDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 63)
	if err != nil || id == 0 {
		s.notFound(w, r)
		return
	}
	actor, _ := actorFrom(r.Context())
	att, f, err := s.svc.Posts.Attachment(r.Context(), actor, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.serveFile(w, r, att, f)
}

// messageFileDownload answers GET /messages/files/{id}/{name} with the file of
// an attachment of a message, for a member of its conversation; anyone else,
// admins included, a missing attachment and one of a deleted message get the
// 404 page. The headers are those of fileDownload.
func (s *Server) messageFileDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 63)
	if err != nil || id == 0 {
		s.notFound(w, r)
		return
	}
	actor, _ := actorFrom(r.Context())
	att, f, err := s.svc.Messages.Attachment(r.Context(), actor, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.serveFile(w, r, att, f)
}

// serveFile sends the file of an attachment with the headers of a download
// (see fileDownload) and closes it.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, att app.AttachmentView, f io.ReadSeekCloser) {
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Debug("closing a download failed", "err", closeErr)
		}
	}()
	files.ServeDownload(w, r, files.Download{
		FileName:    att.FileName,
		ContentType: att.ContentType,
		Inline:      att.IsImage,
		ModTime:     att.CreatedAt,
	}, f)
}
