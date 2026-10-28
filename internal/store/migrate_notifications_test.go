package store

import (
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// legacyNotificationsSQLite is the table notifications as step 18.3 made it:
// no column others, and a check constraint that does not know the kinds
// group_mention and keyword.
const legacyNotificationsSQLite = "CREATE TABLE `notifications` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime," +
	"`user_id` integer NOT NULL,`kind` text NOT NULL,`post_id` integer,`task_id` integer,`room_id` integer," +
	"`actor_id` integer NOT NULL,`read_at` datetime," +
	"CONSTRAINT `fk_notifications_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
	"CONSTRAINT `fk_notifications_post` FOREIGN KEY (`post_id`) REFERENCES `posts`(`id`) ON DELETE CASCADE," +
	"CONSTRAINT `fk_notifications_task` FOREIGN KEY (`task_id`) REFERENCES `tasks`(`id`) ON DELETE CASCADE," +
	"CONSTRAINT `fk_notifications_room` FOREIGN KEY (`room_id`) REFERENCES `rooms`(`id`) ON DELETE CASCADE," +
	"CONSTRAINT `fk_notifications_actor` FOREIGN KEY (`actor_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
	"CONSTRAINT `chk_notifications_kind` CHECK (kind IN ('mention','reply','assigned','added_to_room')))"

// TestMigrationNotificationKindsKeepsTheRows checks migration 0004: a database
// of step 18.3, whose notifications have the old check constraint and no
// column others, is brought up to date with every notification kept (ID, time,
// fields), the kinds group_mention and keyword accepted, an unknown kind still
// refused, and the indexes there. It runs on both dialects; the old state is
// made by putting the 18.3 table back and forgetting that the migration ran.
func TestMigrationNotificationKindsKeepsTheRows(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	post := f.post(room, bob)
	if onPostgres() {
		f.exec("ALTER TABLE notifications DROP COLUMN others")
		f.exec("ALTER TABLE notifications DROP CONSTRAINT chk_notifications_kind")
		f.exec("ALTER TABLE notifications ADD CONSTRAINT chk_notifications_kind CHECK (kind IN ('mention','reply','assigned','added_to_room'))")
	} else {
		f.exec("DROP TABLE notifications")
		f.exec(legacyNotificationsSQLite)
		for _, idx := range []string{
			"CREATE INDEX `idx_notifications_room_id` ON `notifications`(`room_id`)",
			"CREATE INDEX `idx_notifications_task_id` ON `notifications`(`task_id`)",
			"CREATE INDEX `idx_notifications_post_id` ON `notifications`(`post_id`)",
			"CREATE INDEX `idx_notifications_user_read` ON `notifications`(`user_id`,`read_at`,`id`)",
		} {
			f.exec(idx)
		}
	}
	f.exec("DELETE FROM schema_migrations WHERE id = ?", "0004_notification_kinds")

	type row struct {
		ID        uint64
		CreatedAt time.Time
		UserID    uint64
		Kind      string
		PostID    uint64
		RoomID    uint64
		ActorID   uint64
	}
	want := []row{
		{4, day(time.Hour), alice.ID, "mention", post.ID, room.ID, bob.ID},
		{8, day(2 * time.Hour), alice.ID, "reply", post.ID, room.ID, bob.ID},
		{13, day(3 * time.Hour), bob.ID, "added_to_room", post.ID, room.ID, alice.ID},
	}
	for _, r := range want {
		f.exec("INSERT INTO notifications (id, created_at, user_id, kind, post_id, room_id, actor_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
			r.ID, r.CreatedAt, r.UserID, r.Kind, r.PostID, r.RoomID, r.ActorID)
	}
	if err := f.s.db(f.ctx).Exec("INSERT INTO notifications (user_id, kind, post_id, room_id, actor_id) VALUES (?, 'keyword', ?, ?, ?)",
		alice.ID, post.ID, room.ID, bob.ID).Error; err == nil {
		t.Fatal("the old constraint accepted the kind keyword: the test does not start from the 18.3 state")
	}

	if err := f.s.migrate(f.ctx, allModels(), rawMigrations(f.s.dialect)); err != nil {
		t.Fatalf("migrate: %+v", err)
	}

	var got []row
	err := f.s.db(f.ctx).Raw("SELECT id, created_at, user_id, kind, COALESCE(post_id, 0) AS post_id, COALESCE(room_id, 0) AS room_id, actor_id FROM notifications ORDER BY id").Scan(&got).Error
	if err != nil {
		t.Fatalf("read the notifications: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d notifications after the migration, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ID != w.ID || !g.CreatedAt.Equal(w.CreatedAt) || g.UserID != w.UserID || g.Kind != w.Kind ||
			g.PostID != w.PostID || g.RoomID != w.RoomID || g.ActorID != w.ActorID {
			t.Errorf("notification %d = %+v, want %+v", i, g, w)
		}
	}
	var others []int
	if err := f.s.db(f.ctx).Raw("SELECT others FROM notifications ORDER BY id").Scan(&others).Error; err != nil {
		t.Fatalf("read others: %v", err)
	}
	for i, n := range others {
		if n != 0 {
			t.Errorf("notification %d has others = %d, want 0", i, n)
		}
	}

	for _, kind := range []NotificationKind{NotificationGroupMention, NotificationKeyword} {
		n := &Notification{UserID: alice.ID, Kind: kind, PostID: &post.ID, RoomID: &room.ID, ActorID: bob.ID}
		if err := f.s.CreateNotifications(f.ctx, []Notification{*n}); err != nil {
			t.Errorf("the kind %s is still refused: %+v", kind, err)
		}
	}
	bad := Notification{UserID: alice.ID, Kind: "poke", PostID: &post.ID, RoomID: &room.ID, ActorID: bob.ID}
	if err := f.s.CreateNotifications(f.ctx, []Notification{bad}); err == nil || !isCheckFailure(errors.Unwrap(err), "chk_notifications_kind") {
		t.Errorf("an unknown kind: error = %v, want a failure of the check constraint", err)
	}

	for _, name := range []string{"idx_notifications_room_id", "idx_notifications_task_id", "idx_notifications_post_id", "idx_notifications_user_read"} {
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
	if n := f.count("notifications"); n != int64(len(want))+2 {
		t.Errorf("%d notifications after the second migrate, want %d", n, len(want)+2)
	}
}
