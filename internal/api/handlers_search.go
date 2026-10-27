package api

import (
	"net/http"
	"strconv"

	"github.com/rm4n0s/errors"
)

// messagePageInvalid is the problem with the query "page".
const messagePageInvalid = "Use a page number from 1."

// notificationsReadRequest is the body of POST /notifications/read.
type notificationsReadRequest struct {
	UpToID *uint64 `json:"up_to_id"`
}

// searchRoutes are the routes of the search.
func (s *Server) searchRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/search", false, s.search),
	}
}

// notificationRoutes are the routes of the notifications.
func (s *Server) notificationRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/notifications", false, s.notifications),
		s.route(http.MethodPost, "/notifications/read", false, s.readNotifications),
	}
}

// search answers GET /search?q=&page=: the posts and comments of the rooms the
// caller may read that the query finds, 20 a page, with the matching text as
// segments. The query language is that of SPEC §7.8; a mistake in it is 422
// under "q". A query with neither words nor filters finds nothing, and so does
// a page after the 1000th.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	page, err := searchPage(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	found, err := s.svc.Search.Query(r.Context(), c.actor, r.URL.Query().Get("q"), page)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := searchResponse{Results: make([]searchResultJSON, len(found.Results)), Page: found.Page, HasMore: found.HasNext}
	for i, res := range found.Results {
		out.Results[i] = searchResultOf(res)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// notifications answers GET /notifications?before=&limit=: a page of the
// caller's notifications, newest first, 30 by default and at most 100. Those
// of a room the caller can no longer read are left out (SPEC D30).
// next_before is the before of the next page, null on the last.
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	before, limit, err := feedParams(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.svc.Notifications.List(r.Context(), c.actor, before, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := notificationsResponse{Notifications: make([]notificationJSON, len(page.Notifications))}
	for i, n := range page.Notifications {
		out.Notifications[i] = notificationOf(n)
	}
	if page.HasNext {
		next := page.NextBefore
		out.NextBefore = &next
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// readNotifications answers POST /notifications/read: the caller's
// notifications up to the ID are marked read; newer ones stay unread.
func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body notificationsReadRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	upTo, err := upToID(body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Notifications.MarkRead(r.Context(), c.actor, upTo); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// searchPage reads the query "page": 1 when it is not there, else a whole
// number from 1; anything else fails with tagPageInvalid.
func searchPage(r *http.Request) (int, error) {
	query := r.URL.Query()
	if !query.Has("page") {
		return 1, nil
	}
	n, err := strconv.Atoi(query.Get("page"))
	if err != nil || n < 1 {
		return 0, errors.New(tagPageInvalid, "page is not a number from 1",
			"fields", map[string]string{"page": messagePageInvalid})
	}
	return n, nil
}

// upToID returns the ID of a request to mark notifications read; it fails with
// tagFieldMissing when the body has none or has 0.
func upToID(body notificationsReadRequest) (uint64, error) {
	if body.UpToID == nil || *body.UpToID == 0 {
		return 0, errors.New(tagFieldMissing, "up_to_id is missing",
			"fields", map[string]string{"up_to_id": messageFieldMissing})
	}
	return *body.UpToID, nil
}

const tagPageInvalid = "PageInvalid"
