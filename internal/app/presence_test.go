package app_test

import (
	"context"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// presenceEnv is the rooms fixture (admin, root, alice, bob, carol, dave) with
// a disabled user dina and a presence service over a memory presence.
type presenceEnv struct {
	*roomsEnv
	mem  *live.MemoryPresence
	pres *app.Presence
}

func newPresenceEnv(t *testing.T) *presenceEnv {
	t.Helper()
	e := &presenceEnv{roomsEnv: newRoomsEnv(t), mem: live.NewMemoryPresence()}
	e.pres = app.NewPresence(e.st, e.mem)
	e.users["dina"] = e.signUp("dina", store.RoleMember)
	return e
}

// disableDina disables dina.
func (e *presenceEnv) disableDina() {
	e.t.Helper()
	if err := e.svc.SetDisabled(e.t.Context(), e.as("admin"), e.users["dina"].ID, true); err != nil {
		e.t.Fatalf("SetDisabled: %+v", err)
	}
}

// ids are the IDs of the named users.
func (e *presenceEnv) ids(names ...string) []uint64 {
	e.t.Helper()
	out := make([]uint64, len(names))
	for i, n := range names {
		out[i] = e.users[n].ID
	}
	return out
}

// onlineSet makes the set of the named users.
func (e *presenceEnv) onlineSet(names ...string) map[uint64]bool {
	e.t.Helper()
	out := map[uint64]bool{}
	for _, id := range e.ids(names...) {
		out[id] = true
	}
	return out
}

func sameSetOf(a, b map[uint64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestPresenceOnline(t *testing.T) {
	t.Parallel()
	const unauth = "Presence.Online->checkActive.Unauthenticated"
	tests := []struct {
		name string
		// connected are the users with an open connection (dina's is open
		// before she is disabled).
		connected []string
		// disableDina disables dina after she connected.
		disableDina bool
		ask         []string
		// extra are IDs of users who do not exist.
		extra []uint64
		by    string // carol when empty
		want  []string
	}{
		{name: "nobody is online", ask: []string{"alice", "bob"}, want: nil},
		{name: "those who are connected", connected: []string{"alice", "bob"}, ask: []string{"alice", "bob", "carol"}, want: []string{"alice", "bob"}},
		{name: "only those asked about", connected: []string{"alice", "bob", "dave"}, ask: []string{"bob"}, want: []string{"bob"}},
		{name: "the asker may ask about themselves", connected: []string{"carol"}, ask: []string{"carol"}, want: []string{"carol"}},
		{name: "an admin who is a member of nothing", connected: []string{"alice"}, ask: []string{"alice", "bob"}, by: "root", want: []string{"alice"}},
		{name: "any member asks about any users", connected: []string{"admin", "root"}, ask: []string{"admin", "root", "dave"}, by: "dave", want: []string{"admin", "root"}},
		{name: "a disabled user is never online", connected: []string{"alice", "dina"}, disableDina: true, ask: []string{"alice", "dina"}, want: []string{"alice"}},
		{name: "an enabled user who connected is online", connected: []string{"dina"}, ask: []string{"dina"}, want: []string{"dina"}},
		{name: "a user who does not exist is absent", connected: []string{"alice"}, ask: []string{"alice"}, extra: []uint64{9999, 8888}, want: []string{"alice"}},
		{name: "no users asked", connected: []string{"alice"}, ask: nil, want: nil},
		{name: "the same user twice", connected: []string{"alice"}, ask: []string{"alice", "alice"}, want: []string{"alice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newPresenceEnv(t)
			for _, n := range tt.connected {
				e.mem.Connect(e.users[n].ID)
			}
			if tt.disableDina {
				e.disableDina()
			}
			by := tt.by
			if by == "" {
				by = "carol"
			}
			ask := append(e.ids(tt.ask...), tt.extra...)
			got, err := e.pres.Online(t.Context(), e.as(by), ask)
			if err != nil {
				t.Fatalf("Online: %+v", err)
			}
			if want := e.onlineSet(tt.want...); !sameSetOf(got, want) {
				t.Errorf("online = %v, want %v", got, want)
			}
			for id, ok := range got {
				if !ok {
					t.Errorf("user %d is in the set as false", id)
				}
			}
		})
	}
	t.Run("a disabled or missing asker", func(t *testing.T) {
		t.Parallel()
		e := newPresenceEnv(t)
		e.disableDina()
		for name, actor := range map[string]app.Actor{
			"disabled": e.as("dina"),
			"unknown":  {UserID: 99999},
			"nobody":   {},
		} {
			got, err := e.pres.Online(t.Context(), actor, e.ids("alice"))
			_ = requireTag(t, err, app.TagUnauthenticated, unauth)
			if got != nil {
				t.Errorf("%s: online = %v, want nothing", name, got)
			}
		}
	})
}

func TestPresenceOnlineLimit(t *testing.T) {
	t.Parallel()
	const route = "Presence.Online.InvalidInput"
	ids := func(n int) []uint64 {
		out := make([]uint64, n)
		for i := range out {
			out[i] = uint64(i + 1)
		}
		return out
	}
	tests := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"none", 0, false},
		{"one", 1, false},
		{"99", 99, false},
		{"exactly 100", app.PresenceMaxUsers, false},
		{"101", app.PresenceMaxUsers + 1, true},
		{"1000", 1000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newPresenceEnv(t)
			e.mem.Connect(e.users["alice"].ID)
			got, err := e.pres.Online(t.Context(), e.as("bob"), ids(tt.n))
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Online(%d): %+v", tt.n, err)
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, route)
			if f := fieldsOf(t, appErr); f[app.FieldUserIDs] == "" || len(f) != 1 {
				t.Errorf("fields = %v, want only %s", f, app.FieldUserIDs)
			}
			if got != nil {
				t.Errorf("online = %v, want nothing", got)
			}
		})
	}
	if app.FieldUserIDs != "user_ids" || app.PresenceMaxUsers != 100 {
		t.Errorf("FieldUserIDs = %q, PresenceMaxUsers = %d", app.FieldUserIDs, app.PresenceMaxUsers)
	}
}

