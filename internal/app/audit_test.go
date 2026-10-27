package app_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// auditEnv is the audit service on a database with an admin, two members and
// the fake clock.
type auditEnv struct {
	*env
	audit *app.Audit
	admin app.User
	alice app.User
	bob   app.User
}

func newAuditEnv(t *testing.T) *auditEnv {
	t.Helper()
	e := newEnv(t)
	return &auditEnv{
		env:   e,
		audit: app.NewAudit(e.st),
		admin: e.signUp("root", store.RoleAdmin),
		alice: e.signUp("alice", store.RoleMember),
		bob:   e.signUp("bob", store.RoleMember),
	}
}

// record writes an event in its own committed transaction.
func (e *auditEnv) record(ev app.AuditEntry) {
	e.t.Helper()
	err := e.st.Tx(e.t.Context(), func(tx *store.Store) error {
		return app.RecordAudit(e.t.Context(), tx, ev)
	})
	if err != nil {
		e.t.Fatalf("recordAudit: %+v", err)
	}
}

// events returns the first page of the log for the admin, failing on error.
func (e *auditEnv) events(filter app.AuditFilter, page int) app.AuditPage {
	e.t.Helper()
	p, err := e.audit.Events(e.t.Context(), actor(e.admin), filter, page)
	if err != nil {
		e.t.Fatalf("Events: %+v", err)
	}
	return p
}

func summaries(p app.AuditPage) []string {
	out := []string{}
	for _, ev := range p.Events {
		out = append(out, ev.Summary)
	}
	return out
}

func TestRecordAuditCommits(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	e.record(app.AuditEntry{
		ActorID: e.alice.ID, Action: app.ActionMemberAdded, RoomID: 4,
		EntityType: app.EntityMember, EntityID: e.bob.ID,
		Summary: "Alice added Bob to #design",
		Details: map[string]any{"userID": e.bob.ID, "role": map[string]any{"old": nil, "new": "member"}},
	})

	rows, err := e.st.ListAuditEvents(t.Context(), store.AuditFilter{}, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("events = %v, %v, want one", rows, err)
	}
	ev := rows[0]
	if ev.ActorID != e.alice.ID || ev.Action != store.ActionMemberAdded || ev.RoomID != 4 ||
		ev.EntityType != store.EntityMember || ev.EntityID != e.bob.ID || ev.Summary != "Alice added Bob to #design" {
		t.Errorf("event = %+v", ev)
	}
	if !ev.CreatedAt.Equal(start) {
		t.Errorf("CreatedAt = %v, want the time of the clock %v", ev.CreatedAt, start)
	}
	want := fmt.Sprintf(`{"role":{"new":"member","old":null},"userID":%d}`, e.bob.ID)
	if ev.Details != want {
		t.Errorf("Details = %s, want %s", ev.Details, want)
	}
}

func TestRecordAuditNilDetails(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	e.record(app.AuditEntry{ActorID: e.alice.ID, Action: app.ActionRoomCreated, RoomID: 1, EntityType: app.EntityRoom, EntityID: 1, Summary: "s"})
	p := e.events(app.AuditFilter{}, 1)
	if len(p.Events) != 1 || p.Events[0].Details == nil || len(p.Events[0].Details) != 0 {
		t.Errorf("events = %+v, want one with empty, non-nil details", p.Events)
	}
}

func TestRecordAuditRollsBack(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	var boom error = errors.New("ChangeFailed", "the change failed")
	err := e.st.Tx(t.Context(), func(tx *store.Store) error {
		if err := app.RecordAudit(t.Context(), tx, app.AuditEntry{
			ActorID: e.alice.ID, Action: app.ActionPostCreated, RoomID: 1,
			EntityType: app.EntityPost, EntityID: 1, Summary: "Alice posted",
		}); err != nil {
			return err
		}
		return boom
	})
	if err != boom {
		t.Fatalf("Tx returned %v, want the error of the function", err)
	}
	if p := e.events(app.AuditFilter{}, 1); len(p.Events) != 0 {
		t.Errorf("a rolled back transaction left %d events", len(p.Events))
	}
}

