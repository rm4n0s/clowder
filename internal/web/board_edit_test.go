package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// cardPath is the path of an action on a card of general.
func (e *roomsEnv) cardPath(card app.TaskCard, action string) string {
	suffix := "/tasks/" + idText(card.ID)
	if action != "" {
		suffix += "/" + action
	}
	return e.boardURL("general", suffix)
}

func TestBoardTaskEditForm(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		s := e.seedBoard()
		rec := e.getAs("alice", e.cardPath(s.late, "edit"), datastar)
		want := []string{
			`id="board-card-form"`, `action="` + e.cardPath(s.late, "") + `"`, `id="card-title-` + idText(s.late.ID) + `" name="title" type="text" value="Late"`,
			// Everyone in the room is offered, the assignee is selected.
			`<option value="` + idText(e.alice.ID) + `">Alice (@alice)</option>`, `<option value="` + idText(e.bob.ID) + `" selected>Bob (@bob)</option>`,
			`name="due" type="date" value="` + pastDue + `"`, `name="clear_due" type="checkbox" value="1"`, "Remove the due date",
			`>Cancel</a>`,
		}
		if datastar {
			requireSSE(t, rec)
			// The form goes to its own panel, not to the columns.
			requireBody(t, rec, append(want, `id="board-editor"`), `id="board-columns"`)
			return
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		// Without JavaScript the whole page comes with the form open.
		requireBody(t, rec, append(want, `<aside id="sidebar">`, `id="board-columns"`))
	})
}

func TestBoardTaskEditFormKeepsAFormerMember(t *testing.T) {
	e := newRoomsEnv(t)
	s := e.seedBoard()
	if err := e.rooms.RemoveMember(t.Context(), e.actor(e.admin), e.general.ID, e.bob.ID); err != nil {
		t.Fatalf("RemoveMember: %+v", err)
	}
	rec := e.getAs("alice", e.cardPath(s.late, "edit"), true)
	requireBody(t, rec, []string{`<option value="` + idText(e.bob.ID) + `" selected>Bob (@bob, left the room)</option>`})
	// Another card does not offer him.
	rec = e.getAs("alice", e.cardPath(s.soon, "edit"), true)
	requireBody(t, rec, []string{`<option value="` + idText(e.alice.ID) + `">`}, "left the room")
}

