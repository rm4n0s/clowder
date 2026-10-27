package store

import (
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"
)

// countQueries counts the statements the store sends to the database until
// the test ends.
func countQueries(f *fix) *atomic.Int64 {
	f.tb.Helper()
	var n atomic.Int64
	count := func(*gorm.DB) { n.Add(1) }
	cb := f.s.gdb.Callback()
	if err := cb.Query().Before("gorm:query").Register("test:count_query", count); err != nil {
		f.tb.Fatal(err)
	}
	if err := cb.Row().Before("gorm:row").Register("test:count_row", count); err != nil {
		f.tb.Fatal(err)
	}
	return &n
}

// groupWith inserts a group of the users and returns it.
func (f *fix) groupWith(creator *User, others ...*User) *Conversation {
	f.tb.Helper()
	c := &Conversation{Kind: ConversationGroup, CreatedBy: creator.ID}
	f.create(c)
	f.create(&ConversationMember{ConversationID: c.ID, UserID: creator.ID})
	for _, u := range others {
		f.create(&ConversationMember{ConversationID: c.ID, UserID: u.ID, AddedBy: &creator.ID})
	}
	return c
}

// say inserts a message.
func (f *fix) say(c *Conversation, author *User, body string) *Message {
	f.tb.Helper()
	m := &Message{ConversationID: c.ID, AuthorID: author.ID, Body: body}
	f.create(m)
	return m
}

