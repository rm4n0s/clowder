package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

const (
	// messageFieldMissing is the problem with a field of a body that must be
	// there and is not.
	messageFieldMissing = "This field is required."
	// messageTaskDueInvalid is the problem with the due date of a card.
	messageTaskDueInvalid = "Write the due date as YYYY-MM-DD."
)

// columnRequest is the body of a new column and of a rename.
type columnRequest struct {
	Name string `json:"name"`
}

// moveColumnRequest is the body of POST /columns/{id}/move.
type moveColumnRequest struct {
	Position *int `json:"position"`
}

// moveTaskRequest is the body of POST /tasks/{id}/move.
type moveTaskRequest struct {
	ColumnID *uint64 `json:"column_id"`
	Position *int    `json:"position"`
}

// optionalDate is the field due_date of a body, which is a day, null or not
// there: Set is true when the field is in the body, and Value is nil for null.
type optionalDate struct {
	Set   bool
	Value *string
}

// UnmarshalJSON reads the field; it is called only when the field is there,
// and with null for null.
func (d *optionalDate) UnmarshalJSON(data []byte) error {
	d.Set = true
	if string(data) == "null" {
		d.Value = nil
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		// Name the field, which the decoder does not do for a type of its own.
		return &json.UnmarshalTypeError{Value: "non-string", Type: reflect.TypeFor[string](), Field: "due_date"}
	}
	d.Value = &text
	return nil
}

// taskPatchRequest is the body of PATCH /tasks/{id}. What is not there stays
// as it is; "due_date": null clears the due date and "assignee_ids": []
// unassigns everybody.
type taskPatchRequest struct {
	AssigneeIDs *[]uint64    `json:"assignee_ids"`
	DueDate     optionalDate `json:"due_date"`
}

// boardRoutes are the routes of the board, its columns and its cards.
func (s *Server) boardRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/rooms/{id}/board", false, s.board),
		s.route(http.MethodPost, "/rooms/{id}/columns", false, s.addColumn),
		s.route(http.MethodPatch, "/columns/{id}", false, s.renameColumn),
		s.route(http.MethodPost, "/columns/{id}/move", false, s.moveColumn),
		s.route(http.MethodDelete, "/columns/{id}", false, s.deleteColumn),
		s.route(http.MethodPatch, "/tasks/{id}", false, s.patchTask),
		s.route(http.MethodPost, "/tasks/{id}/move", false, s.moveTask),
	}
}

// board answers GET /rooms/{id}/board: the columns of the board of a room the
// caller may read, left to right, each with its cards from top to bottom. A
// non-member of a visible room reads it but cannot change it.
func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.svc.Board.Board(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := boardResponse{RoomID: view.Room.ID, Columns: make([]columnJSON, len(view.Columns))}
	for i, col := range view.Columns {
		out.Columns[i] = columnOf(col)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// addColumn answers POST /rooms/{id}/columns: a member adds a column at the
// right end of the board.
func (s *Server) addColumn(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body columnRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	col, err := s.svc.Board.AddColumn(r.Context(), c.actor, id, body.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", Prefix+"/rooms/"+strconv.FormatUint(id, 10)+"/board")
	s.writeJSON(w, r, http.StatusCreated, columnOf(col))
}

// renameColumn answers PATCH /columns/{id}: a member renames the column and
// gets it back.
func (s *Server) renameColumn(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body columnRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Board.RenameColumn(r.Context(), c.actor, id, body.Name); err != nil {
		s.writeError(w, r, err)
		return
	}
	col, err := s.svc.Board.Column(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, columnOf(col))
}

// moveColumn answers POST /columns/{id}/move: a member puts the column at the
// position (from 0, the left; a position past the end is the end).
func (s *Server) moveColumn(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body moveColumnRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	position, err := columnPosition(body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Board.MoveColumn(r.Context(), c.actor, id, position); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// deleteColumn answers DELETE /columns/{id}: a member removes a column that
// has no cards and is not the last one (409 conflict otherwise).
func (s *Server) deleteColumn(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Board.DeleteColumn(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// patchTask answers PATCH /tasks/{id}: a member changes the assignees and the
// due date of a card and gets it back. What the body leaves out stays; the
// title is changed by editing the post (SPEC D17).
func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body taskPatchRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	patch, err := taskPatchOf(body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Board.UpdateTask(r.Context(), c.actor, id, patch); err != nil {
		s.writeError(w, r, err)
		return
	}
	card, err := s.svc.Board.Task(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, boardTaskOf(card))
}

// moveTask answers POST /tasks/{id}/move: a member puts the card in a column
// of its room at the position (from 0, the top; a position past the end is the
// end).
func (s *Server) moveTask(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body moveTaskRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	columnID, position, err := cardTarget(body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Board.MoveTask(r.Context(), c.actor, id, columnID, position); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// columnPosition returns the position of a request to move a column; it fails
// with tagFieldMissing when the body has none.
func columnPosition(body moveColumnRequest) (int, error) {
	if body.Position == nil {
		return 0, errors.New(tagFieldMissing, "position is missing",
			"fields", map[string]string{"position": messageFieldMissing})
	}
	return *body.Position, nil
}

// cardTarget returns the column and the position of a request to move a card;
// it fails with tagFieldMissing, naming each field that is not there.
func cardTarget(body moveTaskRequest) (columnID uint64, position int, err error) {
	if body.ColumnID == nil || body.Position == nil {
		fields := map[string]string{}
		if body.ColumnID == nil {
			fields["column_id"] = messageFieldMissing
		}
		if body.Position == nil {
			fields["position"] = messageFieldMissing
		}
		return 0, 0, errors.New(tagFieldMissing, "column_id or position is missing", "fields", fields)
	}
	return *body.ColumnID, *body.Position, nil
}

// taskPatchOf makes the patch of the service from the body of PATCH
// /tasks/{id}: what is not there stays, a due date that is null is cleared.
// A due date that is not a day fails with tagTaskDueInvalid.
func taskPatchOf(body taskPatchRequest) (app.TaskPatch, error) {
	patch := app.TaskPatch{Assignees: body.AssigneeIDs}
	switch {
	case !body.DueDate.Set:
	case body.DueDate.Value == nil:
		patch.ClearDueDate = true
	default:
		due, err := time.Parse(dateLayout, *body.DueDate.Value)
		if err != nil {
			return app.TaskPatch{}, errors.New(tagTaskDueInvalid, "the due date is not a day",
				"fields", map[string]string{"due_date": messageTaskDueInvalid})
		}
		patch.DueDate = &due
	}
	return patch, nil
}

const (
	tagFieldMissing   = "FieldMissing"
	tagTaskDueInvalid = "TaskDueInvalid"
)
