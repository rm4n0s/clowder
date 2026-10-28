package store

import (
	"sync"
	"testing"
	"time"
)

func TestCallQueries(t *testing.T) {
	f := newFix(t)
	a, b, c := f.user(), f.user(), f.user()
	ab, ac := f.direct(a, b), f.direct(a, c)

	ringing := f.call(ab, a, b, CallRinging)
	f.clk.Advance(time.Minute)

	got, err := f.s.CallByID(f.ctx, ringing.ID)
	if err != nil || got.State != CallRinging || got.CallerID != a.ID || got.CalleeID != b.ID || got.Home != "p1" {
		t.Errorf("CallByID = %+v, %v", got, err)
	}
	open, err := f.s.OpenCallOfConversation(f.ctx, ab.ID)
	if err != nil || open.ID != ringing.ID {
		t.Errorf("OpenCallOfConversation = %+v, %v", open, err)
	}
	if _, err := f.s.OpenCallOfConversation(f.ctx, ac.ID); err == nil {
		t.Error("OpenCallOfConversation found a call in a conversation with none")
	} else {
		requireTagged(t, err, TagNotFound, "Store.OpenCallOfConversation.NotFound", "openCallOfConversation")
	}
	for _, id := range []uint64{a.ID, b.ID} {
		calls, err := f.s.OpenCallsOfUsers(f.ctx, id)
		if err != nil || len(calls) != 1 || calls[0].ID != ringing.ID {
			t.Errorf("OpenCallsOfUsers(%d) = %+v, %v", id, calls, err)
		}
	}
	if calls, err := f.s.OpenCallsOfUsers(f.ctx, c.ID); err != nil || len(calls) != 0 {
		t.Errorf("OpenCallsOfUsers(c) = %+v, %v; want none", calls, err)
	}
	if n, err := f.s.CountOpenCallsOfHome(f.ctx, "p1"); err != nil || n != 1 {
		t.Errorf("CountOpenCallsOfHome(p1) = %d, %v; want 1", n, err)
	}
	if n, err := f.s.CountOpenCallsOfHome(f.ctx, "p2"); err != nil || n != 0 {
		t.Errorf("CountOpenCallsOfHome(p2) = %d, %v; want 0", n, err)
	}

	// Ringing for 45 seconds: the call made a minute ago is found, one made
	// now is not.
	if rows, err := f.s.RingingCallsBefore(f.ctx, f.clk.Now().Add(-45*time.Second)); err != nil || len(rows) != 1 {
		t.Errorf("RingingCallsBefore = %+v, %v; want the call", rows, err)
	}
	if rows, err := f.s.RingingCallsBefore(f.ctx, f.clk.Now().Add(-2*time.Minute)); err != nil || len(rows) != 0 {
		t.Errorf("RingingCallsBefore(2 minutes ago) = %+v, %v; want none", rows, err)
	}

	// Answering moves a ringing call once.
	if ok, err := f.s.MarkCallAnswered(f.ctx, ringing.ID, f.clk.Now()); err != nil || !ok {
		t.Errorf("MarkCallAnswered = %v, %v; want true", ok, err)
	}
	if ok, err := f.s.MarkCallAnswered(f.ctx, ringing.ID, f.clk.Now()); err != nil || ok {
		t.Errorf("MarkCallAnswered of an active call = %v, %v; want false", ok, err)
	}
	got, _ = f.s.CallByID(f.ctx, ringing.ID)
	if got.State != CallActive || got.AnsweredAt == nil || !got.AnsweredAt.Equal(f.clk.Now()) {
		t.Errorf("answered call = %+v", got)
	}
	if rows, err := f.s.RingingCallsBefore(f.ctx, f.clk.Now()); err != nil || len(rows) != 0 {
		t.Errorf("RingingCallsBefore after the answer = %+v, %v; want none", rows, err)
	}

	// Ending works once, and records who.
	if ok, err := f.s.MarkCallEnded(f.ctx, ringing.ID, CallHangup, &a.ID, f.clk.Now()); err != nil || !ok {
		t.Errorf("MarkCallEnded = %v, %v; want true", ok, err)
	}
	if ok, err := f.s.MarkCallEnded(f.ctx, ringing.ID, CallFailed, nil, f.clk.Now()); err != nil || ok {
		t.Errorf("MarkCallEnded twice = %v, %v; want false", ok, err)
	}
	got, _ = f.s.CallByID(f.ctx, ringing.ID)
	if got.State != CallEnded || got.EndReason != CallHangup || got.EndedBy == nil || *got.EndedBy != a.ID || got.EndedAt == nil {
		t.Errorf("ended call = %+v", got)
	}
	if got.Open() {
		t.Error("an ended call is open")
	}
	if calls, err := f.s.OpenCalls(f.ctx, ""); err != nil || len(calls) != 0 {
		t.Errorf("OpenCalls = %+v, %v; want none", calls, err)
	}

	byID, err := f.s.CallsByIDs(f.ctx, []uint64{ringing.ID, 999})
	if err != nil || len(byID) != 1 || byID[ringing.ID].State != CallEnded {
		t.Errorf("CallsByIDs = %+v, %v", byID, err)
	}
	if _, err := f.s.CallByID(f.ctx, 999); err == nil {
		t.Error("CallByID found a call that does not exist")
	} else {
		requireTagged(t, err, TagNotFound, "Store.CallByID.NotFound", "callByID")
	}
}

