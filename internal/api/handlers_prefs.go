package api

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// messagePrefsEmpty is the problem with a change of the notification
// preferences of a place that changes nothing.
const messagePrefsEmpty = "Send at least one preference."

// placePrefsJSON is the notification preferences of the user in a room or
// conversation (SPEC §7.15): the answer of the routes and the data of a
// place_prefs.changed event.
type placePrefsJSON struct {
	Notify              string `json:"notify"`
	Sound               string `json:"sound"`
	IgnoreGroupMentions bool   `json:"ignore_group_mentions"`
	MarkUnread          string `json:"mark_unread"`
	Muted               bool   `json:"muted"`
}

// placePrefsOf makes the JSON of the preferences of a place.
func placePrefsOf(p app.PlacePrefs) placePrefsJSON {
	return placePrefsJSON{
		Notify: string(p.Notify), Sound: string(p.Sound), IgnoreGroupMentions: p.IgnoreGroupMentions,
		MarkUnread: string(p.MarkUnread), Muted: p.Muted,
	}
}

// placePrefsPatch is the body of PUT .../notification-prefs: a field that is
// not there (or null) keeps its value.
type placePrefsPatch struct {
	Notify              *string `json:"notify"`
	Sound               *string `json:"sound"`
	IgnoreGroupMentions *bool   `json:"ignore_group_mentions"`
	MarkUnread          *string `json:"mark_unread"`
	Muted               *bool   `json:"muted"`
}

// prefsRoutes are the routes of the notification preferences of a room and of
// a conversation.
func (s *Server) prefsRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/rooms/{id}/notification-prefs", false, s.roomPrefs),
		s.route(http.MethodPut, "/rooms/{id}/notification-prefs", false, s.putRoomPrefs),
		s.route(http.MethodGet, "/conversations/{id}/notification-prefs", false, s.conversationPrefs),
		s.route(http.MethodPut, "/conversations/{id}/notification-prefs", false, s.putConversationPrefs),
	}
}

// roomPrefs answers GET /rooms/{id}/notification-prefs.
func (s *Server) roomPrefs(w http.ResponseWriter, r *http.Request) {
	s.getPrefs(w, r, func(id uint64) app.PlaceRef { return app.RoomRef(id) })
}

// conversationPrefs answers GET /conversations/{id}/notification-prefs.
func (s *Server) conversationPrefs(w http.ResponseWriter, r *http.Request) {
	s.getPrefs(w, r, func(id uint64) app.PlaceRef { return app.ConversationRef(id) })
}

// putRoomPrefs answers PUT /rooms/{id}/notification-prefs.
func (s *Server) putRoomPrefs(w http.ResponseWriter, r *http.Request) {
	s.putPrefs(w, r, func(id uint64) app.PlaceRef { return app.RoomRef(id) })
}

// putConversationPrefs answers PUT /conversations/{id}/notification-prefs.
func (s *Server) putConversationPrefs(w http.ResponseWriter, r *http.Request) {
	s.putPrefs(w, r, func(id uint64) app.PlaceRef { return app.ConversationRef(id) })
}

// getPrefs answers the read of the preferences of the caller in a place: the
// defaults while none are saved. A room or conversation the caller may not
// read is 404; a visible room the caller has not joined is 403.
func (s *Server) getPrefs(w http.ResponseWriter, r *http.Request, ref func(uint64) app.PlaceRef) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	prefs, err := s.svc.Prefs.Place(r.Context(), c.actor, ref(id))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, placePrefsOf(prefs))
}

// putPrefs answers the change of the preferences of the caller in a place: the
// preferences that are in the body change, the others keep their value, and
// the preferences come back. A body with none of them is 422.
func (s *Server) putPrefs(w http.ResponseWriter, r *http.Request, ref func(uint64) app.PlaceRef) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body placePrefsPatch
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := checkPrefsPatch(body); err != nil {
		s.writeError(w, r, err)
		return
	}
	update := app.PlaceUpdate{IgnoreGroupMentions: body.IgnoreGroupMentions, Muted: body.Muted}
	if body.Notify != nil {
		v := app.PlaceNotify(*body.Notify)
		update.Notify = &v
	}
	if body.Sound != nil {
		v := app.PlaceSound(*body.Sound)
		update.Sound = &v
	}
	if body.MarkUnread != nil {
		v := app.MarkUnread(*body.MarkUnread)
		update.MarkUnread = &v
	}
	prefs, err := s.svc.Prefs.UpdatePlace(r.Context(), c.actor, ref(id), update)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, placePrefsOf(prefs))
}

// checkPrefsPatch checks that a change of the preferences of a place changes
// something: a body with none of them fails with tagPrefsEmpty.
func checkPrefsPatch(body placePrefsPatch) error {
	if body.Notify == nil && body.Sound == nil && body.IgnoreGroupMentions == nil && body.MarkUnread == nil && body.Muted == nil {
		return errors.New(tagPrefsEmpty, "the change has no preference",
			"fields", map[string]string{
				"notify": messagePrefsEmpty, "sound": messagePrefsEmpty, "ignore_group_mentions": messagePrefsEmpty,
				"mark_unread": messagePrefsEmpty, "muted": messagePrefsEmpty,
			})
	}
	return nil
}

const tagPrefsEmpty = "NotificationPrefsEmpty"
