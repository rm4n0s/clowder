package api

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

const (
	// messageAdminUserEmpty is the problem with a change of a user that has
	// neither a role nor disabled.
	messageAdminUserEmpty = "Send role, disabled or both."
	// messageFilterID is the problem with a filter that is not an ID.
	messageFilterID = "Use a number from 1."
	// messageFilterEntity is the problem with an entity type that is not one.
	messageFilterEntity = "Use one of the entity types of the document."
	// messageFilterAction is the problem with an action that is not one.
	messageFilterAction = "Use one of the actions of the document."
	// messageFilterDay is the problem with a day that is not YYYY-MM-DD.
	messageFilterDay = "Write the day as YYYY-MM-DD."
)

// auditEntities are the entity types of the filter, in the order of SPEC
// §7.10. It is read-only.
var auditEntities = []app.EntityType{
	app.EntityRoom, app.EntityPost, app.EntityComment, app.EntityTask, app.EntityColumn, app.EntityMember, app.EntityUser,
}

// auditActions are the actions of the filter, in the order of SPEC §7.10. It
// is read-only.
var auditActions = []app.AuditAction{
	app.ActionPostCreated, app.ActionPostEdited, app.ActionPostDeleted,
	app.ActionTaskCreated, app.ActionTaskEdited, app.ActionTaskMoved, app.ActionTaskDeleted,
	app.ActionColumnCreated, app.ActionColumnRenamed, app.ActionColumnMoved, app.ActionColumnDeleted,
	app.ActionMemberJoined, app.ActionMemberAdded, app.ActionMemberLeft, app.ActionMemberRemoved,
	app.ActionRoomCreated, app.ActionRoomRenamed, app.ActionRoomDeleted,
	app.ActionUserPasswordChanged,
}

// adminUserPatch is the body of PATCH /admin/users/{id}: a field that is not
// there (or null) stays as it is.
type adminUserPatch struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

// invitationRequest is the body of POST /admin/invitations.
type invitationRequest struct {
	Role string `json:"role"`
}

// adminRoutes are the routes of the admins. Every one of them is 403 for a
// member (SPEC D42): the service says so.
func (s *Server) adminRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/admin/users", false, s.adminUsers),
		s.route(http.MethodPatch, "/admin/users/{id}", false, s.adminPatchUser),
		s.route(http.MethodPost, "/admin/invitations", false, s.adminInvite),
		s.route(http.MethodGet, "/admin/rooms", false, s.adminRooms),
		s.route(http.MethodGet, "/admin/rooms/{id}/members", false, s.adminRoomMembers),
		s.route(http.MethodGet, "/admin/audit", false, s.adminAudit),
	}
}

