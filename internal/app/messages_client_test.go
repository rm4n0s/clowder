package app_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
)

func TestOpenDirect(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	ctx := t.Context()
	id, created, err := e.msgs.OpenDirect(ctx, e.as("alice"), e.uid("bob"))
	if err != nil || !created || id == 0 {
		t.Fatalf("first OpenDirect = %d, %v, %v; want a new conversation", id, created, err)
	}
	for _, from := range []struct{ from, to string }{{"alice", "bob"}, {"bob", "alice"}} {
		again, created, err := e.msgs.OpenDirect(ctx, e.as(from.from), e.uid(from.to))
		if err != nil || created || again != id {
			t.Errorf("OpenDirect %s->%s = %d, %v, %v; want %d, not created", from.from, from.to, again, created, err, id)
		}
	}
	other, created, err := e.msgs.OpenDirect(ctx, e.as("alice"), e.uid("carol"))
	if err != nil || !created || other == id {
		t.Errorf("OpenDirect alice->carol = %d, %v, %v; want another new conversation", other, created, err)
	}
	e.disable("dave")
	for _, tt := range []struct {
		name string
		from string
		to   uint64
	}{
		{"yourself", "alice", e.uid("alice")},
		{"a disabled user", "alice", e.uid("dave")},
		{"an unknown user", "alice", 9999},
	} {
		_, created, err := e.msgs.OpenDirect(ctx, e.as(tt.from), tt.to)
		requireInvalid(t, err, own("OpenDirect", "InvalidInput"), app.FieldUser)
		if created {
			t.Errorf("%s: created = true on a refusal", tt.name)
		}
	}
	_, _, err = e.msgs.OpenDirect(ctx, app.Actor{}, e.uid("bob"))
	_ = requireTag(t, err, app.TagUnauthenticated, "Messages.OpenDirect.Unauthenticated")
}

func TestSummary(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	ctx := t.Context()
	direct := e.direct("alice", "bob")
	group := e.group("crew", "alice", "bob", "carol")
	m1 := e.send("alice", direct, "one")
	m2 := e.send("alice", direct, "two")
	if err := e.msgs.MarkRead(ctx, e.as("bob"), direct, m1.ID); err != nil {
		t.Fatal(err)
	}

	got, err := e.msgs.Summary(ctx, e.as("alice"), direct)
	if err != nil {
		t.Fatalf("Summary: %+v", err)
	}
	if got.ID != direct || got.Kind != app.ConversationDirect || got.DisplayName != "Bob" || got.Unread ||
		!got.CanSend || got.SeenUpToID != m1.ID || got.LastMessage == nil || got.LastMessage.ID != m2.ID || len(got.Members) != 2 {
		t.Errorf("Summary as alice = %+v", got)
	}
	// The other side sees the message unread and that alice has read her own.
	got, err = e.msgs.Summary(ctx, e.as("bob"), direct)
	if err != nil || !got.Unread || got.SeenUpToID != m2.ID || !got.CanSend {
		t.Errorf("Summary as bob = %+v, %v", got, err)
	}
	// A group has no "seen" and always can send.
	got, err = e.msgs.Summary(ctx, e.as("carol"), group)
	if err != nil || got.Kind != app.ConversationGroup || got.Name != "crew" || got.SeenUpToID != 0 || !got.CanSend || got.LastMessage != nil {
		t.Errorf("Summary of the group = %+v, %v", got, err)
	}
	// A disabled other user: nobody can send in the direct conversation.
	e.disable("bob")
	got, err = e.msgs.Summary(ctx, e.as("alice"), direct)
	if err != nil || got.CanSend {
		t.Errorf("Summary with bob disabled = %+v, %v; want CanSend false", got, err)
	}
	list, err := e.msgs.Conversations(ctx, e.as("alice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == direct && c.CanSend {
			t.Errorf("Conversations lists CanSend true with bob disabled: %+v", c)
		}
		if c.ID == group && !c.CanSend {
			t.Errorf("Conversations lists CanSend false for a group: %+v", c)
		}
	}
	// One who left no longer reads it.
	if err := e.msgs.Leave(ctx, e.as("carol"), group); err != nil {
		t.Fatal(err)
	}
	_, err = e.msgs.Summary(ctx, e.as("carol"), group)
	_ = requireTag(t, err, app.TagNotFound, "Messages.Summary->"+notMember)
	_, err = e.msgs.Summary(ctx, e.as("alice"), 9999)
	_ = requireTag(t, err, app.TagNotFound, "Messages.Summary->"+missing)
}

