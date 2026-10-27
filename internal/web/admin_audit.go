package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// auditDateLayout is the format of the date inputs of the filters.
const auditDateLayout = "2006-01-02"

// Texts shown above the audit table when a filter is not valid. They are fixed
// text.
const (
	messageAuditRoom   = "The room filter is not valid."
	messageAuditActor  = "The actor filter is not valid."
	messageAuditEntity = "The entity type filter is not valid."
	messageAuditAction = "The action filter is not valid."
	messageAuditFrom   = "The From date is not a valid date."
	messageAuditTo     = "The To date is not a valid date."
)

// auditEntities are the entity types of the filter, in the order of SPEC
// §7.10. It is read-only.
var auditEntities = []app.EntityType{
	app.EntityRoom, app.EntityPost, app.EntityComment, app.EntityTask, app.EntityColumn, app.EntityMember,
}

// auditActions are the actions of the filter, in the order of SPEC §7.10. It
// is read-only.
var auditActions = []app.AuditAction{
	app.ActionPostCreated, app.ActionPostEdited, app.ActionPostDeleted,
	app.ActionTaskCreated, app.ActionTaskEdited, app.ActionTaskMoved, app.ActionTaskDeleted,
	app.ActionColumnCreated, app.ActionColumnRenamed, app.ActionColumnMoved, app.ActionColumnDeleted,
	app.ActionMemberJoined, app.ActionMemberAdded, app.ActionMemberLeft, app.ActionMemberRemoved,
	app.ActionRoomCreated, app.ActionRoomRenamed, app.ActionRoomDeleted,
}

// auditQuery reads the filters and the page number from the query of the
// request. A value that is not valid is left out of the filter and the first
// one of them is named in the form's Message; the UI never sends one, only a
// hand-written URL does. Dates are UTC days: From is the start of its day, To
// the start of the day after its day.
func auditQuery(q url.Values) (app.AuditFilter, views.AuditFilterForm, int) {
	var filter app.AuditFilter
	var form views.AuditFilterForm
	problem := func(message string) {
		if form.Message == "" {
			form.Message = message
		}
	}
	if v := q.Get("room"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil && id != 0 {
			filter.RoomID, form.Room = id, id
		} else {
			problem(messageAuditRoom)
		}
	}
	if v := q.Get("actor"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil && id != 0 {
			filter.ActorID, form.Actor = id, id
		} else {
			problem(messageAuditActor)
		}
	}
	if v := q.Get("entity"); v != "" {
		if slices.Contains(auditEntities, app.EntityType(v)) {
			filter.EntityType, form.Entity = app.EntityType(v), v
		} else {
			problem(messageAuditEntity)
		}
	}
	if v := q.Get("action"); v != "" {
		if slices.Contains(auditActions, app.AuditAction(v)) {
			filter.Action, form.Action = app.AuditAction(v), v
		} else {
			problem(messageAuditAction)
		}
	}
	if v := q.Get("from"); v != "" {
		if day, err := time.Parse(auditDateLayout, v); err == nil {
			filter.From, form.From = day, day.Format(auditDateLayout)
		} else {
			problem(messageAuditFrom)
		}
	}
	if v := q.Get("to"); v != "" {
		if day, err := time.Parse(auditDateLayout, v); err == nil {
			filter.To, form.To = day.AddDate(0, 0, 1), day.Format(auditDateLayout)
		} else {
			problem(messageAuditTo)
		}
	}
	page := 1
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		page = n
	}
	return filter, form, page
}

// auditRoomName returns the name to show for the room of an event, and
// whether the room still exists. The current name wins; for a room that is
// gone the name is the one in the details of the event (room events), else
// the one in its summary ("... #name"), else empty.
func auditRoomName(ev app.AuditView, current map[uint64]string) (string, bool) {
	if name, ok := current[ev.RoomID]; ok {
		return name, true
	}
	if change, ok := ev.Details["name"].(map[string]any); ok {
		for _, key := range []string{"old", "new"} {
			if name, ok := change[key].(string); ok && name != "" {
				return name, false
			}
		}
	}
	if i := strings.LastIndex(ev.Summary, " #"); i >= 0 {
		return ev.Summary[i+2:], false
	}
	return "", false
}

// auditData reads the events of the page and the rooms and users of the
// filters, and makes the data of the audit log page. It has answered with the
// error page and returns false when a read fails. The sidebar is left out
// (withSidebar false) for a Datastar patch, which only needs the table.
func (s *Server) auditData(w http.ResponseWriter, r *http.Request, actor app.Actor, withSidebar bool) (views.AuditData, bool) {
	filter, form, page := auditQuery(r.URL.Query())
	var d views.AuditData
	if withSidebar {
		sb, ok := s.sidebarFor(w, r, 0)
		if !ok {
			return d, false
		}
		d.Sidebar = sb
	}
	events, err := s.svc.Audit.Events(r.Context(), actor, filter, page)
	if err != nil {
		s.fail(w, r, err)
		return d, false
	}
	rooms, err := s.svc.Rooms.AllRooms(r.Context(), actor)
	if err != nil {
		s.fail(w, r, err)
		return d, false
	}
	users, err := s.svc.Accounts.Users(r.Context(), actor)
	if err != nil {
		s.fail(w, r, err)
		return d, false
	}
	current := make(map[uint64]string, len(rooms))
	for _, room := range rooms {
		current[room.ID] = room.Name
	}
	_, exists := current[form.Room]
	d.Filter, d.Rooms, d.Users = form, rooms, users
	d.MissingRoom = form.Room != 0 && !exists
	for _, e := range auditEntities {
		d.Entities = append(d.Entities, string(e))
	}
	for _, a := range auditActions {
		d.Actions = append(d.Actions, string(a))
	}
	d.Rows = make([]views.AuditRow, len(events.Events))
	for i, ev := range events.Events {
		name, exists := auditRoomName(ev, current)
		d.Rows[i] = views.AuditRow{Event: ev, RoomName: name, RoomExists: exists}
	}
	d.Page, d.HasNext = events.Page, events.HasNext
	return d, true
}

// adminAudit answers GET /admin/audit: the events of the audit log, filtered
// by the query parameters "room", "actor", "entity", "action", "from", "to"
// and paged by "page". A Datastar request gets only the table patched in
// place, and the URL of the browser set to the page with the same filters, so
// that it can be shared.
func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	d, ok := s.auditData(w, r, actor, !isDatastar(r))
	if !ok {
		return
	}
	if !isDatastar(r) {
		s.render(w, r, http.StatusOK, views.AuditPage(d))
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sse.PatchElementTempl(views.AuditPanel(d)); err != nil {
		appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements")
		s.logger.Debug("sse write failed", "err", appErr)
		return
	}
	u := url.URL{Path: "/admin/audit", RawQuery: d.Filter.Query(d.Page)}
	if err := sseReplaceURL(sse, u); err != nil {
		appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "replace-url")
		s.logger.Debug("sse write failed", "err", appErr)
	}
}