// adminUsers answers GET /admin/users: every user by username, the disabled
// ones included.
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	users, err := s.svc.Accounts.Users(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := adminUserListResponse{Users: make([]adminUserJSON, len(users))}
	for i, u := range users {
		out.Users[i] = adminUserOf(u)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// checkUserPatch checks that a change of a user changes something: a body with
// neither a role nor disabled fails with tagAdminUserInvalid.
func checkUserPatch(body adminUserPatch) error {
	if body.Role == nil && body.Disabled == nil {
		return errors.New(tagAdminUserInvalid, "the change has neither a role nor disabled",
			"fields", map[string]string{"role": messageAdminUserEmpty, "disabled": messageAdminUserEmpty})
	}
	return nil
}

// adminPatchUser answers PATCH /admin/users/{id}: an admin gives the user a
// role and/or disables or enables the account, and gets the user back. The
// role is changed first; if the second change is refused the first stays.
// The last active admin keeps the role and stays enabled (409 conflict), and
// an admin cannot disable themselves (422 on disabled). Disabling ends the
// sessions of the user, their API tokens included.
func (s *Server) adminPatchUser(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body adminUserPatch
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := checkUserPatch(body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.Role != nil {
		if err := s.svc.Accounts.SetRole(r.Context(), c.actor, id, app.Role(*body.Role)); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	if body.Disabled != nil {
		if err := s.svc.Accounts.SetDisabled(r.Context(), c.actor, id, *body.Disabled); err != nil {
			s.writeError(w, r, renameField(err, app.FieldUser, "disabled"))
			return
		}
	}
	users, err := s.svc.Accounts.Users(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	for _, u := range users {
		if u.ID == id {
			s.writeJSON(w, r, http.StatusOK, adminUserOf(u))
			return
		}
	}
	// Users are never deleted, so the user that was just changed is there.
	s.writeError(w, r, errors.New(app.TagNotFound, "the user is gone", "userID", id))
}

// adminInvite answers POST /admin/invitations: an admin makes an invitation
// for a user of the role and gets the link, once.
func (s *Server) adminInvite(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body invitationRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	inv, err := s.svc.Accounts.CreateInvitation(r.Context(), c.actor, app.Role(body.Role))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, adminInvitationJSON{
		URL:       s.baseURL + "/invite/" + url.PathEscape(inv.Token),
		Role:      string(inv.Role),
		ExpiresAt: rfc3339(inv.ExpiresAt),
	})
}

// adminRooms answers GET /admin/rooms: every room, the private ones included,
// by name.
func (s *Server) adminRooms(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	list, err := s.svc.Rooms.AllRooms(r.Context(), c.actor)
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

// adminRoomMembers answers GET /admin/rooms/{id}/members: the members of any
// room, a private one too, by display name.
func (s *Server) adminRoomMembers(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	members, err := s.svc.Rooms.AdminMembers(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := memberListResponse{Members: make([]memberJSON, len(members))}
	for i, m := range members {
		out.Members[i] = memberOf(m)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// auditFilter reads the filters of the audit log from the query. Every
// filter that is not valid is named in the fields of one tagFilterInvalid
// error. Dates are UTC days: from is the start of its day, to the start of the
// day after its day.
func auditFilter(r *http.Request) (app.AuditFilter, error) {
	var filter app.AuditFilter
	query := r.URL.Query()
	fields := map[string]string{}
	id := func(name string) uint64 {
		if !query.Has(name) {
			return 0
		}
		n, err := strconv.ParseUint(query.Get(name), 10, 63)
		if err != nil || n == 0 {
			fields[name] = messageFilterID
			return 0
		}
		return n
	}
	day := func(name string) time.Time {
		if !query.Has(name) {
			return time.Time{}
		}
		t, err := time.Parse(dateLayout, query.Get(name))
		if err != nil {
			fields[name] = messageFilterDay
			return time.Time{}
		}
		return t
	}
	filter.RoomID, filter.ActorID = id("room_id"), id("actor_id")
	if query.Has("entity_type") {
		filter.EntityType = app.EntityType(query.Get("entity_type"))
		if !slices.Contains(auditEntities, filter.EntityType) {
			fields["entity_type"] = messageFilterEntity
		}
	}
	if query.Has("action") {
		filter.Action = app.AuditAction(query.Get("action"))
		if !slices.Contains(auditActions, filter.Action) {
			fields["action"] = messageFilterAction
		}
	}
	filter.From = day("from")
	if to := day("to"); !to.IsZero() {
		filter.To = to.AddDate(0, 0, 1)
	}
	if len(fields) > 0 {
		return app.AuditFilter{}, errors.New(tagFilterInvalid, "a filter of the audit log is not valid", "fields", fields)
	}
	return filter, nil
}

// adminAudit answers GET /admin/audit: a page of the audit log, newest first,
// 50 to a page, narrowed by the filters. See auditFilter.
func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	filter, err := auditFilter(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := searchPage(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	got, err := s.svc.Audit.Events(r.Context(), c.actor, filter, page)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := auditResponse{Events: make([]auditEventJSON, len(got.Events)), Page: got.Page, HasMore: got.HasNext}
	for i, ev := range got.Events {
		out.Events[i] = auditEventOf(ev)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

const (
	tagFilterInvalid    = "FilterInvalid"
	tagAdminUserInvalid = "AdminUserChangeEmpty"
)
