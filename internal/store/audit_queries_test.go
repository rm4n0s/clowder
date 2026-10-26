package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// auditEvent makes a valid event of the actor in the room with the summary.
func auditEvent(actor *User, roomID uint64, summary string) *AuditEvent {
	return &AuditEvent{
		ActorID: actor.ID, Action: ActionRoomCreated, RoomID: roomID,
		EntityType: EntityRoom, EntityID: roomID, Summary: summary, Details: "{}",
	}
}

func TestCreateAuditEvent(t *testing.T) {
	f := newFix(t)
	actor := f.user()
	ev := auditEvent(actor, 7, "Alice created #design")
	ev.Details = `{"name":{"old":"a","new":"b"}}`
	if err := f.s.CreateAuditEvent(f.ctx, ev); err != nil {
		t.Fatalf("CreateAuditEvent: %+v", err)
	}
	if ev.ID == 0 {
		t.Fatal("the ID was not set")
	}
	requireSameTime(t, "CreatedAt", fakeStart, ev.CreatedAt)

	got, err := f.s.ListAuditEvents(f.ctx, AuditFilter{}, 10, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListAuditEvents = %v, %v", got, err)
	}
	if got[0].Details != ev.Details || got[0].Summary != ev.Summary || got[0].RoomID != 7 {
		t.Errorf("event = %+v", got[0])
	}
	requireSameTime(t, "stored CreatedAt", fakeStart, got[0].CreatedAt)
}