func TestOpenCallsAreUniquePerUser(t *testing.T) {
	tests := []struct {
		name string
		// second returns the call that breaks the rule, after a→b rings.
		second func(f *fix, a, b, c *User, ac *Conversation) *Call
	}{
		{"the caller calls again", func(f *fix, a, b, c *User, ac *Conversation) *Call {
			return &Call{ConversationID: ac.ID, CallerID: a.ID, CalleeID: c.ID, State: CallRinging, Home: "p"}
		}},
		{"another caller calls the callee", func(f *fix, a, b, c *User, ac *Conversation) *Call {
			bc := f.direct(c, b)
			return &Call{ConversationID: bc.ID, CallerID: c.ID, CalleeID: b.ID, State: CallRinging, Home: "p"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			a, b, c := f.user(), f.user(), f.user()
			f.call(f.direct(a, b), a, b, CallRinging)
			second := tt.second(f, a, b, c, f.direct(a, c))
			err := f.s.CreateCall(f.ctx, second)
			if err == nil {
				t.Fatal("a second open call was accepted")
			}
			requireTagged(t, err, TagConflict, "Store.CreateCall.Conflict", "createCall")
		})
	}

	t.Run("an ended call frees the users", func(t *testing.T) {
		f := newFix(t)
		a, b := f.user(), f.user()
		ab := f.direct(a, b)
		first := f.call(ab, a, b, CallRinging)
		if _, err := f.s.MarkCallEnded(f.ctx, first.ID, CallCancelled, &a.ID, f.clk.Now()); err != nil {
			t.Fatal(err)
		}
		again := &Call{ConversationID: ab.ID, CallerID: b.ID, CalleeID: a.ID, State: CallRinging, Home: "p"}
		if err := f.s.CreateCall(f.ctx, again); err != nil {
			t.Errorf("a call after the first ended: %+v", err)
		}
	})
}

// TestConcurrentStartsRingOnce starts two calls to the same user at the same
// time, each in its own transaction that checks the open calls of both users
// under LockUsersForCall like Calls.Start does, and expects exactly one to
// ring.
func TestConcurrentStartsRingOnce(t *testing.T) {
	f := newFix(t)
	callee := f.user()
	const callers = 4
	users := make([]*User, callers)
	convs := make([]*Conversation, callers)
	for i := range users {
		users[i] = f.user()
		convs[i] = f.direct(users[i], callee)
	}
	var wg sync.WaitGroup
	results := make([]string, callers)
	for i := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := f.s.Tx(f.ctx, func(tx *Store) error {
				if err := tx.LockUsersForCall(f.ctx, users[i].ID, callee.ID); err != nil {
					return err
				}
				open, err := tx.OpenCallsOfUsers(f.ctx, users[i].ID, callee.ID)
				if err != nil {
					return err
				}
				if len(open) > 0 {
					results[i] = "busy"
					return nil
				}
				results[i] = "ringing"
				return tx.CreateCall(f.ctx, &Call{ConversationID: convs[i].ID, CallerID: users[i].ID, CalleeID: callee.ID, State: CallRinging, Home: "p"})
			})
			if err != nil {
				results[i] = "error: " + err.Error()
			}
		}()
	}
	wg.Wait()
	ringing := 0
	for _, r := range results {
		switch r {
		case "ringing":
			ringing++
		case "busy":
		default:
			t.Errorf("a start failed: %s", r)
		}
	}
	if ringing != 1 {
		t.Errorf("%d calls rang, want exactly 1 (%v)", ringing, results)
	}
	if n := f.count("calls"); n != 1 {
		t.Errorf("%d call rows, want 1", n)
	}
}

