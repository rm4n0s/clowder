package web

import (
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// Where the message of a failed room action is shown.
const (
	// targetHeader is the header of the room page (join, leave, delete).
	targetHeader = "header"
	// targetMembers is the members panel of the room page (add, remove).
	targetMembers = "members"
	// targetBrowse is the list of the page of the visible rooms (join).
	targetBrowse = "browse"
)

// sidebarFor reads the rooms of the logged-in user and makes the data of the
// sidebar, which every page of a logged-in user shows. current is the ID of
// the room whose page is shown, 0 on every other page. When the read fails it
// has answered with the error page and returns false.
func (s *Server) sidebarFor(w http.ResponseWriter, r *http.Request, current uint64) (views.SidebarData, bool) {
	actor, okActor := actorFrom(r.Context())
	user, okUser := userFrom(r.Context())
	if !okActor || !okUser {
		s.redirectToLogin(w, r)
		return views.SidebarData{}, false
	}
	rooms, err := s.svc.Rooms.Mine(r.Context(), actor)
	if err != nil {
		s.fail(w, r, err)
		return views.SidebarData{}, false
	}
	return views.SidebarData{User: user, Rooms: rooms, CurrentRoomID: current}, true
}

// roomFail answers a failed read of a room: the error page, except that a
// Datastar request for a room that is gone or hidden is sent to the room's
// page, which says so (the client drops the body of a 404).
func (s *Server) roomFail(w http.ResponseWriter, r *http.Request, err error, id uint64) {
	if isDatastar(r) && isTag(err, app.TagNotFound) {
		s.redirect(w, r, views.RoomPath(id), http.StatusSeeOther)
		return
	}
	s.fail(w, r, err)
}

// roomID reads the ID of the room from the path. When it is not a number it
// has answered with the 404 page and returns false.
func (s *Server) roomID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.notFound(w, r)
		return 0, false
	}
	return id, true
}

// browseData reads the visible rooms and makes the data of the browse page.
// When a read fails it has answered with the error page and returns false.
func (s *Server) browseData(w http.ResponseWriter, r *http.Request, message string) (views.RoomsBrowseData, bool) {
	actor, _ := actorFrom(r.Context())
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return views.RoomsBrowseData{}, false
	}
	rooms, err := s.svc.Rooms.Visible(r.Context(), actor)
	if err != nil {
		s.fail(w, r, err)
		return views.RoomsBrowseData{}, false
	}
	return views.RoomsBrowseData{Sidebar: sb, Rooms: rooms, Message: message}, true
}

// roomData reads a room, its members and, for a member, the users to add, and
// makes the data of the room page. When a read fails it has answered (see
// roomFail) and returns false.
func (s *Server) roomData(w http.ResponseWriter, r *http.Request, id uint64) (views.RoomData, bool) {
	actor, _ := actorFrom(r.Context())
	sb, ok := s.sidebarFor(w, r, id)
	if !ok {
		return views.RoomData{}, false
	}
	room, err := s.svc.Rooms.Get(r.Context(), actor, id)
	if err != nil {
		s.roomFail(w, r, err, id)
		return views.RoomData{}, false
	}
	members, err := s.svc.Rooms.Members(r.Context(), actor, id)
	if err != nil {
		s.roomFail(w, r, err, id)
		return views.RoomData{}, false
	}
	d := views.RoomData{Sidebar: sb, Viewer: sb.User, Room: room, Members: members, Editor: views.NewPostEditorData(id)}
	if room.IsMember {
		if d.Candidates, err = s.svc.Rooms.Candidates(r.Context(), actor, id); err != nil {
			s.roomFail(w, r, err, id)
			return views.RoomData{}, false
		}
	}
	return d, true
}

// roomPageData is roomData with the page of the feed that starts after the
// post before (0 for the newest). When a read fails it has answered (see
// roomFail) and returns false.
func (s *Server) roomPageData(w http.ResponseWriter, r *http.Request, id, before uint64) (views.RoomData, bool) {
	d, ok := s.roomData(w, r, id)
	if !ok {
		return views.RoomData{}, false
	}
	actor, _ := actorFrom(r.Context())
	page, err := s.svc.Posts.Feed(r.Context(), actor, id, before, 0)
	if err != nil {
		s.roomFail(w, r, err, id)
		return views.RoomData{}, false
	}
	d.Feed = views.FeedData{RoomID: id, Page: page, Before: before}
	return d, true
}

// roomsBrowse answers GET /rooms: the visible rooms.
func (s *Server) roomsBrowse(w http.ResponseWriter, r *http.Request) {
	d, ok := s.browseData(w, r, "")
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.RoomsBrowsePage(d))
}

// roomNewPage answers GET /rooms/new: the form that makes a room.
func (s *Server) roomNewPage(w http.ResponseWriter, r *http.Request) {
	actor, _ := actorFrom(r.Context())
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.RoomNewPage(views.RoomNewData{Sidebar: sb, Admin: actor.IsAdmin()}))
}

