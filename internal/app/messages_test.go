package app_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// msgEnv is the messages service on a database with these users:
//
//	admin  admin, a member of nothing
//	alice  member
//	bob    member
//	carol  member
//	dave   member
//	erin   member
//
// A Recorder sits behind the service. The tests make their own conversations.
type msgEnv struct {
	*env
	msgs     *app.Messages
	rec      *live.Recorder
	files    *files.Store
	filesDir string
	users    map[string]app.User
}

func newMsgEnv(t *testing.T) *msgEnv {
	t.Helper()
	e := &msgEnv{env: newEnv(t), rec: &live.Recorder{}, users: map[string]app.User{}}
	e.filesDir = filepath.Join(t.TempDir(), "files")
	var err error
	if e.files, err = files.New(e.filesDir); err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	e.msgs = app.NewMessages(e.st, e.clk, e.files, app.MessagesConfig{
		MaxAttachmentSize: testMaxUpload,
		MaxAttachments:    testMaxAttachments,
	}, e.rec)
	e.users["admin"] = e.signUp("admin", store.RoleAdmin)
	for _, name := range []string{"alice", "bob", "carol", "dave", "erin"} {
		e.users[name] = e.signUp(name, store.RoleMember)
	}
	return e
}

// as returns the Actor of the named user.
func (e *msgEnv) as(name string) app.Actor {
	e.t.Helper()
	u, ok := e.users[name]
	if !ok {
		e.t.Fatalf("no user %q", name)
	}
	return actor(u)
}

// uid is the ID of the named user.
func (e *msgEnv) uid(name string) uint64 {
	e.t.Helper()
	return e.users[name].ID
}

// uids are the IDs of the named users.
func (e *msgEnv) uids(names ...string) []uint64 {
	e.t.Helper()
	ids := make([]uint64, len(names))
	for i, n := range names {
		ids[i] = e.uid(n)
	}
	return ids
}

// disable disables the named user.
func (e *msgEnv) disable(name string) {
	e.t.Helper()
	if err := e.svc.SetDisabled(e.t.Context(), e.as("admin"), e.uid(name), true); err != nil {
		e.t.Fatalf("SetDisabled %s: %+v", name, err)
	}
}

// direct starts the direct conversation of two users.
func (e *msgEnv) direct(from, to string) uint64 {
	e.t.Helper()
	id, err := e.msgs.StartDirect(e.t.Context(), e.as(from), e.uid(to))
	if err != nil {
		e.t.Fatalf("StartDirect %s %s: %+v", from, to, err)
	}
	return id
}

// group starts a group of the first user and the others.
func (e *msgEnv) group(name string, from string, others ...string) uint64 {
	e.t.Helper()
	id, err := e.msgs.StartGroup(e.t.Context(), e.as(from), e.uids(others...), name)
	if err != nil {
		e.t.Fatalf("StartGroup: %+v", err)
	}
	return id
}

// send sends a text message and ticks the clock, so that messages differ in
// time.
func (e *msgEnv) send(user string, conv uint64, body string) app.Message {
	e.t.Helper()
	m, err := e.msgs.Send(e.t.Context(), e.as(user), conv, app.MessageInput{Body: body})
	if err != nil {
		e.t.Fatalf("Send %q: %+v", body, err)
	}
	e.clk.Advance(time.Minute)
	return m
}

// upload makes the user upload a text file.
func (e *msgEnv) upload(user, name string) app.Upload {
	e.t.Helper()
	u, err := e.msgs.Upload(e.t.Context(), e.as(user), name, bytes.NewReader([]byte("content of "+name)))
	if err != nil {
		e.t.Fatalf("Upload %q: %+v", name, err)
	}
	return u
}

// onDisk returns the keys of the files in the files directory, sorted.
func (e *msgEnv) onDisk() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.filesDir)
	if err != nil {
		e.t.Fatalf("ReadDir: %v", err)
	}
	keys := []string{}
	for _, en := range entries {
		if en.Name() != "tmp" {
			keys = append(keys, en.Name())
		}
	}
	slices.Sort(keys)
	return keys
}

// history returns every message of the conversation as the user sees them.
func (e *msgEnv) history(user string, conv uint64) []app.Message {
	e.t.Helper()
	page, err := e.msgs.History(e.t.Context(), e.as(user), conv, 0, 100)
	if err != nil {
		e.t.Fatalf("History: %+v", err)
	}
	return page.Messages
}

// unread is the unread count of the user.
func (e *msgEnv) unread(user string) int {
	e.t.Helper()
	n, err := e.msgs.UnreadCount(e.t.Context(), e.as(user))
	if err != nil {
		e.t.Fatalf("UnreadCount: %+v", err)
	}
	return n
}

// bodies returns the bodies of the messages.
func bodies(ms []app.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Body
	}
	return out
}

// requireInvalid fails unless err is a TagInvalidInput with a route that
// contains route and a message under each of the fields.
func requireInvalid(t *testing.T, err error, route string, fields ...string) {
	t.Helper()
	appErr := requireTag(t, err, app.TagInvalidInput, route)
	got := fieldsOf(t, appErr)
	for _, f := range fields {
		if got[f] == "" {
			t.Errorf("no message for the field %q in %v", f, got)
		}
	}
	if len(got) != len(fields) {
		t.Errorf("fields = %v, want only %v", got, fields)
	}
}

func TestMessagesAliasesMatchTheStore(t *testing.T) {
	if app.ConversationDirect != store.ConversationDirect || app.ConversationGroup != store.ConversationGroup {
		t.Error("the ConversationKind constants differ from the store's")
	}
}

// The routes of the failures of the service. A failure inside a transaction
// is made in the function literal of the method.
const (
	notMember = "loadConversation.NotFound"
	missing   = "loadConversation->Store.ConversationByID.NotFound"
)

// own is the route of a failure made in the transaction of the method itself.
func own(method, tag string) string {
	return "Messages." + method + ".func1." + tag
}

// via is the route of a failure made by a function that the transaction of the
// method calls.
func via(method, rest string) string {
	return "Messages." + method + ".func1->" + rest
}

func TestStartDirect(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *msgEnv)
		from  string
		to    func(e *msgEnv) uint64
		// fail is the field that gets a message; empty for success
		fail string
	}{
		{name: "a new pair", from: "alice", to: func(e *msgEnv) uint64 { return e.uid("bob") }},
		{name: "an admin starts one like anyone", from: "admin", to: func(e *msgEnv) uint64 { return e.uid("bob") }},
		{name: "yourself", from: "alice", to: func(e *msgEnv) uint64 { return e.uid("alice") }, fail: app.FieldUser},
		{name: "an unknown user", from: "alice", to: func(*msgEnv) uint64 { return 9999 }, fail: app.FieldUser},
		{name: "user zero", from: "alice", to: func(*msgEnv) uint64 { return 0 }, fail: app.FieldUser},
		{
			name: "a disabled user", from: "alice", to: func(e *msgEnv) uint64 { return e.uid("dave") }, fail: app.FieldUser,
			setup: func(e *msgEnv) { e.disable("dave") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMsgEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			id, err := e.msgs.StartDirect(t.Context(), e.as(tt.from), tt.to(e))
			if tt.fail != "" {
				requireInvalid(t, err, own("StartDirect", "InvalidInput"), tt.fail)
				if got := e.rec.Events(); len(got) != 0 {
					t.Errorf("a refused call published %+v", got)
				}
				list, lerr := e.msgs.Conversations(t.Context(), e.as(tt.from))
				if lerr != nil || len(list) != 0 {
					t.Errorf("conversations = %v, %v; want none", list, lerr)
				}
				return
			}
			if err != nil {
				t.Fatalf("StartDirect: %+v", err)
			}
			if id == 0 {
				t.Fatal("no conversation ID")
			}
		})
	}
}

func TestStartDirectNeedsALogin(t *testing.T) {
	e := newMsgEnv(t)
	_, err := e.msgs.StartDirect(t.Context(), app.Actor{}, e.uid("bob"))
	_ = requireTag(t, err, app.TagUnauthenticated, "Messages.StartDirect.Unauthenticated")
	_, err = e.msgs.StartGroup(t.Context(), app.Actor{}, e.uids("bob", "carol"), "")
	_ = requireTag(t, err, app.TagUnauthenticated, "Messages.StartGroup.Unauthenticated")
	_, err = e.msgs.Conversations(t.Context(), app.Actor{})
	_ = requireTag(t, err, app.TagUnauthenticated, "Messages.Conversations.Unauthenticated")
	_, err = e.msgs.UnreadCount(t.Context(), app.Actor{})
	_ = requireTag(t, err, app.TagUnauthenticated, "Messages.UnreadCount.Unauthenticated")
	_, err = e.msgs.Upload(t.Context(), app.Actor{}, "a.txt", strings.NewReader("a"))
	_ = requireTag(t, err, app.TagUnauthenticated, "Messages.Upload.Unauthenticated")
}

