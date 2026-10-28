package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// endOfColumn is the position of a move that puts the card at the end of its
// column; the service clamps a position to the number of cards.
const endOfColumn = 1 << 30

// Texts shown to people for what the board cannot do. They are fixed text,
// never the text of an error.
const (
	// messageCardGone is shown when the card an action is about is gone.
	messageCardGone = "That card is gone."
	// messageColumnGone is shown when the column an action is about is gone.
	messageColumnGone = "That column is gone."
	// messageChooseColumn is shown for a move to no column.
	messageChooseColumn = "Choose a column."
	// messageDueDate is shown next to a due date that is not a date.
	messageDueDate = "Write the due date as YYYY-MM-DD."
)

// boardRequest is a request on the board of a room after the checks every
// route of the board starts with.
type boardRequest struct {
	actor app.Actor
	id    uint64
	// view is the board as the actor sees it.
	view app.BoardView
}

// boardView reads the board of the room for the actor.
func (s *Server) boardView(ctx context.Context, actor app.Actor, id uint64) (app.BoardView, error) {
	return s.svc.Board.Board(ctx, actor, id)
}

// boardStart starts every route of the board: it reads the ID of the room and
// the board, so that a room the person may not read is the 404 page whatever
// the route does (admins included, D29). With post it also reads the form. When
// it has answered it returns false.
func (s *Server) boardStart(w http.ResponseWriter, r *http.Request, post bool) (boardRequest, bool) {
	id, ok := s.roomID(w, r)
	if !ok {
		return boardRequest{}, false
	}
	if post && !s.parseForm(w, r) {
		return boardRequest{}, false
	}
	actor, _ := actorFrom(r.Context())
	view, err := s.boardView(r.Context(), actor, id)
	if err != nil {
		s.roomFail(w, r, err, id)
		return boardRequest{}, false
	}
	return boardRequest{actor: actor, id: id, view: view}, true
}

// boardTask reads the ID of the card from the path; false when it is not a
// number or the board has no such card. The card is then answered.
func (s *Server) boardTask(w http.ResponseWriter, r *http.Request, br boardRequest) (app.TaskCard, bool) {
	id, err := strconv.ParseUint(r.PathValue("taskID"), 10, 63)
	if err == nil {
		for _, c := range br.view.Columns {
			for _, t := range c.Tasks {
				if t.ID == id {
					return t, true
				}
			}
		}
	}
	s.boardMissing(w, r, br, messageCardGone)
	return app.TaskCard{}, false
}

// boardColumn reads the ID of the column from the path; false when it is not a
// number or the board has no such column. The column is then answered.
func (s *Server) boardColumn(w http.ResponseWriter, r *http.Request, br boardRequest) (app.ColumnView, bool) {
	id, err := strconv.ParseUint(r.PathValue("columnID"), 10, 63)
	if err == nil {
		for _, c := range br.view.Columns {
			if c.ID == id {
				return c, true
			}
		}
	}
	s.boardMissing(w, r, br, messageColumnGone)
	return app.ColumnView{}, false
}

// boardMissing answers an action on a card or a column that the board does not
// have (it was deleted or moved to another room): the 404 page for a plain
// request, and for a Datastar request the message at the top of the board
// next to the columns as they are.
func (s *Server) boardMissing(w http.ResponseWriter, r *http.Request, br boardRequest, message string) {
	if !isDatastar(r) {
		s.notFound(w, r)
		return
	}
	d := views.BoardData{Board: br.view}
	s.patchAll(w, r, patchOp{component: views.BoardMessage(message)}, patchOp{component: views.BoardColumns(d)})
}

// boardPageData reads the sidebar for the page of the board around view. When
// it fails it has answered and returns false.
func (s *Server) boardPageData(w http.ResponseWriter, r *http.Request, br boardRequest, message string, editor *views.BoardEditorData) (views.BoardData, bool) {
	sb, ok := s.sidebarFor(w, r, br.id)
	if !ok {
		return views.BoardData{}, false
	}
	return views.BoardData{Sidebar: sb, Board: br.view, Message: message, Editor: editor}, true
}

