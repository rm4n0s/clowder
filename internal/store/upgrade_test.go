package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"gorm.io/gorm"
)

// TestOpenUpgradesTheSessionsOfA10Database checks D46: a SQLite database made
// by 1.0, whose sessions have neither a kind nor a client name, is brought up
// to date by opening it, and the sessions that were live stay: they become
// browser sessions with no client name. The 1.0 table is made by dropping the
// two columns that 1.1 added.
func TestOpenUpgradesTheSessionsOfA10Database(t *testing.T) {
	if onPostgres() {
		t.Skip("the upgrade of a 1.0 database is a SQLite case: PostgreSQL is new in 1.1")
	}
	ctx := t.Context()
	clk := clock.NewFake(fakeStart)
	path := filepath.Join(t.TempDir(), "clowder.db")

	s, err := Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	user := User{Username: "alice", DisplayName: "Alice", PasswordHash: "h", Role: RoleMember}
	if err := s.CreateUser(ctx, &user); err != nil {
		t.Fatalf("CreateUser: %+v", err)
	}
	for _, stmt := range []string{"ALTER TABLE sessions DROP COLUMN kind", "ALTER TABLE sessions DROP COLUMN client_name"} {
		if err := s.db(ctx).Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	err = s.db(ctx).Exec("INSERT INTO sessions (token_hash, user_id, expires_at, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?)",
		"legacy-token-hash", user.ID, fakeStart.Add(24*time.Hour), fakeStart, fakeStart).Error
	if err != nil {
		t.Fatalf("insert a 1.0 session: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open the 1.0 database: %+v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	var sess Session
	if err := s.db(ctx).Where("token_hash = ?", "legacy-token-hash").First(&sess).Error; err != nil {
		t.Fatalf("the 1.0 session is gone: %v", err)
	}
	if sess.Kind != SessionBrowser || sess.ClientName != "" || sess.UserID != user.ID {
		t.Errorf("session = kind %q, client name %q, user %d; want a browser session with no client name", sess.Kind, sess.ClientName, sess.UserID)
	}
}

// TestOpenUpgradesTheSettingsOf181 checks that a SQLite database made by step
// 18.1, whose user_settings has neither notify, sound_name nor replies, is
// brought up to date by opening it: the rows stay and get the defaults.
func TestOpenUpgradesTheSettingsOf181(t *testing.T) {
	if onPostgres() {
		t.Skip("the upgrade of an earlier 1.2 database is a SQLite case")
	}
	ctx := t.Context()
	clk := clock.NewFake(fakeStart)
	path := filepath.Join(t.TempDir(), "clowder.db")

	s, err := Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	user := User{Username: "alice", DisplayName: "Alice", PasswordHash: "h", Role: RoleMember}
	if err := s.CreateUser(ctx, &user); err != nil {
		t.Fatalf("CreateUser: %+v", err)
	}
	for _, stmt := range []string{
		"DROP TABLE user_settings",
		"CREATE TABLE `user_settings` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`user_id` integer NOT NULL," +
			"`theme` text NOT NULL,`sound` numeric NOT NULL,`browser_notifications` numeric NOT NULL," +
			"CONSTRAINT `fk_user_settings_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
			"CONSTRAINT `chk_user_settings_theme` CHECK (theme IN ('system','light','dark')))",
		"CREATE UNIQUE INDEX `uq_user_settings_user_id` ON `user_settings`(`user_id`)",
	} {
		if err := s.db(ctx).Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	err = s.db(ctx).Exec("INSERT INTO user_settings (user_id, theme, sound, browser_notifications, created_at) VALUES (?, 'dark', 0, 1, ?)",
		user.ID, fakeStart).Error
	if err != nil {
		t.Fatalf("insert an 18.1 row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open the 18.1 database: %+v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	got, err := s.UserSettingsOf(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != ThemeDark || got.Sound || !got.BrowserNotifications ||
		got.Notify != NotifyMentions || got.SoundName != SoundPurr || got.Replies != RepliesFollowing {
		t.Errorf("settings = %+v, want the 18.1 values and the defaults of the new fields", got)
	}
	got.Notify = NotifyAll
	if err := s.SaveUserSettings(ctx, &got); err != nil {
		t.Fatalf("SaveUserSettings: %+v", err)
	}
	bad := got
	bad.Replies = "some"
	if err := s.SaveUserSettings(ctx, &bad); err == nil {
		t.Error("a value outside the check of an upgraded table was accepted")
	}
}

// TestOpenUpgradesTheDatabaseOf183 checks that a SQLite database made by step
// 18.3, whose user_settings has neither keywords nor group_mentions_off, whose
// mentions have no kind, whose notifications have no others and which has no
// post_follows, is brought up to date by opening it: the rows stay, the new
// columns get their defaults and the new table works.
func TestOpenUpgradesTheDatabaseOf183(t *testing.T) {
	if onPostgres() {
		t.Skip("the upgrade of an earlier 1.2 database is a SQLite case")
	}
	ctx := t.Context()
	clk := clock.NewFake(fakeStart)
	path := filepath.Join(t.TempDir(), "clowder.db")

	s, err := Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	alice := User{Username: "alice", DisplayName: "Alice", PasswordHash: "h", Role: RoleMember}
	bob := User{Username: "bob", DisplayName: "Bob", PasswordHash: "h", Role: RoleMember}
	for _, u := range []*User{&alice, &bob} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %+v", err)
		}
	}
	room := Room{Name: "lobby", Visibility: VisibilityVisible, CreatedBy: alice.ID}
	if err := s.db(ctx).Create(&room).Error; err != nil {
		t.Fatalf("create the room: %v", err)
	}
	post := Post{RoomID: room.ID, AuthorID: alice.ID, Format: FormatText, Body: "hello @bob"}
	if err := s.db(ctx).Create(&post).Error; err != nil {
		t.Fatalf("create the post: %v", err)
	}
	for _, stmt := range []string{
		"INSERT INTO user_settings (user_id, theme, sound, browser_notifications, notify, sound_name, replies, created_at) VALUES (" +
			"1, 'dark', 1, 0, 'all', 'bell', 'direct', '2026-10-26 09:30:00+00:00')",
		"INSERT INTO mentions (post_id, user_id, created_at) VALUES (" + "1, 2, '2026-10-26 09:30:00+00:00')",
		"INSERT INTO notifications (user_id, kind, post_id, room_id, actor_id, created_at) VALUES (" +
			"2, 'mention', 1, 1, 1, '2026-10-26 09:30:00+00:00')",
	} {
		if err := s.db(ctx).Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Step 18.3 had none of these columns and no table post_follows.
	for _, stmt := range []string{
		"ALTER TABLE user_settings DROP COLUMN keywords",
		"ALTER TABLE user_settings DROP COLUMN group_mentions_off",
		"ALTER TABLE mentions DROP COLUMN kind",
		"ALTER TABLE notifications DROP COLUMN others",
		"DROP TABLE post_follows",
	} {
		if err := s.db(ctx).Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open the 18.3 database: %+v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	t.Run("the settings keep their values and get the defaults", func(t *testing.T) {
		got, err := s.UserSettingsOf(ctx, alice.ID)
		if err != nil {
			t.Fatalf("UserSettingsOf: %+v", err)
		}
		if got.Theme != ThemeDark || !got.Sound || got.Notify != NotifyAll || got.SoundName != SoundBell || got.Replies != RepliesDirect {
			t.Errorf("settings = %+v, want the 18.3 values", got)
		}
		if got.KeywordsJSON != "[]" || len(got.Keywords()) != 0 || !got.GroupMentions() {
			t.Errorf("settings = %+v, want no keywords and group mentions on", got)
		}
		got.SetKeywords([]string{"deploy"})
		got.SetGroupMentions(false)
		if err := s.SaveUserSettings(ctx, &got); err != nil {
			t.Fatalf("SaveUserSettings: %+v", err)
		}
		again, err := s.UserSettingsOf(ctx, alice.ID)
		if err != nil || !reflect.DeepEqual(again.Keywords(), []string{"deploy"}) || again.GroupMentions() {
			t.Errorf("after saving: %+v, %v", again, err)
		}
	})

	t.Run("the mention is a user mention", func(t *testing.T) {
		got, err := s.PostMentions(ctx, post.ID)
		if err != nil || !reflect.DeepEqual(got, []MentionedUser{{UserID: bob.ID, Kind: MentionUser}}) {
			t.Fatalf("PostMentions = %v, %v", got, err)
		}
		names, err := s.MentionedUsernamesOfPosts(ctx, []uint64{post.ID})
		if err != nil || !reflect.DeepEqual(names, map[uint64][]string{post.ID: {"bob"}}) {
			t.Errorf("MentionedUsernamesOfPosts = %v, %v", names, err)
		}
		if err := s.ReplacePostMentions(ctx, post.ID, []MentionedUser{{UserID: bob.ID, Kind: MentionKeyword}}); err != nil {
			t.Fatalf("ReplacePostMentions: %+v", err)
		}
		if got, err := s.PostMentions(ctx, post.ID); err != nil || got[0].Kind != MentionKeyword {
			t.Errorf("after the change: %v, %v", got, err)
		}
	})

	t.Run("the notification has no others and takes a fold", func(t *testing.T) {
		rows, err := s.ListNotifications(ctx, bob.ID, 0, 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("ListNotifications = %v, %v", rows, err)
		}
		if rows[0].Kind != NotificationMention || rows[0].Others != 0 || rows[0].ActorID != alice.ID {
			t.Errorf("notification = %+v", rows[0])
		}
		if err := s.FoldReply(ctx, rows[0].ID, bob.ID, 2); err != nil {
			t.Fatalf("FoldReply: %+v", err)
		}
		if rows, err := s.ListNotifications(ctx, bob.ID, 0, 10); err != nil || rows[0].Others != 2 || rows[0].ActorID != bob.ID {
			t.Errorf("after the fold: %+v, %v", rows, err)
		}
		n := Notification{UserID: bob.ID, Kind: NotificationKeyword, PostID: &post.ID, RoomID: &room.ID, ActorID: alice.ID}
		if err := s.CreateNotifications(ctx, []Notification{n}); err != nil {
			t.Errorf("a keyword notification: %+v", err)
		}
	})

	t.Run("the table post_follows exists and works", func(t *testing.T) {
		if err := s.AutoFollow(ctx, post.ID, []uint64{alice.ID, bob.ID}); err != nil {
			t.Fatalf("AutoFollow: %+v", err)
		}
		if err := s.SetFollow(ctx, bob.ID, post.ID, false); err != nil {
			t.Fatalf("SetFollow: %+v", err)
		}
		got, err := s.ThreadFollowers(ctx, post.ID, 0)
		if err != nil {
			t.Fatalf("ThreadFollowers: %+v", err)
		}
		// Alice saved the setting replies = direct: she is not a follower for
		// the replies.
		if len(got) != 0 {
			t.Errorf("ThreadFollowers = %v, want none (alice replies direct, bob unfollowed)", got)
		}
		following, err := s.FollowingPosts(ctx, alice.ID, []uint64{post.ID})
		if err != nil || !following[post.ID] {
			t.Errorf("FollowingPosts = %v, %v", following, err)
		}
	})
}

// TestOpenUpgradesTheDatabaseOf215 checks that a database made by step 21.5,
// whose messages have neither a kind nor a call and which has no calls table,
// is brought up to date by opening it: the messages stay and are text, the
// table and its indexes are made, a call and its entry can be written, and
// the database refuses a second open call of a user. On SQLite the 21.5
// messages table is made again without the two columns (a column that a check
// names cannot be dropped); on PostgreSQL the columns and the table are
// dropped and the migration is undone.
func TestOpenUpgradesTheDatabaseOf215(t *testing.T) {
	ctx := t.Context()
	clk := clock.NewFake(fakeStart)
	var f *fix
	var reopen func()
	if onPostgres() {
		f = newFix(t)
		reopen = func() {
			for _, stmt := range []string{
				"DROP TABLE calls CASCADE", // the foreign key of messages.call_id goes with it
				"ALTER TABLE messages DROP COLUMN IF EXISTS call_id",
				"ALTER TABLE messages DROP COLUMN kind",
				"DELETE FROM schema_migrations WHERE id = '0007_calls_open'",
			} {
				f.exec(stmt)
			}
			if err := f.s.migrate(ctx, allModels(), rawMigrations(dialectPostgres)); err != nil {
				t.Fatalf("migrate the 21.5 database: %+v", err)
			}
		}
	} else {
		path := filepath.Join(t.TempDir(), "clowder.db")
		s, err := Open(ctx, path, clk, nil)
		if err != nil {
			t.Fatalf("Open: %+v", err)
		}
		f = &fix{tb: t, ctx: ctx, s: s, clk: clk}
		reopen = func() {
			// The table is replaced with foreign keys off, as SQLite's own
			// guide says, and only the rows of messages are copied.
			if err := f.s.db(ctx).Connection(func(tx *gorm.DB) error {
				for _, stmt := range []string{
					"PRAGMA foreign_keys = OFF",
					"ALTER TABLE messages RENAME TO messages_215",
					"CREATE TABLE `messages` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`conversation_id` integer NOT NULL," +
						"`author_id` integer NOT NULL,`body` text NOT NULL,`reply_to_id` integer,`edited_at` datetime,`deleted_at` datetime," +
						"CONSTRAINT `fk_messages_conversation` FOREIGN KEY (`conversation_id`) REFERENCES `conversations`(`id`) ON DELETE CASCADE," +
						"CONSTRAINT `fk_messages_author` FOREIGN KEY (`author_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
						"CONSTRAINT `fk_messages_reply_to` FOREIGN KEY (`reply_to_id`) REFERENCES `messages`(`id`) ON DELETE SET NULL)",
					"INSERT INTO messages (id, created_at, conversation_id, author_id, body, reply_to_id, edited_at, deleted_at) " +
						"SELECT id, created_at, conversation_id, author_id, body, reply_to_id, edited_at, deleted_at FROM messages_215",
					"DROP TABLE messages_215",
					"CREATE INDEX `idx_messages_conversation` ON `messages`(`conversation_id`,`id`)",
					"DROP TABLE calls",
					"DELETE FROM schema_migrations WHERE id = '0007_calls_open'",
					"PRAGMA foreign_keys = ON",
				} {
					if err := tx.Exec(stmt).Error; err != nil {
						return fmt.Errorf("%s: %w", stmt, err)
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("make the 21.5 database: %v", err)
			}
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(ctx, path, clk, nil)
			if err != nil {
				t.Fatalf("Open the 21.5 database: %+v", err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			f = &fix{tb: t, ctx: ctx, s: s, clk: clk}
		}
	}
	a, b := f.user(), f.user()
	conv := f.direct(a, b)
	old := f.message(conv, a)
	reopen()
	s := f.s

	got, err := s.MessageByID(ctx, old.ID)
	if err != nil || got.Kind != MessageText || got.CallID != nil || got.Body != old.Body {
		t.Fatalf("the 21.5 message = %+v, %v; want the same text message", got, err)
	}
	call := &Call{ConversationID: conv.ID, CallerID: a.ID, CalleeID: b.ID, Video: true, State: CallRinging, Home: "p1"}
	if err := s.CreateCall(ctx, call); err != nil {
		t.Fatalf("CreateCall on the upgraded database: %+v", err)
	}
	entry := &Message{ConversationID: conv.ID, AuthorID: a.ID, Kind: MessageCall, CallID: &call.ID}
	if err := s.CreateMessage(ctx, entry); err != nil {
		t.Fatalf("CreateMessage of a call entry: %+v", err)
	}
	second := &Call{ConversationID: conv.ID, CallerID: a.ID, CalleeID: b.ID, State: CallRinging, Home: "p1"}
	if err := s.CreateCall(ctx, second); err == nil {
		t.Error("the upgraded database accepted a second open call")
	}
	bad := &Message{ConversationID: conv.ID, AuthorID: a.ID, Kind: "poke"}
	if err := s.db(ctx).Create(bad).Error; err == nil {
		t.Error("the upgraded database accepted a message of an unknown kind")
	}
	// Opening an up-to-date database changes nothing.
	if err := s.migrate(ctx, allModels(), rawMigrations(s.dialect)); err != nil {
		t.Errorf("migrate again: %+v", err)
	}
}
