package web

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/starfederation/datastar-go/datastar"
)

// pageCSP is the Content-Security-Policy of every answer except a download
// (which has downloadCSP). Scripts come only from this site, and none is
// written in a page: the only thing that evaluates text is Datastar, which
// builds a function from each data-* expression and so needs 'unsafe-eval'.
// A redirect or a change of the address is therefore not an inline script but
// an element with a data-init expression (see runInBrowser). Styles come from
// this site only: no markup of Clowder has a style attribute. Images may also
// come from other sites over https and http, because a markdown body may
// embed one (SPEC §7.3); everything else, such as frames, plug-ins, forms to
// other sites, a <base> element and framing of Clowder itself, is refused.
const pageCSP = "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self'; " +
	"img-src 'self' https: http:; connect-src 'self'; font-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// hstsValue is the Strict-Transport-Security of a server whose base URL is
// https: a year, for this host only.
const hstsValue = "max-age=31536000"

// secureHeaders sets the security headers of every answer before the handler
// runs, so that the error pages, the redirects and the streams have them too.
// A handler that needs other values for one of them (the static files and the
// downloads set their own Cache-Control, a download its own policy) sets it
// again.
//
//   - Content-Security-Policy: pageCSP
//   - X-Content-Type-Options: nosniff
//   - Referrer-Policy: same-origin, so that an address (an invitation link)
//     never leaves the site in a Referer
//   - X-Frame-Options: DENY, for the browsers that ignore frame-ancestors
//   - Cache-Control: no-store, so that no page of a logged-in person stays in
//     a cache
//   - Cross-Origin-Opener-Policy and Cross-Origin-Resource-Policy:
//     same-origin
//   - Strict-Transport-Security, when the base URL is https
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	hsts := s.cfg.SecureCookies()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", pageCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		w = cacheGuard{w}
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if hsts {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		next.ServeHTTP(w, r)
	})
}

// cacheGuard keeps an event stream out of caches. The SDK of Datastar sets
// "Cache-Control: no-cache" on every event stream, which lets a cache keep it
// and ask again; the guard turns that into no-store when the headers go out
// (the SDK flushes them first thing).
type cacheGuard struct {
	http.ResponseWriter
}

// Unwrap returns the underlying writer, for http.ResponseController.
func (g cacheGuard) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

// WriteHeader fixes the cache header, then sends the status.
func (g cacheGuard) WriteHeader(code int) {
	g.fixCache()
	g.ResponseWriter.WriteHeader(code)
}

// FlushError fixes the cache header, then flushes; it is what
// http.ResponseController.Flush calls.
func (g cacheGuard) FlushError() error {
	g.fixCache()
	return http.NewResponseController(g.ResponseWriter).Flush()
}

// fixCache replaces the no-cache of an event stream by no-store.
func (g cacheGuard) fixCache() {
	h := g.Header()
	if h.Get("Content-Type") == "text/event-stream" && h.Get("Cache-Control") == "no-cache" {
		h.Set("Cache-Control", "no-store")
	}
}

// runInBrowser makes the client run the JavaScript js once. It appends a
// hidden element with a data-init expression to the body, which Datastar
// evaluates when the element enters the page. It is what the SDK's
// ExecuteScript does with a <script> element, minus the inline script, which
// the Content-Security-Policy does not allow. js is escaped for the
// attribute here; build it with jsString for every value it holds.
func runInBrowser(sse *datastar.ServerSentEventGenerator, js string) error {
	return sse.PatchElements(`<div hidden data-init="`+html.EscapeString(js)+`"></div>`,
		datastar.WithSelector("body"), datastar.WithModeAppend())
}

// redirectJS is the JavaScript that sends the browser to target.
func redirectJS(target string) string {
	return "setTimeout(() => window.location.href = " + jsString(target) + ")"
}

// replaceURLJS is the JavaScript that changes the address in the address bar
// to u without loading it.
func replaceURLJS(u url.URL) string {
	return "window.history.replaceState({}, \"\", " + jsString(u.String()) + ")"
}

// jsString returns s as a double-quoted JavaScript string literal that is safe
// in the data-init attribute of a patch: besides the escapes of a Go quoted
// string, "<" and "@" are written as \u escapes. "<" so that "</script>" or
// "<!--" in a value is only text, and "@" so that Datastar, which rewrites
// every "@name(" of an expression to a call of its own actions, even inside a
// string, finds none.
func jsString(s string) string {
	return strings.NewReplacer("<", "\\u003c", "@", "\\u0040").Replace(strconv.Quote(s))
}

// sseRedirect sends the browser to target through sse.
func sseRedirect(sse *datastar.ServerSentEventGenerator, target string) error {
	return runInBrowser(sse, redirectJS(target))
}

// sseReplaceURL changes the address in the address bar through sse.
func sseReplaceURL(sse *datastar.ServerSentEventGenerator, u url.URL) error {
	return runInBrowser(sse, replaceURLJS(u))
}

// hasUnsafeNextRune reports whether next has a character that no path of this
// site needs and that is dangerous where the path ends up: markup (< > " '
// and the backtick), a space, and the characters that a URL parser rewrites
// or a template reads ({ } | ^).
func hasUnsafeNextRune(next string) bool {
	return strings.ContainsAny(next, "<>\"'` {}|^")
}

// bodyDeadline limits how long a body may take to arrive: formTimeout for a
// form, uploadTimeout for a multipart body. The deadline is lifted when the
// body has been read to its end, so it cannot cut a request that is being
// answered. A request without a body, and the streams (GET), have no
// deadline here.
func (s *Server) bodyDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Body != nil && r.Body != http.NoBody {
				d := s.formTimeout
				if isMultipart(r) {
					d = s.uploadTimeout
				}
				rc := http.NewResponseController(w)
				if d > 0 && rc.SetReadDeadline(time.Now().Add(d)) == nil {
					r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// deadlineBody is a request body that lifts the read deadline of the
// connection when it ends.
type deadlineBody struct {
	io.ReadCloser
	rc     *http.ResponseController
	lifted bool
}

// Read reads from the body; at its end it lifts the deadline. An error leaves
// it: when the time is up the server must not wait for the rest of the body
// that it discards before it closes the connection.
func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF && !b.lifted {
		b.lifted = true
		// Best effort: a connection that cannot lift it is being closed.
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

// arm gives the next writes to the stream writeTimeout to finish. A client
// that stops reading makes the write fail when the time is up, and the stream
// ends, instead of the handler waiting for it for ever. It is called before
// each thing the stream writes.
func (ls *liveStream) arm() {
	if ls.s.writeTimeout <= 0 {
		return
	}
	// A writer that has no deadlines (a test recorder) is not an error.
	_ = http.NewResponseController(ls.w).SetWriteDeadline(time.Now().Add(ls.s.writeTimeout))
}