// boardPage answers GET /rooms/{id}/board: the board of a room that the person
// may read, else the 404 page. A person who has not joined the room reads it
// without any control.
func (s *Server) boardPage(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, false)
	if !ok {
		return
	}
	d, ok := s.boardPageData(w, r, br, "", nil)
	if !ok {
		return
	}
	// Opening the board marks the room as read, like opening its feed (SPEC
	// §7.17). The board shows no posts, so the stream of the page keeps the
	// counts of the sidebar live afterwards.
	if br.view.CanEdit {
		actor, _ := actorFrom(r.Context())
		_, sb, err := s.visitRoom(r.Context(), actor, br.id, d.Sidebar)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Sidebar = sb
	}
	s.render(w, r, http.StatusOK, views.BoardPage(d))
}

// boardColumns answers GET /rooms/{id}/board/columns: the columns alone, which
// the board asks for when a drag ended without a drop and a live update may
// have been held back. A plain request is sent to the board page.
func (s *Server) boardColumns(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, false)
	if !ok {
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.BoardPath(br.id), http.StatusSeeOther)
		return
	}
	s.patchAll(w, r, patchOp{component: views.BoardColumns(views.BoardData{Board: br.view})})
}

// boardTaskEdit answers GET /rooms/{id}/board/tasks/{taskID}/edit: the form of
// the card, patched into the panel of the board (a Datastar request) or in the
// whole page (a plain request). Only members edit cards.
func (s *Server) boardTaskEdit(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, false)
	if !ok {
		return
	}
	card, ok := s.boardTask(w, r, br)
	if !ok {
		return
	}
	if !br.view.CanEdit {
		s.boardFailed(w, r, br, errors.New(app.TagForbidden, "only members change cards",
			"userID", br.actor.UserID, "roomID", br.id), nil)
		return
	}
	members, err := s.svc.Rooms.Members(r.Context(), br.actor, br.id)
	if err != nil {
		s.roomFail(w, r, err, br.id)
		return
	}
	editor := views.NewBoardEditorData(card, members)
	if !isDatastar(r) {
		d, ok := s.boardPageData(w, r, br, "", &editor)
		if !ok {
			return
		}
		s.render(w, r, http.StatusOK, views.BoardPage(d))
		return
	}
	s.patchAll(w, r,
		patchOp{component: views.BoardMessage("")},
		patchOp{component: views.BoardEditor(views.BoardData{Board: br.view, Editor: &editor})},
	)
}

// positionOf reads a position from a form value: missing means the end of the
// column or of the board, a value that is not a number is a mistake.
func positionOf(value string, missing int) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return missing, true
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return n, true
}

// boardTaskMove answers POST /rooms/{id}/board/tasks/{taskID}/move with the form
// values "column" and "position": the card goes to that column at that place
// (counted without the card, from 0; missing means the end of the column). A
// Datastar request gets the columns as the server has them, so that the board
// always shows the server's order; a plain post is sent back to the board.
func (s *Server) boardTaskMove(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, true)
	if !ok {
		return
	}
	card, ok := s.boardTask(w, r, br)
	if !ok {
		return
	}
	columnID := idOfText(r.PostFormValue("column"))
	if columnID == 0 {
		s.boardFailed(w, r, br, errors.New(app.TagInvalidInput, "no column to move the card to",
			"fields", map[string]string{"column": messageChooseColumn}), nil)
		return
	}
	position, ok := positionOf(r.PostFormValue("position"), endOfColumn)
	if !ok {
		s.boardFailed(w, r, br, errors.New(app.TagInvalidInput, "the position is not a number",
			"fields", map[string]string{app.FieldForm: messageInvalid}), nil)
		return
	}
	if !hasColumn(br.view, columnID) {
		s.boardMissing(w, r, br, messageColumnGone)
		return
	}
	if err := s.svc.Board.MoveTask(r.Context(), br.actor, card.ID, columnID, position); err != nil {
		s.boardFailed(w, r, br, err, nil)
		return
	}
	s.boardChanged(w, r, br, "#task-"+strconv.FormatUint(card.ID, 10), false)
}

// hasColumn reports whether the board has the column.
func hasColumn(view app.BoardView, id uint64) bool {
	for _, c := range view.Columns {
		if c.ID == id {
			return true
		}
	}
	return false
}

