package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/errors"
	"gorm.io/gorm"
)

// requireSameRow fails the test unless got has the fields of want. Times are
// compared as instants and must be in UTC.
func requireSameRow(t *testing.T, want, got any) {
	t.Helper()
	wv, gv := reflect.ValueOf(want).Elem(), reflect.ValueOf(got).Elem()
	for i := range wv.NumField() {
		name := wv.Type().Field(i).Name
		w, g := wv.Field(i).Interface(), gv.Field(i).Interface()
		switch w := w.(type) {
		case time.Time:
			requireSameTime(t, name, w, g.(time.Time))
		case *time.Time:
			gt := g.(*time.Time)
			if (w == nil) != (gt == nil) {
				t.Errorf("%s = %v, want %v", name, gt, w)
				continue
			}
			if w != nil {
				requireSameTime(t, name, *w, *gt)
			}
		default:
			if !reflect.DeepEqual(w, g) {
				t.Errorf("%s = %v, want %v", name, g, w)
			}
		}
	}
}

func requireSameTime(t *testing.T, name string, want, got time.Time) {
	t.Helper()
	if !want.Equal(got) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
	if _, offset := got.Zone(); offset != 0 {
		t.Errorf("%s = %v, want UTC", name, got)
	}
}

func TestModelsRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		table string
		// row returns a row with every field set, without its ID.
		row func(f *fix) any
	}{
		{"User", "users", func(f *fix) any {
			return &User{
				Username: "alice", DisplayName: "Alice A", PasswordHash: "$argon2id$x", Role: RoleAdmin,
				DisabledAt: ptr(day(time.Hour)), AvatarKey: "0123abcd",
			}
		}},
		{"Invitation", "invitations", func(f *fix) any {
			admin, used := f.user(), f.user()
			return &Invitation{
				TokenHash: "h1", Role: RoleMember, CreatedBy: &admin.ID, ExpiresAt: day(24 * time.Hour),
				UsedBy: &used.ID, UsedAt: ptr(day(time.Hour)),
			}
		}},
		{"Invitation made by the command line", "invitations", func(f *fix) any {
			return &Invitation{TokenHash: "h2", Role: RoleAdmin, CreatedBy: nil, ExpiresAt: day(24 * time.Hour)}
		}},
		{"PasswordReset", "password_resets", func(f *fix) any {
			admin, user := f.user(), f.user()
			return &PasswordReset{
				UserID: user.ID, TokenHash: "h1", CreatedBy: &admin.ID, ExpiresAt: day(24 * time.Hour),
				UsedAt: ptr(day(time.Hour)),
			}
		}},
		{"PasswordReset made by the command line", "password_resets", func(f *fix) any {
			return &PasswordReset{UserID: f.user().ID, TokenHash: "h2", CreatedBy: nil, ExpiresAt: day(24 * time.Hour)}
		}},
		{"SSHKey", "ssh_keys", func(f *fix) any {
			return &SSHKey{UserID: f.user().ID, Name: "laptop", Type: "ssh-ed25519", Fingerprint: "SHA256:a", PublicKey: "ssh-ed25519 AAAA", LastUsedAt: ptr(day(time.Hour))}
		}},
		{"SSHCode", "ssh_codes", func(f *fix) any {
			u := f.user()
			k := &SSHKey{UserID: u.ID, Name: "k", Type: "ssh-ed25519", Fingerprint: "SHA256:b", PublicKey: "ssh-ed25519 BBBB"}
			f.create(k)
			return &SSHCode{UserID: u.ID, KeyID: k.ID, CodeHash: "c", ExpiresAt: day(time.Minute), UsedAt: ptr(day(time.Second)), Replaced: true}
		}},
		{"Session", "sessions", func(f *fix) any {
			return &Session{TokenHash: "h1", UserID: f.user().ID, ExpiresAt: day(48 * time.Hour), LastSeenAt: day(time.Minute)}
		}},
		{"Room", "rooms", func(f *fix) any {
			return &Room{Name: "Lobby", Description: "Welcome", Visibility: VisibilityPrivate, CreatedBy: f.user().ID}
		}},
		{"RoomMember", "room_members", func(f *fix) any {
			u := f.user()
			return &RoomMember{RoomID: f.room(u).ID, UserID: f.user().ID, AddedBy: &u.ID}
		}},
		{"Post", "posts", func(f *fix) any {
			u := f.user()
			parent := f.post(f.room(u), u)
			return &Post{
				RoomID: parent.RoomID, AuthorID: u.ID, ParentID: &parent.ID, RootID: &parent.ID,
				Format: FormatMarkdown, Body: "# hi", EditedAt: ptr(day(time.Hour)), DeletedAt: ptr(day(2 * time.Hour)),
			}
		}},
		{"Tag", "tags", func(f *fix) any {
			return &Tag{Name: "go"}
		}},
		{"PostTag", "post_tags", func(f *fix) any {
			u := f.user()
			return &PostTag{PostID: f.post(f.room(u), u).ID, TagID: f.tag().ID}
		}},
		{"Attachment", "attachments", func(f *fix) any {
			u := f.user()
			return &Attachment{
				PostID: f.post(f.room(u), u).ID, UploaderID: u.ID, FileName: "cat.png", ContentType: "image/png",
				Size: 1234, StorageKey: "k1", Width: ptr(640), Height: ptr(480),
			}
		}},
		{"Reaction", "reactions", func(f *fix) any {
			u := f.user()
			return &Reaction{PostID: f.post(f.room(u), u).ID, UserID: u.ID, Emoji: "\U0001F44D"}
		}},
		{"Mention", "mentions", func(f *fix) any {
			u := f.user()
			return &Mention{PostID: f.post(f.room(u), u).ID, UserID: f.user().ID}
		}},
		{"BoardColumn", "board_columns", func(f *fix) any {
			return &BoardColumn{RoomID: f.room(f.user()).ID, Name: "To do", Position: 3}
		}},
		{"Task", "tasks", func(f *fix) any {
			u := f.user()
			room := f.room(u)
			return &Task{
				RoomID: room.ID, PostID: f.post(room, u).ID, ColumnID: f.column(room).ID, Position: 2,
				Title: "Write", DueDate: ptr(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
			}
		}},
		{"TaskAssignee", "task_assignees", func(f *fix) any {
			u := f.user()
			room := f.room(u)
			return &TaskAssignee{TaskID: f.task(f.post(room, u), f.column(room)).ID, UserID: u.ID}
		}},
		{"Notification", "notifications", func(f *fix) any {
			u := f.user()
			room := f.room(u)
			post := f.post(room, u)
			task := f.task(post, f.column(room))
			return &Notification{
				UserID: f.user().ID, Kind: NotificationMention, PostID: &post.ID, TaskID: &task.ID, RoomID: &room.ID,
				ActorID: u.ID, ReadAt: ptr(day(time.Hour)),
			}
		}},
		{"PostFollow", "post_follows", func(f *fix) any {
			u := f.user()
			return &PostFollow{UserID: u.ID, PostID: f.post(f.room(u), u).ID, Unfollowed: true}
		}},
		{"Conversation", "conversations", func(f *fix) any {
			return &Conversation{Kind: ConversationDirect, Name: "", CreatedBy: f.user().ID, DirectKey: ptr("1-2")}
		}},
		{"ConversationMember", "conversation_members", func(f *fix) any {
			u := f.user()
			c := f.group(u)
			return &ConversationMember{
				ConversationID: c.ID, UserID: f.user().ID, AddedBy: &u.ID,
				LastReadMessageID: &f.message(c, u).ID, LeftAt: ptr(day(time.Hour)),
			}
		}},
		{"Message", "messages", func(f *fix) any {
			u := f.user()
			c := f.group(u)
			return &Message{
				ConversationID: c.ID, AuthorID: u.ID, Body: "hello", ReplyToID: &f.message(c, u).ID,
				EditedAt: ptr(day(time.Hour)), DeletedAt: ptr(day(2 * time.Hour)),
			}
		}},
		{"MessageAttachment", "message_attachments", func(f *fix) any {
			u := f.user()
			return &MessageAttachment{
				MessageID: f.message(f.group(u), u).ID, UploaderID: u.ID, FileName: "a.pdf", ContentType: "application/pdf",
				Size: 99, StorageKey: "k1", Width: ptr(1), Height: ptr(2),
			}
		}},
		{"MessageReaction", "message_reactions", func(f *fix) any {
			u := f.user()
			return &MessageReaction{MessageID: f.message(f.group(u), u).ID, UserID: u.ID, Emoji: "ok"}
		}},
		{"UserSettings", "user_settings", func(f *fix) any {
			return &UserSettings{UserID: f.user().ID, Theme: ThemeDark, Sound: false, BrowserNotifications: true}
		}},
		{"PlacePref", "place_prefs", func(f *fix) any {
			u := f.user()
			return &PlacePref{
				UserID: u.ID, RoomID: &f.room(u).ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOff,
				IgnoreGroupMentions: true, MarkUnread: MarkUnreadMentions, Muted: true,
			}
		}},
		{"AuditEvent", "audit_events", func(f *fix) any {
			return &AuditEvent{
				ActorID: f.user().ID, Action: ActionRoomRenamed, RoomID: 77, EntityType: EntityRoom, EntityID: 77,
				Summary: "alice renamed Lobby to Hall", Details: `{"name":{"old":"Lobby","new":"Hall"}}`,
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			want := tt.row(f)
			f.clk.Advance(time.Minute) // the rows made for the test are older
			f.create(want)

			wv := reflect.ValueOf(want).Elem()
			id := wv.FieldByName("ID").Uint()
			if id == 0 {
				t.Fatal("the ID was not set")
			}
			created := wv.FieldByName("CreatedAt").Interface().(time.Time)
			if !created.Equal(f.clk.Now()) {
				t.Errorf("CreatedAt = %v, want the clock's %v", created, f.clk.Now())
			}

			got := reflect.New(wv.Type()).Interface()
			if err := f.s.db(f.ctx).First(got, id).Error; err != nil {
				t.Fatalf("read back: %v", err)
			}
			requireSameRow(t, want, got)

			// The time is stored as text in UTC.
			var raw string
			err := f.s.db(f.ctx).Raw("SELECT created_at FROM "+tt.table+" WHERE id = ?", id).Scan(&raw).Error
			if err != nil {
				t.Fatal(err)
			}
			if want := f.clk.Now().Format(time.RFC3339); raw != want || !strings.HasSuffix(raw, "Z") {
				t.Errorf("stored created_at = %q, want %q (UTC)", raw, want)
			}
		})
	}
}

func TestEveryModelIsMigrated(t *testing.T) {
	f := newFix(t)
	for _, m := range allModels() {
		if !f.s.db(f.ctx).Migrator().HasTable(m) {
			t.Errorf("no table for %T", m)
		}
	}
	if got := len(allModels()); got != 27 {
		t.Errorf("%d models, want the 27 of SPEC §6.3", got)
	}
}

func TestUniqueConstraints(t *testing.T) {
	tests := []struct {
		name string
		// rows returns a row to insert, and a second row that breaks the
		// constraint.
		rows func(f *fix) (first, second any)
	}{
		{"users.username", func(f *fix) (any, any) {
			return &User{Username: "a", Role: RoleMember}, &User{Username: "a", Role: RoleAdmin}
		}},
		{"invitations.token_hash", func(f *fix) (any, any) {
			u := f.user()
			return &Invitation{TokenHash: "h", Role: RoleMember, CreatedBy: &u.ID, ExpiresAt: day(time.Hour)},
				&Invitation{TokenHash: "h", Role: RoleAdmin, CreatedBy: &u.ID, ExpiresAt: day(time.Hour)}
		}},
		{"password_resets.token_hash", func(f *fix) (any, any) {
			u := f.user()
			return &PasswordReset{UserID: u.ID, TokenHash: "h", ExpiresAt: day(time.Hour)},
				&PasswordReset{UserID: u.ID, TokenHash: "h", ExpiresAt: day(time.Hour)}
		}},
		{"ssh_keys.fingerprint", func(f *fix) (any, any) {
			a, b := f.user(), f.user()
			return &SSHKey{UserID: a.ID, Name: "a", Type: "ssh-ed25519", Fingerprint: "SHA256:x", PublicKey: "ssh-ed25519 AAAA"},
				&SSHKey{UserID: b.ID, Name: "b", Type: "ssh-ed25519", Fingerprint: "SHA256:x", PublicKey: "ssh-ed25519 AAAA"}
		}},
		{"sessions.token_hash", func(f *fix) (any, any) {
			u := f.user()
			return &Session{TokenHash: "h", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0)},
				&Session{TokenHash: "h", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0)}
		}},
		{"rooms.name", func(f *fix) (any, any) {
			u := f.user()
			return &Room{Name: "Lobby", Visibility: VisibilityVisible, CreatedBy: u.ID},
				&Room{Name: "Lobby", Visibility: VisibilityPrivate, CreatedBy: u.ID}
		}},
		{"rooms.name ignoring case", func(f *fix) (any, any) {
			u := f.user()
			return &Room{Name: "Lobby", Visibility: VisibilityVisible, CreatedBy: u.ID},
				&Room{Name: "LOBBY", Visibility: VisibilityVisible, CreatedBy: u.ID}
		}},
		{"room_members room,user", func(f *fix) (any, any) {
			u := f.user()
			r := f.room(u)
			return &RoomMember{RoomID: r.ID, UserID: u.ID}, &RoomMember{RoomID: r.ID, UserID: u.ID, AddedBy: &u.ID}
		}},
		{"tags.name", func(f *fix) (any, any) {
			return &Tag{Name: "go"}, &Tag{Name: "go"}
		}},
		{"post_tags post,tag", func(f *fix) (any, any) {
			u := f.user()
			p, tag := f.post(f.room(u), u), f.tag()
			return &PostTag{PostID: p.ID, TagID: tag.ID}, &PostTag{PostID: p.ID, TagID: tag.ID}
		}},
		{"attachments.storage_key", func(f *fix) (any, any) {
			u := f.user()
			p := f.post(f.room(u), u)
			return &Attachment{PostID: p.ID, UploaderID: u.ID, StorageKey: "k"}, &Attachment{PostID: p.ID, UploaderID: u.ID, StorageKey: "k"}
		}},
		{"reactions post,user,emoji", func(f *fix) (any, any) {
			u := f.user()
			p := f.post(f.room(u), u)
			return &Reaction{PostID: p.ID, UserID: u.ID, Emoji: "x"}, &Reaction{PostID: p.ID, UserID: u.ID, Emoji: "x"}
		}},
		{"mentions post,user", func(f *fix) (any, any) {
			u := f.user()
			p := f.post(f.room(u), u)
			return &Mention{PostID: p.ID, UserID: u.ID}, &Mention{PostID: p.ID, UserID: u.ID}
		}},
		{"post_follows user,post", func(f *fix) (any, any) {
			u := f.user()
			p := f.post(f.room(u), u)
			return &PostFollow{UserID: u.ID, PostID: p.ID}, &PostFollow{UserID: u.ID, PostID: p.ID}
		}},
		{"task_assignees task,user", func(f *fix) (any, any) {
			u := f.user()
			r := f.room(u)
			task := f.task(f.post(r, u), f.column(r))
			return &TaskAssignee{TaskID: task.ID, UserID: u.ID}, &TaskAssignee{TaskID: task.ID, UserID: u.ID}
		}},
		{"conversations.direct_key", func(f *fix) (any, any) {
			u := f.user()
			return &Conversation{Kind: ConversationDirect, CreatedBy: u.ID, DirectKey: ptr("1-2")},
				&Conversation{Kind: ConversationDirect, CreatedBy: u.ID, DirectKey: ptr("1-2")}
		}},
		{"conversation_members conversation,user", func(f *fix) (any, any) {
			u := f.user()
			c := f.group(u)
			return &ConversationMember{ConversationID: c.ID, UserID: u.ID}, &ConversationMember{ConversationID: c.ID, UserID: u.ID}
		}},
		{"place_prefs user,room", func(f *fix) (any, any) {
			u := f.user()
			r := f.room(u)
			return &PlacePref{UserID: u.ID, RoomID: &r.ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOn, MarkUnread: MarkUnreadAll},
				&PlacePref{UserID: u.ID, RoomID: &r.ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOn, MarkUnread: MarkUnreadAll}
		}},
		{"place_prefs user,conversation", func(f *fix) (any, any) {
			u := f.user()
			c := f.group(u)
			return &PlacePref{UserID: u.ID, ConversationID: &c.ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOn, MarkUnread: MarkUnreadAll},
				&PlacePref{UserID: u.ID, ConversationID: &c.ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOn, MarkUnread: MarkUnreadAll}
		}},
		{"message_attachments.storage_key", func(f *fix) (any, any) {
			u := f.user()
			m := f.message(f.group(u), u)
			return &MessageAttachment{MessageID: m.ID, UploaderID: u.ID, StorageKey: "k"},
				&MessageAttachment{MessageID: m.ID, UploaderID: u.ID, StorageKey: "k"}
		}},
		{"message_reactions message,user,emoji", func(f *fix) (any, any) {
			u := f.user()
			m := f.message(f.group(u), u)
			return &MessageReaction{MessageID: m.ID, UserID: u.ID, Emoji: "x"}, &MessageReaction{MessageID: m.ID, UserID: u.ID, Emoji: "x"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			first, second := tt.rows(f)
			f.create(first)
			err := f.s.db(f.ctx).Create(second).Error
			if err == nil {
				t.Fatal("the duplicate was stored")
			}
			if classify(err) != failureConflict {
				t.Errorf("classify(%v) = %d, want failureConflict", err, classify(err))
			}
		})
	}
}