func TestStartDirectIsUniquePerPair(t *testing.T) {
	e := newMsgEnv(t)
	first := e.direct("alice", "bob")
	if again := e.direct("alice", "bob"); again != first {
		t.Errorf("the same call gave %d, want %d", again, first)
	}
	if reverse := e.direct("bob", "alice"); reverse != first {
		t.Errorf("the other user gave %d, want %d", reverse, first)
	}
	if other := e.direct("alice", "carol"); other == first {
		t.Error("another pair got the same conversation")
	}
	if got := len(e.rec.Events()); got != 4 {
		t.Errorf("published %d events, want 4 (two conversations, two users each)", got)
	}
	for _, name := range []string{"alice", "bob"} {
		list, err := e.msgs.Conversations(t.Context(), e.as(name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "bob" && len(list) != 1 {
			t.Errorf("bob has %d conversations, want 1", len(list))
		}
	}
}

func TestStartDirectInParallelMakesOne(t *testing.T) {
	e := newMsgEnv(t)
	const callers = 8
	ids := make([]uint64, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			from, to := "alice", "bob"
			if i%2 == 1 {
				from, to = to, from
			}
			ids[i], errs[i] = e.msgs.StartDirect(t.Context(), e.as(from), e.uid(to))
		}()
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("call %d: %+v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("call %d got conversation %d, call 0 got %d", i, ids[i], ids[0])
		}
	}
	list, err := e.msgs.Conversations(t.Context(), e.as("alice"))
	if err != nil || len(list) != 1 {
		t.Errorf("alice has %d conversations (%v), want 1", len(list), err)
	}
}

func TestStartGroup(t *testing.T) {
	many := func(e *msgEnv, n int) []uint64 {
		ids := e.uids("bob", "carol", "dave", "erin")
		for i := len(ids); i < n; i++ {
			u := e.signUp("extra"+strconv.Itoa(i), store.RoleMember)
			ids = append(ids, u.ID)
		}
		return ids[:n]
	}
	tests := []struct {
		name  string
		setup func(e *msgEnv)
		ids   func(e *msgEnv) []uint64
		title string
		// want is the stored name when it works; fail lists the fields with a message
		want string
		fail []string
	}{
		{name: "two others, no name", ids: func(e *msgEnv) []uint64 { return e.uids("bob", "carol") }},
		{name: "a name is trimmed", ids: func(e *msgEnv) []uint64 { return e.uids("bob", "carol") }, title: "  Launch crew \n", want: "Launch crew"},
		{name: "a name of 80 characters", ids: func(e *msgEnv) []uint64 { return e.uids("bob", "carol") }, title: strings.Repeat("é", 80), want: strings.Repeat("é", 80)},
		{name: "a name of 81 characters", ids: func(e *msgEnv) []uint64 { return e.uids("bob", "carol") }, title: strings.Repeat("é", 81), fail: []string{app.FieldName}},
		{name: "one other", ids: func(e *msgEnv) []uint64 { return e.uids("bob") }, fail: []string{app.FieldUsers}},
		{name: "none", ids: func(*msgEnv) []uint64 { return nil }, fail: []string{app.FieldUsers}},
		{name: "repeats and yourself do not count", ids: func(e *msgEnv) []uint64 { return e.uids("bob", "bob", "alice", "bob") }, fail: []string{app.FieldUsers}},
		{name: "yourself is ignored", ids: func(e *msgEnv) []uint64 { return e.uids("bob", "alice", "carol") }},
		{name: "an unknown user", ids: func(e *msgEnv) []uint64 { return append(e.uids("bob", "carol"), 9999) }, fail: []string{app.FieldUsers}},
		{
			name: "a disabled user", ids: func(e *msgEnv) []uint64 { return e.uids("bob", "carol") }, fail: []string{app.FieldUsers},
			setup: func(e *msgEnv) { e.disable("carol") },
		},
		{name: "50 members with the starter", ids: func(e *msgEnv) []uint64 { return many(e, 49) }},
		{name: "51 members with the starter", ids: func(e *msgEnv) []uint64 { return many(e, 50) }, fail: []string{app.FieldUsers}},
		{name: "two mistakes at once", ids: func(e *msgEnv) []uint64 { return e.uids("bob") }, title: strings.Repeat("a", 81), fail: []string{app.FieldUsers, app.FieldName}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMsgEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			id, err := e.msgs.StartGroup(t.Context(), e.as("alice"), tt.ids(e), tt.title)
			if tt.fail != nil {
				requireInvalid(t, err, own("StartGroup", "InvalidInput"), tt.fail...)
				if got := e.rec.Events(); len(got) != 0 {
					t.Errorf("a refused call published %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("StartGroup: %+v", err)
			}
			h, err := e.msgs.Conversation(t.Context(), e.as("alice"), id)
			if err != nil {
				t.Fatal(err)
			}
			if h.Kind != app.ConversationGroup || !h.IsGroup || !h.CanSend || h.Name != tt.want {
				t.Errorf("header = %+v, want a group named %q", h, tt.want)
			}
			if h.Members[0].User.ID != e.uid("alice") {
				t.Errorf("the starter is not the first member: %+v", h.Members)
			}
			for _, m := range h.Members {
				if m.LastReadMessageID != 0 {
					t.Errorf("%s has read %d in a new group", m.User.Username, m.LastReadMessageID)
				}
			}
			if got := len(e.rec.Events()); got != len(h.Members) {
				t.Errorf("published %d events, want one for each of the %d members", got, len(h.Members))
			}
		})
	}
}

func TestGroupTitles(t *testing.T) {
	e := newMsgEnv(t)
	named := e.group("Launch crew", "alice", "bob", "carol")
	unnamed := e.group("", "alice", "bob", "carol")
	direct := e.direct("alice", "dave")
	list, err := e.msgs.Conversations(t.Context(), e.as("alice"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint64]string{}
	for _, c := range list {
		got[c.ID] = c.DisplayName
	}
	want := map[uint64]string{named: "Launch crew", unnamed: "Bob, Carol", direct: "Dave"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("titles as alice = %v, want %v", got, want)
	}
	bobList, err := e.msgs.Conversations(t.Context(), e.as("bob"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range bobList {
		if c.ID == unnamed && c.DisplayName != "Alice, Carol" {
			t.Errorf("the unnamed group as bob = %q, want %q", c.DisplayName, "Alice, Carol")
		}
	}
	h, err := e.msgs.Conversation(t.Context(), e.as("bob"), direct)
	if err == nil {
		t.Errorf("bob read alice and dave's conversation: %+v", h)
	}
	hd, err := e.msgs.Conversation(t.Context(), e.as("dave"), direct)
	if err != nil || hd.DisplayName != "Alice" || hd.IsGroup || hd.Kind != app.ConversationDirect {
		t.Errorf("direct header as dave = %+v, %v", hd, err)
	}
}

func TestAddMembers(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *msgEnv, conv uint64)
		from  string
		add   []string
		// direct runs the call on a direct conversation of alice and bob
		direct bool
		// members are the current members' names afterwards; fail the fields
		members []string
		fail    []string
		events  int
	}{
		{name: "one new member", from: "alice", add: []string{"dave"}, members: []string{"alice", "bob", "carol", "dave"}, events: 1 + 4},
		{name: "any member adds", from: "bob", add: []string{"dave", "erin"}, members: []string{"alice", "bob", "carol", "dave", "erin"}, events: 1 + 5},
		{name: "someone who is a member already: nothing happens", from: "alice", add: []string{"bob"}, members: []string{"alice", "bob", "carol"}},
		{name: "yourself: nothing to add", from: "alice", add: []string{"alice"}, fail: []string{app.FieldUsers}},
		{name: "nobody", from: "alice", add: nil, fail: []string{app.FieldUsers}},
		{name: "a repeated name counts once", from: "alice", add: []string{"dave", "dave"}, members: []string{"alice", "bob", "carol", "dave"}, events: 1 + 4},
		{
			name: "a member who left comes back", from: "alice", add: []string{"erin"}, members: []string{"alice", "bob", "carol", "erin"}, events: 1 + 4,
			setup: func(e *msgEnv, conv uint64) {
				if err := e.msgs.AddMembers(t.Context(), e.as("alice"), conv, e.uids("erin")); err != nil {
					t.Fatal(err)
				}
				if err := e.msgs.Leave(t.Context(), e.as("erin"), conv); err != nil {
					t.Fatal(err)
				}
				e.rec.Reset()
			},
		},
		{
			name: "a disabled user", from: "alice", add: []string{"dave"}, fail: []string{app.FieldUsers},
			setup: func(e *msgEnv, _ uint64) { e.disable("dave") },
		},
		{
			name: "a disabled user who is a member already is left alone", from: "alice", add: []string{"carol"}, members: []string{"alice", "bob", "carol"},
			setup: func(e *msgEnv, _ uint64) { e.disable("carol") },
		},
		{name: "an unknown user", from: "alice", add: []string{"nobody"}, fail: []string{app.FieldUsers}},
		{name: "a direct conversation", from: "alice", add: []string{"carol"}, direct: true, fail: []string{app.FieldUsers}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMsgEnv(t)
			conv := e.group("crew", "alice", "bob", "carol")
			if tt.direct {
				conv = e.direct("alice", "bob")
			}
			if tt.setup != nil {
				tt.setup(e, conv)
			}
			e.rec.Reset()
			ids := e.uids(slices.DeleteFunc(slices.Clone(tt.add), func(n string) bool { return n == "nobody" })...)
			if slices.Contains(tt.add, "nobody") {
				ids = append(ids, 9999)
			}
			err := e.msgs.AddMembers(t.Context(), e.as(tt.from), conv, ids)
			if tt.fail != nil {
				requireInvalid(t, err, own("AddMembers", "InvalidInput"), tt.fail...)
				if got := e.rec.Events(); len(got) != 0 {
					t.Errorf("a refused call published %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("AddMembers: %+v", err)
			}
			h, err := e.msgs.Conversation(t.Context(), e.as("alice"), conv)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, m := range h.Members {
				got = append(got, m.User.Username)
			}
			if !sameSet(got, tt.members) {
				t.Errorf("members = %v, want %v", got, tt.members)
			}
			if n := len(e.rec.Events()); n != tt.events {
				t.Errorf("published %d events, want %d: %+v", n, tt.events, e.rec.Events())
			}
		})
	}
}

func TestAddMembersLimitsTheGroupTo50(t *testing.T) {
	e := newMsgEnv(t)
	var ids []uint64
	for i := range 47 {
		ids = append(ids, e.signUp("user"+strconv.Itoa(i), store.RoleMember).ID)
	}
	conv, err := e.msgs.StartGroup(t.Context(), e.as("alice"), append(ids[:48-1], e.uid("bob")), "big")
	if err != nil {
		t.Fatalf("StartGroup of 49: %+v", err)
	}
	// 48 others + alice = 49 members; one more fits, two do not.
	err = e.msgs.AddMembers(t.Context(), e.as("alice"), conv, e.uids("carol", "dave"))
	requireInvalid(t, err, own("AddMembers", "InvalidInput"), app.FieldUsers)
	if err := e.msgs.AddMembers(t.Context(), e.as("alice"), conv, e.uids("carol")); err != nil {
		t.Fatalf("the 50th member: %+v", err)
	}
	err = e.msgs.AddMembers(t.Context(), e.as("alice"), conv, e.uids("dave"))
	requireInvalid(t, err, own("AddMembers", "InvalidInput"), app.FieldUsers)
	// A member who leaves makes room; coming back counts again.
	if err := e.msgs.Leave(t.Context(), e.as("carol"), conv); err != nil {
		t.Fatal(err)
	}
	if err := e.msgs.AddMembers(t.Context(), e.as("alice"), conv, e.uids("dave")); err != nil {
		t.Fatalf("a member for the one who left: %+v", err)
	}
	err = e.msgs.AddMembers(t.Context(), e.as("alice"), conv, e.uids("carol"))
	requireInvalid(t, err, own("AddMembers", "InvalidInput"), app.FieldUsers)
}

func TestAReturningMemberSeesTheWholeHistory(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.group("crew", "alice", "bob", "carol")
	e.send("alice", conv, "one")
	if err := e.msgs.Leave(t.Context(), e.as("carol"), conv); err != nil {
		t.Fatal(err)
	}
	e.send("bob", conv, "two while carol is away")
	if err := e.msgs.AddMembers(t.Context(), e.as("bob"), conv, e.uids("carol", "dave")); err != nil {
		t.Fatal(err)
	}
	e.send("alice", conv, "three")
	for _, name := range []string{"carol", "dave"} {
		if got, want := bodies(e.history(name, conv)), []string{"one", "two while carol is away", "three"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s sees %v, want %v", name, got, want)
		}
	}
}

func TestRename(t *testing.T) {
	tests := []struct {
		name   string
		from   string
		to     string
		direct bool
		want   string
		fail   []string
		events int
	}{
		{name: "a member renames", from: "bob", to: " New name ", want: "New name", events: 1 + 3},
		{name: "the name is cleared", from: "alice", to: "  ", want: "", events: 1 + 3},
		{name: "the same name changes nothing", from: "alice", to: "crew", want: "crew"},
		{name: "80 characters", from: "alice", to: strings.Repeat("x", 80), want: strings.Repeat("x", 80), events: 1 + 3},
		{name: "81 characters", from: "alice", to: strings.Repeat("x", 81), want: "crew", fail: []string{app.FieldName}},
		{name: "a direct conversation has no name", from: "alice", to: "x", direct: true, fail: []string{app.FieldName}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMsgEnv(t)
			conv := e.group("crew", "alice", "bob", "carol")
			if tt.direct {
				conv = e.direct("alice", "bob")
			}
			e.rec.Reset()
			err := e.msgs.Rename(t.Context(), e.as(tt.from), conv, tt.to)
			if tt.fail != nil {
				requireInvalid(t, err, own("Rename", "InvalidInput"), tt.fail...)
				if got := e.rec.Events(); len(got) != 0 {
					t.Errorf("a refused call published %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Rename: %+v", err)
			}
			h, err := e.msgs.Conversation(t.Context(), e.as("alice"), conv)
			if err != nil {
				t.Fatal(err)
			}
			if h.Name != tt.want {
				t.Errorf("name = %q, want %q", h.Name, tt.want)
			}
			if n := len(e.rec.Events()); n != tt.events {
				t.Errorf("published %d events, want %d", n, tt.events)
			}
		})
	}
}

func TestLeave(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.group("crew", "alice", "bob", "carol")
	e.send("alice", conv, "hello")
	e.rec.Reset()
	if err := e.msgs.Leave(t.Context(), e.as("carol"), conv); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	// The group stays for the others; the one who left no longer reads it.
	h, err := e.msgs.Conversation(t.Context(), e.as("alice"), conv)
	if err != nil || len(h.Members) != 2 {
		t.Errorf("header = %+v, %v; want two members", h, err)
	}
	_, err = e.msgs.Conversation(t.Context(), e.as("carol"), conv)
	_ = requireTag(t, err, app.TagNotFound, "Messages.Conversation->loadConversation.NotFound")
	list, err := e.msgs.Conversations(t.Context(), e.as("carol"))
	if err != nil || len(list) != 0 {
		t.Errorf("carol's conversations = %v, %v; want none", list, err)
	}
	if n := e.unread("carol"); n != 0 {
		t.Errorf("carol's unread count = %d, want 0", n)
	}
	// Leaving twice: she is out already.
	err = e.msgs.Leave(t.Context(), e.as("carol"), conv)
	_ = requireTag(t, err, app.TagNotFound, via("Leave", notMember))
	want := []live.Event{
		{Topic: live.ConversationTopic(conv), Kind: live.KindConversationUpdated, ConversationID: conv},
		chgEvt(conv, e.uid("alice")), chgEvt(conv, e.uid("bob")), chgEvt(conv, e.uid("carol")),
	}
	if got := e.rec.Events(); !reflect.DeepEqual(got, want) {
		t.Errorf("published\n got  %+v\n want %+v", got, want)
	}
	// A direct conversation is never left.
	direct := e.direct("alice", "bob")
	err = e.msgs.Leave(t.Context(), e.as("alice"), direct)
	requireInvalid(t, err, own("Leave", "InvalidInput"), app.FieldForm)
}

// chgEvt is conversation.changed for the user.
func chgEvt(conv, user uint64) live.Event {
	return live.Event{Topic: live.UserTopic(user), Kind: live.KindConversationChanged, ConversationID: conv, UserID: user}
}

// msgEvt is an event of a message on the conversation.
func msgEvt(kind string, conv, msg uint64) live.Event {
	return live.Event{Topic: live.ConversationTopic(conv), Kind: kind, ConversationID: conv, MessageID: msg}
}

// permEnv is a group "crew" of alice (who started it), bob and carol, with
// erin, who joined and left. dave and admin are no members; frank is no member
// and disabled. alice wrote a message with a file.
type permEnv struct {
	*msgEnv
	conv uint64
	msg  app.Message
	att  uint64
}

func newPermEnv(t *testing.T) *permEnv {
	t.Helper()
	e := &permEnv{msgEnv: newMsgEnv(t)}
	e.users["frank"] = e.signUp("frank", store.RoleMember)
	e.disable("frank")
	e.conv = e.group("crew", "alice", "bob", "carol")
	if err := e.msgs.AddMembers(t.Context(), e.as("alice"), e.conv, e.uids("erin")); err != nil {
		t.Fatalf("AddMembers erin: %+v", err)
	}
	if err := e.msgs.Leave(t.Context(), e.as("erin"), e.conv); err != nil {
		t.Fatalf("Leave erin: %+v", err)
	}
	var err error
	e.msg, err = e.msgs.Send(t.Context(), e.as("alice"), e.conv, app.MessageInput{
		Body: "from alice", Uploads: []app.Upload{e.upload("alice", "a.txt")},
	})
	if err != nil {
		t.Fatalf("Send: %+v", err)
	}
	e.att = e.msg.Attachments[0].ID
	e.rec.Reset()
	return e
}

func TestMessagesPermissions(t *testing.T) {
	// outcomes of a member who may, a member who may not, and everyone else
	type who struct{ alice, bob, erin, dave, admin, frank string }
	everyoneElse := func(ok, bob string) who {
		return who{alice: ok, bob: bob, erin: app.TagNotFound, dave: app.TagNotFound, admin: app.TagNotFound, frank: app.TagNotFound}
	}
	methods := []struct {
		name  string
		route string
		call  func(e *permEnv, a app.Actor) error
		want  who
	}{
		{"Conversation", "Messages.Conversation->" + notMember, func(e *permEnv, a app.Actor) error {
			_, err := e.msgs.Conversation(t.Context(), a, e.conv)
			return err
		}, everyoneElse("", "")},
		{"History", "Messages.History->" + notMember, func(e *permEnv, a app.Actor) error {
			_, err := e.msgs.History(t.Context(), a, e.conv, 0, 0)
			return err
		}, everyoneElse("", "")},
		{"Send", via("Send", notMember), func(e *permEnv, a app.Actor) error {
			_, err := e.msgs.Send(t.Context(), a, e.conv, app.MessageInput{Body: "hi"})
			return err
		}, everyoneElse("", "")},
		{"Edit", via("Edit", notMember), func(e *permEnv, a app.Actor) error {
			_, err := e.msgs.Edit(t.Context(), a, e.msg.ID, "changed")
			return err
		}, everyoneElse("", app.TagForbidden)},
		{"Delete", via("Delete", notMember), func(e *permEnv, a app.Actor) error {
			return e.msgs.Delete(t.Context(), a, e.msg.ID)
		}, everyoneElse("", app.TagForbidden)},
		{"MarkRead", via("MarkRead", notMember), func(e *permEnv, a app.Actor) error {
			return e.msgs.MarkRead(t.Context(), a, e.conv, e.msg.ID)
		}, everyoneElse("", "")},
		{"React", via("React", notMember), func(e *permEnv, a app.Actor) error {
			_, err := e.msgs.React(t.Context(), a, e.msg.ID, "\U0001F44D")
			return err
		}, everyoneElse("", "")},
		{"Attachment", "Messages.Attachment->" + notMember, func(e *permEnv, a app.Actor) error {
			_, f, err := e.msgs.Attachment(t.Context(), a, e.att)
			if err == nil {
				return f.Close()
			}
			return err
		}, everyoneElse("", "")},
		{"Rename", via("Rename", notMember), func(e *permEnv, a app.Actor) error {
			return e.msgs.Rename(t.Context(), a, e.conv, "renamed")
		}, everyoneElse("", "")},
		{"AddMembers", via("AddMembers", notMember), func(e *permEnv, a app.Actor) error {
			return e.msgs.AddMembers(t.Context(), a, e.conv, e.uids("dave"))
		}, everyoneElse("", "")},
		{"Leave", via("Leave", notMember), func(e *permEnv, a app.Actor) error {
			return e.msgs.Leave(t.Context(), a, e.conv)
		}, everyoneElse("", "")},
	}
	actors := []string{"alice", "bob", "erin", "dave", "admin", "frank"}
	for _, m := range methods {
		for _, name := range actors {
			t.Run(m.name+" as "+name, func(t *testing.T) {
				e := newPermEnv(t)
				want := map[string]string{
					"alice": m.want.alice, "bob": m.want.bob, "erin": m.want.erin,
					"dave": m.want.dave, "admin": m.want.admin, "frank": m.want.frank,
				}[name]
				err := m.call(e, e.as(name))
				if want == "" {
					if err != nil {
						t.Fatalf("got %+v, want success", err)
					}
					return
				}
				route := m.route
				if want == app.TagForbidden {
					route = own(m.name, app.TagForbidden)
				}
				_ = requireTag(t, err, want, route)
				if name == "bob" {
					return // a member who may not: Forbidden changes nothing either
				}
				if got := e.rec.Events(); len(got) != 0 {
					t.Errorf("a refused call published %+v", got)
				}
			})
		}
	}
}

func TestRefusedCallsChangeNothing(t *testing.T) {
	e := newPermEnv(t)
	for _, name := range []string{"bob", "dave", "admin", "erin"} {
		_, _ = e.msgs.Edit(t.Context(), e.as(name), e.msg.ID, "hijacked")
		_ = e.msgs.Delete(t.Context(), e.as(name), e.msg.ID)
	}
	got := e.history("alice", e.conv)
	if len(got) != 1 || got[0].Body != "from alice" || got[0].Deleted || got[0].EditedAt != nil || len(got[0].Attachments) != 1 {
		t.Errorf("the message changed: %+v", got)
	}
	if on := e.onDisk(); len(on) != 1 {
		t.Errorf("files on disk = %v, want the one of the message", on)
	}
	if got := e.rec.Events(); len(got) != 0 {
		t.Errorf("refused calls published %+v", got)
	}
}

func TestMissingThingsAreNotFound(t *testing.T) {
	e := newPermEnv(t)
	ctx := t.Context()
	a := e.as("alice")
	_, err := e.msgs.Conversation(ctx, a, 9999)
	_ = requireTag(t, err, app.TagNotFound, "Messages.Conversation->"+missing)
	_, err = e.msgs.History(ctx, a, 9999, 0, 0)
	_ = requireTag(t, err, app.TagNotFound, "Messages.History->"+missing)
	_, err = e.msgs.Send(ctx, a, 9999, app.MessageInput{Body: "x"})
	_ = requireTag(t, err, app.TagNotFound, via("Send", missing))
	_, err = e.msgs.Edit(ctx, a, 9999, "x")
	_ = requireTag(t, err, app.TagNotFound, via("Edit", "Store.MessageByID.NotFound"))
	err = e.msgs.Delete(ctx, a, 9999)
	_ = requireTag(t, err, app.TagNotFound, via("Delete", "Store.MessageByID.NotFound"))
	_, err = e.msgs.React(ctx, a, 9999, "\U0001F44D")
	_ = requireTag(t, err, app.TagNotFound, via("React", "Store.MessageByID.NotFound"))
	err = e.msgs.MarkRead(ctx, a, 9999, e.msg.ID)
	_ = requireTag(t, err, app.TagNotFound, via("MarkRead", missing))
	_, _, err = e.msgs.Attachment(ctx, a, 9999)
	_ = requireTag(t, err, app.TagNotFound, "Messages.Attachment->Store.MessageAttachmentByID.NotFound")
}

func TestAttachmentDownload(t *testing.T) {
	e := newPermEnv(t)
	view, f, err := e.msgs.Attachment(t.Context(), e.as("bob"), e.att)
	if err != nil {
		t.Fatalf("Attachment: %+v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(f); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "content of a.txt" || view.FileName != "a.txt" || view.Size != int64(buf.Len()) || view.IsImage {
		t.Errorf("view = %+v, content %q", view, buf.String())
	}
}

func TestSendRules(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.group("crew", "alice", "bob", "carol")
	other := e.direct("alice", "dave")
	otherMsg := e.send("alice", other, "elsewhere")
	deleted := e.send("alice", conv, "to be deleted")
	if err := e.msgs.Delete(t.Context(), e.as("alice"), deleted.ID); err != nil {
		t.Fatal(err)
	}
	ok := e.send("alice", conv, "a real message")
	tests := []struct {
		name string
		in   app.MessageInput
		// stored is the body kept; fail lists the fields with a message
		stored string
		fail   []string
	}{
		{name: "plain text", in: app.MessageInput{Body: "hello"}, stored: "hello"},
		{name: "trimmed", in: app.MessageInput{Body: " \n hello \t"}, stored: "hello"},
		{name: "line breaks inside are kept", in: app.MessageInput{Body: "a\n\nb"}, stored: "a\n\nb"},
		{name: "4000 characters", in: app.MessageInput{Body: strings.Repeat("é", 4000)}, stored: strings.Repeat("é", 4000)},
		{name: "4001 characters", in: app.MessageInput{Body: strings.Repeat("é", 4001)}, fail: []string{app.FieldBody}},
		{name: "4001 characters of which spaces at the end", in: app.MessageInput{Body: strings.Repeat("a", 4000) + "   "}, stored: strings.Repeat("a", 4000)},
		{name: "empty", in: app.MessageInput{}, fail: []string{app.FieldBody}},
		{name: "only spaces", in: app.MessageInput{Body: " \n "}, fail: []string{app.FieldBody}},
		{name: "invalid text", in: app.MessageInput{Body: "a\xffb"}, fail: []string{app.FieldBody}},
		{name: "a reply", in: app.MessageInput{Body: "re", ReplyToID: ok.ID}, stored: "re"},
		{name: "a reply to a message of another conversation", in: app.MessageInput{Body: "re", ReplyToID: otherMsg.ID}, fail: []string{app.FieldReplyTo}},
		{name: "a reply to a deleted message", in: app.MessageInput{Body: "re", ReplyToID: deleted.ID}, fail: []string{app.FieldReplyTo}},
		{name: "a reply to nothing", in: app.MessageInput{Body: "re", ReplyToID: 9999}, fail: []string{app.FieldReplyTo}},
		{name: "two mistakes at once", in: app.MessageInput{Body: " ", ReplyToID: 9999}, fail: []string{app.FieldBody, app.FieldReplyTo}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.history("alice", conv))
			e.rec.Reset()
			m, err := e.msgs.Send(t.Context(), e.as("bob"), conv, tt.in)
			if tt.fail != nil {
				requireInvalid(t, err, own("Send", "InvalidInput"), tt.fail...)
				if got := len(e.history("alice", conv)); got != before {
					t.Errorf("a refused message was stored")
				}
				if got := e.rec.Events(); len(got) != 0 {
					t.Errorf("a refused call published %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Send: %+v", err)
			}
			if m.Body != tt.stored || m.Author.ID != e.uid("bob") || m.ConversationID != conv || m.EditedAt != nil || m.Deleted {
				t.Errorf("message = %+v", m)
			}
			if !m.CanEdit || !m.CanDelete || !m.CanReact {
				t.Errorf("the author cannot act on the message: %+v", m)
			}
			if (tt.in.ReplyToID != 0) != (m.ReplyTo != nil) {
				t.Errorf("ReplyTo = %+v for ReplyToID %d", m.ReplyTo, tt.in.ReplyToID)
			}
		})
	}
}

func TestSendAttachments(t *testing.T) {
	t.Run("files only, no body", func(t *testing.T) {
		e := newMsgEnv(t)
		conv := e.direct("alice", "bob")
		img, err := e.msgs.Upload(t.Context(), e.as("alice"), "../pics/photo.png", bytes.NewReader(pngData(t, 4, 3)))
		if err != nil {
			t.Fatal(err)
		}
		txt := e.upload("alice", "notes.txt")
		m, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Uploads: []app.Upload{img, txt}})
		if err != nil {
			t.Fatalf("Send: %+v", err)
		}
		if len(m.Attachments) != 2 {
			t.Fatalf("attachments = %+v", m.Attachments)
		}
		a, b := m.Attachments[0], m.Attachments[1]
		if a.FileName != "photo.png" || !a.IsImage || a.Width != 4 || a.Height != 3 || b.FileName != "notes.txt" || b.IsImage {
			t.Errorf("attachments = %+v", m.Attachments)
		}
		if on := e.onDisk(); len(on) != 2 {
			t.Errorf("files on disk = %v", on)
		}
	})
	t.Run("too many files", func(t *testing.T) {
		e := newMsgEnv(t)
		conv := e.direct("alice", "bob")
		var ups []app.Upload
		for i := range testMaxAttachments + 1 {
			ups = append(ups, e.upload("alice", "f"+strconv.Itoa(i)+".txt"))
		}
		_, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "x", Uploads: ups})
		requireInvalid(t, err, own("Send", "InvalidInput"), app.FieldAttachments)
		if on := e.onDisk(); len(on) != 0 {
			t.Errorf("the files of a refused message stay on disk: %v", on)
		}
	})
	t.Run("a refused message removes its files", func(t *testing.T) {
		e := newMsgEnv(t)
		conv := e.direct("alice", "bob")
		up := e.upload("alice", "a.txt")
		_, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "x", ReplyToID: 9999, Uploads: []app.Upload{up}})
		requireInvalid(t, err, own("Send", "InvalidInput"), app.FieldReplyTo)
		if on := e.onDisk(); len(on) != 0 {
			t.Errorf("files on disk = %v, want none", on)
		}
	})
	t.Run("a non-member's message removes its files", func(t *testing.T) {
		e := newMsgEnv(t)
		conv := e.direct("alice", "bob")
		up := e.upload("dave", "a.txt")
		_, err := e.msgs.Send(t.Context(), e.as("dave"), conv, app.MessageInput{Uploads: []app.Upload{up}})
		_ = requireTag(t, err, app.TagNotFound, via("Send", notMember))
		if on := e.onDisk(); len(on) != 0 {
			t.Errorf("files on disk = %v, want none", on)
		}
	})
	t.Run("an upload used by a message cannot be used again", func(t *testing.T) {
		e := newMsgEnv(t)
		conv := e.direct("alice", "bob")
		up := e.upload("alice", "a.txt")
		if _, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Uploads: []app.Upload{up}}); err != nil {
			t.Fatal(err)
		}
		_, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "again", Uploads: []app.Upload{up}})
		requireInvalid(t, err, own("Send", "InvalidInput"), app.FieldAttachments)
		if on := e.onDisk(); len(on) != 1 {
			t.Errorf("the file of the first message was removed: %v", on)
		}
	})
	t.Run("DiscardUploads keeps files that a message uses", func(t *testing.T) {
		e := newMsgEnv(t)
		conv := e.direct("alice", "bob")
		used := e.upload("alice", "a.txt")
		free := e.upload("alice", "b.txt")
		if _, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Uploads: []app.Upload{used}}); err != nil {
			t.Fatal(err)
		}
		e.msgs.DiscardUploads(t.Context(), e.as("alice"), []app.Upload{used, free})
		e.msgs.DiscardUploads(t.Context(), app.Actor{}, []app.Upload{used})
		if on := e.onDisk(); !reflect.DeepEqual(on, []string{used.Key}) {
			t.Errorf("files on disk = %v, want only %s", on, used.Key)
		}
	})
	t.Run("an upload over the limit", func(t *testing.T) {
		e := newMsgEnv(t)
		_, err := e.msgs.Upload(t.Context(), e.as("alice"), "big.bin", bytes.NewReader(make([]byte, testMaxUpload+1)))
		_ = requireTag(t, err, app.TagTooLarge, "Messages.Upload")
	})
}

func TestSendFailsWhenTheDiskFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	e := newMsgEnv(t)
	conv := e.direct("alice", "bob")
	up := e.upload("alice", "a.txt")
	if err := os.Chmod(e.filesDir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.filesDir, 0o750) })
	_, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "x", Uploads: []app.Upload{up}})
	_ = requireTag(t, err, "FileStatFailed", "Messages.Send.func1->checkUploadFiles->Store.Size.FileStatFailed")
}

func TestSendToADisabledUser(t *testing.T) {
	e := newMsgEnv(t)
	direct := e.direct("alice", "bob")
	group := e.group("crew", "alice", "bob", "carol")
	e.send("bob", direct, "before")
	e.send("bob", group, "before")
	e.disable("bob")
	e.rec.Reset()

	_, err := e.msgs.Send(t.Context(), e.as("alice"), direct, app.MessageInput{Body: "hello?"})
	requireInvalid(t, err, own("Send", "InvalidInput"), app.FieldBody)
	if got := e.rec.Events(); len(got) != 0 {
		t.Errorf("a refused call published %+v", got)
	}
	// The messages are read, and the group goes on.
	h, err := e.msgs.Conversation(t.Context(), e.as("alice"), direct)
	if err != nil || h.CanSend {
		t.Errorf("header = %+v, %v; want CanSend false", h, err)
	}
	if got := bodies(e.history("alice", direct)); !reflect.DeepEqual(got, []string{"before"}) {
		t.Errorf("history = %v", got)
	}
	if hg, err := e.msgs.Conversation(t.Context(), e.as("alice"), group); err != nil || !hg.CanSend {
		t.Errorf("group header = %+v, %v; want CanSend true", hg, err)
	}
	e.send("alice", group, "the group goes on")
	if _, err := e.msgs.React(t.Context(), e.as("alice"), e.history("alice", direct)[0].ID, "\U0001F44D"); err != nil {
		t.Errorf("React in a conversation with a disabled user: %+v", err)
	}
}

