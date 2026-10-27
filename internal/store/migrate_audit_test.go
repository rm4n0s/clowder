package store

import (
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// legacyAuditSQLite is the table audit_events as version 1.1 made it, whose
// check constraint does not know the entity type "user".
const legacyAuditSQLite = "CREATE TABLE `audit_events` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime," +
	"`actor_id` integer NOT NULL,`action` text NOT NULL,`room_id` integer NOT NULL,`entity_type` text NOT NULL," +
	"`entity_id` integer NOT NULL,`summary` text NOT NULL,`details` text NOT NULL," +
	"CONSTRAINT `fk_audit_events_actor` FOREIGN KEY (`actor_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
	"CONSTRAINT `chk_audit_events_entity_type` CHECK (entity_type IN ('room','post','comment','task','column','member'))," +
	"CONSTRAINT `chk_audit_events_details_json` CHECK (json_valid(details)))"

// TestMigrationAuditEntityUserKeepsTheRows checks migration 0003: a database
// of 1.1, whose audit log has the old check constraint, is brought up to date
// with every event kept (ID, time, fields), the entity type "user" accepted,
// the other values still refused and the indexes there. It runs on both
// dialects; the old state is made by putting the 1.1 constraint back and
// forgetting that the migration ran.
func TestMigrationAuditEntityUserKeepsTheRows(t *testing.T) {
	f := newFix(t)
	actor := f.user()
	if onPostgres() {
		f.exec("ALTER TABLE audit_events DROP CONSTRAINT chk_audit_events_entity_type")
		f.exec("ALTER TABLE audit_events ADD CONSTRAINT chk_audit_events_entity_type CHECK (entity_type IN ('room','post','comment','task','column','member'))")
	} else {
		f.exec("DROP TABLE audit_events")
		f.exec(legacyAuditSQLite)
		for _, idx := range []string{
			"CREATE INDEX `idx_audit_events_created_at` ON `audit_events`(`created_at`)",
			"CREATE INDEX `idx_audit_events_action` ON `audit_events`(`action`,`id`)",
			"CREATE INDEX `idx_audit_events_actor` ON `audit_events`(`actor_id`,`id`)",
			"CREATE INDEX `idx_audit_events_room` ON `audit_events`(`room_id`,`id`)",
		} {
			f.exec(idx)
		}
	}
	f.exec("DELETE FROM schema_migrations WHERE id = ?", "0003_audit_entity_user")

	type row struct {
		ID         uint64
		CreatedAt  time.Time
		ActorID    uint64
		Action     string
		RoomID     uint64
		EntityType string
		EntityID   uint64
		Summary    string
		Details    string
	}
	want := []row{
		{5, day(time.Hour), actor.ID, "room.created", 3, "room", 3, "Alice made #lobby", `{}`},
		{9, day(2 * time.Hour), actor.ID, "post.edited", 3, "post", 40, "Alice edited a post in #lobby", `{"bodyChanged":true,"length":12}`},
		{12, day(3 * time.Hour), actor.ID, "room.deleted", 3, "room", 3, "Alice deleted #lobby", `{"name":{"old":"lobby"}}`},
	}
	for _, r := range want {
		f.exec("INSERT INTO audit_events (id, created_at, actor_id, action, room_id, entity_type, entity_id, summary, details) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			r.ID, r.CreatedAt, r.ActorID, r.Action, r.RoomID, r.EntityType, r.EntityID, r.Summary, r.Details)
	}
	if err := f.s.db(f.ctx).Create(&AuditEvent{ActorID: actor.ID, Action: ActionUserPasswordChanged, RoomID: NoRoom, EntityType: EntityUser, Details: "{}"}).Error; err == nil {
		t.Fatal("the old constraint accepted the entity type user: the test does not start from the 1.1 state")
	}

	if err := f.s.migrate(f.ctx, allModels(), rawMigrations(f.s.dialect)); err != nil {
		t.Fatalf("migrate: %+v", err)
	}

	var got []row
	err := f.s.db(f.ctx).Raw("SELECT id, created_at, actor_id, action, room_id, entity_type, entity_id, summary, details FROM audit_events ORDER BY id").Scan(&got).Error
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d events after the migration, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ID != w.ID || !g.CreatedAt.Equal(w.CreatedAt) || g.ActorID != w.ActorID || g.Action != w.Action || g.RoomID != w.RoomID ||
			g.EntityType != w.EntityType || g.EntityID != w.EntityID || g.Summary != w.Summary || g.Details != w.Details {
			t.Errorf("event %d = %+v, want %+v", i, g, w)
		}
	}

	ev := &AuditEvent{ActorID: actor.ID, Action: ActionUserPasswordChanged, RoomID: NoRoom, EntityType: EntityUser, Summary: "Alice changed their password", Details: "{}"}
	if err := f.s.CreateAuditEvent(f.ctx, ev); err != nil {
		t.Fatalf("the entity type user is still refused: %+v", err)
	}
	if !onPostgres() && ev.ID <= 12 {
		t.Errorf("the new event got ID %d, want one above the copied 12", ev.ID)
	}
	bad := &AuditEvent{ActorID: actor.ID, Action: ActionRoomCreated, EntityType: "poke", Summary: "x", Details: "{}"}
	if err := f.s.CreateAuditEvent(f.ctx, bad); err == nil || !isCheckFailure(errors.Unwrap(err), "chk_audit_events_entity_type") {
		t.Errorf("an unknown entity type: error = %v, want a failure of the check constraint", err)
	}

	// The four indexes are back, and a second migration changes nothing.
	for _, name := range []string{"idx_audit_events_created_at", "idx_audit_events_room", "idx_audit_events_actor", "idx_audit_events_action"} {
		var n int64
		q := "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?"
		if onPostgres() {
			q = "SELECT count(*) FROM pg_indexes WHERE indexname = ?"
		}
		if err := f.s.db(f.ctx).Raw(q, name).Scan(&n).Error; err != nil || n != 1 {
			t.Errorf("index %s: %d found (%v), want 1", name, n, err)
		}
	}
	if err := f.s.migrate(f.ctx, allModels(), rawMigrations(f.s.dialect)); err != nil {
		t.Errorf("the second migrate: %+v", err)
	}
	if n := f.count("audit_events"); n != int64(len(want))+1 {
		t.Errorf("%d events after the second migrate, want %d", n, len(want)+1)
	}
}
