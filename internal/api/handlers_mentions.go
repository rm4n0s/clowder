package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

const (
	// messageFormatInvalid is the problem with a format other than text or
	// markdown.
	messageFormatInvalid = "Choose text or markdown."
	// messageUserIDsInvalid is the problem with a list of user IDs that is
	// not numbers separated by commas.
	messageUserIDsInvalid = "Send user IDs separated by commas."
)

// groupMentionCountResponse is the answer of the group-mention-count routes.
type groupMentionCountResponse struct {
	// Count is the number of people the group mentions of the body would
	// notify.
	Count int `json:"count"`
}

// presenceResponse is the answer of GET /me/presence.
type presenceResponse struct {
	// Online maps the ID of each user who is online to true; users who are
	// offline are not there.
	Online map[string]bool `json:"online"`
}

// mentionRoutes are the routes of the group mentions, the follows and the
// presence (SPEC §7.15).
func (s *Server) mentionRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/rooms/{id}/group-mention-count", false, s.roomGroupMentionCount),
		s.route(http.MethodGet, "/conversations/{id}/group-mention-count", false, s.conversationGroupMentionCount),
		s.route(http.MethodPut, "/posts/{id}/follow", false, s.putFollow),
		s.route(http.MethodDelete, "/posts/{id}/follow", false, s.deleteFollow),
		s.route(http.MethodGet, "/me/presence", false, s.presence),
	}
}

// roomGroupMentionCount answers GET /rooms/{id}/group-mention-count?body=&format=:
// how many people the group mentions (@room, @all, @here) of the body would
// notify if the caller posted it in the room; 0 when it has none. A client
// asks it before it sends, to ask its own user "This will notify 14 people.
// Send anyway?" above 5: the API never asks by itself. The permission is that
// of posting: 404 for a room the caller may not read, 403 for a visible room
// the caller has not joined.
func (s *Server) roomGroupMentionCount(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	format, err := formatParam(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	n, err := s.svc.Posts.GroupMentionCount(r.Context(), c.actor, id, format, r.URL.Query().Get("body"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, groupMentionCountResponse{Count: n})
}

// conversationGroupMentionCount answers GET /conversations/{id}/group-mention-count?body=:
// the same for a message in a group conversation; always 0 in a direct one.
// The query format is accepted and ignored: a message is plain text. 404 for a
// conversation the caller is no member of.
func (s *Server) conversationGroupMentionCount(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	n, err := s.svc.Messages.GroupMentionCount(r.Context(), c.actor, id, r.URL.Query().Get("body"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, groupMentionCountResponse{Count: n})
}

// formatParam reads the query "format": text or markdown, markdown when it is
// missing; anything else fails with tagFormatInvalid.
func formatParam(r *http.Request) (app.Format, error) {
	switch v := r.URL.Query().Get("format"); v {
	case "", string(app.FormatMarkdown):
		return app.FormatMarkdown, nil
	case string(app.FormatText):
		return app.FormatText, nil
	default:
		return "", errors.New(tagFormatInvalid, "the format is not text or markdown",
			"fields", map[string]string{"format": messageFormatInvalid})
	}
}

// putFollow answers PUT /posts/{id}/follow: the caller follows the post, so
// that every comment in its thread notifies them. On a comment it acts on its
// top-level post. Following a post that is followed is not an error: 204. 404
// when the caller cannot read the post.
func (s *Server) putFollow(w http.ResponseWriter, r *http.Request) {
	s.setFollow(w, r, true)
}

// deleteFollow answers DELETE /posts/{id}/follow: the caller stops following
// the post (the top-level post of a comment), and an automatic follow does not
// bring it back. 204, also when the post was not followed.
func (s *Server) deleteFollow(w http.ResponseWriter, r *http.Request) {
	s.setFollow(w, r, false)
}

// setFollow is putFollow and deleteFollow.
func (s *Server) setFollow(w http.ResponseWriter, r *http.Request, following bool) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if following {
		err = s.svc.Posts.Follow(r.Context(), c.actor, id)
	} else {
		err = s.svc.Posts.Unfollow(r.Context(), c.actor, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// presence answers GET /me/presence?user_ids=1,2,3: which of the users (at most
// 100; more is 422) are online now, that is, have a page or a client connected
// in any process. Disabled users are never online. Presence is not private:
// any user may ask about any users.
func (s *Server) presence(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	ids, err := userIDsParam(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := presenceResponse{Online: map[string]bool{}}
	if s.svc.Presence != nil {
		online, err := s.svc.Presence.Online(r.Context(), c.actor, ids)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		for id, ok := range online {
			if ok {
				out.Online[strconv.FormatUint(id, 10)] = true
			}
		}
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// userIDsParam reads the query "user_ids": numbers separated by commas. A
// missing or empty list is no users; anything that is not a positive number
// fails with tagUserIDsInvalid.
func userIDsParam(r *http.Request) ([]uint64, error) {
	text := strings.TrimSpace(r.URL.Query().Get("user_ids"))
	if text == "" {
		return nil, nil
	}
	parts := strings.Split(text, ",")
	ids := make([]uint64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 63)
		if err != nil || id == 0 {
			return nil, errors.New(tagUserIDsInvalid, "user_ids is not a list of user IDs",
				"fields", map[string]string{"user_ids": messageUserIDsInvalid})
		}
		ids = append(ids, id)
	}
	return ids, nil
}

const (
	tagFormatInvalid  = "FormatInvalid"
	tagUserIDsInvalid = "UserIDsInvalid"
)