func TestEdit(t *testing.T) {
	tests := []struct {
		name string
		// withFile makes the message carry a file, so that its body may be empty
		withFile bool
		body     string
		want     string
		fail     bool
		same     bool
	}{
		{name: "a new body", body: "  changed ", want: "changed"},
		{name: "4000 characters", body: strings.Repeat("a", 4000), want: strings.Repeat("a", 4000)},
		{name: "4001 characters", body: strings.Repeat("a", 4001), fail: true},
		{name: "an empty body", body: "", fail: true},
		{name: "an empty body is fine with a file", withFile: true, body: " ", want: ""},
		{name: "the same body changes nothing", body: "original", same: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMsgEnv(t)
			conv := e.group("crew", "alice", "bob", "carol")
			in := app.MessageInput{Body: "original"}
			if tt.withFile {
				in.Uploads = []app.Upload{e.upload("alice", "a.txt")}
			}
			m, err := e.msgs.Send(t.Context(), e.as("alice"), conv, in)
			if err != nil {
				t.Fatal(err)
			}
			e.clk.Advance(time.Hour)
			e.rec.Reset()
			got, err := e.msgs.Edit(t.Context(), e.as("alice"), m.ID, tt.body)
			if tt.fail {
				requireInvalid(t, err, own("Edit", "InvalidInput"), app.FieldBody)
				if n := len(e.rec.Events()); n != 0 {
					t.Errorf("a refused call published %d events", n)
				}
				if h := e.history("alice", conv)[0]; h.Body != "original" || h.EditedAt != nil {
					t.Errorf("the message changed: %+v", h)
				}
				return
			}
			if err != nil {
				t.Fatalf("Edit: %+v", err)
			}
			if tt.same {
				if got.EditedAt != nil || len(e.rec.Events()) != 0 {
					t.Errorf("an edit that changed nothing: %+v, events %+v", got, e.rec.Events())
				}
				return
			}
			if got.Body != tt.want || got.EditedAt == nil || !got.EditedAt.Equal(e.clk.Now()) {
				t.Errorf("message = %+v, want body %q edited at %v", got, tt.want, e.clk.Now())
			}
			want := []live.Event{
				msgEvt(live.KindMessageUpdated, conv, m.ID),
				chgEvt(conv, e.uid("alice")), chgEvt(conv, e.uid("bob")), chgEvt(conv, e.uid("carol")),
			}
			if events := e.rec.Events(); !reflect.DeepEqual(events, want) {
				t.Errorf("published\n got  %+v\n want %+v", events, want)
			}
		})
	}
}