// roomCreate answers POST /rooms with the form values "name", "description"
// and "visibility": it makes the room and sends the browser to it. A failure
// shows the problems in the form and keeps what was typed.
func (s *Server) roomCreate(w http.ResponseWriter, r *http.Request) {
	actor, _ := actorFrom(r.Context())
	if !s.parseForm(w, r) {
		return
	}
	name, description, visibility := r.PostFormValue("name"), r.PostFormValue("description"), r.PostFormValue("visibility")
	room, err := s.svc.Rooms.Create(r.Context(), actor, app.RoomInput{
		Name: name, Description: description, Visibility: app.Visibility(visibility),
	})
	if err != nil {
		// A plain post gets the whole page back, so it needs the sidebar.
		var sb views.SidebarData
		if !isDatastar(r) && !isTag(err, app.TagUnauthenticated) {
			var ok bool
			if sb, ok = s.sidebarFor(w, r, 0); !ok {
				return
			}
		}
		s.formFailed(w, r, err, func(problems views.FormErrors) formView {
			data := views.RoomNewData{
				Sidebar: sb, Admin: actor.IsAdmin(),
				Name: name, Description: description, Visibility: visibility, Errors: problems,
			}
			return formView{fragment: views.RoomNewForm(data), page: views.RoomNewPage(data)}
		})
		return
	}
	s.redirect(w, r, views.RoomPath(room.ID), http.StatusSeeOther)
}

// roomPage answers GET /rooms/{id}: the room with the editor and the newest
// page of the feed, for a person who may read it, else the 404 page. The query
// "before" shows the posts older than that post, which is what the link "Older
// posts" asks for.
func (s *Server) roomPage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.roomID(w, r)
	if !ok {
		return
	}
	d, ok := s.roomPageData(w, r, id, beforeOf(r))
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.RoomPage(d))
}

// roomPost starts every POST on a room: it reads the ID and the form and
// checks that the person may read the room, so that a room they may not see is
// the 404 page whatever the action is (admins included, D29: they manage other
// rooms from the admin rooms page). When it has answered it returns false.
// target says where the message of a failure is shown; a form value
// "from=browse" moves it to the list of the browse page.
func (s *Server) roomPost(w http.ResponseWriter, r *http.Request, target string) (app.Actor, uint64, string, bool) {
	id, ok := s.roomID(w, r)
	if !ok {
		return app.Actor{}, 0, "", false
	}
	if !s.parseForm(w, r) {
		return app.Actor{}, 0, "", false
	}
	if r.PostFormValue("from") == targetBrowse {
		target = targetBrowse
	}
	actor, _ := actorFrom(r.Context())
	if _, err := s.svc.Rooms.Get(r.Context(), actor, id); err != nil {
		s.roomActionFailed(w, r, err, id, target)
		return app.Actor{}, 0, "", false
	}
	return actor, id, target, true
}

// roomActionFailed shows the problem of a failed action on a room. A room that
// is gone or hidden is the 404 page (a Datastar request is sent to it); any
// other problem is a message in the header, the members panel or the browse
// list, patched in place for a Datastar request (status 200) and in the whole
// page, with the status of statusOf, for a plain post.
func (s *Server) roomActionFailed(w http.ResponseWriter, r *http.Request, err error, id uint64, target string) {
	status := statusOf(err)
	if status == http.StatusFound {
		s.redirectToLogin(w, r)
		return
	}
	if isTag(err, app.TagNotFound) {
		s.roomFail(w, r, err, id)
		return
	}
	var message string
	switch {
	case isTag(err, app.TagInvalidInput):
		problems := formErrorsOf(err)
		message = firstNonEmpty(problems.Fields[app.FieldUser], problems.Form, messageActionInvalid)
	case isTag(err, app.TagConflict):
		message = messageConflict
	case isTag(err, app.TagForbidden):
		message = messageForbidden
	default:
		s.logFailure(r, err)
		message = messageTryAgain
	}
	if target == targetBrowse {
		d, ok := s.browseData(w, r, message)
		if !ok {
			return
		}
		if isDatastar(r) {
			s.patch(w, r, views.RoomsPanel(d))
			return
		}
		s.render(w, r, status, views.RoomsBrowsePage(d))
		return
	}
	var (
		d  views.RoomData
		ok bool
	)
	if isDatastar(r) {
		d, ok = s.roomData(w, r, id)
	} else {
		// A plain post gets the whole page back, feed included.
		d, ok = s.roomPageData(w, r, id, 0)
	}
	if !ok {
		return
	}
	if target == targetMembers {
		d.MembersMessage = message
	} else {
		d.Message = message
	}
	if !isDatastar(r) {
		s.render(w, r, status, views.RoomPage(d))
		return
	}
	if target == targetMembers {
		s.patch(w, r, views.MembersPanel(d))
		return
	}
	s.patch(w, r, views.RoomHeader(d))
}

