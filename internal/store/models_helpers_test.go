package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
)

// fix is a store with every model migrated and helpers that insert valid rows,
// so that a test names only what it is about.
type fix struct {
	tb  testing.TB
	ctx context.Context
	s   *Store
	clk *clock.Fake
	n   int
}

// newFix opens a store with the models of the program: on a file in a
// temporary directory, or, when CLOWDER_TEST_POSTGRES is set, on a new
// PostgreSQL database. The store is closed when the test ends.
func newFix(tb testing.TB) *fix {
	tb.Helper()
	clk := clock.NewFake(fakeStart)
	if onPostgres() {
		return &fix{tb: tb, ctx: tb.Context(), s: openPostgresCopy(tb, clk), clk: clk}
	}
	path := filepath.Join(tb.TempDir(), "models.db")
	s, err := open(tb.Context(), path, clk, nil, defaultOptions())
	if err != nil {
		tb.Fatalf("open: %+v", err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Errorf("close: %v", err)
		}
	})
	return &fix{tb: tb, ctx: tb.Context(), s: s, clk: clk}
}

// ptr returns a pointer to a copy of v.
func ptr[T any](v T) *T {
	return &v
}

// next returns a number that no earlier call of the fixture returned.
func (f *fix) next() int {
	f.n++
	return f.n
}

// create inserts row, which must be a pointer to a model, and fails the test
// on an error.
func (f *fix) create(row any) {
	f.tb.Helper()
	if err := f.s.db(f.ctx).Create(row).Error; err != nil {
		f.tb.Fatalf("create %T: %v", row, err)
	}
}

// exec runs a statement and fails the test on an error.
func (f *fix) exec(query string, args ...any) {
	f.tb.Helper()
	if err := f.s.db(f.ctx).Exec(query, args...).Error; err != nil {
		f.tb.Fatalf("%s: %v", query, err)
	}
}

// count returns the number of rows of a table.
func (f *fix) count(table string) int64 {
	f.tb.Helper()
	var n int64
	if err := f.s.db(f.ctx).Raw("SELECT count(*) FROM " + table).Scan(&n).Error; err != nil {
		f.tb.Fatalf("count %s: %v", table, err)
	}
	return n
}

func (f *fix) user() *User {
	f.tb.Helper()
	n := f.next()
	u := &User{
		Username:     fmt.Sprintf("user%d", n),
		DisplayName:  fmt.Sprintf("User %d", n),
		PasswordHash: "hash",
		Role:         RoleMember,
	}
	f.create(u)
	return u
}

func (f *fix) room(creator *User) *Room {
	f.tb.Helper()
	r := &Room{Name: fmt.Sprintf("room%d", f.next()), Visibility: VisibilityVisible, CreatedBy: creator.ID}
	f.create(r)
	return r
}

func (f *fix) post(room *Room, author *User) *Post {
	f.tb.Helper()
	p := &Post{RoomID: room.ID, AuthorID: author.ID, Format: FormatText, Body: fmt.Sprintf("post%d", f.next())}
	f.create(p)
	return p
}

// comment inserts a comment under parent, a post or a comment.
func (f *fix) comment(parent *Post, author *User) *Post {
	f.tb.Helper()
	root := parent.ID
	if parent.RootID != nil {
		root = *parent.RootID
	}
	c := &Post{
		RoomID: parent.RoomID, AuthorID: author.ID, ParentID: &parent.ID, RootID: &root,
		Format: FormatText, Body: fmt.Sprintf("comment%d", f.next()),
	}
	f.create(c)
	return c
}

func (f *fix) tag() *Tag {
	f.tb.Helper()
	t := &Tag{Name: fmt.Sprintf("tag%d", f.next())}
	f.create(t)
	return t
}

func (f *fix) column(room *Room) *BoardColumn {
	f.tb.Helper()
	c := &BoardColumn{RoomID: room.ID, Name: fmt.Sprintf("column%d", f.next()), Position: f.next()}
	f.create(c)
	return c
}

func (f *fix) task(post *Post, col *BoardColumn) *Task {
	f.tb.Helper()
	t := &Task{RoomID: post.RoomID, PostID: post.ID, ColumnID: col.ID, Position: f.next(), Title: "task"}
	f.create(t)
	return t
}

// group inserts a group conversation.
func (f *fix) group(creator *User) *Conversation {
	f.tb.Helper()
	c := &Conversation{Kind: ConversationGroup, CreatedBy: creator.ID}
	f.create(c)
	return c
}

// direct inserts the direct conversation of two users.
func (f *fix) direct(a, b *User) *Conversation {
	f.tb.Helper()
	key := DirectKeyOf(a.ID, b.ID)
	c := &Conversation{Kind: ConversationDirect, CreatedBy: a.ID, DirectKey: &key}
	f.create(c)
	return c
}

// call inserts a call from caller to callee in their direct conversation c,
// in the state given (ringing, or ended with reason hangup).
func (f *fix) call(c *Conversation, caller, callee *User, state CallState) *Call {
	f.tb.Helper()
	call := &Call{ConversationID: c.ID, CallerID: caller.ID, CalleeID: callee.ID, State: state, Home: "p1"}
	if state == CallEnded {
		call.EndedAt = ptr(f.clk.Now())
		call.EndReason = CallHangup
	}
	f.create(call)
	return call
}

func (f *fix) message(c *Conversation, author *User) *Message {
	f.tb.Helper()
	m := &Message{ConversationID: c.ID, AuthorID: author.ID, Body: fmt.Sprintf("message%d", f.next())}
	f.create(m)
	return m
}

func (f *fix) attachment(post *Post, uploader *User) *Attachment {
	f.tb.Helper()
	a := &Attachment{
		PostID: post.ID, UploaderID: uploader.ID, FileName: "a.txt", ContentType: "text/plain", Size: 1,
		StorageKey: fmt.Sprintf("key%d", f.next()),
	}
	f.create(a)
	return a
}

// hang inserts one row of every kind that hangs on a post: a reaction, an
// attachment, a mention, a tag link, a task with an assignee, and a
// notification of the post and one of the task. col is a column of the room
// of the post and by a user.
func (f *fix) hang(post *Post, col *BoardColumn, by *User, tag *Tag) {
	f.tb.Helper()
	f.create(&Reaction{PostID: post.ID, UserID: by.ID, Emoji: "x"})
	f.attachment(post, by)
	f.create(&Mention{PostID: post.ID, UserID: by.ID})
	f.create(&PostTag{PostID: post.ID, TagID: tag.ID})
	task := f.task(post, col)
	f.create(&TaskAssignee{TaskID: task.ID, UserID: by.ID})
	f.create(&Notification{UserID: by.ID, Kind: NotificationReply, PostID: &post.ID, ActorID: by.ID})
	f.create(&Notification{UserID: by.ID, Kind: NotificationAssigned, TaskID: &task.ID, ActorID: by.ID})
}

// day is a time on the fake clock's calendar, for fields that are not set by
// the clock.
func day(offset time.Duration) time.Time {
	return fakeStart.Add(offset)
}
