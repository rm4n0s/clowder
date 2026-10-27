package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// adminRoomsPath is the admin rooms page.
const adminRoomsPath = "/admin/rooms"

// adminRoomsOpenPath is where a plain request is sent after an action on the
// members of a room: the admin rooms page with that room's members open and
// the page scrolled to its row.
func adminRoomsOpenPath(id uint64) string {
	return adminRoomsPath + "?members=" + strconv.FormatUint(id, 10) + "#room-" + strconv.FormatUint(id, 10)
}

// adminRoomRows reads every room with the display name of its creator, and the
// members of the room openID (0 for none), and makes the rows of the page. A
// room whose members cannot be read because it is gone is shown closed.
func (s *Server) adminRoomRows(ctx context.Context, actor app.Actor, openID uint64) ([]views.AdminRoomRow, error) {
	rooms, err := s.svc.Rooms.AllRooms(ctx, actor)
	if err != nil {
		return nil, err
	}
	users, err := s.svc.Accounts.Users(ctx, actor)
	if err != nil {
		return nil, err
	}
	names := make(map[uint64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.DisplayName
	}
	rows := make([]views.AdminRoomRow, len(rooms))
	for i, room := range rooms {
		rows[i] = views.AdminRoomRow{Room: room, Creator: names[room.CreatedBy]}
		if room.ID != openID {
			continue
		}
		members, err := s.svc.Rooms.AdminMembers(ctx, actor, room.ID)
		if err != nil {
			return nil, err
		}
		rows[i].MembersOpen = true
		rows[i].Members = members
	}
	return rows, nil
}

// adminRoomsData makes the data of the admin rooms page, with the members of
// the room openID open. It has answered with the error page and returns false
// when a read fails.
func (s *Server) adminRoomsData(w http.ResponseWriter, r *http.Request, actor app.Actor, openID uint64) (views.AdminRoomsData, bool) {
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return views.AdminRoomsData{}, false
	}
	rows, err := s.adminRoomRows(r.Context(), actor, openID)
	if err != nil {
		s.fail(w, r, err)
		return views.AdminRoomsData{}, false
	}
	return views.AdminRoomsData{Sidebar: sb, Rooms: rows}, true
}

// adminRooms answers GET /admin/rooms: every room. The query parameter
// "members" is the ID of a room whose members are open, which is how a page
// without JavaScript opens them.
func (s *Server) adminRooms(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	// A value that is not an ID is 0: no room is open.
	openID, _ := strconv.ParseUint(r.URL.Query().Get("members"), 10, 63)
	data, ok := s.adminRoomsData(w, r, actor, openID)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.AdminRoomsPage(data))
}

// adminRoomShow answers with the row of room id after apply has set its
// state: a Datastar request gets the row patched in place, a plain request the
// whole page with status. open says whether the members of the room are open.
// A room that is gone is answered by adminRoomGone.
func (s *Server) adminRoomShow(w http.ResponseWriter, r *http.Request, actor app.Actor, id uint64, open bool, status int, apply func(*views.AdminRoomRow)) {
	openID := uint64(0)
	if open {
		openID = id
	}
	data, ok := s.adminRoomsData(w, r, actor, openID)
	if !ok {
		return
	}
	for i := range data.Rooms {
		if data.Rooms[i].Room.ID != id {
			continue
		}
		if apply != nil {
			apply(&data.Rooms[i])
		}
		if isDatastar(r) {
			s.patch(w, r, views.AdminRoom(data.Rooms[i]))
			return
		}
		s.render(w, r, status, views.AdminRoomsPage(data))
		return
	}
	s.adminRoomGone(w, r, id)
}

// adminRoomGone answers an action on a room that does not exist (any more): a
// Datastar request gets its row removed from the page, a plain request the 404
// page.
func (s *Server) adminRoomGone(w http.ResponseWriter, r *http.Request, id uint64) {
	if !isDatastar(r) {
		s.notFound(w, r)
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sse.RemoveElementByID("room-" + strconv.FormatUint(id, 10)); err != nil {
		appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "remove-elements")
		s.logger.Debug("sse write failed", "err", appErr)
	}
}

// adminRoomPost starts every POST on a room of the admin rooms page: it checks
// that the request is an admin's (members get the 404 page, whatever the
// action), and reads the ID of the room and the form. When it has answered it
// returns false.
func (s *Server) adminRoomPost(w http.ResponseWriter, r *http.Request) (app.Actor, uint64, bool) {
	actor, _, ok := s.adminOnly(w, r)
	if !ok {
		return app.Actor{}, 0, false
	}
	id, ok := s.roomID(w, r)
	if !ok {
		return app.Actor{}, 0, false
	}
	if !s.parseForm(w, r) {
		return app.Actor{}, 0, false
	}
	return actor, id, true
}