func TestRecordAuditErrors(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	tests := []struct {
		name      string
		entry     app.AuditEntry
		wantTag   string
		wantRoute string
	}{
		{
			name: "details that are not JSON",
			entry: app.AuditEntry{
				ActorID: e.alice.ID, Action: app.ActionPostCreated, RoomID: 1, EntityType: app.EntityPost, EntityID: 1,
				Summary: "s", Details: map[string]any{"bad": make(chan int)},
			},
			wantTag: app.TagAuditDetailsFailed, wantRoute: "recordAudit.AuditDetailsFailed",
		},
		{
			name: "unknown actor",
			entry: app.AuditEntry{
				ActorID: 9999, Action: app.ActionPostCreated, RoomID: 1, EntityType: app.EntityPost, EntityID: 1, Summary: "s",
			},
			wantTag: store.TagQueryFailed, wantRoute: "recordAudit->Store.CreateAuditEvent.QueryFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := e.st.Tx(t.Context(), func(tx *store.Store) error {
				return app.RecordAudit(t.Context(), tx, tt.entry)
			})
			_ = requireTag(t, err, tt.wantTag, tt.wantRoute)
			if p := e.events(app.AuditFilter{}, 1); len(p.Events) != 0 {
				t.Errorf("a failed event left %d events", len(p.Events))
			}
		})
	}
}

func TestBodyChangeDetailsHoldNoText(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	tests := []struct {
		name       string
		body       string
		wantLength float64
	}{
		{name: "ascii", body: "my secret plan", wantLength: 14},
		{name: "characters, not bytes", body: "héllo wörld ✓", wantLength: 13},
		{name: "empty", body: "", wantLength: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.record(app.AuditEntry{
				ActorID: e.alice.ID, Action: app.ActionPostEdited, RoomID: 1, EntityType: app.EntityPost, EntityID: 3,
				Summary: "Alice edited a post", Details: app.BodyChangeDetails(tt.body),
			})
			rows, err := e.st.ListAuditEvents(t.Context(), store.AuditFilter{}, 1, 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("events = %v, %v", rows, err)
			}
			wantJSON := fmt.Sprintf(`{"bodyChanged":true,"length":%d}`, int(tt.wantLength))
			if rows[0].Details != wantJSON {
				t.Errorf("Details = %s, want %s", rows[0].Details, wantJSON)
			}
			if tt.body != "" && strings.Contains(rows[0].Details, tt.body) {
				t.Errorf("Details %s holds the text of the body", rows[0].Details)
			}
			view := e.events(app.AuditFilter{}, 1).Events[0]
			want := map[string]any{"bodyChanged": true, "length": tt.wantLength}
			if !reflect.DeepEqual(view.Details, want) {
				t.Errorf("decoded details = %#v, want %#v", view.Details, want)
			}
		})
	}
}

func TestAuditDetailsRoundTrip(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	details := map[string]any{
		"title":     map[string]any{"old": "Draft", "new": "Final"},
		"assignees": map[string]any{"old": []any{}, "new": []any{"alice", "bob"}},
		"due":       map[string]any{"old": nil, "new": "2026-11-01"},
		"nested":    map[string]any{"a": map[string]any{"b": map[string]any{"c": true}}},
		"count":     float64(3),
		"text":      "quotes \" and <tags> and ünïcode",
	}
	e.record(app.AuditEntry{
		ActorID: e.alice.ID, Action: app.ActionTaskEdited, RoomID: 1, EntityType: app.EntityTask, EntityID: 5,
		Summary: "Alice edited a task", Details: details,
	})
	got := e.events(app.AuditFilter{}, 1).Events[0].Details
	if !reflect.DeepEqual(got, details) {
		t.Errorf("details = %#v, want %#v", got, details)
	}
}