// TestUniqueConstraintsAllowDifferences checks that the unique constraints
// refuse only what they should.
func TestUniqueConstraintsAllowDifferences(t *testing.T) {
	f := newFix(t)
	u1, u2 := f.user(), f.user()
	room := f.room(u1)
	post := f.post(room, u1)
	other := f.post(room, u1)

	rows := []any{
		// The same user in another room, another user in the same room.
		&RoomMember{RoomID: room.ID, UserID: u1.ID},
		&RoomMember{RoomID: room.ID, UserID: u2.ID},
		&RoomMember{RoomID: f.room(u1).ID, UserID: u1.ID},
		// Another emoji, another user, another post.
		&Reaction{PostID: post.ID, UserID: u1.ID, Emoji: "a"},
		&Reaction{PostID: post.ID, UserID: u1.ID, Emoji: "b"},
		&Reaction{PostID: post.ID, UserID: u2.ID, Emoji: "a"},
		&Reaction{PostID: other.ID, UserID: u1.ID, Emoji: "a"},
		// Two groups have no direct key; two direct conversations differ by it.
		&Conversation{Kind: ConversationGroup, CreatedBy: u1.ID},
		&Conversation{Kind: ConversationGroup, CreatedBy: u1.ID},
		&Conversation{Kind: ConversationDirect, CreatedBy: u1.ID, DirectKey: ptr("1-2")},
		&Conversation{Kind: ConversationDirect, CreatedBy: u1.ID, DirectKey: ptr("1-3")},
		// A mention-free notification list may hold many alike.
		&Notification{UserID: u1.ID, Kind: NotificationReply, PostID: &post.ID, ActorID: u2.ID},
		&Notification{UserID: u1.ID, Kind: NotificationReply, PostID: &post.ID, ActorID: u2.ID},
	}
	for _, row := range rows {
		f.create(row)
	}
}