// adminRoomFailed answers a failed action on a room. TagNotFound (the room is
// gone) is adminRoomGone, TagUnauthenticated the login page; any other problem
// is a message in the row, patched in place for a Datastar request (status
// 200) and in the whole page, with the status of statusOf, for a plain post.
// members says that the problem belongs to the members list, which is then
// shown open.
func (s *Server) adminRoomFailed(w http.ResponseWriter, r *http.Request, err error, actor app.Actor, id uint64, members bool) {
	status := statusOf(err)
	if status == http.StatusFound {
		s.redirectToLogin(w, r)
		return
	}
	if isTag(err, app.TagNotFound) {
		s.adminRoomGone(w, r, id)
		return
	}
	var message string
	switch {
	case isTag(err, app.TagInvalidInput):
		problems := formErrorsOf(err)
		message = firstNonEmpty(problems.Form, messageActionInvalid)
	case isTag(err, app.TagConflict):
		message = messageConflict
	case isTag(err, app.TagForbidden):
		message = messageForbidden
	default:
		s.logFailure(r, err)
		message = messageTryAgain
	}
	s.adminRoomShow(w, r, actor, id, members, status, func(row *views.AdminRoomRow) {
		if members {
			row.MembersMessage = message
		} else {
			row.Message = message
		}
	})
}

// adminRoomRename answers POST /admin/rooms/{id}/rename with the form values
// "name" and "description". A mistake in the form shows the problems in the
// rename form of the row and keeps what was typed.
func (s *Server) adminRoomRename(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := s.adminRoomPost(w, r)
	if !ok {
		return
	}
	name, description := r.PostFormValue("name"), r.PostFormValue("description")
	err := s.svc.Rooms.Update(r.Context(), actor, id, app.RoomInput{Name: name, Description: description})
	switch {
	case err == nil:
		if isDatastar(r) {
			s.adminRoomShow(w, r, actor, id, false, http.StatusOK, nil)
			return
		}
		http.Redirect(w, r, adminRoomsPath+"#room-"+strconv.FormatUint(id, 10), http.StatusSeeOther)
	case isTag(err, app.TagInvalidInput):
		problems := formErrorsOf(err)
		s.adminRoomShow(w, r, actor, id, false, statusOf(err), func(row *views.AdminRoomRow) {
			row.Edit = views.RoomEditData{Name: name, Description: description, Open: true, Errors: problems}
		})
	default:
		s.adminRoomFailed(w, r, err, actor, id, false)
	}
}

// adminRoomDelete answers POST /admin/rooms/{id}/delete: the room is deleted
// with everything in it and its row disappears.
func (s *Server) adminRoomDelete(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := s.adminRoomPost(w, r)
	if !ok {
		return
	}
	if err := s.svc.Rooms.Delete(r.Context(), actor, id); err != nil {
		s.adminRoomFailed(w, r, err, actor, id, false)
		return
	}
	if isDatastar(r) {
		s.adminRoomGone(w, r, id)
		return
	}
	http.Redirect(w, r, adminRoomsPath, http.StatusSeeOther)
}

// adminRoomMembers answers GET /admin/rooms/{id}/members: the row of the room
// with its members open. A plain request is sent to the page with the members
// open.
func (s *Server) adminRoomMembers(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	id, ok := s.roomID(w, r)
	if !ok {
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, adminRoomsOpenPath(id), http.StatusSeeOther)
		return
	}
	s.adminRoomShow(w, r, actor, id, true, http.StatusOK, nil)
}

// adminMemberRemove answers POST /admin/rooms/{id}/members/{userID}/remove: an
// admin removes a member from any room, and the row is shown with the members
// that are left.
func (s *Server) adminMemberRemove(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := s.adminRoomPost(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseUint(r.PathValue("userID"), 10, 63)
	if err != nil || userID == 0 {
		s.notFound(w, r)
		return
	}
	if err := s.svc.Rooms.RemoveMember(r.Context(), actor, id, userID); err != nil {
		s.adminRoomFailed(w, r, err, actor, id, true)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, adminRoomsOpenPath(id), http.StatusSeeOther)
		return
	}
	s.adminRoomShow(w, r, actor, id, true, http.StatusOK, nil)
}