// boardTaskUpdate answers POST /rooms/{id}/board/tasks/{taskID} with the form
// values "title", "assignees" (any number), "due" (YYYY-MM-DD) and "clear_due"
// (ticked removes the due date, whatever "due" holds; an empty "due" keeps it).
// The title and the assignees are always taken from the form. A mistake shows
// its problems in the form of the card, with what was typed.
func (s *Server) boardTaskUpdate(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, true)
	if !ok {
		return
	}
	card, ok := s.boardTask(w, r, br)
	if !ok {
		return
	}
	title := r.PostFormValue("title")
	assignees := make([]uint64, 0, len(r.PostForm["assignees"]))
	for _, v := range r.PostForm["assignees"] {
		assignees = append(assignees, idOfText(v))
	}
	patch := app.TaskPatch{Title: &title, Assignees: &assignees}
	due := strings.TrimSpace(r.PostFormValue("due"))
	clearDue := r.PostFormValue("clear_due") != ""
	var dueProblem error
	switch {
	case clearDue:
		patch.ClearDueDate = true
	case due != "":
		day, err := time.Parse(dueLayout, due)
		if err != nil {
			dueProblem = errors.New(app.TagInvalidInput, "the due date is not a date",
				"fields", map[string]string{app.FieldDueDate: messageDueDate})
			break
		}
		patch.DueDate = &day
	}
	err := dueProblem
	if err == nil {
		err = s.svc.Board.UpdateTask(r.Context(), br.actor, card.ID, patch)
	}
	if err != nil {
		editor := s.typedCardEditor(r, br, card, title, assignees, due, clearDue)
		s.boardFailed(w, r, br, err, editor)
		return
	}
	s.boardChanged(w, r, br, "#task-"+strconv.FormatUint(card.ID, 10), true)
}

// typedCardEditor makes the form of the card from what the person typed, to
// show it again after a submit that failed. When the members of the room
// cannot be read the picker offers only the people the card has.
func (s *Server) typedCardEditor(r *http.Request, br boardRequest, card app.TaskCard, title string, assignees []uint64, due string, clearDue bool) *views.BoardEditorData {
	members, err := s.svc.Rooms.Members(r.Context(), br.actor, br.id)
	if err != nil {
		s.logger.Debug("the members of the room are not read for the card form", "room_id", br.id, "err", err)
	}
	editor := views.NewBoardEditorData(card, members)
	editor.Title, editor.Assignees, editor.Due, editor.ClearDue = title, assignees, due, clearDue
	return &editor
}

// boardColumnAdd answers POST /rooms/{id}/board/columns with the form value
// "name": a column is added at the right end of the board.
func (s *Server) boardColumnAdd(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, true)
	if !ok {
		return
	}
	if _, err := s.svc.Board.AddColumn(r.Context(), br.actor, br.id, r.PostFormValue("name")); err != nil {
		s.boardFailed(w, r, br, err, nil)
		return
	}
	s.boardChanged(w, r, br, "", false, patchOp{
		component: views.BoardAddColumn(views.BoardData{Board: br.view}),
		options:   []datastar.PatchElementOption{datastar.WithModeReplace()},
	})
}

// boardColumnRename answers POST /rooms/{id}/board/columns/{columnID}/rename
// with the form value "name".
func (s *Server) boardColumnRename(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, true)
	if !ok {
		return
	}
	col, ok := s.boardColumn(w, r, br)
	if !ok {
		return
	}
	if err := s.svc.Board.RenameColumn(r.Context(), br.actor, col.ID, r.PostFormValue("name")); err != nil {
		s.boardFailed(w, r, br, err, nil)
		return
	}
	s.boardChanged(w, r, br, "#column-"+strconv.FormatUint(col.ID, 10), false)
}

// boardColumnMove answers POST /rooms/{id}/board/columns/{columnID}/move with the
// form value "position": the column goes to that place from the left (from 0).
func (s *Server) boardColumnMove(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, true)
	if !ok {
		return
	}
	col, ok := s.boardColumn(w, r, br)
	if !ok {
		return
	}
	position, ok := positionOf(r.PostFormValue("position"), -1)
	if !ok || position < 0 {
		s.boardFailed(w, r, br, errors.New(app.TagInvalidInput, "the position is not a place on the board",
			"fields", map[string]string{app.FieldForm: messageInvalid}), nil)
		return
	}
	if err := s.svc.Board.MoveColumn(r.Context(), br.actor, col.ID, position); err != nil {
		s.boardFailed(w, r, br, err, nil)
		return
	}
	s.boardChanged(w, r, br, "#column-"+strconv.FormatUint(col.ID, 10), false)
}