func TestDirectKeyOf(t *testing.T) {
	tests := []struct {
		a, b uint64
		want string
	}{
		{3, 7, "3:7"}, {7, 3, "3:7"}, {10, 9, "9:10"}, {1, 1, "1:1"},
	}
	for _, tt := range tests {
		if got := DirectKeyOf(tt.a, tt.b); got != tt.want {
			t.Errorf("DirectKeyOf(%d, %d) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestConversationQueries(t *testing.T) {
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()

	t.Run("a direct conversation is unique for its key", func(t *testing.T) {
		key := DirectKeyOf(alice.ID, bob.ID)
		c := &Conversation{Kind: ConversationDirect, CreatedBy: alice.ID, DirectKey: &key}
		if err := f.s.CreateConversation(f.ctx, c); err != nil || c.ID == 0 {
			t.Fatalf("CreateConversation = %v, id %d", err, c.ID)
		}
		again := &Conversation{Kind: ConversationDirect, CreatedBy: bob.ID, DirectKey: &key}
		err := f.s.CreateConversation(f.ctx, again)
		requireTagged(t, err, TagConflict, "Store.CreateConversation.Conflict", "createConversation")
		got, err := f.s.DirectConversation(f.ctx, key)
		if err != nil || got.ID != c.ID {
			t.Errorf("DirectConversation = %+v, %v; want %d", got, err, c.ID)
		}
		_, err = f.s.DirectConversation(f.ctx, DirectKeyOf(alice.ID, carol.ID))
		requireTagged(t, err, TagNotFound, "Store.DirectConversation.NotFound", "directConversation")
	})

	t.Run("ConversationByID", func(t *testing.T) {
		c := f.groupWith(alice, bob)
		got, err := f.s.ConversationByID(f.ctx, c.ID)
		if err != nil || got.Kind != ConversationGroup || got.DirectKey != nil {
			t.Errorf("ConversationByID = %+v, %v", got, err)
		}
		_, err = f.s.ConversationByID(f.ctx, 99999)
		requireTagged(t, err, TagNotFound, "Store.ConversationByID.NotFound", "conversationByID")
	})

	t.Run("members, leaving and coming back", func(t *testing.T) {
		c := f.groupWith(alice, bob, carol)
		if err := f.s.CreateConversationMembers(f.ctx, []ConversationMember{{ConversationID: c.ID, UserID: bob.ID}}); err == nil {
			t.Error("a user was added twice")
		} else {
			requireTagged(t, err, TagConflict, "Store.CreateConversationMembers.Conflict", "createConversationMembers")
		}
		if err := f.s.CreateConversationMembers(f.ctx, nil); err != nil {
			t.Errorf("no members: %+v", err)
		}
		if err := f.s.MarkConversationMemberLeft(f.ctx, c.ID, carol.ID, f.clk.Now()); err != nil {
			t.Fatal(err)
		}
		names := func(withLeft bool) []string {
			t.Helper()
			m, err := f.s.ConversationMembers(f.ctx, []uint64{c.ID}, withLeft)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, r := range m[c.ID] {
				out = append(out, r.Username)
			}
			return out
		}
		if got, want := names(false), []string{alice.Username, bob.Username}; !slices.Equal(got, want) {
			t.Errorf("current members = %v, want %v", got, want)
		}
		if got, want := names(true), []string{alice.Username, bob.Username, carol.Username}; !slices.Equal(got, want) {
			t.Errorf("all members = %v, want %v", got, want)
		}
		if err := f.s.ReaddConversationMember(f.ctx, c.ID, carol.ID, alice.ID); err != nil {
			t.Fatal(err)
		}
		m, err := f.s.ConversationMemberOf(f.ctx, c.ID, carol.ID)
		if err != nil || m.LeftAt != nil || m.AddedBy == nil || *m.AddedBy != alice.ID {
			t.Errorf("ConversationMemberOf = %+v, %v", m, err)
		}
		_, err = f.s.ConversationMemberOf(f.ctx, c.ID, 99999)
		requireTagged(t, err, TagNotFound, "Store.ConversationMemberOf.NotFound", "conversationMemberOf")
		if got, err := f.s.ConversationMembers(f.ctx, nil, true); err != nil || len(got) != 0 {
			t.Errorf("ConversationMembers(nil) = %v, %v", got, err)
		}
		if err := f.s.SetConversationName(f.ctx, c.ID, "crew"); err != nil {
			t.Fatal(err)
		}
		if got, err := f.s.ConversationByID(f.ctx, c.ID); err != nil || got.Name != "crew" {
			t.Errorf("name = %q, %v", got.Name, err)
		}
	})

	t.Run("UsersByIDs", func(t *testing.T) {
		got, err := f.s.UsersByIDs(f.ctx, []uint64{alice.ID, carol.ID, 99999})
		if err != nil || len(got) != 2 {
			t.Errorf("UsersByIDs = %+v, %v", got, err)
		}
		if got, err := f.s.UsersByIDs(f.ctx, nil); err != nil || got != nil {
			t.Errorf("UsersByIDs(nil) = %+v, %v", got, err)
		}
	})

	t.Run("UsersByUsernames", func(t *testing.T) {
		got, err := f.s.UsersByUsernames(f.ctx, []string{alice.Username, carol.Username, "nobody"})
		if err != nil || len(got) != 2 {
			t.Errorf("UsersByUsernames = %+v, %v", got, err)
		}
		if got, err := f.s.UsersByUsernames(f.ctx, nil); err != nil || got != nil {
			t.Errorf("UsersByUsernames(nil) = %+v, %v", got, err)
		}
	})
}

func TestAdvanceLastReadAndUnreadCount(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	c := f.groupWith(alice, bob)
	m1, m2 := f.say(c, alice, "one"), f.say(c, alice, "two")
	own := f.say(c, bob, "mine")

	count := func(u *User) int {
		t.Helper()
		n, err := f.s.CountUnreadConversations(f.ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(bob) != 1 || count(alice) != 1 {
		t.Fatalf("counts = %d, %d; want 1, 1", count(bob), count(alice))
	}
	moved, err := f.s.AdvanceLastRead(f.ctx, c.ID, bob.ID, m1.ID)
	if err != nil || !moved || count(bob) != 1 {
		t.Fatalf("AdvanceLastRead(m1) = %v, %v, count %d", moved, err, count(bob))
	}
	for _, tt := range []struct {
		name string
		to   uint64
		want bool
	}{{"forward", m2.ID, true}, {"the same", m2.ID, false}, {"back", m1.ID, false}, {"further", own.ID, true}} {
		if moved, err := f.s.AdvanceLastRead(f.ctx, c.ID, bob.ID, tt.to); err != nil || moved != tt.want {
			t.Errorf("%s: moved = %v, %v; want %v", tt.name, moved, err, tt.want)
		}
	}
	if count(bob) != 0 {
		t.Errorf("bob's count = %d after reading everything", count(bob))
	}
	if count(alice) != 1 {
		t.Fatalf("alice's count = %d, want 1: bob's message is unread", count(alice))
	}
	if err := f.s.MarkMessageDeleted(f.ctx, own.ID, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if count(alice) != 0 {
		t.Errorf("alice's count = %d after bob deleted his message, want 0", count(alice))
	}
	f.say(c, bob, "again")
	f.exec("UPDATE conversation_members SET left_at = ? WHERE conversation_id = ? AND user_id = ?", f.clk.Now(), c.ID, alice.ID)
	if count(alice) != 0 {
		t.Errorf("a member who left counts %d", count(alice))
	}
}

func TestMessageQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	c := f.groupWith(alice, bob)
	var ms []*Message
	for _, body := range []string{"a", "b", "c", "d", "e"} {
		ms = append(ms, f.say(c, alice, body))
	}
	other := f.groupWith(bob, alice)
	f.say(other, bob, "elsewhere")

	t.Run("MessagePage newest first, before and limit", func(t *testing.T) {
		ids := func(before uint64, limit int) []uint64 {
			t.Helper()
			rows, err := f.s.MessagePage(f.ctx, c.ID, before, limit)
			if err != nil {
				t.Fatal(err)
			}
			var out []uint64
			for _, r := range rows {
				out = append(out, r.ID)
			}
			return out
		}
		if got, want := ids(0, 2), []uint64{ms[4].ID, ms[3].ID}; !slices.Equal(got, want) {
			t.Errorf("first page = %v, want %v", got, want)
		}
		if got, want := ids(ms[3].ID, 10), []uint64{ms[2].ID, ms[1].ID, ms[0].ID}; !slices.Equal(got, want) {
			t.Errorf("older page = %v, want %v", got, want)
		}
	})

	t.Run("MessageRowsByIDs has the author", func(t *testing.T) {
		got, err := f.s.MessageRowsByIDs(f.ctx, []uint64{ms[0].ID, 99999})
		if err != nil || len(got) != 1 || got[ms[0].ID].AuthorUsername != alice.Username || got[ms[0].ID].Body != "a" {
			t.Errorf("MessageRowsByIDs = %+v, %v", got, err)
		}
		if got, err := f.s.MessageRowsByIDs(f.ctx, nil); err != nil || len(got) != 0 {
			t.Errorf("MessageRowsByIDs(nil) = %v, %v", got, err)
		}
	})

	t.Run("edit and delete", func(t *testing.T) {
		if err := f.s.UpdateMessageBody(f.ctx, ms[0].ID, "edited", f.clk.Now()); err != nil {
			t.Fatal(err)
		}
		got, err := f.s.MessageByID(f.ctx, ms[0].ID)
		if err != nil || got.Body != "edited" || got.EditedAt == nil {
			t.Errorf("MessageByID = %+v, %v", got, err)
		}
		_, err = f.s.MessageByID(f.ctx, 99999)
		requireTagged(t, err, TagNotFound, "Store.MessageByID.NotFound", "messageByID")
		f.create(&MessageAttachment{MessageID: ms[1].ID, UploaderID: alice.ID, FileName: "f", ContentType: "text/plain", StorageKey: "mk1"})
		f.create(&MessageReaction{MessageID: ms[1].ID, UserID: bob.ID, Emoji: "x"})
		if err := f.s.DeleteMessageExtras(f.ctx, ms[1].ID); err != nil {
			t.Fatal(err)
		}
		if err := f.s.MarkMessageDeleted(f.ctx, ms[1].ID, f.clk.Now()); err != nil {
			t.Fatal(err)
		}
		got, err = f.s.MessageByID(f.ctx, ms[1].ID)
		if err != nil || got.Body != "" || got.DeletedAt == nil {
			t.Errorf("deleted message = %+v, %v", got, err)
		}
		if f.count("message_attachments") != 0 || f.count("message_reactions") != 0 {
			t.Error("the extras are still there")
		}
	})

	t.Run("attachments, and which keys are in use", func(t *testing.T) {
		rows := []MessageAttachment{
			{MessageID: ms[2].ID, UploaderID: alice.ID, FileName: "b.txt", ContentType: "text/plain", StorageKey: "mk2"},
			{MessageID: ms[2].ID, UploaderID: alice.ID, FileName: "a.txt", ContentType: "text/plain", StorageKey: "mk3"},
			{MessageID: ms[3].ID, UploaderID: alice.ID, FileName: "c.txt", ContentType: "text/plain", StorageKey: "mk4"},
		}
		if err := f.s.CreateMessageAttachments(f.ctx, rows); err != nil {
			t.Fatal(err)
		}
		err := f.s.CreateMessageAttachments(f.ctx, []MessageAttachment{{MessageID: ms[3].ID, UploaderID: alice.ID, FileName: "d", ContentType: "t", StorageKey: "mk2"}})
		requireTagged(t, err, TagConflict, "Store.CreateMessageAttachments.Conflict", "createMessageAttachments")
		if err := f.s.CreateMessageAttachments(f.ctx, nil); err != nil {
			t.Fatal(err)
		}
		got, err := f.s.MessageAttachmentsOf(f.ctx, []uint64{ms[2].ID, ms[3].ID, ms[4].ID})
		if err != nil || len(got[ms[2].ID]) != 2 || got[ms[2].ID][0].FileName != "b.txt" || len(got[ms[3].ID]) != 1 || len(got[ms[4].ID]) != 0 {
			t.Errorf("MessageAttachmentsOf = %+v, %v", got, err)
		}
		one, err := f.s.MessageAttachmentByID(f.ctx, rows[0].ID)
		if err != nil || one.StorageKey != "mk2" {
			t.Errorf("MessageAttachmentByID = %+v, %v", one, err)
		}
		_, err = f.s.MessageAttachmentByID(f.ctx, 99999)
		requireTagged(t, err, TagNotFound, "Store.MessageAttachmentByID.NotFound", "messageAttachmentByID")
		// A post's attachment and a message's attachment both count as in use.
		room := f.room(alice)
		f.create(&Attachment{PostID: f.post(room, alice).ID, UploaderID: alice.ID, FileName: "p", ContentType: "t", Size: 1, StorageKey: "pk1"})
		used, err := f.s.StorageKeysInUse(f.ctx, []string{"mk2", "pk1", "free"})
		slices.Sort(used)
		if err != nil || !slices.Equal(used, []string{"mk2", "pk1"}) {
			t.Errorf("StorageKeysInUse = %v, %v", used, err)
		}
	})

	t.Run("reactions in order of first use, with names", func(t *testing.T) {
		for _, r := range []struct {
			u     *User
			emoji string
		}{{bob, "b"}, {alice, "a"}, {alice, "b"}} {
			if added, err := f.s.ToggleMessageReaction(f.ctx, ms[4].ID, r.u.ID, r.emoji); err != nil || !added {
				t.Fatalf("toggle = %v, %v", added, err)
			}
		}
		got, err := f.s.ReactionsOfMessages(f.ctx, []uint64{ms[4].ID, ms[0].ID})
		if err != nil || len(got) != 1 {
			t.Fatalf("ReactionsOfMessages = %+v, %v", got, err)
		}
		var keys [][2]string
		for _, r := range got[ms[4].ID] {
			keys = append(keys, [2]string{r.Emoji, r.DisplayName})
		}
		if want := [][2]string{{"b", bob.DisplayName}, {"a", alice.DisplayName}, {"b", alice.DisplayName}}; !reflect.DeepEqual(keys, want) {
			t.Errorf("reactions = %v, want %v", keys, want)
		}
		if added, err := f.s.ToggleMessageReaction(f.ctx, ms[4].ID, bob.ID, "b"); err != nil || added {
			t.Errorf("second toggle = %v, %v; want removed", added, err)
		}
		if got, err := f.s.ReactionsOfMessages(f.ctx, nil); err != nil || len(got) != 0 {
			t.Errorf("ReactionsOfMessages(nil) = %v, %v", got, err)
		}
	})

	t.Run("PutMessageReaction and DeleteMessageReaction are idempotent", func(t *testing.T) {
		steps := []struct {
			name string
			put  bool
			want bool
			rows int
		}{
			{"put adds", true, true, 1},
			{"put again changes nothing", true, false, 1},
			{"delete removes", false, true, 0},
			{"delete again changes nothing", false, false, 0},
		}
		for _, st := range steps {
			var changed bool
			var err error
			if st.put {
				changed, err = f.s.PutMessageReaction(f.ctx, ms[1].ID, alice.ID, "\U0001F525")
			} else {
				changed, err = f.s.DeleteMessageReaction(f.ctx, ms[1].ID, alice.ID, "\U0001F525")
			}
			if err != nil || changed != st.want {
				t.Fatalf("%s = %v, %v, want %v", st.name, changed, err, st.want)
			}
			rows, err := f.s.ReactionsOfMessages(f.ctx, []uint64{ms[1].ID})
			if err != nil || len(rows[ms[1].ID]) != st.rows {
				t.Fatalf("%s: %d rows, %v, want %d", st.name, len(rows[ms[1].ID]), err, st.rows)
			}
		}
		_, err := f.s.PutMessageReaction(f.ctx, 99999, alice.ID, "x")
		requireTagged(t, err, TagQueryFailed, "Store.PutMessageReaction.QueryFailed", "putMessageReaction")
	})
}

func TestListConversationsRunsAFewQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	n := countQueries(f)

	list := func() []ConversationListItem {
		t.Helper()
		n.Store(0)
		items, err := f.s.ListConversations(f.ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	if items := list(); len(items) != 0 || n.Load() != 1 {
		t.Fatalf("no conversations: %d items, %d queries", len(items), n.Load())
	}
	var first *Conversation
	for i := range 3 {
		c := f.groupWith(alice, bob)
		if first == nil {
			first = c
		}
		f.say(c, bob, "hello")
		last := f.say(c, alice, "last")
		f.create(&MessageAttachment{MessageID: last.ID, UploaderID: alice.ID, FileName: "f.txt", ContentType: "t", StorageKey: "k" + string(rune('a'+i))})
	}
	few := list()
	fewQueries := n.Load()
	for range 20 {
		c := f.groupWith(alice, bob)
		f.say(c, bob, "hello")
		f.say(c, alice, "last")
	}
	many := list()
	if len(few) != 3 || len(many) != 23 {
		t.Fatalf("items = %d and %d", len(few), len(many))
	}
	if n.Load() != fewQueries || fewQueries > 4 {
		t.Errorf("queries: %d for 3 conversations, %d for 23; want the same and at most 4", fewQueries, n.Load())
	}
	item := few[0]
	if item.Conversation.ID != first.ID || item.LastMessage == nil || item.LastMessage.Body != "last" ||
		item.LastMessage.AuthorUsername != alice.Username || !slices.Equal(item.LastAttachmentNames, []string{"f.txt"}) ||
		len(item.Members) != 2 || !item.Unread || item.LastReadMessageID != 0 {
		t.Errorf("first item = %+v", item)
	}
}
