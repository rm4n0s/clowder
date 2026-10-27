package api

import (
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
)

// roomListResponse is the answer of GET /rooms.
type roomListResponse struct {
	Rooms []roomJSON `json:"rooms"`
}

// memberListResponse is the answer of GET /rooms/{id}/members.
type memberListResponse struct {
	Members []memberJSON `json:"members"`
}

// roomCreateRequest is the body of POST /rooms.
type roomCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
}

// roomPatchRequest is the body of PATCH /rooms/{id}: a field that is not there
// (or null) stays as it is.
type roomPatchRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// memberAddRequest is the body of POST /rooms/{id}/members.
type memberAddRequest struct {
	UserID uint64 `json:"user_id"`
}

// roomRoutes are the routes of the rooms and their members.
func (s *Server) roomRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/rooms", false, s.rooms),
		s.route(http.MethodPost, "/rooms", false, s.createRoom),
		s.route(http.MethodGet, "/rooms/{id}", false, s.room),
		s.route(http.MethodPatch, "/rooms/{id}", false, s.patchRoom),
		s.route(http.MethodDelete, "/rooms/{id}", false, s.deleteRoom),
		s.route(http.MethodPost, "/rooms/{id}/join", false, s.joinRoom),
		s.route(http.MethodPost, "/rooms/{id}/leave", false, s.leaveRoom),
		s.route(http.MethodGet, "/rooms/{id}/members", false, s.roomMembers),
		s.route(http.MethodPost, "/rooms/{id}/members", false, s.addRoomMember),
		s.route(http.MethodDelete, "/rooms/{id}/members/{user_id}", false, s.removeRoomMember),
		s.route(http.MethodGet, "/rooms/{id}/candidates", false, s.roomCandidates),
	}
}

// rooms answers GET /rooms: every visible room and the private rooms of the
// caller, by name, each with whether the caller is a member.
func (s *Server) rooms(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	list, err := s.svc.Rooms.Available(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := roomListResponse{Rooms: make([]roomJSON, len(list))}
	for i, room := range list {
		out.Rooms[i] = roomOf(room)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// createRoom answers POST /rooms: it makes a room with the caller as its first
// member. Visible rooms are for admins (403 for anyone else).
func (s *Server) createRoom(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var in roomCreateRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	room, err := s.svc.Rooms.Create(r.Context(), c.actor, app.RoomInput{
		Name:        in.Name,
		Description: in.Description,
		Visibility:  app.Visibility(in.Visibility),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", Prefix+"/rooms/"+strconv.FormatUint(room.ID, 10))
	s.writeJSON(w, r, http.StatusCreated, roomOf(room))
}

// room answers GET /rooms/{id}: a visible room, or a private room of the
// caller. Any other room is 404.
func (s *Server) room(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	room, err := s.svc.Rooms.Get(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, roomOf(room))
}

// patchRoom answers PATCH /rooms/{id}: it changes the name and the
// description that are in the body and returns the room.
func (s *Server) patchRoom(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var in roomPatchRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	room, err := s.svc.Rooms.Patch(r.Context(), c.actor, id, app.RoomPatch{Name: in.Name, Description: in.Description})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, roomOf(room))
}

// deleteRoom answers DELETE /rooms/{id}: it deletes the room with everything
// in it.
func (s *Server) deleteRoom(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Rooms.Delete(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// joinRoom answers POST /rooms/{id}/join. Joining a room again is not an
// error.
func (s *Server) joinRoom(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Rooms.Join(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// leaveRoom answers POST /rooms/{id}/leave. Leaving a room the caller is not
// in is not an error.
func (s *Server) leaveRoom(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Rooms.Leave(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// roomMembers answers GET /rooms/{id}/members.
func (s *Server) roomMembers(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	list, err := s.svc.Rooms.Members(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := memberListResponse{Members: make([]memberJSON, len(list))}
	for i, m := range list {
		out.Members[i] = memberOf(m)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// addRoomMember answers POST /rooms/{id}/members: a member adds a user.
func (s *Server) addRoomMember(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var in memberAddRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Rooms.AddMember(r.Context(), c.actor, id, in.UserID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// removeRoomMember answers DELETE /rooms/{id}/members/{user_id}: an admin
// removes a member.
func (s *Server) removeRoomMember(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	userID, err := pathID(r, "user_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Rooms.RemoveMember(r.Context(), c.actor, id, userID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// roomCandidates answers GET /rooms/{id}/candidates?q=: the enabled users who
// are not members and whose username or display name starts with q, 20 at
// most.
func (s *Server) roomCandidates(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	list, err := s.svc.Rooms.SearchCandidates(r.Context(), c.actor, id, r.URL.Query().Get("q"))
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
