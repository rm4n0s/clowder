package api

import (
	"net/http"
)

// meResponse is the answer of GET /me.
type meResponse struct {
	User                userJSON `json:"user"`
	UnreadNotifications int      `json:"unread_notifications"`
	UnreadConversations int      `json:"unread_conversations"`
}

// sessionsResponse is the answer of GET /me/sessions.
type sessionsResponse struct {
	Sessions []sessionJSON `json:"sessions"`
}

// usersResponse is the answer of GET /users.
type usersResponse struct {
	Users []userJSON `json:"users"`
}

// meRoutes are the routes about the logged-in user.
func (s *Server) meRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/me", false, s.me),
		s.route(http.MethodGet, "/me/sessions", false, s.sessions),
		s.route(http.MethodDelete, "/me/sessions/{id}", false, s.endSession),
	}
}

// userRoutes are the routes about the other users.
func (s *Server) userRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/users", false, s.users),
	}
}

// me answers GET /me: the user and the counts the sidebar of the pages shows.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	notifications, err := s.svc.Notifications.UnreadCount(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	conversations, err := s.svc.Messages.UnreadCount(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, meResponse{
		User:                userOf(c.user),
		UnreadNotifications: notifications,
		UnreadConversations: conversations,
	})
}

// sessions answers GET /me/sessions: the live sessions of the user, browsers
// and clients, the one of the request marked as current.
func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	list, err := s.svc.Accounts.Sessions(r.Context(), c.actor, c.sessionID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := sessionsResponse{Sessions: make([]sessionJSON, len(list))}
	for i, sess := range list {
		out.Sessions[i] = sessionOf(sess)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// endSession answers DELETE /me/sessions/{id}: it ends one of the user's own
// sessions. Someone else's, or none, is 404.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Accounts.EndSession(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// users answers GET /users?q=: the enabled users whose username or display
// name starts with q, 20 at most, by username. It is what a client needs to
// pick the members of a room or to mention someone.
func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	list, err := s.svc.Accounts.SearchUsers(r.Context(), c.actor, r.URL.Query().Get("q"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := usersResponse{Users: make([]userJSON, len(list))}
	for i, u := range list {
		out.Users[i] = userOf(u)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}