// boardColumnDelete answers POST /rooms/{id}/board/columns/{columnID}/delete. A
// column that has cards and the last column are refused with their messages
// at the top of the board.
func (s *Server) boardColumnDelete(w http.ResponseWriter, r *http.Request) {
	br, ok := s.boardStart(w, r, true)
	if !ok {
		return
	}
	col, ok := s.boardColumn(w, r, br)
	if !ok {
		return
	}
	if err := s.svc.Board.DeleteColumn(r.Context(), br.actor, col.ID); err != nil {
		s.boardFailed(w, r, br, err, nil)
		return
	}
	s.boardChanged(w, r, br, "", false)
}

// boardChanged answers an action on the board that worked. A Datastar request
// gets the columns as the server has them now (read again, so that they show
// the server's order), the message at the top cleared, the card form closed
// when closeEditor says so, and the extra patches. A plain post is sent back to
// the board, to anchor on it when it has one.
func (s *Server) boardChanged(w http.ResponseWriter, r *http.Request, br boardRequest, anchor string, closeEditor bool, extra ...patchOp) {
	if !isDatastar(r) {
		http.Redirect(w, r, views.BoardPath(br.id)+anchor, http.StatusSeeOther)
		return
	}
	view, err := s.boardView(r.Context(), br.actor, br.id)
	if err != nil {
		s.roomFail(w, r, err, br.id)
		return
	}
	d := views.BoardData{Board: view}
	ops := []patchOp{{component: views.BoardColumns(d)}, {component: views.BoardMessage("")}}
	if closeEditor {
		ops = append(ops, patchOp{component: views.BoardEditor(d)})
	}
	s.patchAll(w, r, append(ops, extra...)...)
}

// boardFailed shows the problem of a board action that failed. A room that is
// gone or hidden is the 404 page (a Datastar request is sent to it); a login
// that ended sends the person to the login page. Any other problem is a message
// at the top of the board, or, when the action was on the form of a card
// (editor is not nil), in that form with what was typed. A Datastar request
// gets only the message or the form patched in place, with status 200; a
// plain post gets the whole page with the status of statusOf.
func (s *Server) boardFailed(w http.ResponseWriter, r *http.Request, br boardRequest, err error, editor *views.BoardEditorData) {
	status := statusOf(err)
	if status == http.StatusFound {
		s.redirectToLogin(w, r)
		return
	}
	if isTag(err, app.TagNotFound) {
		s.roomFail(w, r, err, br.id)
		return
	}
	var problems views.FormErrors
	switch {
	case isTag(err, app.TagInvalidInput):
		problems = formErrorsOf(err)
		if editor == nil {
			problems.Form = firstNonEmpty(problems.Fields[app.FieldName], problems.Fields["column"], problems.Form, messageInvalid)
		}
	case isTag(err, app.TagConflict):
		problems.Form = conflictMessage(err)
	case isTag(err, app.TagForbidden):
		problems.Form = messageForbidden
	default:
		s.logFailure(r, err)
		problems.Form = messageTryAgain
	}
	var message string
	if editor != nil {
		editor.Errors = problems
	} else {
		message = problems.Form
	}
	if isDatastar(r) {
		ops := []patchOp{{component: views.BoardMessage(message)}}
		if editor != nil {
			ops = append(ops, patchOp{component: views.BoardEditor(views.BoardData{Board: br.view, Editor: editor})})
		}
		s.patchAll(w, r, ops...)
		return
	}
	d, ok := s.boardPageData(w, r, br, message, editor)
	if !ok {
		return
	}
	s.render(w, r, status, views.BoardPage(d))
}

// conflictMessage is the message for people of a TagConflict error of the
// board (the two refusals to delete a column); it is the message the service
// gives the error. An error without one gets the general text.
func conflictMessage(err error) string {
	if appErr, ok := errors.FromError(err); ok && appErr.Message != "" {
		return appErr.Message
	}
	return messageConflict
}