func TestListAuditEvents(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	type seed struct {
		actor   *User
		room    uint64
		action  AuditAction
		entity  EntityType
		minutes int
		summary string
	}
	// The ID order is the order of the list: "e1" is the oldest.
	seeds := []seed{
		{alice, 1, ActionRoomCreated, EntityRoom, 0, "e1"},
		{bob, 1, ActionPostCreated, EntityPost, 10, "e2"},
		{alice, 2, ActionPostCreated, EntityComment, 20, "e3"},
		{bob, 2, ActionTaskMoved, EntityTask, 30, "e4"},
		{alice, 1, ActionMemberAdded, EntityMember, 40, "e5"},
	}
	for _, s := range seeds {
		f.clk.Set(fakeStart.Add(time.Duration(s.minutes) * time.Minute))
		ev := auditEvent(s.actor, s.room, s.summary)
		ev.Action, ev.EntityType = s.action, s.entity
		if err := f.s.CreateAuditEvent(f.ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	at := func(m int) time.Time { return fakeStart.Add(time.Duration(m) * time.Minute) }

	tests := []struct {
		name   string
		filter AuditFilter
		limit  int
		offset int
		want   []string
	}{
		{name: "no filter, newest first", want: []string{"e5", "e4", "e3", "e2", "e1"}},
		{name: "room", filter: AuditFilter{RoomID: 1}, want: []string{"e5", "e2", "e1"}},
		{name: "unknown room", filter: AuditFilter{RoomID: 99}, want: []string{}},
		{name: "actor", filter: AuditFilter{ActorID: bob.ID}, want: []string{"e4", "e2"}},
		{name: "entity type", filter: AuditFilter{EntityType: EntityComment}, want: []string{"e3"}},
		{name: "action", filter: AuditFilter{Action: ActionPostCreated}, want: []string{"e3", "e2"}},
		{name: "from is inclusive", filter: AuditFilter{From: at(20)}, want: []string{"e5", "e4", "e3"}},
		{name: "to is exclusive", filter: AuditFilter{To: at(20)}, want: []string{"e2", "e1"}},
		{name: "from and to", filter: AuditFilter{From: at(10), To: at(40)}, want: []string{"e4", "e3", "e2"}},
		{name: "from in another zone", filter: AuditFilter{From: at(30).In(time.FixedZone("x", 3*3600))}, want: []string{"e5", "e4"}},
		{name: "room and actor", filter: AuditFilter{RoomID: 1, ActorID: alice.ID}, want: []string{"e5", "e1"}},
		{name: "room and action", filter: AuditFilter{RoomID: 2, Action: ActionPostCreated}, want: []string{"e3"}},
		{
			name:   "every filter",
			filter: AuditFilter{RoomID: 2, ActorID: bob.ID, EntityType: EntityTask, Action: ActionTaskMoved, From: at(30), To: at(31)},
			want:   []string{"e4"},
		},
		{name: "filters that exclude each other", filter: AuditFilter{ActorID: bob.ID, EntityType: EntityRoom}, want: []string{}},
		{name: "limit", limit: 2, want: []string{"e5", "e4"}},
		{name: "offset", limit: 2, offset: 2, want: []string{"e3", "e2"}},
		{name: "offset past the end", limit: 2, offset: 5, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := tt.limit
			if limit == 0 {
				limit = 50
			}
			got, err := f.s.ListAuditEvents(f.ctx, tt.filter, limit, tt.offset)
			if err != nil {
				t.Fatalf("ListAuditEvents: %+v", err)
			}
			summaries := []string{}
			for _, ev := range got {
				summaries = append(summaries, ev.Summary)
				if ev.Actor == nil || ev.Actor.ID != ev.ActorID {
					t.Errorf("event %s: Actor = %+v, want user %d", ev.Summary, ev.Actor, ev.ActorID)
				}
			}
			if !reflect.DeepEqual(summaries, tt.want) {
				t.Errorf("events = %v, want %v", summaries, tt.want)
			}
		})
	}
}

func TestAuditQueryTags(t *testing.T) {
	f := newFix(t)
	actor := f.user()
	first := auditEvent(actor, 1, "first")
	if err := f.s.CreateAuditEvent(f.ctx, first); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
		wantOp    string
	}{
		{
			name: "duplicate ID",
			call: func() error {
				ev := auditEvent(actor, 1, "again")
				ev.ID = first.ID
				return f.s.CreateAuditEvent(f.ctx, ev)
			},
			wantTag: TagConflict, wantRoute: "Store.CreateAuditEvent.Conflict", wantOp: "createAuditEvent",
		},
		{
			name:    "unknown actor",
			call:    func() error { return f.s.CreateAuditEvent(f.ctx, auditEvent(&User{ID: 9999}, 1, "x")) },
			wantTag: TagQueryFailed, wantRoute: "Store.CreateAuditEvent.QueryFailed", wantOp: "createAuditEvent",
		},
		{
			name: "details that are not JSON",
			call: func() error {
				ev := auditEvent(actor, 1, "x")
				ev.Details = "{not json"
				return f.s.CreateAuditEvent(f.ctx, ev)
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateAuditEvent.QueryFailed", wantOp: "createAuditEvent",
		},
		{
			name: "entity type that does not exist",
			call: func() error {
				ev := auditEvent(actor, 1, "x")
				ev.EntityType = "planet"
				return f.s.CreateAuditEvent(f.ctx, ev)
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateAuditEvent.QueryFailed", wantOp: "createAuditEvent",
		},
		{
			name:    "create with a canceled context",
			call:    func() error { return f.s.CreateAuditEvent(canceled(t), auditEvent(actor, 1, "x")) },
			wantTag: TagQueryFailed, wantRoute: "Store.CreateAuditEvent.QueryFailed", wantOp: "createAuditEvent",
		},
		{
			name: "list with a canceled context",
			call: func() error {
				_, err := f.s.ListAuditEvents(canceled(t), AuditFilter{}, 10, 0)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListAuditEvents.QueryFailed", wantOp: "listAuditEvents",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
	if n := f.count("audit_events"); n != 1 {
		t.Errorf("audit_events has %d rows after the failures, want 1", n)
	}
}

func TestAuditQueriesUseTheIndexes(t *testing.T) {
	f := newFix(t)
	tests := []struct {
		name  string
		where string
		index string
	}{
		{name: "room", where: "room_id = 1", index: "idx_audit_events_room"},
		{name: "actor", where: "actor_id = 1", index: "idx_audit_events_actor"},
		{name: "action", where: "action = 'room.created'", index: "idx_audit_events_action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var plan []struct {
				Detail string
			}
			q := "EXPLAIN QUERY PLAN SELECT * FROM audit_events WHERE " + tt.where + " ORDER BY id DESC LIMIT 51"
			if err := f.s.db(f.ctx).Raw(q).Scan(&plan).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range plan {
				if strings.Contains(row.Detail, tt.index) {
					return
				}
			}
			t.Errorf("plan %v does not use %s", plan, tt.index)
		})
	}
}