func TestAuditEventsFilters(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	type seed struct {
		actor   app.User
		room    uint64
		action  app.AuditAction
		entity  app.EntityType
		minutes int
		summary string
	}
	seeds := []seed{
		{e.alice, 1, app.ActionRoomCreated, app.EntityRoom, 0, "e1"},
		{e.bob, 1, app.ActionPostCreated, app.EntityPost, 10, "e2"},
		{e.alice, 2, app.ActionPostCreated, app.EntityComment, 20, "e3"},
		{e.bob, 2, app.ActionTaskMoved, app.EntityTask, 30, "e4"},
		{e.alice, 1, app.ActionMemberAdded, app.EntityMember, 40, "e5"},
	}
	for _, s := range seeds {
		e.clk.Set(start.Add(time.Duration(s.minutes) * time.Minute))
		e.record(app.AuditEntry{
			ActorID: s.actor.ID, Action: s.action, RoomID: s.room, EntityType: s.entity, EntityID: 1, Summary: s.summary,
		})
	}
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }

	tests := []struct {
		name   string
		filter app.AuditFilter
		want   []string
	}{
		{name: "no filter, newest first", want: []string{"e5", "e4", "e3", "e2", "e1"}},
		{name: "room", filter: app.AuditFilter{RoomID: 1}, want: []string{"e5", "e2", "e1"}},
		{name: "actor", filter: app.AuditFilter{ActorID: e.bob.ID}, want: []string{"e4", "e2"}},
		{name: "entity type", filter: app.AuditFilter{EntityType: app.EntityComment}, want: []string{"e3"}},
		{name: "action", filter: app.AuditFilter{Action: app.ActionPostCreated}, want: []string{"e3", "e2"}},
		{name: "from", filter: app.AuditFilter{From: at(30)}, want: []string{"e5", "e4"}},
		{name: "to", filter: app.AuditFilter{To: at(30)}, want: []string{"e3", "e2", "e1"}},
		{name: "from and to", filter: app.AuditFilter{From: at(10), To: at(30)}, want: []string{"e3", "e2"}},
		{name: "room and actor", filter: app.AuditFilter{RoomID: 1, ActorID: e.alice.ID}, want: []string{"e5", "e1"}},
		{name: "room and action", filter: app.AuditFilter{RoomID: 2, Action: app.ActionPostCreated}, want: []string{"e3"}},
		{name: "actor and entity type", filter: app.AuditFilter{ActorID: e.bob.ID, EntityType: app.EntityTask}, want: []string{"e4"}},
		{
			name:   "all filters",
			filter: app.AuditFilter{RoomID: 2, ActorID: e.bob.ID, EntityType: app.EntityTask, Action: app.ActionTaskMoved, From: at(30), To: at(31)},
			want:   []string{"e4"},
		},
		{name: "nothing matches", filter: app.AuditFilter{RoomID: 1, EntityType: app.EntityTask}, want: []string{}},
		{name: "unknown room", filter: app.AuditFilter{RoomID: 99}, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := e.events(tt.filter, 1)
			if got := summaries(p); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("events = %v, want %v", got, tt.want)
			}
			if p.Page != 1 || p.HasNext {
				t.Errorf("page = %d, HasNext = %v, want 1, false", p.Page, p.HasNext)
			}
		})
	}
}

func TestAuditEventsPagination(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
		// wantSizes is the number of events of each page; wantNext is HasNext.
		wantSizes []int
		wantNext  []bool
	}{
		{name: "none", n: 0, wantSizes: []int{0}, wantNext: []bool{false}},
		{name: "49", n: 49, wantSizes: []int{49}, wantNext: []bool{false}},
		{name: "50", n: 50, wantSizes: []int{50}, wantNext: []bool{false}},
		{name: "51", n: 51, wantSizes: []int{50, 1}, wantNext: []bool{true, false}},
		{name: "101", n: 101, wantSizes: []int{50, 50, 1}, wantNext: []bool{true, true, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAuditEnv(t)
			for i := 1; i <= tt.n; i++ {
				e.record(app.AuditEntry{
					ActorID: e.alice.ID, Action: app.ActionPostCreated, RoomID: 1, EntityType: app.EntityPost,
					EntityID: uint64(i), Summary: fmt.Sprintf("event %d", i),
				})
			}
			next := tt.n // the number of the event that must come first
			for i, size := range tt.wantSizes {
				p := e.events(app.AuditFilter{}, i+1)
				if p.Page != i+1 || len(p.Events) != size || p.HasNext != tt.wantNext[i] {
					t.Fatalf("page %d = {Page %d, %d events, HasNext %v}, want {%d, %d, %v}",
						i+1, p.Page, len(p.Events), p.HasNext, i+1, size, tt.wantNext[i])
				}
				for _, ev := range p.Events {
					if want := fmt.Sprintf("event %d", next); ev.Summary != want {
						t.Fatalf("page %d has %q, want %q (newest first, no gaps)", i+1, ev.Summary, want)
					}
					next--
				}
			}
			if next != 0 {
				t.Errorf("the pages ended before event %d", next)
			}
			past := e.events(app.AuditFilter{}, len(tt.wantSizes)+1)
			if len(past.Events) != 0 || past.HasNext {
				t.Errorf("the page after the last = %+v, want empty", past)
			}
		})
	}
}