func TestBoardTaskEditFormFailures(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		s := e.seedBoard()
		// A person who has not joined may not open it.
		rec := e.getAs("carol", e.cardPath(s.late, "edit"), datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="board-message"`, messageForbidden}, `id="board-card-form"`)
		} else {
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			requireBody(t, rec, []string{messageForbidden}, `id="board-card-form"`)
		}
		// A card that is not on this board.
		hidden := e.writeTasks("alice", "secret", "x", app.TaskInput{Title: "Elsewhere"})
		rec = e.getAs("alice", e.boardURL("general", "/tasks/"+idText(hidden.Tasks[0].ID)+"/edit"), datastar)
		if datastar {
			requireBody(t, rec, []string{messageCardGone})
		} else if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

// cardByID returns the card with the ID, found on the board of general.
func (e *roomsEnv) cardByID(id uint64) app.TaskCard {
	e.t.Helper()
	for _, c := range e.boardOf("alice", "general").Columns {
		for _, t := range c.Tasks {
			if t.ID == id {
				return t
			}
		}
	}
	e.t.Fatalf("no card %d", id)
	return app.TaskCard{}
}

func TestBoardTaskUpdate(t *testing.T) {
	tests := []struct {
		name  string
		card  func(s boardSeed) app.TaskCard
		form  func(e *roomsEnv, s boardSeed) url.Values
		check func(t *testing.T, e *roomsEnv, got app.TaskCard)
	}{
		{
			name: "a new title",
			card: func(s boardSeed) app.TaskCard { return s.plain },
			form: func(e *roomsEnv, s boardSeed) url.Values { return url.Values{"title": {"  Plain, renamed "}} },
			check: func(t *testing.T, e *roomsEnv, got app.TaskCard) {
				if got.Title != "Plain, renamed" {
					t.Errorf("title = %q", got.Title)
				}
			},
		},
		{
			name: "assign two people",
			card: func(s boardSeed) app.TaskCard { return s.plain },
			form: func(e *roomsEnv, s boardSeed) url.Values {
				return url.Values{"title": {"Plain"}, "assignees": {idText(e.alice.ID), idText(e.bob.ID)}}
			},
			check: func(t *testing.T, e *roomsEnv, got app.TaskCard) {
				if names := assigneeNamesOf(got); !slices.Equal(names, []string{"alice", "bob"}) {
					t.Errorf("assignees = %v", names)
				}
			},
		},
		{
			name: "nobody selected unassigns everybody",
			card: func(s boardSeed) app.TaskCard { return s.late },
			form: func(e *roomsEnv, s boardSeed) url.Values { return url.Values{"title": {"Late"}, "due": {pastDue}} },
			check: func(t *testing.T, e *roomsEnv, got app.TaskCard) {
				if len(got.Assignees) != 0 {
					t.Errorf("assignees = %v, want none", assigneeNamesOf(got))
				}
			},
		},
		{
			name: "a due date is set",
			card: func(s boardSeed) app.TaskCard { return s.plain },
			form: func(e *roomsEnv, s boardSeed) url.Values { return url.Values{"title": {"Plain"}, "due": {futureDue}} },
			check: func(t *testing.T, e *roomsEnv, got app.TaskCard) {
				if got.DueDate == nil || got.DueDate.Format(dueLayout) != futureDue {
					t.Errorf("due date = %v, want %s", got.DueDate, futureDue)
				}
			},
		},
		{
			name: "the due date is cleared, whatever the date field holds",
			card: func(s boardSeed) app.TaskCard { return s.late },
			form: func(e *roomsEnv, s boardSeed) url.Values {
				return url.Values{"title": {"Late"}, "assignees": {idText(e.bob.ID)}, "due": {pastDue}, "clear_due": {"1"}}
			},
			check: func(t *testing.T, e *roomsEnv, got app.TaskCard) {
				if got.DueDate != nil || got.Overdue {
					t.Errorf("due date = %v, want none", got.DueDate)
				}
			},
		},
		{
			name: "an empty date field keeps the due date",
			card: func(s boardSeed) app.TaskCard { return s.late },
			form: func(e *roomsEnv, s boardSeed) url.Values {
				return url.Values{"title": {"Late"}, "assignees": {idText(e.bob.ID)}}
			},
			check: func(t *testing.T, e *roomsEnv, got app.TaskCard) {
				if got.DueDate == nil || got.DueDate.Format(dueLayout) != pastDue {
					t.Errorf("due date = %v, want %s kept", got.DueDate, pastDue)
				}
			},
		},
		{
			name: "everything at once",
			card: func(s boardSeed) app.TaskCard { return s.soon },
			form: func(e *roomsEnv, s boardSeed) url.Values {
				return url.Values{"title": {"Sooner"}, "assignees": {idText(e.alice.ID)}, "due": {pastDue}}
			},
			check: func(t *testing.T, e *roomsEnv, got app.TaskCard) {
				if got.Title != "Sooner" || !slices.Equal(assigneeNamesOf(got), []string{"alice"}) || got.DueDate.Format(dueLayout) != pastDue || !got.Overdue {
					t.Errorf("card = %+v", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				s := e.seedBoard()
				card := tt.card(s)
				rec := e.post("bob", e.cardPath(card, ""), tt.form(e, s), datastar)
				tt.check(t, e, e.cardByID(card.ID))
				if !datastar {
					if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != e.boardURL("general", "#task-"+idText(card.ID)) {
						t.Errorf("answer = %d to %q, want 303 to the card on the board", rec.Code, rec.Header().Get("Location"))
					}
					return
				}
				requireSSE(t, rec)
				// The columns as they are now, the message cleared and the form
				// closed (an empty panel).
				requireBody(t, rec, []string{`id="board-columns"`, `id="board-message" class="board-message"></div>`, `<div id="board-editor" class="board-editor"></div>`}, `id="board-card-form"`)
			})
		})
	}
}

func TestBoardTaskUpdateKeepsAFormerMember(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		s := e.seedBoard()
		if err := e.rooms.RemoveMember(t.Context(), e.actor(e.admin), e.general.ID, e.bob.ID); err != nil {
			t.Fatalf("RemoveMember: %+v", err)
		}
		// Bob is on Late; the form still offers him, so he is sent back.
		rec := e.post("alice", e.cardPath(s.late, ""), url.Values{
			"title": {"Late, still"}, "assignees": {idText(e.bob.ID), idText(e.alice.ID)}, "due": {pastDue},
		}, datastar)
		got := e.cardByID(s.late.ID)
		if got.Title != "Late, still" || !slices.Equal(assigneeNamesOf(got), []string{"bob", "alice"}) {
			t.Errorf("card = %+v (answer %d), want the former member kept next to the new one", got, rec.Code)
		}
		// Somebody who was never on the card and is not a member is refused.
		rec = e.post("alice", e.cardPath(s.plain, ""), url.Values{"title": {"Plain"}, "assignees": {idText(e.carol.ID)}}, datastar)
		if len(e.cardByID(s.plain.ID).Assignees) != 0 {
			t.Errorf("carol was assigned (answer %d)", rec.Code)
		}
	})
}

func TestBoardTaskUpdateMistakes(t *testing.T) {
	tests := []struct {
		name string
		form func(e *roomsEnv) url.Values
		want []string
	}{
		{
			name: "no title",
			form: func(e *roomsEnv) url.Values { return url.Values{"title": {"  "}, "due": {futureDue}} },
			want: []string{"Write a title of 1 to 200 characters.", `aria-invalid="true"`, `name="due" type="date" value="` + futureDue + `"`},
		},
		{
			name: "a title that is too long",
			form: func(e *roomsEnv) url.Values { return url.Values{"title": {strings.Repeat("x", 201)}} },
			want: []string{"Write a title of 1 to 200 characters.", `value="` + strings.Repeat("x", 201) + `"`},
		},
		{
			name: "an assignee who is not a member",
			form: func(e *roomsEnv) url.Values {
				return url.Values{"title": {"Mine now"}, "assignees": {idText(e.carol.ID), idText(e.bob.ID)}}
			},
			want: []string{"Assign only members of the room.", `value="Mine now"`},
		},
		{
			name: "an assignee that is not an ID",
			form: func(e *roomsEnv) url.Values { return url.Values{"title": {"Odd"}, "assignees": {"x"}} },
			want: []string{"Assign only members of the room.", `value="Odd"`},
		},
		{
			name: "a due date that is not a date",
			form: func(e *roomsEnv) url.Values { return url.Values{"title": {"Later"}, "due": {"next week"}} },
			want: []string{messageDueDate, `id="card-due-`, `value="next week"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				s := e.seedBoard()
				before := e.cardByID(s.plain.ID)
				rec := e.post("alice", e.cardPath(s.plain, ""), tt.form(e), datastar)
				want := append([]string{`id="board-card-form"`, `class="field-error"`}, tt.want...)
				if datastar {
					requireSSE(t, rec)
					// The form is patched in its panel, not the columns.
					requireBody(t, rec, append(want, `id="board-editor"`), `id="board-columns"`)
				} else {
					if rec.Code != http.StatusUnprocessableEntity {
						t.Fatalf("status = %d, want 422", rec.Code)
					}
					requireBody(t, rec, append(want, `<aside id="sidebar">`, `id="board-columns"`))
				}
				if got := e.cardByID(s.plain.ID); got.Title != before.Title || len(got.Assignees) != 0 || got.DueDate != nil {
					t.Errorf("card changed to %+v", got)
				}
			})
		})
	}
}

func TestBoardTaskUpdateTyped(t *testing.T) {
	// A failed save shows the form with what was typed: the title, the people
	// ticked, the date and the box.
	e := newRoomsEnv(t)
	s := e.seedBoard()
	rec := e.post("alice", e.cardPath(s.plain, ""), url.Values{
		"title": {""}, "assignees": {idText(e.alice.ID)}, "due": {futureDue}, "clear_due": {"1"},
	}, true)
	requireBody(t, rec, []string{
		`<option value="` + idText(e.alice.ID) + `" selected>`, `<option value="` + idText(e.bob.ID) + `">`,
		`value="` + futureDue + `"`, `name="clear_due" type="checkbox" value="1" checked`,
	})
}

func TestBoardTaskUpdateByAPersonWhoHasNotJoined(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		s := e.seedBoard()
		rec := e.post("carol", e.cardPath(s.plain, ""), url.Values{"title": {"Mine"}}, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="board-card-form"`, messageForbidden})
		} else if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
		if got := e.cardByID(s.plain.ID).Title; got != "Plain" {
			t.Errorf("title = %q", got)
		}
	})
}