func TestListConversationsGivesTheCallOfALastCallEntry(t *testing.T) {
	f := newFix(t)
	a, b := f.user(), f.user()
	conv := f.direct(a, b)
	f.create(&ConversationMember{ConversationID: conv.ID, UserID: a.ID})
	f.create(&ConversationMember{ConversationID: conv.ID, UserID: b.ID})
	f.message(conv, a)
	call := f.call(conv, a, b, CallEnded)
	entry := &Message{ConversationID: conv.ID, AuthorID: a.ID, Kind: MessageCall, CallID: &call.ID}
	f.create(entry)

	items, err := f.s.ListConversations(f.ctx, b.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListConversations = %+v, %v", items, err)
	}
	it := items[0]
	if it.LastMessage == nil || it.LastMessage.ID != entry.ID || it.LastMessage.Kind != MessageCall || it.LastCall == nil || it.LastCall.ID != call.ID {
		t.Errorf("last message %+v, call %+v; want the call entry and its call", it.LastMessage, it.LastCall)
	}
	if !it.Unread {
		t.Error("a call entry of the other user does not make the conversation unread")
	}
	page, err := f.s.MessagePage(f.ctx, conv.ID, 0, 10)
	if err != nil || len(page) != 2 || page[0].Kind != MessageCall || page[0].CallID == nil || *page[0].CallID != call.ID || page[1].Kind != MessageText {
		t.Errorf("MessagePage = %+v, %v", page, err)
	}
}

func TestCallEntriesAreNotSearchable(t *testing.T) {
	f := newFix(t)
	a, b := f.user(), f.user()
	conv := f.direct(a, b)
	call := f.call(conv, a, b, CallEnded)
	entry := &Message{ConversationID: conv.ID, AuthorID: a.ID, Kind: MessageCall, CallID: &call.ID}
	f.create(entry)
	if err := f.s.ReindexMessage(f.ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	if rows := f.messageIndex(); len(rows) != 0 {
		t.Errorf("the index holds %v, want no row for a call entry", rows)
	}
	// Even with a body, which a call entry never has.
	f.exec("UPDATE messages SET body = 'hello' WHERE id = ?", entry.ID)
	if err := f.s.ReindexMessage(f.ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	if rows := f.messageIndex(); len(rows) != 0 {
		t.Errorf("the index holds %v, want no row for a call entry", rows)
	}
}

// TestCallQueriesFailWithQueryFailed checks the route of every call query on
// a database that cannot be reached (a canceled context).
func TestCallQueriesFailWithQueryFailed(t *testing.T) {
	f := newFix(t)
	ctx := canceled(t)
	now := f.clk.Now()
	tests := []struct {
		name string
		call func() error
		op   string
	}{
		{"CreateCall", func() error { return f.s.CreateCall(ctx, &Call{State: CallRinging}) }, "createCall"},
		{"CallByID", func() error { _, err := f.s.CallByID(ctx, 1); return err }, "callByID"},
		{"CallsByIDs", func() error { _, err := f.s.CallsByIDs(ctx, []uint64{1}); return err }, "callsByIDs"},
		{"OpenCallOfConversation", func() error { _, err := f.s.OpenCallOfConversation(ctx, 1); return err }, "openCallOfConversation"},
		{"OpenCallsOfUsers", func() error { _, err := f.s.OpenCallsOfUsers(ctx, 1, 2); return err }, "openCallsOfUsers"},
		{"CountOpenCallsOfHome", func() error { _, err := f.s.CountOpenCallsOfHome(ctx, "p"); return err }, "countOpenCallsOfHome"},
		{"OpenCalls", func() error { _, err := f.s.OpenCalls(ctx, "p"); return err }, "openCalls"},
		{"RingingCallsBefore", func() error { _, err := f.s.RingingCallsBefore(ctx, now); return err }, "ringingCallsBefore"},
		{"MarkCallAnswered", func() error { _, err := f.s.MarkCallAnswered(ctx, 1, now); return err }, "markCallAnswered"},
		{"MarkCallEnded", func() error { _, err := f.s.MarkCallEnded(ctx, 1, CallHangup, nil, now); return err }, "markCallEnded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), TagQueryFailed, "Store."+tt.name+".QueryFailed", tt.op)
		})
	}
}

// TestLockUsersForCallInATransaction checks that the lock can be taken, for
// users in any order and repeated, where the database has one (PostgreSQL),
// and does nothing where it has none (SQLite).
func TestLockUsersForCallInATransaction(t *testing.T) {
	f := newFix(t)
	a, b := f.user(), f.user()
	err := f.s.Tx(f.ctx, func(tx *Store) error {
		return tx.LockUsersForCall(f.ctx, b.ID, a.ID, b.ID)
	})
	if err != nil {
		t.Errorf("LockUsersForCall: %+v", err)
	}
	if onPostgres() {
		err := f.s.Tx(canceled(t), func(tx *Store) error { return tx.LockUsersForCall(canceled(t), a.ID) })
		if err == nil {
			t.Error("LockUsersForCall on a canceled context succeeded")
		}
	}
}