func TestAuditEventsPageNumbers(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	e.record(app.AuditEntry{ActorID: e.alice.ID, Action: app.ActionRoomCreated, RoomID: 1, EntityType: app.EntityRoom, EntityID: 1, Summary: "only"})
	tests := []struct {
		name      string
		page      int
		wantPage  int
		wantCount int
	}{
		{name: "zero is the first page", page: 0, wantPage: 1, wantCount: 1},
		{name: "negative is the first page", page: -3, wantPage: 1, wantCount: 1},
		{name: "first", page: 1, wantPage: 1, wantCount: 1},
		{name: "second is empty", page: 2, wantPage: 2, wantCount: 0},
		{name: "huge is empty", page: 1 << 60, wantPage: 1 << 60, wantCount: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := e.events(app.AuditFilter{}, tt.page)
			if p.Page != tt.wantPage || len(p.Events) != tt.wantCount || p.HasNext || p.Events == nil {
				t.Errorf("page = %+v, want Page %d with %d events", p, tt.wantPage, tt.wantCount)
			}
		})
	}
}

func TestAuditEventsViews(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	e.clk.Set(start.Add(time.Hour))
	e.record(app.AuditEntry{
		ActorID: e.alice.ID, Action: app.ActionColumnRenamed, RoomID: 8, EntityType: app.EntityColumn, EntityID: 21,
		Summary: "Alice renamed column Todo to Doing", Details: map[string]any{"name": map[string]any{"old": "Todo", "new": "Doing"}},
	})
	e.record(app.AuditEntry{
		ActorID: e.bob.ID, Action: app.ActionPostDeleted, RoomID: 8, EntityType: app.EntityComment, EntityID: 22,
		Summary: "Bob deleted a comment",
	})

	p := e.events(app.AuditFilter{}, 1)
	if len(p.Events) != 2 {
		t.Fatalf("events = %+v", p.Events)
	}
	newest, oldest := p.Events[0], p.Events[1]
	if newest.ID <= oldest.ID {
		t.Errorf("IDs %d, %d are not newest first", newest.ID, oldest.ID)
	}
	wantNewest := app.AuditView{
		ID: newest.ID, CreatedAt: start.Add(time.Hour), ActorID: e.bob.ID, ActorUsername: "bob", ActorDisplayName: "Bob",
		Action: app.ActionPostDeleted, RoomID: 8, EntityType: app.EntityComment, EntityID: 22,
		Summary: "Bob deleted a comment", Details: map[string]any{},
	}
	if !reflect.DeepEqual(newest, wantNewest) {
		t.Errorf("newest = %+v\nwant     %+v", newest, wantNewest)
	}
	wantOldest := app.AuditView{
		ID: oldest.ID, CreatedAt: start.Add(time.Hour), ActorID: e.alice.ID, ActorUsername: "alice", ActorDisplayName: "Alice",
		Action: app.ActionColumnRenamed, RoomID: 8, EntityType: app.EntityColumn, EntityID: 21,
		Summary: "Alice renamed column Todo to Doing",
		Details: map[string]any{"name": map[string]any{"old": "Todo", "new": "Doing"}},
	}
	if !reflect.DeepEqual(oldest, wantOldest) {
		t.Errorf("oldest = %+v\nwant     %+v", oldest, wantOldest)
	}
}

