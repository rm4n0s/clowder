package web

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// search answers GET /search: the search box and, for the query parameter "q",
// the results of the page "page" (SPEC §7.8). A query with a malformed date
// shows its message under the box: a Datastar request gets it as a patch
// with status 200, a plain request the page with status 422. A Datastar
// request gets only the panel of the results patched in place, and the URL of
// the browser set to the search, so that it can be shared.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	actor, _ := actorFrom(r.Context())
	q := r.URL.Query().Get("q")
	page := 1
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 1 {
		page = n
	}
	d := views.SearchData{Query: q, Page: page}
	status := http.StatusOK
	res, err := s.svc.Search.Query(r.Context(), actor, q, page)
	switch {
	case err == nil:
		d.Searched = !res.Parsed.IsEmpty()
		d.Chips = views.SearchChips(res.Parsed)
		d.Results, d.HasNext = res.Results, res.HasNext
	case isTag(err, app.TagInvalidInput):
		status = http.StatusUnprocessableEntity
		d.Message = formErrorsOf(err).Fields[app.FieldQuery]
		if d.Message == "" {
			d.Message = messageInvalid
		}
	default:
		s.fail(w, r, err)
		return
	}
	if !isDatastar(r) {
		sb, ok := s.sidebarFor(w, r, 0)
		if !ok {
			return
		}
		sb.HideSearch = true
		d.Sidebar = sb
		s.render(w, r, status, views.SearchPage(d))
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sse.PatchElementTempl(views.SearchPanel(d)); err != nil {
		appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements")
		s.logger.Debug("sse write failed", "err", appErr)
		return
	}
	u := url.URL{Path: "/search"}
	if path := views.SearchPath(q, page); path != "/search" {
		u.RawQuery = path[len("/search?"):]
	}
	if err := sse.ReplaceURL(u); err != nil {
		appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "replace-url")
		s.logger.Debug("sse write failed", "err", appErr)
	}
}