func TestCheckConstraints(t *testing.T) {
	tests := []struct {
		name       string
		constraint string
		row        func(f *fix) any
	}{
		{"user role", "chk_users_role", func(f *fix) any {
			return &User{Username: "a", Role: "owner"}
		}},
		{"user role empty", "chk_users_role", func(f *fix) any {
			return &User{Username: "a"}
		}},
		{"username with upper case", "chk_users_username_lower", func(f *fix) any {
			return &User{Username: "Alice", Role: RoleMember}
		}},
		{"invitation role", "chk_invitations_role", func(f *fix) any {
			return &Invitation{TokenHash: "h", Role: "owner", CreatedBy: &f.user().ID, ExpiresAt: day(time.Hour)}
		}},
		{"room visibility", "chk_rooms_visibility", func(f *fix) any {
			return &Room{Name: "r", Visibility: "secret", CreatedBy: f.user().ID}
		}},
		{"post format", "chk_posts_format", func(f *fix) any {
			u := f.user()
			return &Post{RoomID: f.room(u).ID, AuthorID: u.ID, Format: "html"}
		}},
		{"comment without root", "chk_posts_root_with_parent", func(f *fix) any {
			u := f.user()
			p := f.post(f.room(u), u)
			return &Post{RoomID: p.RoomID, AuthorID: u.ID, ParentID: &p.ID, Format: FormatText}
		}},
		{"post with a root", "chk_posts_root_with_parent", func(f *fix) any {
			u := f.user()
			p := f.post(f.room(u), u)
			return &Post{RoomID: p.RoomID, AuthorID: u.ID, RootID: &p.ID, Format: FormatText}
		}},
		{"attachment size", "chk_attachments_size", func(f *fix) any {
			u := f.user()
			return &Attachment{PostID: f.post(f.room(u), u).ID, UploaderID: u.ID, Size: -1, StorageKey: "k"}
		}},
		{"notification kind", "chk_notifications_kind", func(f *fix) any {
			u := f.user()
			return &Notification{UserID: u.ID, Kind: "poke", ActorID: u.ID}
		}},
		{"conversation kind", "chk_conversations_kind", func(f *fix) any {
			return &Conversation{Kind: "channel", CreatedBy: f.user().ID}
		}},
		{"direct conversation without key", "chk_conversations_direct_key", func(f *fix) any {
			return &Conversation{Kind: ConversationDirect, CreatedBy: f.user().ID}
		}},
		{"group conversation with key", "chk_conversations_direct_key", func(f *fix) any {
			return &Conversation{Kind: ConversationGroup, CreatedBy: f.user().ID, DirectKey: ptr("1-2")}
		}},
		{"message attachment size", "chk_message_attachments_size", func(f *fix) any {
			u := f.user()
			return &MessageAttachment{MessageID: f.message(f.group(u), u).ID, UploaderID: u.ID, Size: -1, StorageKey: "k"}
		}},
		{"audit entity type", "chk_audit_events_entity_type", func(f *fix) any {
			return &AuditEvent{ActorID: f.user().ID, Action: ActionRoomCreated, EntityType: "poke", Details: "{}"}
		}},
		{"user settings theme", "chk_user_settings_theme", func(f *fix) any {
			return &UserSettings{UserID: f.user().ID, Theme: "neon", Sound: true}
		}},
		{"user settings notify", "chk_user_settings_notify", func(f *fix) any {
			return &UserSettings{UserID: f.user().ID, Theme: ThemeSystem, Notify: "some", SoundName: SoundPurr, Replies: RepliesFollowing}
		}},
		{"user settings sound name", "chk_user_settings_sound_name", func(f *fix) any {
			return &UserSettings{UserID: f.user().ID, Theme: ThemeSystem, Notify: NotifyAll, SoundName: "moo", Replies: RepliesFollowing}
		}},
		{"user settings replies", "chk_user_settings_replies", func(f *fix) any {
			return &UserSettings{UserID: f.user().ID, Theme: ThemeSystem, Notify: NotifyAll, SoundName: SoundPurr, Replies: "some"}
		}},
		{"place prefs two places", "chk_place_prefs_one_place", func(f *fix) any {
			u := f.user()
			c := f.group(u)
			return &PlacePref{UserID: u.ID, RoomID: &f.room(u).ID, ConversationID: &c.ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOn, MarkUnread: MarkUnreadAll}
		}},
		{"place prefs no place", "chk_place_prefs_one_place", func(f *fix) any {
			return &PlacePref{UserID: f.user().ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOn, MarkUnread: MarkUnreadAll}
		}},
		{"place prefs notify", "chk_place_prefs_notify", func(f *fix) any {
			u := f.user()
			return &PlacePref{UserID: u.ID, RoomID: &f.room(u).ID, Notify: "some", Sound: PlaceSoundOn, MarkUnread: MarkUnreadAll}
		}},
		{"place prefs sound", "chk_place_prefs_sound", func(f *fix) any {
			u := f.user()
			return &PlacePref{UserID: u.ID, RoomID: &f.room(u).ID, Notify: PlaceNotifyAll, Sound: "loud", MarkUnread: MarkUnreadAll}
		}},
		{"place prefs mark unread", "chk_place_prefs_mark_unread", func(f *fix) any {
			u := f.user()
			return &PlacePref{UserID: u.ID, RoomID: &f.room(u).ID, Notify: PlaceNotifyAll, Sound: PlaceSoundOn, MarkUnread: "none"}
		}},
		{"audit details not JSON", "chk_audit_events_details_json", func(f *fix) any {
			return &AuditEvent{ActorID: f.user().ID, Action: ActionRoomCreated, EntityType: EntityRoom, Details: "{"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			row := tt.row(f)
			err := f.s.db(f.ctx).Create(row).Error
			if err == nil {
				t.Fatal("the wrong value was stored")
			}
			if classify(err) == failureConflict {
				t.Fatalf("reported as a conflict: %v", err)
			}
			if !isCheckFailure(err, tt.constraint) {
				t.Errorf("error = %v, want a failure of the check constraint %s", err, tt.constraint)
			}
		})
	}
}

// isCheckFailure reports whether err is the failure of the check constraint
// named constraint. SQLite words it ("CHECK constraint failed: name"); on
// PostgreSQL it is the SQLSTATE 23514 with the constraint's name.
func isCheckFailure(err error, constraint string) bool {
	if onPostgres() {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == constraint
	}
	return err != nil && strings.Contains(err.Error(), "CHECK constraint failed: "+constraint)
}

func TestIDsAreNeverReused(t *testing.T) {
	f := newFix(t)
	first := f.tag()
	f.exec("DELETE FROM tags WHERE id = ?", first.ID)
	second := f.tag()
	if second.ID <= first.ID {
		t.Errorf("a new row got ID %d after a row with ID %d was deleted", second.ID, first.ID)
	}
}

// isForeignKeyFailure reports whether err is a foreign key violation: on
// SQLite GORM translates the one of an insert, and the driver words the one of
// a RESTRICT action by itself; on PostgreSQL it is the SQLSTATE.
func isForeignKeyFailure(err error) bool {
	if onPostgres() {
		// 23503 is a foreign key violation; a RESTRICT action reports 23001.
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23001")
	}
	return err != nil && (errors.Is(err, gorm.ErrForeignKeyViolated) || strings.Contains(err.Error(), "FOREIGN KEY constraint failed"))
}

// foreignKey is a row of PRAGMA foreign_key_list with the table it belongs to.
type foreignKey struct {
	Table    string
	Column   string
	RefTable string
	OnDelete string
}

func (k foreignKey) String() string {
	return fmt.Sprintf("%s.%s -> %s ON DELETE %s", k.Table, k.Column, k.RefTable, k.OnDelete)
}

func TestForeignKeyActions(t *testing.T) {
	const (
		cascade  = "CASCADE"
		restrict = "RESTRICT"
		setNull  = "SET NULL"
	)
	want := []foreignKey{
		{"invitations", "created_by", "users", restrict},
		{"invitations", "used_by", "users", restrict},
		{"sessions", "user_id", "users", restrict},
		{"rooms", "created_by", "users", restrict},
		{"room_members", "room_id", "rooms", cascade},
		{"room_members", "user_id", "users", restrict},
		{"room_members", "added_by", "users", restrict},
		{"posts", "room_id", "rooms", cascade},
		{"posts", "author_id", "users", restrict},
		{"posts", "parent_id", "posts", cascade},
		{"posts", "root_id", "posts", cascade},
		{"post_tags", "post_id", "posts", cascade},
		{"post_tags", "tag_id", "tags", cascade},
		{"attachments", "post_id", "posts", cascade},
		{"attachments", "uploader_id", "users", restrict},
		{"reactions", "post_id", "posts", cascade},
		{"reactions", "user_id", "users", restrict},
		{"mentions", "post_id", "posts", cascade},
		{"mentions", "user_id", "users", restrict},
		{"board_columns", "room_id", "rooms", cascade},
		{"tasks", "room_id", "rooms", cascade},
		{"tasks", "post_id", "posts", cascade},
		{"tasks", "column_id", "board_columns", cascade},
		{"task_assignees", "task_id", "tasks", cascade},
		{"task_assignees", "user_id", "users", restrict},
		{"notifications", "user_id", "users", restrict},
		{"notifications", "post_id", "posts", cascade},
		{"notifications", "task_id", "tasks", cascade},
		{"notifications", "room_id", "rooms", cascade},
		{"notifications", "actor_id", "users", restrict},
		{"post_follows", "user_id", "users", cascade},
		{"post_follows", "post_id", "posts", cascade},
		{"conversations", "created_by", "users", restrict},
		{"conversation_members", "conversation_id", "conversations", cascade},
		{"conversation_members", "user_id", "users", restrict},
		{"conversation_members", "added_by", "users", restrict},
		{"conversation_members", "last_read_message_id", "messages", setNull},
		{"place_prefs", "user_id", "users", restrict},
		{"place_prefs", "room_id", "rooms", cascade},
		{"place_prefs", "conversation_id", "conversations", cascade},
		{"messages", "conversation_id", "conversations", cascade},
		{"messages", "author_id", "users", restrict},
		{"messages", "reply_to_id", "messages", setNull},
		{"message_attachments", "message_id", "messages", cascade},
		{"message_attachments", "uploader_id", "users", restrict},
		{"message_reactions", "message_id", "messages", cascade},
		{"message_reactions", "user_id", "users", restrict},
		// The audit event outlives its room and entity: only the actor.
		{"audit_events", "actor_id", "users", restrict},
		{"user_settings", "user_id", "users", restrict},
		{"password_resets", "user_id", "users", restrict},
		{"password_resets", "created_by", "users", restrict},
		{"ssh_keys", "user_id", "users", restrict},
		{"ssh_codes", "user_id", "users", restrict},
		{"ssh_codes", "key_id", "ssh_keys", cascade},
	}

	f := newFix(t)
	var got []foreignKey
	query := `
		SELECT m.name AS "table", p."from" AS "column", p."table" AS ref_table, p.on_delete
		FROM sqlite_master m, pragma_foreign_key_list(m.name) p
		WHERE m.type = 'table'`
	if onPostgres() {
		// confdeltype: a no action, r restrict, c cascade, n set null.
		query = `
		SELECT c.conrelid::regclass::text AS "table", a.attname AS "column", c.confrelid::regclass::text AS ref_table,
			CASE c.confdeltype WHEN 'c' THEN 'CASCADE' WHEN 'r' THEN 'RESTRICT' WHEN 'n' THEN 'SET NULL'
				WHEN 'a' THEN 'NO ACTION' ELSE 'OTHER' END AS on_delete
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace`
	}
	err := f.s.db(f.ctx).Raw(query).Scan(&got).Error
	if err != nil {
		t.Fatal(err)
	}
	gotSet := map[foreignKey]bool{}
	for _, k := range got {
		gotSet[k] = true
	}
	for _, k := range want {
		if !gotSet[k] {
			t.Errorf("missing foreign key %s", k)
		}
		delete(gotSet, k)
	}
	for k := range gotSet {
		t.Errorf("unexpected foreign key %s", k)
	}
}

func TestIndexes(t *testing.T) {
	// The columns of each index, in order; "id DESC" is a descending column.
	want := map[string]struct {
		table   string
		unique  bool
		columns string
	}{
		"uq_users_username":                         {"users", true, "username"},
		"uq_invitations_token_hash":                 {"invitations", true, "token_hash"},
		"uq_password_resets_token_hash":             {"password_resets", true, "token_hash"},
		"idx_password_resets_user_id":               {"password_resets", false, "user_id"},
		"uq_ssh_keys_fingerprint":                   {"ssh_keys", true, "fingerprint"},
		"idx_ssh_keys_user_id":                      {"ssh_keys", false, "user_id"},
		"idx_ssh_codes_user_id":                     {"ssh_codes", false, "user_id"},
		"idx_ssh_codes_created_at":                  {"ssh_codes", false, "created_at"},
		"uq_sessions_token_hash":                    {"sessions", true, "token_hash"},
		"idx_sessions_user_id":                      {"sessions", false, "user_id"},
		"uq_rooms_name":                             {"rooms", true, "name"},
		"uq_room_members_room_user":                 {"room_members", true, "room_id,user_id"},
		"idx_room_members_user_id":                  {"room_members", false, "user_id"},
		"idx_posts_feed":                            {"posts", false, "room_id,parent_id,id DESC"},
		"idx_posts_thread":                          {"posts", false, "root_id,id"},
		"idx_posts_parent_id":                       {"posts", false, "parent_id"},
		"uq_tags_name":                              {"tags", true, "name"},
		"uq_post_tags_post_tag":                     {"post_tags", true, "post_id,tag_id"},
		"idx_post_tags_tag_id":                      {"post_tags", false, "tag_id"},
		"idx_attachments_post_id":                   {"attachments", false, "post_id"},
		"uq_attachments_storage_key":                {"attachments", true, "storage_key"},
		"uq_reactions_post_user_emoji":              {"reactions", true, "post_id,user_id,emoji"},
		"uq_mentions_post_user":                     {"mentions", true, "post_id,user_id"},
		"idx_board_columns_room_position":           {"board_columns", false, "room_id,position"},
		"idx_tasks_board":                           {"tasks", false, "room_id,column_id,position"},
		"idx_tasks_post_id":                         {"tasks", false, "post_id"},
		"uq_task_assignees_task_user":               {"task_assignees", true, "task_id,user_id"},
		"idx_task_assignees_user_id":                {"task_assignees", false, "user_id"},
		"idx_notifications_user_read":               {"notifications", false, "user_id,read_at,id"},
		"idx_notifications_post_id":                 {"notifications", false, "post_id"},
		"idx_notifications_task_id":                 {"notifications", false, "task_id"},
		"idx_notifications_room_id":                 {"notifications", false, "room_id"},
		"uq_post_follows_user_post":                 {"post_follows", true, "user_id,post_id"},
		"idx_post_follows_post":                     {"post_follows", false, "post_id"},
		"uq_conversations_direct_key":               {"conversations", true, "direct_key"},
		"uq_conversation_members_conversation_user": {"conversation_members", true, "conversation_id,user_id"},
		"idx_conversation_members_user_id":          {"conversation_members", false, "user_id"},
		"uq_place_prefs_user_room":                  {"place_prefs", true, "user_id,room_id"},
		"uq_place_prefs_user_conversation":          {"place_prefs", true, "user_id,conversation_id"},
		"idx_messages_conversation":                 {"messages", false, "conversation_id,id"},
		"idx_message_attachments_message_id":        {"message_attachments", false, "message_id"},
		"uq_message_attachments_storage_key":        {"message_attachments", true, "storage_key"},
		"uq_message_reactions_message_user_emoji":   {"message_reactions", true, "message_id,user_id,emoji"},
		"idx_audit_events_created_at":               {"audit_events", false, "created_at"},
		"idx_audit_events_room":                     {"audit_events", false, "room_id,id"},
		"idx_audit_events_actor":                    {"audit_events", false, "actor_id,id"},
		"idx_audit_events_action":                   {"audit_events", false, "action,id"},
		"uq_user_settings_user_id":                  {"user_settings", true, "user_id"},
	}

	if onPostgres() {
		// Found in step 14.2: see migrationPostsFeedRootsPostgres.
		want["idx_posts_feed_roots"] = struct {
			table   string
			unique  bool
			columns string
		}{"posts", false, "room_id,id DESC"}
	}

	f := newFix(t)
	// Each index with the columns of its key, in order.
	var indexes []struct {
		Name     string
		TblName  string
		IsUnique bool
		Columns  string
	}
	if onPostgres() {
		// The search index (posts_search and its GIN index) has no SQLite
		// counterpart among these: SQLite keeps it in a virtual table.
		err := f.s.db(f.ctx).Raw(`
			SELECT i.relname AS name, t.relname AS tbl_name, x.indisunique AS is_unique, (
				SELECT string_agg(a.attname || CASE WHEN x.indoption[k.n - 1] & 1 = 1 THEN ' DESC' ELSE '' END, ',' ORDER BY k.n)
				FROM generate_series(1, x.indnkeyatts) AS k(n)
				JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = x.indkey[k.n - 1]) AS columns
			FROM pg_index x
			JOIN pg_class i ON i.oid = x.indexrelid
			JOIN pg_class t ON t.oid = x.indrelid
			WHERE t.relnamespace = 'public'::regnamespace AND NOT x.indisprimary AND t.relname <> 'posts_search'`).Scan(&indexes).Error
		if err != nil {
			t.Fatal(err)
		}
	} else {
		err := f.s.db(f.ctx).Raw(`
			SELECT m.name, m.tbl_name, l."unique" AS is_unique
			FROM sqlite_master m, pragma_index_list(m.tbl_name) l
			WHERE m.type = 'index' AND m.name = l.name AND m.sql IS NOT NULL`).Scan(&indexes).Error
		if err != nil {
			t.Fatal(err)
		}
		for i, idx := range indexes {
			var cols []struct {
				Name string
				Desc bool
			}
			err := f.s.db(f.ctx).Raw(`SELECT name, "desc" FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, idx.Name).Scan(&cols).Error
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, len(cols))
			for j, c := range cols {
				names[j] = c.Name
				if c.Desc {
					names[j] += " DESC"
				}
			}
			indexes[i].Columns = strings.Join(names, ",")
		}
	}
	seen := map[string]bool{}
	for _, idx := range indexes {
		seen[idx.Name] = true
		w, ok := want[idx.Name]
		if !ok {
			t.Errorf("unexpected index %s on %s", idx.Name, idx.TblName)
			continue
		}
		if w.table != idx.TblName || w.unique != idx.IsUnique {
			t.Errorf("index %s is on %s (unique %v), want %s (unique %v)", idx.Name, idx.TblName, idx.IsUnique, w.table, w.unique)
		}
		if idx.Columns != w.columns {
			t.Errorf("index %s has columns %s, want %s", idx.Name, idx.Columns, w.columns)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("missing index %s", name)
		}
	}
}

func TestFeedAndThreadQueriesUseTheirIndexes(t *testing.T) {
	tests := []struct {
		name  string
		query string
		args  []any
		index string
	}{
		{"feed", "SELECT id FROM posts WHERE room_id = ? AND parent_id IS NULL ORDER BY id DESC LIMIT 30", []any{1}, "idx_posts_feed"},
		{"thread", "SELECT id FROM posts WHERE root_id = ? ORDER BY id", []any{1}, "idx_posts_thread"},
		{"board", "SELECT id FROM tasks WHERE room_id = ? ORDER BY column_id, position", []any{1}, "idx_tasks_board"},
		{"columns", "SELECT id FROM board_columns WHERE room_id = ? ORDER BY position", []any{1}, "idx_board_columns_room_position"},
		{"unread", "SELECT id FROM notifications WHERE user_id = ? AND read_at IS NULL ORDER BY id DESC", []any{1}, "idx_notifications_user_read"},
		{"messages", "SELECT id FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT 50", []any{1}, "idx_messages_conversation"},
		{"audit by room", "SELECT id FROM audit_events WHERE room_id = ? ORDER BY id DESC LIMIT 50", []any{1}, "idx_audit_events_room"},
		{"audit by actor", "SELECT id FROM audit_events WHERE actor_id = ? ORDER BY id DESC LIMIT 50", []any{1}, "idx_audit_events_actor"},
		{"audit by action", "SELECT id FROM audit_events WHERE action = ? ORDER BY id DESC LIMIT 50", []any{"x"}, "idx_audit_events_action"},
		{"audit by time", "SELECT id FROM audit_events WHERE created_at >= ? AND created_at < ?", []any{fakeStart, fakeStart.Add(time.Hour)}, "idx_audit_events_created_at"},
		{"task of a post", "SELECT id FROM tasks WHERE post_id = ?", []any{1}, "idx_tasks_post_id"},
		{"attachments of a post", "SELECT id FROM attachments WHERE post_id = ?", []any{1}, "idx_attachments_post_id"},
		{"session by token", "SELECT id FROM sessions WHERE token_hash = ?", []any{"x"}, "uq_sessions_token_hash"},
		{"invitation by token", "SELECT id FROM invitations WHERE token_hash = ?", []any{"x"}, "uq_invitations_token_hash"},
	}
	f := newFix(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := explain(t, f.s, tt.query, tt.args...)
			if !plan.uses(tt.index) {
				t.Errorf("plan %q does not use %s", plan.details, tt.index)
			}
		})
	}
}

func TestMigrationOfModelsIsIdempotent(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "twice.db")
	clk := clock.NewFake(fakeStart)

	schema := func(s *Store) []string {
		var rows []string
		err := s.db(ctx).Raw("SELECT type || ' ' || name || ' ' || coalesce(sql, '') FROM sqlite_master ORDER BY type, name").Scan(&rows).Error
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	var first []string
	for i := range 3 {
		s, err := open(ctx, path, clk, nil, defaultOptions())
		if err != nil {
			t.Fatalf("open #%d: %+v", i+1, err)
		}
		if i == 0 {
			if err := s.db(ctx).Create(&Tag{Name: "kept"}).Error; err != nil {
				t.Fatal(err)
			}
			first = schema(s)
			if len(first) < 60 {
				t.Fatalf("only %d schema objects", len(first))
			}
		} else if got := schema(s); !slices.Equal(got, first) {
			t.Errorf("open #%d changed the schema", i+1)
		}
		var n int64
		if err := s.db(ctx).Model(&Tag{}).Count(&n).Error; err != nil || n != 1 {
			t.Errorf("open #%d: %d tags (%v), want 1", i+1, n, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// room delete and post delete ---------------------------------------------

// world is two rooms, each with a post, and comments, a board, and one row of
// each kind hanging on every post.
type world struct {
	f          *fix
	alice, bob *User
	roomA      *Room
	roomB      *Room
	// postA is a post of room A with the comment commentA1 and its reply
	// commentA2; postB is a post of room B with the comment commentB1.
	postA, commentA1, commentA2 *Post
	postB, commentB1            *Post
	colsA                       []*BoardColumn
}

func newWorld(f *fix) *world {
	w := &world{f: f, alice: f.user(), bob: f.user()}
	tag := f.tag()
	w.roomA, w.roomB = f.room(w.alice), f.room(w.alice)
	for _, r := range []*Room{w.roomA, w.roomB} {
		f.create(&RoomMember{RoomID: r.ID, UserID: w.alice.ID})
		f.create(&RoomMember{RoomID: r.ID, UserID: w.bob.ID, AddedBy: &w.alice.ID})
		cols := []*BoardColumn{f.column(r), f.column(r), f.column(r)}
		if r == w.roomA {
			w.colsA = cols
		}
		f.create(&Notification{UserID: w.bob.ID, Kind: NotificationAddedToRoom, RoomID: &r.ID, ActorID: w.alice.ID})
		f.create(&AuditEvent{
			ActorID: w.alice.ID, Action: ActionRoomCreated, RoomID: r.ID, EntityType: EntityRoom, EntityID: r.ID,
			Summary: "room created", Details: "{}",
		})
		post := f.post(r, w.alice)
		c1 := f.comment(post, w.bob)
		posts := []*Post{post, c1}
		if r == w.roomA {
			w.postA, w.commentA1 = post, c1
			w.commentA2 = f.comment(c1, w.alice)
			posts = append(posts, w.commentA2)
		} else {
			w.postB, w.commentB1 = post, c1
		}
		for _, p := range posts {
			f.hang(p, cols[0], w.bob, tag)
		}
	}
	return w
}

// requireCounts fails the test unless each table has the number of rows.
func requireCounts(t *testing.T, f *fix, want map[string]int64) {
	t.Helper()
	for table, n := range want {
		if got := f.count(table); got != n {
			t.Errorf("%s has %d rows, want %d", table, got, n)
		}
	}
}

func TestDeleteRoomCascades(t *testing.T) {
	f := newFix(t)
	w := newWorld(f)

	requireCounts(t, f, map[string]int64{
		"rooms": 2, "room_members": 4, "board_columns": 6, "posts": 5, "post_tags": 5, "attachments": 5,
		"reactions": 5, "mentions": 5, "tasks": 5, "task_assignees": 5, "notifications": 12, "audit_events": 2,
		"tags": 1, "users": 2,
	})
	if err := f.s.db(f.ctx).Delete(&Room{}, w.roomA.ID).Error; err != nil {
		t.Fatalf("delete the room: %v", err)
	}
	// Only the rows of room B are left, and the audit events of both rooms,
	// the tags and the users.
	requireCounts(t, f, map[string]int64{
		"rooms": 1, "room_members": 2, "board_columns": 3, "posts": 2, "post_tags": 2, "attachments": 2,
		"reactions": 2, "mentions": 2, "tasks": 2, "task_assignees": 2, "notifications": 5, "audit_events": 2,
		"tags": 1, "users": 2,
	})
	var left int64
	err := f.s.db(f.ctx).Model(&AuditEvent{}).Where("room_id = ?", w.roomA.ID).Count(&left).Error
	if err != nil || left != 1 {
		t.Errorf("%d audit events of the deleted room (%v), want 1", left, err)
	}
}

func TestDeletePostCascades(t *testing.T) {
	tests := []struct {
		name string
		// victim returns the post to delete.
		victim func(w *world) *Post
		// wantPosts is how many posts are left, which is how many of every
		// kind of row that hangs on a post are left too.
		wantPosts int64
	}{
		{"a post takes its comments with it", func(w *world) *Post { return w.postA }, 2},
		{"a comment takes its replies with it", func(w *world) *Post { return w.commentA1 }, 3},
		{"a comment without replies", func(w *world) *Post { return w.commentA2 }, 4},
		{"a comment of another room", func(w *world) *Post { return w.commentB1 }, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			w := newWorld(f)
			if err := f.s.db(f.ctx).Delete(&Post{}, tt.victim(w).ID).Error; err != nil {
				t.Fatalf("delete: %v", err)
			}
			n := tt.wantPosts
			// 5 posts at the start; the rooms, members, columns and the two
			// notifications of the rooms are not touched.
			requireCounts(t, f, map[string]int64{
				"posts": n, "post_tags": n, "attachments": n, "reactions": n, "mentions": n, "tasks": n,
				"task_assignees": n, "notifications": 2*n + 2, "rooms": 2, "room_members": 4, "board_columns": 6,
				"audit_events": 2,
			})
		})
	}
}

func TestDeleteColumnCascadesToItsTasks(t *testing.T) {
	f := newFix(t)
	w := newWorld(f)
	if err := f.s.db(f.ctx).Delete(&BoardColumn{}, w.colsA[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	// The three tasks of room A were in its first column; their posts stay.
	requireCounts(t, f, map[string]int64{
		"board_columns": 5, "tasks": 2, "task_assignees": 2, "posts": 5, "notifications": 9,
	})
}

func TestDeleteUserIsRefused(t *testing.T) {
	tests := []struct {
		name string
		// use makes a row that refers to the user it returns.
		use func(f *fix) *User
	}{
		{"post author", func(f *fix) *User { u := f.user(); f.post(f.room(f.user()), u); return u }},
		{"room creator", func(f *fix) *User { u := f.user(); f.room(u); return u }},
		{"room member", func(f *fix) *User {
			u := f.user()
			f.create(&RoomMember{RoomID: f.room(f.user()).ID, UserID: u.ID})
			return u
		}},
		{"who added a room member", func(f *fix) *User {
			u, other := f.user(), f.user()
			f.create(&RoomMember{RoomID: f.room(u).ID, UserID: other.ID, AddedBy: &u.ID})
			return u
		}},
		{"session", func(f *fix) *User {
			u := f.user()
			f.create(&Session{TokenHash: "h", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0)})
			return u
		}},
		{"invitation creator", func(f *fix) *User {
			u := f.user()
			f.create(&Invitation{TokenHash: "h", Role: RoleMember, CreatedBy: &u.ID, ExpiresAt: day(time.Hour)})
			return u
		}},
		{"invitation user", func(f *fix) *User {
			u := f.user()
			f.create(&Invitation{TokenHash: "h", Role: RoleMember, CreatedBy: &f.user().ID, ExpiresAt: day(time.Hour), UsedBy: &u.ID})
			return u
		}},
		{"password reset user", func(f *fix) *User {
			u := f.user()
			f.create(&PasswordReset{UserID: u.ID, TokenHash: "h", ExpiresAt: day(time.Hour)})
			return u
		}},
		{"password reset creator", func(f *fix) *User {
			u := f.user()
			f.create(&PasswordReset{UserID: f.user().ID, TokenHash: "h", CreatedBy: &u.ID, ExpiresAt: day(time.Hour)})
			return u
		}},
		{"ssh key", func(f *fix) *User {
			u := f.user()
			f.create(&SSHKey{UserID: u.ID, Name: "k", Type: "ssh-ed25519", Fingerprint: "SHA256:z", PublicKey: "ssh-ed25519 ZZZZ"})
			return u
		}},
		{"reaction", func(f *fix) *User {
			u := f.user()
			creator := f.user()
			f.create(&Reaction{PostID: f.post(f.room(creator), creator).ID, UserID: u.ID, Emoji: "x"})
			return u
		}},
		{"uploader", func(f *fix) *User {
			u, creator := f.user(), f.user()
			f.attachment(f.post(f.room(creator), creator), u)
			return u
		}},
		{"notified user", func(f *fix) *User {
			u := f.user()
			f.create(&Notification{UserID: u.ID, Kind: NotificationReply, ActorID: f.user().ID})
			return u
		}},
		{"notification actor", func(f *fix) *User {
			u := f.user()
			f.create(&Notification{UserID: f.user().ID, Kind: NotificationReply, ActorID: u.ID})
			return u
		}},
		{"audit actor", func(f *fix) *User {
			u := f.user()
			f.create(&AuditEvent{ActorID: u.ID, Action: ActionRoomCreated, EntityType: EntityRoom, Details: "{}"})
			return u
		}},
		{"message author", func(f *fix) *User {
			u, creator := f.user(), f.user()
			f.message(f.group(creator), u)
			return u
		}},
		{"conversation member", func(f *fix) *User {
			u := f.user()
			f.create(&ConversationMember{ConversationID: f.group(f.user()).ID, UserID: u.ID})
			return u
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			u := tt.use(f)
			err := f.s.db(f.ctx).Delete(&User{}, u.ID).Error
			if err == nil {
				t.Fatal("the user was deleted")
			}
			if !isForeignKeyFailure(err) {
				t.Errorf("error = %v, want a foreign key failure", err)
			}
			var n int64
			if err := f.s.db(f.ctx).Model(&User{}).Where("id = ?", u.ID).Count(&n).Error; err != nil || n != 1 {
				t.Errorf("%d users with the ID (%v), want 1", n, err)
			}
		})
	}

	t.Run("a user without rows can be deleted", func(t *testing.T) {
		f := newFix(t)
		u := f.user()
		if err := f.s.db(f.ctx).Delete(&User{}, u.ID).Error; err != nil {
			t.Errorf("delete: %v", err)
		}
	})
}

func TestDeleteMessageSetsReferencesToNull(t *testing.T) {
	f := newFix(t)
	u := f.user()
	conv := f.group(u)
	original := f.message(conv, u)
	reply := &Message{ConversationID: conv.ID, AuthorID: u.ID, Body: "reply", ReplyToID: &original.ID}
	f.create(reply)
	member := &ConversationMember{ConversationID: conv.ID, UserID: u.ID, LastReadMessageID: &original.ID}
	f.create(member)

	if err := f.s.db(f.ctx).Delete(&Message{}, original.ID).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	var gotReply Message
	if err := f.s.db(f.ctx).First(&gotReply, reply.ID).Error; err != nil {
		t.Fatalf("the reply is gone: %v", err)
	}
	if gotReply.ReplyToID != nil {
		t.Errorf("ReplyToID = %d, want nil", *gotReply.ReplyToID)
	}
	var gotMember ConversationMember
	if err := f.s.db(f.ctx).First(&gotMember, member.ID).Error; err != nil {
		t.Fatalf("the member is gone: %v", err)
	}
	if gotMember.LastReadMessageID != nil {
		t.Errorf("LastReadMessageID = %d, want nil", *gotMember.LastReadMessageID)
	}
}

func TestDeleteConversationCascades(t *testing.T) {
	f := newFix(t)
	u := f.user()
	conv, other := f.group(u), f.group(u)
	for _, c := range []*Conversation{conv, other} {
		f.create(&ConversationMember{ConversationID: c.ID, UserID: u.ID})
		m := f.message(c, u)
		f.create(&MessageAttachment{MessageID: m.ID, UploaderID: u.ID, StorageKey: fmt.Sprintf("k%d", c.ID)})
		f.create(&MessageReaction{MessageID: m.ID, UserID: u.ID, Emoji: "x"})
	}
	if err := f.s.db(f.ctx).Delete(&Conversation{}, conv.ID).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	requireCounts(t, f, map[string]int64{
		"conversations": 1, "conversation_members": 1, "messages": 1, "message_attachments": 1, "message_reactions": 1,
	})
}

func TestForeignKeysRefuseMissingParents(t *testing.T) {
	tests := []struct {
		name string
		row  func(f *fix) any
	}{
		{"post in a missing room", func(f *fix) any {
			return &Post{RoomID: 999, AuthorID: f.user().ID, Format: FormatText}
		}},
		{"comment of a missing post", func(f *fix) any {
			u := f.user()
			missing := uint64(999)
			return &Post{RoomID: f.room(u).ID, AuthorID: u.ID, ParentID: &missing, RootID: &missing, Format: FormatText}
		}},
		{"audit event of a missing actor", func(f *fix) any {
			return &AuditEvent{ActorID: 999, Action: ActionRoomCreated, EntityType: EntityRoom, Details: "{}"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			err := f.s.db(f.ctx).Create(tt.row(f)).Error
			if !isForeignKeyFailure(err) {
				t.Errorf("error = %v, want a foreign key failure", err)
			}
		})
	}

	t.Run("audit event of a missing room is stored", func(t *testing.T) {
		f := newFix(t)
		ev := &AuditEvent{ActorID: f.user().ID, Action: ActionRoomDeleted, RoomID: 999, EntityType: EntityRoom, EntityID: 999, Details: "{}"}
		if err := f.s.db(f.ctx).Create(ev).Error; err != nil {
			t.Errorf("create: %v", err)
		}
	})
}