func TestEditAndDeleteOfADeletedMessage(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.direct("alice", "bob")
	m := e.send("alice", conv, "bye")
	if err := e.msgs.Delete(t.Context(), e.as("alice"), m.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		_, err := e.msgs.Edit(t.Context(), e.as(name), m.ID, "back")
		_ = requireTag(t, err, app.TagNotFound, own("Edit", app.TagNotFound))
		err = e.msgs.Delete(t.Context(), e.as(name), m.ID)
		_ = requireTag(t, err, app.TagNotFound, own("Delete", app.TagNotFound))
		_, err = e.msgs.React(t.Context(), e.as(name), m.ID, "\U0001F44D")
		_ = requireTag(t, err, app.TagNotFound, own("React", app.TagNotFound))
	}
}

func TestDeleteLeavesAPlaceholder(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.group("crew", "alice", "bob", "carol")
	first := e.send("bob", conv, "first")
	doomed, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{
		Body: "secret plans", ReplyToID: first.ID, Uploads: []app.Upload{e.upload("alice", "a.txt"), e.upload("alice", "b.txt")},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	reply, err := e.msgs.Send(t.Context(), e.as("bob"), conv, app.MessageInput{Body: "re: secret", ReplyToID: doomed.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.msgs.React(t.Context(), e.as("bob"), doomed.ID, "\U0001F44D"); err != nil {
		t.Fatal(err)
	}
	attID := doomed.Attachments[0].ID
	if on := e.onDisk(); len(on) != 2 {
		t.Fatalf("files on disk = %v", on)
	}
	e.rec.Reset()

	if err := e.msgs.Delete(t.Context(), e.as("alice"), doomed.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}

	// The files go after the change is committed.
	if on := e.onDisk(); len(on) != 0 {
		t.Errorf("files on disk = %v, want none", on)
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		got := e.history(name, conv)
		if len(got) != 3 {
			t.Fatalf("%s sees %d messages, want the placeholder to keep its place", name, len(got))
		}
		d := got[1]
		if d.ID != doomed.ID || !d.Deleted || d.Body != "" || len(d.Attachments) != 0 || len(d.Reactions) != 0 ||
			d.CanEdit || d.CanDelete || d.CanReact || d.Author.ID != e.uid("alice") {
			t.Errorf("%s sees the deleted message as %+v", name, d)
		}
		if d.ReplyTo == nil || d.ReplyTo.ID != first.ID || d.ReplyTo.Preview != "first" {
			t.Errorf("the deleted message lost its own quote: %+v", d.ReplyTo)
		}
		q := got[2].ReplyTo
		if got[2].ID != reply.ID || q == nil || !q.Deleted || q.Preview != app.MessageDeletedPreview || q.Author.ID != e.uid("alice") {
			t.Errorf("%s sees the quote of the reply as %+v", name, q)
		}
	}
	_, _, err = e.msgs.Attachment(t.Context(), e.as("bob"), attID)
	_ = requireTag(t, err, app.TagNotFound, "Messages.Attachment->Store.MessageAttachmentByID.NotFound")
	rows, err := e.st.MessageAttachmentsOf(t.Context(), []uint64{doomed.ID})
	if err != nil || len(rows) != 0 {
		t.Errorf("attachment rows left: %+v, %v", rows, err)
	}
	reactions, err := e.st.ReactionsOfMessages(t.Context(), []uint64{doomed.ID})
	if err != nil || len(reactions) != 0 {
		t.Errorf("reaction rows left: %+v, %v", reactions, err)
	}
	// A second delete finds nothing, and the unrelated file stays.
	err = e.msgs.Delete(t.Context(), e.as("alice"), doomed.ID)
	_ = requireTag(t, err, app.TagNotFound, own("Delete", app.TagNotFound))
	want := []live.Event{
		msgEvt(live.KindMessageDeleted, conv, doomed.ID),
		chgEvt(conv, e.uid("alice")), chgEvt(conv, e.uid("bob")), chgEvt(conv, e.uid("carol")),
	}
	if got := e.rec.Events(); !reflect.DeepEqual(got, want) {
		t.Errorf("published\n got  %+v\n want %+v", got, want)
	}
}

func TestDeleteKeepsTheFilesOfOtherMessages(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.direct("alice", "bob")
	a, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Uploads: []app.Upload{e.upload("alice", "a.txt")}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Uploads: []app.Upload{e.upload("alice", "b.txt")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.msgs.Delete(t.Context(), e.as("alice"), a.ID); err != nil {
		t.Fatal(err)
	}
	if on := e.onDisk(); len(on) != 1 {
		t.Errorf("files on disk = %v, want the one of the other message", on)
	}
	if _, f, err := e.msgs.Attachment(t.Context(), e.as("bob"), b.Attachments[0].ID); err != nil {
		t.Errorf("the other message lost its file: %+v", err)
	} else if err := f.Close(); err != nil {
		t.Error(err)
	}
}

func TestReact(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.group("crew", "alice", "bob", "carol")
	m := e.send("alice", conv, "react to me")
	ctx := t.Context()
	toggle := func(user, emoji string) app.Reactions {
		t.Helper()
		r, err := e.msgs.React(ctx, e.as(user), m.ID, emoji)
		if err != nil {
			t.Fatalf("React %s %q: %+v", user, emoji, err)
		}
		return r
	}
	toggle("bob", "\U0001F44D")
	toggle("alice", "\U0001F44D")
	got := toggle("carol", "❤️")
	want := app.Reactions{
		{Emoji: "\U0001F44D", Count: 2, Names: []string{"Bob", "Alice"}},
		{Emoji: "❤️", Count: 1, Mine: true, Names: []string{"Carol"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reactions as carol = %+v, want %+v", got, want)
	}
	// The history shows them per reader.
	asBob := e.history("bob", conv)[0].Reactions
	if len(asBob) != 2 || !asBob[0].Mine || asBob[1].Mine {
		t.Errorf("reactions as bob = %+v", asBob)
	}
	// Again removes it.
	if got := toggle("alice", "\U0001F44D"); len(got) != 2 || got[0].Count != 1 || got[0].Mine {
		t.Errorf("after alice took hers off: %+v", got)
	}
	for _, bad := range []string{"", "x", "\U0001F44D\U0001F44D", "ab"} {
		_, err := e.msgs.React(ctx, e.as("bob"), m.ID, bad)
		requireInvalid(t, err, own("React", "InvalidInput"), app.FieldEmoji)
	}
	e.rec.Reset()
	toggle("bob", "\U0001F680")
	if got, want := e.rec.Events(), []live.Event{msgEvt(live.KindMessageReaction, conv, m.ID)}; !reflect.DeepEqual(got, want) {
		t.Errorf("published\n got  %+v\n want %+v", got, want)
	}
}

func TestReadState(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.direct("alice", "bob")
	other := e.direct("alice", "carol")
	if e.unread("alice") != 0 || e.unread("bob") != 0 {
		t.Fatal("a new conversation is unread")
	}
	m1 := e.send("alice", conv, "one")
	m2 := e.send("alice", conv, "two")
	if n := e.unread("bob"); n != 1 {
		t.Errorf("bob's unread count = %d, want 1 (conversations, not messages)", n)
	}
	if n := e.unread("alice"); n != 0 {
		t.Errorf("alice's unread count = %d: sending marks the conversation read", n)
	}
	e.send("alice", other, "to carol")
	e.send("carol", other, "from carol")
	if n := e.unread("alice"); n != 1 {
		t.Errorf("alice's unread count = %d, want 1", n)
	}
	if n := e.unread("bob"); n != 1 {
		t.Errorf("bob's unread count = %d, want 1", n)
	}

	// Reading up to the first message leaves the second unread.
	e.rec.Reset()
	if err := e.msgs.MarkRead(t.Context(), e.as("bob"), conv, m1.ID); err != nil {
		t.Fatalf("MarkRead: %+v", err)
	}
	if n := e.unread("bob"); n != 1 {
		t.Errorf("bob's unread count after reading one = %d, want 1", n)
	}
	want := []live.Event{
		{Topic: live.ConversationTopic(conv), Kind: live.KindConversationRead, ConversationID: conv, MessageID: m1.ID, UserID: e.uid("bob")},
		{Topic: live.UserTopic(e.uid("bob")), Kind: live.KindConversationRead, ConversationID: conv, MessageID: m1.ID, UserID: e.uid("bob")},
	}
	if got := e.rec.Events(); !reflect.DeepEqual(got, want) {
		t.Errorf("published\n got  %+v\n want %+v", got, want)
	}
	if err := e.msgs.MarkRead(t.Context(), e.as("bob"), conv, m2.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.unread("bob"); n != 0 {
		t.Errorf("bob's unread count after reading both = %d", n)
	}

	// It never moves back, and publishes nothing when it did not move.
	e.rec.Reset()
	if err := e.msgs.MarkRead(t.Context(), e.as("bob"), conv, m1.ID); err != nil {
		t.Fatal(err)
	}
	h, err := e.msgs.Conversation(t.Context(), e.as("alice"), conv)
	if err != nil {
		t.Fatal(err)
	}
	reads := map[string]uint64{}
	for _, m := range h.Members {
		reads[m.User.Username] = m.LastReadMessageID
	}
	if reads["bob"] != m2.ID || reads["alice"] != m2.ID {
		t.Errorf("last read = %v, want both at %d", reads, m2.ID)
	}
	if got := e.rec.Events(); len(got) != 0 {
		t.Errorf("a read that moved nothing published %+v", got)
	}

	// A message of another conversation, or none, is refused.
	foreign := e.history("alice", other)[0]
	err = e.msgs.MarkRead(t.Context(), e.as("bob"), conv, foreign.ID)
	requireInvalid(t, err, own("MarkRead", "InvalidInput"), app.FieldMessage)
	err = e.msgs.MarkRead(t.Context(), e.as("bob"), conv, 9999)
	requireInvalid(t, err, own("MarkRead", "InvalidInput"), app.FieldMessage)
	err = e.msgs.MarkRead(t.Context(), e.as("bob"), conv, 0)
	requireInvalid(t, err, own("MarkRead", "InvalidInput"), app.FieldMessage)
}

func TestUnreadIgnoresOwnAndDeletedMessages(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.direct("alice", "bob")
	m := e.send("alice", conv, "oops")
	if e.unread("bob") != 1 {
		t.Fatal("bob should have one unread conversation")
	}
	if err := e.msgs.Delete(t.Context(), e.as("alice"), m.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.unread("bob"); n != 0 {
		t.Errorf("a deleted message still counts: %d", n)
	}
	list, err := e.msgs.Conversations(t.Context(), e.as("bob"))
	if err != nil || len(list) != 1 || list[0].Unread {
		t.Errorf("list = %+v, %v", list, err)
	}
	// A message of someone else counts again.
	e.send("alice", conv, "again")
	if e.unread("bob") != 1 {
		t.Error("a new message is not unread")
	}
	// A member who left does not count the group.
	g := e.group("crew", "alice", "bob", "carol")
	e.send("alice", g, "hi")
	if e.unread("bob") != 2 {
		t.Fatal("the group should be unread")
	}
	if err := e.msgs.Leave(t.Context(), e.as("bob"), g); err != nil {
		t.Fatal(err)
	}
	if n := e.unread("bob"); n != 1 {
		t.Errorf("unread after leaving the group = %d, want 1", n)
	}
}

func TestHistory(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.direct("alice", "bob")
	var all []app.Message
	for i := range 7 {
		user := "alice"
		if i%2 == 1 {
			user = "bob"
		}
		all = append(all, e.send(user, conv, "m"+strconv.Itoa(i)))
	}
	ctx := t.Context()
	page := func(before uint64, limit int) app.HistoryPage {
		t.Helper()
		p, err := e.msgs.History(ctx, e.as("bob"), conv, before, limit)
		if err != nil {
			t.Fatalf("History(%d, %d): %+v", before, limit, err)
		}
		return p
	}
	p1 := page(0, 3)
	if got, want := bodies(p1.Messages), []string{"m4", "m5", "m6"}; !reflect.DeepEqual(got, want) || !p1.HasOlder || p1.NextBefore != all[4].ID {
		t.Errorf("first page = %v older %v before %d", bodies(p1.Messages), p1.HasOlder, p1.NextBefore)
	}
	p2 := page(p1.NextBefore, 3)
	if got, want := bodies(p2.Messages), []string{"m1", "m2", "m3"}; !reflect.DeepEqual(got, want) || !p2.HasOlder || p2.NextBefore != all[1].ID {
		t.Errorf("second page = %v older %v before %d", bodies(p2.Messages), p2.HasOlder, p2.NextBefore)
	}
	p3 := page(p2.NextBefore, 3)
	if got, want := bodies(p3.Messages), []string{"m0"}; !reflect.DeepEqual(got, want) || p3.HasOlder || p3.NextBefore != 0 {
		t.Errorf("last page = %v older %v before %d", bodies(p3.Messages), p3.HasOlder, p3.NextBefore)
	}
	exact := page(0, 7)
	if len(exact.Messages) != 7 || exact.HasOlder || exact.NextBefore != 0 {
		t.Errorf("a page of exactly all = %d messages older %v before %d", len(exact.Messages), exact.HasOlder, exact.NextBefore)
	}
	if def := page(0, 0); len(def.Messages) != 7 {
		t.Errorf("the default page has %d messages", len(def.Messages))
	}
	if cap := page(0, 100000); len(cap.Messages) != 7 {
		t.Errorf("a huge page has %d messages", len(cap.Messages))
	}
	// 50 by default, and 100 at most.
	big := e.direct("alice", "carol")
	for i := range 120 {
		if _, err := e.msgs.Send(ctx, e.as("alice"), big, app.MessageInput{Body: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ limit, want int }{{0, 50}, {-5, 50}, {10, 10}, {1000, 100}} {
		p, err := e.msgs.History(ctx, e.as("carol"), big, 0, tc.limit)
		if err != nil || len(p.Messages) != tc.want || !p.HasOlder {
			t.Errorf("limit %d: %d messages, older %v, %v; want %d", tc.limit, len(p.Messages), p.HasOlder, err, tc.want)
		}
	}
	empty := e.direct("bob", "carol")
	if p, err := e.msgs.History(ctx, e.as("bob"), empty, 0, 0); err != nil || p.Messages == nil || len(p.Messages) != 0 || p.HasOlder {
		t.Errorf("empty history = %+v, %v", p, err)
	}
}

func TestHistoryShowsQuotesAttachmentsAndEdits(t *testing.T) {
	e := newMsgEnv(t)
	conv := e.group("crew", "alice", "bob", "carol")
	first := e.send("bob", conv, "  a   long\n question  ")
	long, err := e.msgs.Send(t.Context(), e.as("carol"), conv, app.MessageInput{Body: strings.Repeat("é", 150)})
	if err != nil {
		t.Fatal(err)
	}
	filesOnly, err := e.msgs.Send(t.Context(), e.as("carol"), conv, app.MessageInput{Uploads: []app.Upload{e.upload("carol", "x.txt"), e.upload("carol", "y.txt")}})
	if err != nil {
		t.Fatal(err)
	}
	r1, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "answer", ReplyToID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "re long", ReplyToID: long.ID})
	if err != nil {
		t.Fatal(err)
	}
	r3, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "re files", ReplyToID: filesOnly.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Hour)
	if _, err := e.msgs.Edit(t.Context(), e.as("alice"), r1.ID, "answer, edited"); err != nil {
		t.Fatal(err)
	}
	got := map[uint64]app.Message{}
	for _, m := range e.history("bob", conv) {
		got[m.ID] = m
	}
	if q := got[r1.ID].ReplyTo; q == nil || q.Preview != "a long question" || q.Author.DisplayName != "Bob" || q.Deleted {
		t.Errorf("quote of the first = %+v", q)
	}
	if q := got[r2.ID].ReplyTo; q == nil || q.Preview != strings.Repeat("é", 100)+"…" {
		t.Errorf("quote of the long one = %+v", q)
	}
	if q := got[r3.ID].ReplyTo; q == nil || q.Preview != "x.txt, y.txt" {
		t.Errorf("quote of the files = %+v", q)
	}
	if m := got[r1.ID]; m.EditedAt == nil || m.Body != "answer, edited" || m.CanEdit || m.CanDelete || !m.CanReact {
		t.Errorf("edited message as bob = %+v", m)
	}
	if m := got[filesOnly.ID]; len(m.Attachments) != 2 || m.Body != "" {
		t.Errorf("files-only message = %+v", m)
	}
}

func TestConversationsList(t *testing.T) {
	e := newMsgEnv(t)
	ctx := t.Context()
	quiet := e.direct("alice", "dave") // no messages
	e.clk.Advance(time.Minute)
	old := e.group("old group", "alice", "bob", "carol")
	e.send("bob", old, "from bob")
	recent := e.direct("alice", "bob")
	e.send("bob", recent, "  the   newest\nmessage ")
	e.send("carol", e.direct("carol", "dave"), "not alice's")
	filesConv := e.direct("alice", "erin")
	if _, err := e.msgs.Send(ctx, e.as("erin"), filesConv, app.MessageInput{Uploads: []app.Upload{e.upload("erin", "plan.txt"), e.upload("erin", "map.txt")}}); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	delConv := e.group("", "alice", "carol", "dave")
	gone := e.send("carol", delConv, "oops")
	if err := e.msgs.Delete(ctx, e.as("carol"), gone.ID); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	longConv := e.direct("alice", "carol")
	e.send("alice", longConv, strings.Repeat("x", 300))

	list, err := e.msgs.Conversations(ctx, e.as("alice"))
	if err != nil {
		t.Fatalf("Conversations: %+v", err)
	}
	var order []uint64
	byID := map[uint64]app.ConversationSummary{}
	for _, c := range list {
		order = append(order, c.ID)
		byID[c.ID] = c
	}
	if want := []uint64{longConv, delConv, filesConv, recent, old, quiet}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	check := func(id uint64, kind app.ConversationKind, title, preview, author string, unread bool) {
		t.Helper()
		c := byID[id]
		if c.Kind != kind || c.DisplayName != title || c.Unread != unread {
			t.Errorf("conversation %d = %+v; want %s %q unread %v", id, c, kind, title, unread)
		}
		if preview == "" {
			if c.LastMessage != nil {
				t.Errorf("conversation %d has a last message: %+v", id, c.LastMessage)
			}
			return
		}
		if c.LastMessage == nil || c.LastMessage.Preview != preview || c.LastMessage.Author.DisplayName != author {
			t.Errorf("conversation %d last message = %+v; want %q by %s", id, c.LastMessage, preview, author)
		}
	}
	check(longConv, app.ConversationDirect, "Carol", strings.Repeat("x", 100)+"…", "Alice", false)
	check(delConv, app.ConversationGroup, "Carol, Dave", app.MessageDeletedPreview, "Carol", false)
	check(filesConv, app.ConversationDirect, "Erin", "plan.txt, map.txt", "Erin", true)
	check(recent, app.ConversationDirect, "Bob", "the newest message", "Bob", true)
	check(old, app.ConversationGroup, "old group", "from bob", "Bob", true)
	check(quiet, app.ConversationDirect, "Dave", "", "", false)
	if !byID[delConv].LastMessage.Deleted || byID[old].LastMessage.Deleted {
		t.Error("Deleted is wrong")
	}
	if c := byID[old]; len(c.Members) != 3 || c.Members[0].ID != e.uid("alice") || c.Name != "old group" {
		t.Errorf("members/name of the old group = %+v", c)
	}
	if byID[quiet].LastActivity.IsZero() || !byID[recent].LastActivity.After(byID[old].LastActivity) {
		t.Errorf("last activity: quiet %v, recent %v, old %v", byID[quiet].LastActivity, byID[recent].LastActivity, byID[old].LastActivity)
	}
	// Opening the newest and reading it clears its mark.
	if err := e.msgs.MarkRead(ctx, e.as("alice"), recent, e.history("alice", recent)[0].ID); err != nil {
		t.Fatal(err)
	}
	again, err := e.msgs.Conversations(ctx, e.as("alice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again {
		if c.ID == recent && c.Unread {
			t.Error("a read conversation is still unread")
		}
	}
	// Nothing for someone with no conversations.
	if none, err := e.msgs.Conversations(ctx, e.as("admin")); err != nil || len(none) != 0 {
		t.Errorf("admin's conversations = %v, %v; want none", none, err)
	}
}

func TestConversationsOrderByTheLatestMessage(t *testing.T) {
	// Messages sent at the same moment: the newer message wins, then the newer conversation.
	e := newMsgEnv(t)
	a := e.direct("alice", "bob")
	b := e.direct("alice", "carol")
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), b, app.MessageInput{Body: "b first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), a, app.MessageInput{Body: "a second"}); err != nil {
		t.Fatal(err)
	}
	list, err := e.msgs.Conversations(t.Context(), e.as("alice"))
	if err != nil || len(list) != 2 || list[0].ID != a || list[1].ID != b {
		t.Errorf("list = %+v, %v; want the conversation of the newest message first", list, err)
	}
}

func TestMessagesPublishEvents(t *testing.T) {
	tests := []struct {
		name string
		run  func(e *msgEnv) ([]live.Event, error)
	}{
		{"StartDirect", func(e *msgEnv) ([]live.Event, error) {
			id, err := e.msgs.StartDirect(t.Context(), e.as("alice"), e.uid("bob"))
			return []live.Event{chgEvt(id, e.uid("alice")), chgEvt(id, e.uid("bob"))}, err
		}},
		{"StartDirect again publishes nothing", func(e *msgEnv) ([]live.Event, error) {
			e.direct("alice", "bob")
			e.rec.Reset()
			_, err := e.msgs.StartDirect(t.Context(), e.as("bob"), e.uid("alice"))
			return nil, err
		}},
		{"StartGroup", func(e *msgEnv) ([]live.Event, error) {
			id, err := e.msgs.StartGroup(t.Context(), e.as("alice"), e.uids("bob", "carol"), "")
			return []live.Event{chgEvt(id, e.uid("alice")), chgEvt(id, e.uid("bob")), chgEvt(id, e.uid("carol"))}, err
		}},
		{"AddMembers: the added member hears of it too", func(e *msgEnv) ([]live.Event, error) {
			id := e.group("", "alice", "bob", "carol")
			e.rec.Reset()
			err := e.msgs.AddMembers(t.Context(), e.as("bob"), id, e.uids("dave"))
			return []live.Event{
				{Topic: live.ConversationTopic(id), Kind: live.KindConversationUpdated, ConversationID: id},
				chgEvt(id, e.uid("dave")), chgEvt(id, e.uid("alice")), chgEvt(id, e.uid("bob")), chgEvt(id, e.uid("carol")),
			}, err
		}},
		{"AddMembers: nobody new publishes nothing", func(e *msgEnv) ([]live.Event, error) {
			id := e.group("", "alice", "bob", "carol")
			e.rec.Reset()
			return nil, e.msgs.AddMembers(t.Context(), e.as("alice"), id, e.uids("bob"))
		}},
		{"Rename", func(e *msgEnv) ([]live.Event, error) {
			id := e.group("", "alice", "bob", "carol")
			e.rec.Reset()
			err := e.msgs.Rename(t.Context(), e.as("carol"), id, "crew")
			return []live.Event{
				{Topic: live.ConversationTopic(id), Kind: live.KindConversationUpdated, ConversationID: id},
				chgEvt(id, e.uid("alice")), chgEvt(id, e.uid("bob")), chgEvt(id, e.uid("carol")),
			}, err
		}},
		{"Send", func(e *msgEnv) ([]live.Event, error) {
			id := e.group("", "alice", "bob", "carol")
			e.rec.Reset()
			m, err := e.msgs.Send(t.Context(), e.as("bob"), id, app.MessageInput{Body: "hi"})
			return []live.Event{
				msgEvt(live.KindMessageCreated, id, m.ID),
				chgEvt(id, e.uid("alice")), chgEvt(id, e.uid("bob")), chgEvt(id, e.uid("carol")),
			}, err
		}},
		{"Send does not reach a member who left", func(e *msgEnv) ([]live.Event, error) {
			id := e.group("", "alice", "bob", "carol")
			if err := e.msgs.Leave(t.Context(), e.as("carol"), id); err != nil {
				return nil, err
			}
			e.rec.Reset()
			m, err := e.msgs.Send(t.Context(), e.as("bob"), id, app.MessageInput{Body: "hi"})
			return []live.Event{
				msgEvt(live.KindMessageCreated, id, m.ID),
				chgEvt(id, e.uid("alice")), chgEvt(id, e.uid("bob")),
			}, err
		}},
		{"A refused Send publishes nothing", func(e *msgEnv) ([]live.Event, error) {
			id := e.group("", "alice", "bob", "carol")
			e.rec.Reset()
			_, err := e.msgs.Send(t.Context(), e.as("bob"), id, app.MessageInput{})
			if err == nil {
				return []live.Event{{}}, nil // it must fail
			}
			return nil, nil
		}},
		{"A returning member hears of it", func(e *msgEnv) ([]live.Event, error) {
			id := e.group("", "alice", "bob", "carol")
			if err := e.msgs.Leave(t.Context(), e.as("carol"), id); err != nil {
				return nil, err
			}
			e.rec.Reset()
			err := e.msgs.AddMembers(t.Context(), e.as("alice"), id, e.uids("carol"))
			return []live.Event{
				{Topic: live.ConversationTopic(id), Kind: live.KindConversationUpdated, ConversationID: id},
				chgEvt(id, e.uid("carol")), chgEvt(id, e.uid("alice")), chgEvt(id, e.uid("bob")),
			}, err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMsgEnv(t)
			e.rec.Reset()
			want, err := tt.run(e)
			if err != nil {
				t.Fatalf("got %+v", err)
			}
			got := e.rec.Events()
			if len(got) == 0 && len(want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("published\n got  %+v\n want %+v", got, want)
			}
		})
	}
}

// TestMessagesPublishAfterTheCommit checks that the change can be read when
// the event arrives, which cannot be the case inside the transaction.
func TestMessagesPublishAfterTheCommit(t *testing.T) {
	e := newMsgEnv(t)
	pr := &probe{fn: func(live.Event) {}}
	e.msgs = app.NewMessages(e.st, e.clk, e.files, app.MessagesConfig{MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments}, pr)
	conv := e.group("", "alice", "bob", "carol")
	readCtx := func() context.Context {
		ctx, cancel := context.WithTimeout(e.t.Context(), 3*time.Second)
		e.t.Cleanup(cancel)
		return ctx
	}
	seen := 0
	pr.fn = func(ev live.Event) {
		if ev.Kind != live.KindMessageCreated {
			return
		}
		rows, err := e.st.MessageRowsByIDs(readCtx(), []uint64{ev.MessageID})
		if err != nil {
			t.Errorf("MessageRowsByIDs inside Publish: %+v", err)
			return
		}
		if rows[ev.MessageID].Body == "committed" {
			seen++
		}
	}
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "committed"}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("the message was readable in %d of the Publish calls, want 1", seen)
	}
}

func TestMessagesWithoutAPublisher(t *testing.T) {
	e := newMsgEnv(t)
	msgs := app.NewMessages(e.st, e.clk, e.files, app.MessagesConfig{}, nil)
	id, err := msgs.StartDirect(t.Context(), e.as("alice"), e.uid("bob"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := msgs.Send(t.Context(), e.as("alice"), id, app.MessageInput{Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := msgs.MarkRead(t.Context(), e.as("bob"), id, m.ID); err != nil {
		t.Fatal(err)
	}
}

// TestMessagesStoreErrorPublishesNothing closes the database under every
// method that publishes: nothing is published.
func TestMessagesStoreErrorPublishesNothing(t *testing.T) {
	calls := []struct {
		name string
		call func(e *permEnv) error
	}{
		{"StartDirect", func(e *permEnv) error {
			_, err := e.msgs.StartDirect(t.Context(), e.as("alice"), e.uid("dave"))
			return err
		}},
		{"StartGroup", func(e *permEnv) error {
			_, err := e.msgs.StartGroup(t.Context(), e.as("alice"), e.uids("bob", "dave"), "")
			return err
		}},
		{"AddMembers", func(e *permEnv) error {
			return e.msgs.AddMembers(t.Context(), e.as("alice"), e.conv, e.uids("dave"))
		}},
		{"Rename", func(e *permEnv) error { return e.msgs.Rename(t.Context(), e.as("alice"), e.conv, "x") }},
		{"Leave", func(e *permEnv) error { return e.msgs.Leave(t.Context(), e.as("bob"), e.conv) }},
		{"Send", func(e *permEnv) error {
			_, err := e.msgs.Send(t.Context(), e.as("bob"), e.conv, app.MessageInput{Body: "x"})
			return err
		}},
		{"Edit", func(e *permEnv) error {
			_, err := e.msgs.Edit(t.Context(), e.as("alice"), e.msg.ID, "x")
			return err
		}},
		{"Delete", func(e *permEnv) error { return e.msgs.Delete(t.Context(), e.as("alice"), e.msg.ID) }},
		{"React", func(e *permEnv) error {
			_, err := e.msgs.React(t.Context(), e.as("bob"), e.msg.ID, "\U0001F44D")
			return err
		}},
		{"MarkRead", func(e *permEnv) error { return e.msgs.MarkRead(t.Context(), e.as("bob"), e.conv, e.msg.ID) }},
		{"Conversations", func(e *permEnv) error {
			_, err := e.msgs.Conversations(t.Context(), e.as("bob"))
			return err
		}},
		{"UnreadCount", func(e *permEnv) error {
			_, err := e.msgs.UnreadCount(t.Context(), e.as("bob"))
			return err
		}},
		{"History", func(e *permEnv) error {
			_, err := e.msgs.History(t.Context(), e.as("bob"), e.conv, 0, 0)
			return err
		}},
		{"Conversation", func(e *permEnv) error {
			_, err := e.msgs.Conversation(t.Context(), e.as("bob"), e.conv)
			return err
		}},
		{"Attachment", func(e *permEnv) error {
			_, _, err := e.msgs.Attachment(t.Context(), e.as("bob"), e.att)
			return err
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			e := newPermEnv(t)
			if err := e.st.Close(); err != nil {
				t.Fatalf("Close: %+v", err)
			}
			if err := tc.call(e); err == nil {
				t.Fatal("the call succeeded on a closed database")
			}
			if got := e.rec.Events(); len(got) != 0 {
				t.Errorf("published %+v after a store error", got)
			}
		})
	}
}
