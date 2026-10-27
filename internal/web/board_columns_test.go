package web

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
)

// columnPath is the path of an action on a column of general.
func (e *roomsEnv) columnPath(id uint64, action string) string {
	return e.boardURL("general", "/columns/"+idText(id)+"/"+action)
}

// columnIDOf returns the ID of the column with the name on the board of general.
func (e *roomsEnv) columnIDOf(name string) uint64 {
	e.t.Helper()
	for _, c := range e.boardOf("alice", "general").Columns {
		if c.Name == name {
			return c.ID
		}
	}
	e.t.Fatalf("no column %q", name)
	return 0
}

func TestBoardColumnAdd(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		rec := e.post("bob", e.boardURL("general", "/columns"), url.Values{"name": {"  Review "}}, datastar)
		if got := e.columnNames("general"); !slices.Equal(got, []string{"To do", "In progress", "Done", "Review"}) {
			t.Fatalf("columns = %v", got)
		}
		if !datastar {
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != e.boardURL("general", "") {
				t.Errorf("answer = %d to %q, want 303 to the board", rec.Code, rec.Header().Get("Location"))
			}
			return
		}
		requireSSE(t, rec)
		// The columns, with the new one on the right, and the add form fresh.
		requireBody(t, rec, []string{`aria-label="Review"`, `id="board-columns"`, `id="board-add-form"`, `data: mode replace`, `aria-label="Done"`})
		requireOrder(t, rec.Body.String(), `aria-label="Done"`, `aria-label="Review"`)
	})
}

func TestBoardColumnMistakes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		path   func(e *roomsEnv) string
		form   url.Values
		status int
		want   string
	}{
		{"add an empty name", func(e *roomsEnv) string { return e.boardURL("general", "/columns") }, url.Values{"name": {"   "}}, http.StatusUnprocessableEntity, "Use 1 to 40 characters."},
		{"add a name that is too long", func(e *roomsEnv) string { return e.boardURL("general", "/columns") }, url.Values{"name": {"123456789012345678901234567890123456789012"}}, http.StatusUnprocessableEntity, "Use 1 to 40 characters."},
		{"add a name that exists in another case", func(e *roomsEnv) string { return e.boardURL("general", "/columns") }, url.Values{"name": {"DONE"}}, http.StatusUnprocessableEntity, "A column with this name exists."},
		{"rename to the name of another column", func(e *roomsEnv) string { return e.columnPath(e.columnIDOf("To do"), "rename") }, url.Values{"name": {"done"}}, http.StatusUnprocessableEntity, "A column with this name exists."},
		{"rename to nothing", func(e *roomsEnv) string { return e.columnPath(e.columnIDOf("To do"), "rename") }, url.Values{"name": {""}}, http.StatusUnprocessableEntity, "Use 1 to 40 characters."},
		{"move to a place that is not a number", func(e *roomsEnv) string { return e.columnPath(e.columnIDOf("To do"), "move") }, url.Values{"position": {"left"}}, http.StatusUnprocessableEntity, messageInvalid},
		{"move to no place", func(e *roomsEnv) string { return e.columnPath(e.columnIDOf("To do"), "move") }, url.Values{}, http.StatusUnprocessableEntity, messageInvalid},
		{"move to a negative place", func(e *roomsEnv) string { return e.columnPath(e.columnIDOf("In progress"), "move") }, url.Values{"position": {"-1"}}, http.StatusUnprocessableEntity, messageInvalid},
		{"delete a column that has cards", func(e *roomsEnv) string { return e.columnPath(e.columnIDOf("To do"), "delete") }, nil, http.StatusConflict, "Move its cards first."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				e.seedBoard()
				names := e.columnNames("general")
				rec := e.post("alice", tt.path(e), tt.form, datastar)
				if datastar {
					// 200 and the message at the top of the board.
					requireSSE(t, rec)
					requireBody(t, rec, []string{`id="board-message"`, `class="form-error" role="alert">` + tt.want + `</p>`}, `id="board-columns"`)
				} else {
					if rec.Code != tt.status {
						t.Fatalf("status = %d, want %d", rec.Code, tt.status)
					}
					requireBody(t, rec, []string{`class="form-error" role="alert">` + tt.want + `</p>`, `aria-label="To do"`})
				}
				if got := e.columnNames("general"); !slices.Equal(got, names) {
					t.Errorf("columns changed to %v", got)
				}
			})
		})
	}
}

func TestBoardColumnRename(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		e.seedBoard()
		id := e.columnIDOf("In progress")
		rec := e.post("bob", e.columnPath(id, "rename"), url.Values{"name": {" Doing "}}, datastar)
		if got := e.columnNames("general"); !slices.Equal(got, []string{"To do", "Doing", "Done"}) {
			t.Fatalf("columns = %v", got)
		}
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`aria-label="Doing"`, `value="Doing"`}, "In progress")
		} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != e.boardURL("general", "#column-"+idText(id)) {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
		// Another case of its own name is allowed.
		e.post("bob", e.columnPath(id, "rename"), url.Values{"name": {"DOING"}}, datastar)
		if got := e.columnNames("general"); !slices.Equal(got, []string{"To do", "DOING", "Done"}) {
			t.Errorf("columns = %v", got)
		}
	})
}