// TestAuditEventsOfDeletedRoomsAreListed: the log has no foreign key to rooms,
// so the events of a room that no longer exists (or never had a row here) stay.
func TestAuditEventsOfDeletedRoomsAreListed(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	const goneRoom = 4242
	e.record(app.AuditEntry{ActorID: e.alice.ID, Action: app.ActionRoomCreated, RoomID: goneRoom, EntityType: app.EntityRoom, EntityID: goneRoom, Summary: "Alice created #old"})
	e.record(app.AuditEntry{ActorID: e.admin.ID, Action: app.ActionRoomDeleted, RoomID: goneRoom, EntityType: app.EntityRoom, EntityID: goneRoom, Summary: "Root deleted #old"})

	p := e.events(app.AuditFilter{RoomID: goneRoom}, 1)
	if got, want := summaries(p), []string{"Root deleted #old", "Alice created #old"}; !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestAuditEventsPermissions(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	e.record(app.AuditEntry{ActorID: e.alice.ID, Action: app.ActionRoomCreated, RoomID: 1, EntityType: app.EntityRoom, EntityID: 1, Summary: "Alice created #general"})
	tests := []struct {
		name      string
		actor     app.Actor
		wantAllow bool
	}{
		{name: "admin", actor: actor(e.admin), wantAllow: true},
		{name: "member", actor: actor(e.alice)},
		{name: "member with the admin's ID", actor: app.Actor{UserID: e.admin.ID, Username: "root", Role: store.RoleMember}},
		{name: "empty actor", actor: app.Actor{}},
		{name: "unknown role", actor: app.Actor{UserID: e.admin.ID, Role: "owner"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := e.audit.Events(t.Context(), tt.actor, app.AuditFilter{}, 1)
			if tt.wantAllow {
				if err != nil || len(p.Events) != 1 {
					t.Fatalf("Events = %+v, %v, want one event", p, err)
				}
				return
			}
			_ = requireTag(t, err, app.TagForbidden, "Audit.Events.Forbidden")
			if len(p.Events) != 0 || p.Page != 0 || p.HasNext {
				t.Errorf("a refused call returned %+v", p)
			}
		})
	}
}

func TestAuditEventsErrors(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := e.audit.Events(ctx, actor(e.admin), app.AuditFilter{}, 1)
		appErr := requireTag(t, err, store.TagQueryFailed, "Audit.Events->Store.ListAuditEvents.QueryFailed")
		if appErr.Metadata["op"] != "listAuditEvents" {
			t.Errorf("op = %v", appErr.Metadata["op"])
		}
	})
	t.Run("details that are not an object", func(t *testing.T) {
		// Valid JSON that is not an object passes the table's check; only a bug
		// can store it.
		err := e.st.Tx(t.Context(), func(tx *store.Store) error {
			return tx.CreateAuditEvent(t.Context(), &store.AuditEvent{
				ActorID: e.alice.ID, Action: store.ActionRoomCreated, RoomID: 1, EntityType: store.EntityRoom,
				EntityID: 1, Summary: "bad", Details: "[1,2]",
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.audit.Events(t.Context(), actor(e.admin), app.AuditFilter{}, 1)
		appErr := requireTag(t, err, app.TagAuditDetailsInvalid, "Audit.Events->auditViewOf.AuditDetailsInvalid")
		if appErr.Metadata["eventID"] == nil {
			t.Errorf("metadata = %v, want the event ID", appErr.Metadata)
		}
	})
}

func TestAuditNames(t *testing.T) {
	t.Parallel()
	// The aliases are the store's values, so a stored event and an app
	// constant compare equal.
	if app.ActionRoomDeleted != store.ActionRoomDeleted || app.EntityColumn != store.EntityColumn {
		t.Error("the aliases differ from the store's constants")
	}
	if app.ActionPostEdited != "post.edited" || app.EntityComment != "comment" {
		t.Errorf("names = %q, %q", app.ActionPostEdited, app.EntityComment)
	}
}
