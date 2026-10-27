package files

import (
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DownloadCSP is the Content-Security-Policy of a download: nothing may
	// load or run if the file is opened as a page (SPEC §7.5, §17), and the
	// sandbox turns off scripts, forms and the origin of the page.
	DownloadCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox"
	// DownloadCacheControl lets the client of the reader keep a file for a day;
	// shared caches never see it (SPEC D36).
	DownloadCacheControl = "private, max-age=86400"
	// OctetStream is the type of every download that is not an inline image.
	OctetStream = "application/octet-stream"
)

// Download describes the file that ServeDownload sends.
type Download struct {
	// FileName is the name the file had when it was uploaded; the client
	// saves the file under it.
	FileName string
	// ContentType is the type found by sniffing the content. It is used only
	// when Inline is true.
	ContentType string
	// Inline is true for the image types that SPEC §7.5 shows inline (PNG,
	// JPEG, GIF, WebP). Every other file is sent as an attachment of the type
	// application/octet-stream.
	Inline bool
	// ModTime is when the file was attached, for If-Modified-Since.
	ModTime time.Time
}

// ServeDownload sends the file with the headers of a download (SPEC §7.5,
// D36): X-Content-Type-Options: nosniff, the sandbox policy DownloadCSP,
// DownloadCacheControl, Content-Disposition with the name of the file, and
// the type of the file only for an inline image (application/octet-stream
// otherwise). http.ServeContent handles ranges and If-Modified-Since. The
// caller closes f.
func ServeDownload(w http.ResponseWriter, r *http.Request, d Download, f io.ReadSeeker) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", DownloadCSP)
	h.Set("Cache-Control", DownloadCacheControl)
	disposition := "attachment"
	contentType := OctetStream
	if d.Inline {
		disposition = "inline"
		contentType = d.ContentType
	}
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", ContentDisposition(disposition, d.FileName))
	http.ServeContent(w, r, "", d.ModTime, f)
}

// ContentDisposition makes the header Content-Disposition for a file name
// (RFC 6266): the plain filename with the characters that need care replaced,
// and for a name that has more than that, the exact name as filename* (RFC
// 5987, UTF-8, percent-encoded).
func ContentDisposition(disposition, name string) string {
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