func TestBoardColumnMove(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		column string
		pos    string
		want   []string
	}{
		{"right", "To do", "1", []string{"In progress", "To do", "Done"}},
		{"to the end", "To do", "2", []string{"In progress", "Done", "To do"}},
		{"left", "Done", "1", []string{"To do", "Done", "In progress"}},
		{"to the start", "Done", "0", []string{"Done", "To do", "In progress"}},
		{"past the end is the end", "To do", "50", []string{"In progress", "Done", "To do"}},
		{"to where it is", "In progress", "1", []string{"To do", "In progress", "Done"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				e.seedBoard()
				id := e.columnIDOf(tt.column)
				rec := e.post("alice", e.columnPath(id, "move"), url.Values{"position": {tt.pos}}, datastar)
				if got := e.columnNames("general"); !slices.Equal(got, tt.want) {
					t.Errorf("columns = %v, want %v", got, tt.want)
				}
				if datastar {
					requireSSE(t, rec)
					// The answer shows the columns in the new order; the cards come along.
					var order []string
					for _, n := range tt.want {
						order = append(order, `aria-label="`+n+`"`)
					}
					requireOrder(t, rec.Body.String(), order...)
					requireBody(t, rec, []string{`<p class="card-title">Late</p>`})
				}
			})
		})
	}
}

func TestBoardColumnDelete(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		e.seedBoard()
		// An empty column goes.
		rec := e.post("alice", e.columnPath(e.columnIDOf("In progress"), "delete"), nil, datastar)
		if got := e.columnNames("general"); !slices.Equal(got, []string{"To do", "Done"}) {
			t.Fatalf("columns = %v", got)
		}
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`aria-label="To do"`, `aria-label="Done"`}, "In progress")
		} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != e.boardURL("general", "") {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
		// The card goes to Done; To do is empty now and goes too.
		e.post("alice", e.cardPath(e.cardTitled("general", "Late"), "move"), url.Values{"column": {idText(e.columnIDOf("Done"))}}, datastar)
		for _, title := range []string{"Soon", "Plain", "Comment task"} {
			e.post("alice", e.cardPath(e.cardTitled("general", title), "move"), url.Values{"column": {idText(e.columnIDOf("Done"))}}, datastar)
		}
		e.post("alice", e.columnPath(e.columnIDOf("To do"), "delete"), nil, datastar)
		if got := e.columnNames("general"); !slices.Equal(got, []string{"Done"}) {
			t.Fatalf("columns = %v", got)
		}
		// The last column stays, with its cards or not.
		rec = e.post("alice", e.columnPath(e.columnIDOf("Done"), "delete"), nil, datastar)
		if got := e.columnNames("general"); !slices.Equal(got, []string{"Done"}) {
			t.Fatalf("columns = %v", got)
		}
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{"A board needs at least one column."})
		} else if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rec.Code)
		} else {
			requireBody(t, rec, []string{"A board needs at least one column."})
		}
	})
}

func TestBoardColumnActionsOfAPersonWhoHasNotJoined(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		e.seedBoard()
		id := e.columnIDOf("To do")
		actions := []struct {
			name string
			path string
			form url.Values
		}{
			{"add", e.boardURL("general", "/columns"), url.Values{"name": {"Mine"}}},
			{"rename", e.columnPath(id, "rename"), url.Values{"name": {"Mine"}}},
			{"move", e.columnPath(id, "move"), url.Values{"position": {"2"}}},
			{"delete", e.columnPath(e.columnIDOf("In progress"), "delete"), nil},
		}
		for _, a := range actions {
			t.Run(a.name, func(t *testing.T) {
				rec := e.post("carol", a.path, a.form, datastar)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{messageForbidden})
				} else if rec.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", rec.Code)
				}
			})
		}
		if got := e.columnNames("general"); !slices.Equal(got, []string{"To do", "In progress", "Done"}) {
			t.Errorf("columns = %v", got)
		}
	})
}

func TestBoardColumnThatIsGone(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		e.seedBoard()
		other := e.boardOf("alice", "secret").Columns[0].ID
		for _, id := range []string{idText(other), "9999", "x"} {
			for _, action := range []string{"rename", "move", "delete"} {
				rec := e.post("alice", e.boardURL("general", "/columns/"+id+"/"+action), url.Values{"name": {"x"}, "position": {"0"}}, datastar)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{messageColumnGone, `id="board-columns"`})
				} else if rec.Code != http.StatusNotFound {
					t.Errorf("%s %s: status = %d, want 404", action, id, rec.Code)
				}
			}
		}
		if got := e.columnNames("secret"); !slices.Equal(got, []string{"To do", "In progress", "Done"}) {
			t.Errorf("the other board changed: %v", got)
		}
	})
}