// roomJoin answers POST /rooms/{id}/join: the person joins a visible room and
// is sent to it.
func (s *Server) roomJoin(w http.ResponseWriter, r *http.Request) {
	actor, id, target, ok := s.roomPost(w, r, targetHeader)
	if !ok {
		return
	}
	if err := s.svc.Rooms.Join(r.Context(), actor, id); err != nil {
		s.roomActionFailed(w, r, err, id, target)
		return
	}
	s.redirect(w, r, views.RoomPath(id), http.StatusSeeOther)
}

// roomLeave answers POST /rooms/{id}/leave: the person leaves the room and is
// sent to the list of the visible rooms.
func (s *Server) roomLeave(w http.ResponseWriter, r *http.Request) {
	actor, id, target, ok := s.roomPost(w, r, targetHeader)
	if !ok {
		return
	}
	if err := s.svc.Rooms.Leave(r.Context(), actor, id); err != nil {
		s.roomActionFailed(w, r, err, id, target)
		return
	}
	s.redirect(w, r, "/rooms", http.StatusSeeOther)
}

// roomDelete answers POST /rooms/{id}/delete: the room is deleted with
// everything in it and the person is sent to the home page.
func (s *Server) roomDelete(w http.ResponseWriter, r *http.Request) {
	actor, id, target, ok := s.roomPost(w, r, targetHeader)
	if !ok {
		return
	}
	if err := s.svc.Rooms.Delete(r.Context(), actor, id); err != nil {
		s.roomActionFailed(w, r, err, id, target)
		return
	}
	s.redirect(w, r, "/", http.StatusSeeOther)
}

// roomRename answers POST /rooms/{id}/rename with the form values "name" and
// "description". A mistake in the form shows the problems in the rename form and
// keeps what was typed; any other failure is a message in the header.
func (s *Server) roomRename(w http.ResponseWriter, r *http.Request) {
	actor, id, target, ok := s.roomPost(w, r, targetHeader)
	if !ok {
		return
	}
	name, description := r.PostFormValue("name"), r.PostFormValue("description")
	err := s.svc.Rooms.Update(r.Context(), actor, id, app.RoomInput{Name: name, Description: description})
	if err == nil {
		s.redirect(w, r, views.RoomPath(id), http.StatusSeeOther)
		return
	}
	if !isTag(err, app.TagInvalidInput) {
		// Not a mistake in the form: a message in the header, or the 404 page.
		s.roomActionFailed(w, r, err, id, target)
		return
	}
	var d views.RoomData
	if isDatastar(r) {
		d, ok = s.roomData(w, r, id)
	} else {
		d, ok = s.roomPageData(w, r, id, 0)
	}
	if !ok {
		return
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		d.Edit = views.RoomEditData{Name: name, Description: description, Open: true, Errors: problems}
		return formView{fragment: views.RoomEditForm(d), page: views.RoomPage(d)}
	})
}

// membersChanged answers a successful change of the members: a Datastar request
// gets the members panel patched, a plain post is sent back to the room.
func (s *Server) membersChanged(w http.ResponseWriter, r *http.Request, id uint64) {
	if !isDatastar(r) {
		http.Redirect(w, r, views.RoomPath(id), http.StatusSeeOther)
		return
	}
	d, ok := s.roomData(w, r, id)
	if !ok {
		return
	}
	s.patch(w, r, views.MembersPanel(d))
}

// memberAdd answers POST /rooms/{id}/members with the form value "user", the
// ID of the user to add.
func (s *Server) memberAdd(w http.ResponseWriter, r *http.Request) {
	actor, id, target, ok := s.roomPost(w, r, targetMembers)
	if !ok {
		return
	}
	// A value that is not an ID is 0, which the service answers as "Choose a user".
	userID, _ := strconv.ParseUint(r.PostFormValue("user"), 10, 64)
	if err := s.svc.Rooms.AddMember(r.Context(), actor, id, userID); err != nil {
		s.roomActionFailed(w, r, err, id, target)
		return
	}
	s.membersChanged(w, r, id)
}

// memberRemove answers POST /rooms/{id}/members/{userID}/remove: an admin
// removes a member.
func (s *Server) memberRemove(w http.ResponseWriter, r *http.Request) {
	actor, id, target, ok := s.roomPost(w, r, targetMembers)
	if !ok {
		return
	}
	userID, err := strconv.ParseUint(r.PathValue("userID"), 10, 64)
	if err != nil {
		s.roomActionFailed(w, r, errors.New(app.TagInvalidInput, "the user ID is not a number",
			"fields", map[string]string{app.FieldUser: messageActionInvalid}), id, target)
		return
	}
	if err := s.svc.Rooms.RemoveMember(r.Context(), actor, id, userID); err != nil {
		s.roomActionFailed(w, r, err, id, target)
		return
	}
	s.membersChanged(w, r, id)
}
