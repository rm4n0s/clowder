package web

import (
	"net/http"
	"strconv"
	"strings"
)

const (
	// downloadCSP is the Content-Security-Policy of a download: nothing may
	// load or run if the file is opened as a page (SPEC §7.5, §17), and the
	// sandbox turns off scripts, forms and the origin of the page.
	downloadCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox"
	// downloadCacheControl lets the browser of the reader keep a file for a
	// day; shared caches never see it.
	downloadCacheControl = "private, max-age=86400"
	// octetStream is the type of every download that is not an inline image.
	octetStream = "application/octet-stream"
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
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
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
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Debug("closing a download failed", "err", closeErr)
		}
	}()

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", downloadCSP)
	h.Set("Cache-Control", downloadCacheControl)
	disposition := "attachment"
	contentType := octetStream
	if att.IsImage {
		disposition = "inline"
		contentType = att.ContentType
	}
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", contentDisposition(disposition, att.FileName))
	http.ServeContent(w, r, "", att.CreatedAt, f)
}

// contentDisposition makes the header Content-Disposition for a file name
// (RFC 6266): the plain filename with the characters that need care replaced,
// and for a name that has more than that, the exact name as filename* (RFC
// 5987, UTF-8, percent-encoded).
func contentDisposition(disposition, name string) string {
	var plain strings.Builder
	exact := true
	for _, r := range name {
		switch {
		case r == '"' || r == '\\' || r == '%' || r == ';':
			plain.WriteByte('_')
			exact = false
		case r < ' ' || r > '~':
			plain.WriteByte('_')
			exact = false
		default:
			plain.WriteRune(r)
		}
	}
	header := disposition + `; filename="` + plain.String() + `"`
	if !exact {
		header += "; filename*=UTF-8''" + percentEncode(name)
	}
	return header
}

// percentEncode encodes s as the value of an RFC 5987 extended parameter: the
// attr-char characters stay, every other byte is %XX.
func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// isAttrChar reports whether the byte is an attr-char of RFC 5987.
func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