func TestPresenceConnectAndDisconnectPassThrough(t *testing.T) {
	t.Parallel()
	e := newPresenceEnv(t)
	alice, bob := e.users["alice"].ID, e.users["bob"].ID
	ask := func() map[uint64]bool {
		t.Helper()
		got, err := e.pres.Online(t.Context(), e.as("carol"), []uint64{alice, bob})
		if err != nil {
			t.Fatalf("Online: %+v", err)
		}
		return got
	}
	if got := ask(); len(got) != 0 {
		t.Fatalf("online at the start = %v", got)
	}
	e.pres.Connect(alice)
	e.pres.Connect(alice)
	e.pres.Connect(bob)
	if got := ask(); !sameSetOf(got, e.onlineSet("alice", "bob")) {
		t.Errorf("after Connect = %v", got)
	}
	// The memory presence under it sees the same.
	if online, _ := e.mem.Online(t.Context(), []uint64{alice, bob}); !sameSetOf(online, e.onlineSet("alice", "bob")) {
		t.Errorf("the presence under the service = %v", online)
	}
	// Two connections: one Disconnect leaves her online.
	e.pres.Disconnect(alice)
	if got := ask(); !sameSetOf(got, e.onlineSet("alice", "bob")) {
		t.Errorf("after one Disconnect of two = %v", got)
	}
	e.pres.Disconnect(alice)
	e.pres.Disconnect(bob)
	if got := ask(); len(got) != 0 {
		t.Errorf("after the last Disconnect = %v", got)
	}
	// A Disconnect without a Connect does nothing.
	e.pres.Disconnect(alice)
	e.pres.Connect(alice)
	if got := ask(); !sameSetOf(got, e.onlineSet("alice")) {
		t.Errorf("after a stray Disconnect and a Connect = %v", got)
	}
}

func TestPresenceOnlineIn(t *testing.T) {
	t.Parallel()
	e := newPresenceEnv(t)
	for _, n := range []string{"alice", "carol"} {
		e.pres.Connect(e.users[n].ID)
	}
	tests := []struct {
		name string
		ask  []string
		want []string
	}{
		{"those who are online", []string{"alice", "bob", "carol"}, []string{"alice", "carol"}},
		{"nobody online among them", []string{"bob", "dave"}, nil},
		{"nobody asked", nil, nil},
		{"more than 100 are not refused", nil, []string{"alice", "carol"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ask := e.ids(tt.ask...)
			if tt.name == "more than 100 are not refused" {
				ask = append(ask, e.ids("alice", "carol")...)
				for i := uint64(1000); i < 1150; i++ {
					ask = append(ask, i)
				}
			}
			got := e.pres.OnlineIn(t.Context(), ask)
			if want := e.onlineSet(tt.want...); !sameSetOf(got, want) {
				t.Errorf("OnlineIn = %v, want %v", got, want)
			}
		})
	}
}

func TestANilPresenceServiceIsSafe(t *testing.T) {
	t.Parallel()
	var p *app.Presence
	p.Connect(1)
	p.Disconnect(1)
	if got := p.OnlineIn(t.Context(), []uint64{1, 2}); len(got) != 0 {
		t.Errorf("OnlineIn on a nil service = %v, want nobody", got)
	}
}

func TestNewPresenceWithoutAPresenceHasNobodyOnline(t *testing.T) {
	t.Parallel()
	e := newPresenceEnv(t)
	p := app.NewPresence(e.st, nil)
	got, err := p.Online(t.Context(), e.as("bob"), e.ids("alice", "carol"))
	if err != nil || len(got) != 0 {
		t.Errorf("online = %v, %v; want nobody", got, err)
	}
	// It still counts the connections it is told about.
	p.Connect(e.users["alice"].ID)
	got, err = p.Online(t.Context(), e.as("bob"), e.ids("alice", "carol"))
	if err != nil || !sameSetOf(got, e.onlineSet("alice")) {
		t.Errorf("online after Connect = %v, %v; want alice", got, err)
	}
}

// failingPresence is a Presence whose shared store (Redis) cannot be read.
type failingPresence struct{ *live.MemoryPresence }

const tagPresenceUnreadable = "PresenceUnreadable"

func (*failingPresence) Online(context.Context, []uint64) (map[uint64]bool, error) {
	return nil, errors.New(tagPresenceUnreadable, "the shared presence cannot be read")
}

func TestPresenceThatCannotBeRead(t *testing.T) {
	t.Parallel()
	e := newPresenceEnv(t)
	p := app.NewPresence(e.st, &failingPresence{live.NewMemoryPresence()})
	got, err := p.Online(t.Context(), e.as("bob"), e.ids("alice"))
	_ = requireTag(t, err, tagPresenceUnreadable, "Presence.Online->failingPresence.Online."+tagPresenceUnreadable)
	if got != nil {
		t.Errorf("online = %v, want nothing", got)
	}
	// The members list just shows nobody online.
	if got := p.OnlineIn(t.Context(), e.ids("alice")); len(got) != 0 {
		t.Errorf("OnlineIn = %v, want nobody when the presence fails", got)
	}
}