func TestSetReaction(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	ctx := t.Context()
	conv := e.group("crew", "alice", "bob", "carol")
	m := e.send("alice", conv, "react to me")
	const thumbs, heart = "\U0001F44D", "❤️"
	set := func(user, emoji string, present bool) app.Reactions {
		t.Helper()
		r, err := e.msgs.SetReaction(ctx, e.as(user), m.ID, emoji, present)
		if err != nil {
			t.Fatalf("SetReaction %s %q %v: %+v", user, emoji, present, err)
		}
		return r
	}
	e.rec.Reset()
	got := set("bob", thumbs, true)
	if len(got) != 1 || got[0].Emoji != thumbs || got[0].Count != 1 || !got[0].Mine {
		t.Errorf("after bob reacts: %+v", got)
	}
	wantEvent := []live.Event{msgEvt(live.KindMessageReaction, conv, m.ID)}
	if ev := e.rec.Events(); !reflect.DeepEqual(ev, wantEvent) {
		t.Errorf("published %+v, want %+v", ev, wantEvent)
	}
	// Putting it again changes nothing and publishes nothing.
	e.rec.Reset()
	if got := set("bob", thumbs, true); len(got) != 1 || got[0].Count != 1 {
		t.Errorf("putting twice: %+v", got)
	}
	if ev := e.rec.Events(); len(ev) != 0 {
		t.Errorf("a repeated put published %+v", ev)
	}
	// Another user sees it without the mark; carol's own heart is marked.
	set("carol", heart, true)
	asAlice := set("alice", thumbs, true)
	if len(asAlice) != 2 || asAlice[0].Count != 2 || !asAlice[0].Mine || asAlice[1].Mine {
		t.Errorf("as alice: %+v", asAlice)
	}
	// Taking off one that is not there is fine, and so is taking off one that is.
	e.rec.Reset()
	if got := set("alice", heart, false); len(got) != 2 {
		t.Errorf("taking off a heart alice never put: %+v", got)
	}
	if ev := e.rec.Events(); len(ev) != 0 {
		t.Errorf("a no-op delete published %+v", ev)
	}
	if got := set("alice", thumbs, false); len(got) != 2 || got[0].Count != 1 || got[0].Mine {
		t.Errorf("after alice took hers off: %+v", got)
	}
	if ev := e.rec.Events(); !reflect.DeepEqual(ev, wantEvent) {
		t.Errorf("a delete published %+v, want %+v", ev, wantEvent)
	}
	for _, bad := range []string{"", "x", thumbs + thumbs, "ab"} {
		for _, present := range []bool{true, false} {
			_, err := e.msgs.SetReaction(ctx, e.as("bob"), m.ID, bad, present)
			requireInvalid(t, err, own("SetReaction", "InvalidInput"), app.FieldEmoji)
		}
	}
	// A deleted message and a missing one are not found.
	if err := e.msgs.Delete(ctx, e.as("alice"), m.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.msgs.SetReaction(ctx, e.as("bob"), m.ID, thumbs, true)
	_ = requireTag(t, err, app.TagNotFound, own("SetReaction", "NotFound"))
	_, err = e.msgs.SetReaction(ctx, e.as("bob"), 9999, thumbs, true)
	_ = requireTag(t, err, app.TagNotFound, "Messages.SetReaction")
}

func TestMessagesStagedUploads(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	ctx := t.Context()
	posts := app.NewPosts(e.st, e.clk, markdown.New(), e.files, app.PostsConfig{
		MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments,
	}, nil)
	staged, err := posts.UploadStaged(ctx, e.as("alice"), "a.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	conv := e.direct("alice", "bob")

	// Only the uploader gets it back.
	for _, who := range []string{"bob", "admin"} {
		_, err := e.msgs.StagedUploads(ctx, e.as(who), []string{staged.ID})
		appErr := requireTag(t, err, app.TagInvalidInput, "Messages.StagedUploads->stagedUploads.InvalidInput")
		if _, ok := appErr.Metadata["fields"].(map[string]string)[app.FieldAttachments]; !ok {
			t.Errorf("%s: no message under %s: %v", who, app.FieldAttachments, appErr.Metadata)
		}
	}
	for _, bad := range []string{"", "nonsense", strings.Repeat("0", 64)} {
		_, err := e.msgs.StagedUploads(ctx, e.as("alice"), []string{bad})
		_ = requireTag(t, err, app.TagInvalidInput, "Messages.StagedUploads->stagedUploads.InvalidInput")
	}
	_, err = e.msgs.StagedUploads(ctx, app.Actor{}, []string{staged.ID})
	_ = requireTag(t, err, app.TagUnauthenticated, "Messages.StagedUploads->stagedUploads.Unauthenticated")

	ups, err := e.msgs.StagedUploads(ctx, e.as("alice"), []string{staged.ID})
	if err != nil || len(ups) != 1 || ups[0] != staged.Upload {
		t.Fatalf("StagedUploads = %+v, %v; want the staged upload", ups, err)
	}
	if none, err := e.msgs.StagedUploads(ctx, e.as("alice"), nil); err != nil || len(none) != 0 {
		t.Errorf("StagedUploads(nil) = %v, %v", none, err)
	}
	msg, err := e.msgs.Send(ctx, e.as("alice"), conv, app.MessageInput{Uploads: ups})
	if err != nil || len(msg.Attachments) != 1 || msg.Attachments[0].FileName != "a.txt" {
		t.Fatalf("Send = %+v, %v", msg, err)
	}
	// ForgetStaged ends the note: the upload cannot be used again.
	e.msgs.ForgetStaged(ctx, app.Actor{}, ups) // nobody: nothing happens
	if _, err := e.msgs.StagedUploads(ctx, e.as("alice"), []string{staged.ID}); err != nil {
		t.Fatalf("a call by nobody ended the note: %+v", err)
	}
	e.msgs.ForgetStaged(ctx, e.as("alice"), ups)
	_, err = e.msgs.StagedUploads(ctx, e.as("alice"), []string{staged.ID})
	_ = requireTag(t, err, app.TagInvalidInput, "Messages.StagedUploads->stagedUploads.InvalidInput")
}
