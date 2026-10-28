package app_test

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// searchMessages runs a search as the user and fails the test on an error.
func (e *msgEnv) searchMessages(user, q string, before uint64) app.MessageSearchPage {
	e.t.Helper()
	page, err := e.msgs.Search(e.t.Context(), e.as(user), q, before)
	if err != nil {
		e.t.Fatalf("Search(%q) = %+v", q, err)
	}
	return page
}

// foundMessages returns the IDs of the results of a search, in order.
func (e *msgEnv) foundMessages(user, q string) []uint64 {
	e.t.Helper()
	ids := []uint64{}
	for _, r := range e.searchMessages(user, q, 0).Results {
		ids = append(ids, r.MessageID)
	}
	return ids
}

// TestMessagesSearchPermissions is the table of SPEC §12 for Messages.Search:
// who finds a message of a conversation.
func TestMessagesSearchPermissions(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	ab := e.direct("alice", "bob")
	crew := e.group("Crew", "alice", "bob", "carol")
	mine := e.send("alice", ab, "the lighthouse plan")
	theirs := e.send("alice", crew, "the lighthouse crew")
	// carol leaves the crew, erin was never in it.
	if err := e.msgs.Leave(t.Context(), e.as("carol"), crew); err != nil {
		t.Fatal(err)
	}
	cd := e.direct("carol", "dave")
	cdMsg := e.send("dave", cd, "lighthouse supper")

	tests := []struct {
		name string
		user string
		want []uint64
	}{
		{"a member of both conversations", "alice", []uint64{theirs.ID, mine.ID}},
		{"a member of one", "bob", []uint64{theirs.ID, mine.ID}},
		{"a member who left the group finds nothing of it", "carol", []uint64{cdMsg.ID}},
		{"another user's direct conversation", "dave", []uint64{cdMsg.ID}},
		{"a user in no conversation", "erin", []uint64{}},
		{"an admin who is not a member", "admin", []uint64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := e.foundMessages(tt.user, "lighthouse"); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("found %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("coming back brings the conversation back", func(t *testing.T) {
		if err := e.msgs.AddMembers(t.Context(), e.as("alice"), crew, []uint64{e.uid("carol")}); err != nil {
			t.Fatal(err)
		}
		if got, want := e.foundMessages("carol", "crew"), []uint64{theirs.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("found %v, want %v", got, want)
		}
	})

	t.Run("a member who joined later finds the earlier messages", func(t *testing.T) {
		g := e.group("Late", "alice", "bob", "carol")
		early := e.send("alice", g, "earlybird note")
		if err := e.msgs.AddMembers(t.Context(), e.as("alice"), g, []uint64{e.uid("erin")}); err != nil {
			t.Fatal(err)
		}
		if got, want := e.foundMessages("erin", "earlybird"), []uint64{early.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("found %v, want %v", got, want)
		}
	})

	t.Run("a disabled user", func(t *testing.T) {
		e.disable("bob")
		_, err := e.msgs.Search(t.Context(), e.as("bob"), "lighthouse", 0)
		_ = requireTag(t, err, app.TagUnauthenticated, "Messages.Search->checkActive.Unauthenticated")
	})

	t.Run("no user", func(t *testing.T) {
		_, err := e.msgs.Search(t.Context(), app.Actor{}, "lighthouse", 0)
		_ = requireTag(t, err, app.TagUnauthenticated, "Messages.Search->checkActive.Unauthenticated")
	})

	t.Run("a user that does not exist", func(t *testing.T) {
		_, err := e.msgs.Search(t.Context(), app.Actor{UserID: 99999}, "lighthouse", 0)
		_ = requireTag(t, err, app.TagUnauthenticated, "Messages.Search->checkActive.Unauthenticated")
	})
}

// TestMessagesSearchQuery is the table of the queries of SPEC §7.8 that
// apply to messages: words, prefixes, phrases, accents and case, and the
// queries that are refused.
func TestMessagesSearchQuery(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	c := e.direct("alice", "bob")
	deploy := e.send("alice", c, "Please deploy the staging server tonight")
	release := e.send("bob", c, "The deployment of the release is done")
	cafe := e.send("alice", c, "Café at noon? Bring the Résumé")
	hash := e.send("bob", c, "Issue #12 and room:lobby are plain text here")

	tests := []struct {
		name string
		q    string
		want []uint64 // newest first
	}{
		{"a word", "staging", []uint64{deploy.ID}},
		{"a prefix", "deploy", []uint64{release.ID, deploy.ID}},
		{"two words, both must match", "deploy server", []uint64{deploy.ID}},
		{"two words that never meet", "staging release", []uint64{}},
		{"a phrase", `"staging server"`, []uint64{deploy.ID}},
		{"a phrase in the wrong order", `"server staging"`, []uint64{}},
		{"a phrase and a word", `"the release" done`, []uint64{release.ID}},
		{"case", "DEPLOY Server", []uint64{deploy.ID}},
		{"accents in the text", "cafe resume", []uint64{cafe.ID}},
		{"accents in the query", "CAFÉ", []uint64{cafe.ID}},
		{"operators are words", "deploy OR release", []uint64{}},
		{"white space around", "   staging   ", []uint64{deploy.ID}},
		{"a quoted hash is text", `"#12"`, []uint64{hash.ID}},
		{"a quoted filter is text", `"room:lobby"`, []uint64{hash.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := e.foundMessages("alice", tt.q); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("found %v, want %v", got, tt.want)
			}
		})
	}

	refused := []struct {
		name  string
		q     string
		field string
	}{
		{"empty", "", "Type a word to search the messages."},
		{"white space", "  \t ", "Type a word to search the messages."},
		{"only punctuation", "- ** ()", "Type a word to search the messages."},
		{"a tag only", "#launch", "Type a word to search the messages."},
		{"filters only", "room:lobby from:bob is:post after:2026-10-01", "Type a word to search the messages."},
		{"a tag next to words", "launch #plan", "Filters such as #tag, room: and from: do not work in messages. Type words, or put the text in quotes."},
		{"a room next to words", "launch room:lobby", "Filters such as #tag, room: and from: do not work in messages. Type words, or put the text in quotes."},
		{"a date next to words", "launch before:2026-10-01", "Filters such as #tag, room: and from: do not work in messages. Type words, or put the text in quotes."},
		{"is:comment next to words", "launch is:comment", "Filters such as #tag, room: and from: do not work in messages. Type words, or put the text in quotes."},
		{"a malformed date", "launch after:soon", "Use dates like 2026-10-27."},
	}
	for _, tt := range refused {
		t.Run("refused: "+tt.name, func(t *testing.T) {
			_, err := e.msgs.Search(t.Context(), e.as("alice"), tt.q, 0)
			route := "Messages.Search.InvalidInput"
			if strings.HasPrefix(tt.field, "Use dates") {
				route = "Messages.Search->ParseQuery.InvalidInput"
			}
			appErr := requireTag(t, err, app.TagInvalidInput, route)
			if got := fieldsOf(t, appErr)[app.FieldQuery]; got != tt.field {
				t.Errorf("message = %q, want %q", got, tt.field)
			}
		})
	}
}

// TestMessagesSearchFollowsEdits checks that the index follows the service:
// an edit replaces the words, a deletion removes the message, and a failed
// change leaves the index as it was.
func TestMessagesSearchFollowsEdits(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	c := e.direct("alice", "bob")
	m := e.send("alice", c, "the quarterly budget")

	if got := e.foundMessages("bob", "quarterly"); !slices.Equal(got, []uint64{m.ID}) {
		t.Fatalf("found %v before the edit", got)
	}
	if _, err := e.msgs.Edit(t.Context(), e.as("alice"), m.ID, "the annual roadmap"); err != nil {
		t.Fatal(err)
	}
	if got := e.foundMessages("bob", "quarterly"); len(got) != 0 {
		t.Errorf("the old word still finds %v", got)
	}
	if got := e.foundMessages("bob", "roadmap"); !slices.Equal(got, []uint64{m.ID}) {
		t.Errorf("the new word finds %v", got)
	}

	// Failed edits leave the old words.
	for name, body := range map[string]string{"empty": "  ", "too long": strings.Repeat("x", 4001)} {
		if _, err := e.msgs.Edit(t.Context(), e.as("alice"), m.ID, body); err == nil {
			t.Fatalf("%s edit worked", name)
		}
	}
	if _, err := e.msgs.Edit(t.Context(), e.as("bob"), m.ID, "stolen words"); err == nil {
		t.Fatal("another member edited the message")
	}
	if got := e.foundMessages("bob", "roadmap"); !slices.Equal(got, []uint64{m.ID}) {
		t.Errorf("after failed edits the search finds %v", got)
	}
	if got := e.foundMessages("bob", "stolen"); len(got) != 0 {
		t.Errorf("a refused edit is found: %v", got)
	}

	if err := e.msgs.Delete(t.Context(), e.as("alice"), m.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.foundMessages("bob", "roadmap"); len(got) != 0 {
		t.Errorf("a deleted message is found: %v", got)
	}
	if got := e.foundMessages("alice", "annual"); len(got) != 0 {
		t.Errorf("a deleted message is found by its author: %v", got)
	}
}

// TestMessagesSearchPaging checks 30 results a page, the cursor and the
// results' conversation and author.
func TestMessagesSearchPaging(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	c := e.direct("alice", "bob")
	other := e.direct("carol", "dave")
	var ids []uint64
	for i := range 31 {
		author := "alice"
		if i%2 == 1 {
			author = "bob"
		}
		ids = append(ids, e.send(author, c, "pager line "+strconv.Itoa(i)).ID)
		e.send("carol", other, "pager line elsewhere")
	}
	slices.Reverse(ids)

	first := e.searchMessages("alice", "pager", 0)
	got := make([]uint64, len(first.Results))
	for i, r := range first.Results {
		got[i] = r.MessageID
	}
	if !slices.Equal(got, ids[:30]) || !first.HasMore || first.NextBefore != ids[29] {
		t.Fatalf("first page = %v more %v next %d, want %v more true next %d", got, first.HasMore, first.NextBefore, ids[:30], ids[29])
	}
	r := first.Results[0]
	if r.ConversationID != c || r.ConversationTitle != "Bob" || r.ConversationKind != app.ConversationDirect ||
		r.Author.Username != "bob" && r.Author.Username != "alice" || r.CreatedAt.IsZero() {
		t.Errorf("first result = %+v", r)
	}

	second := e.searchMessages("alice", "pager", first.NextBefore)
	if len(second.Results) != 1 || second.Results[0].MessageID != ids[30] || second.HasMore || second.NextBefore != 0 {
		t.Errorf("second page = %+v, want the oldest message only", second)
	}
	if third := e.searchMessages("alice", "pager", second.Results[0].MessageID); len(third.Results) != 0 || third.HasMore {
		t.Errorf("third page = %+v, want none", third)
	}
	// Conversations of others do not use up room on a page.
	if got := e.searchMessages("carol", "pager", 0); len(got.Results) != 30 || !got.HasMore {
		t.Errorf("carol finds %d results (more %v) in her conversation, want a full page", len(got.Results), got.HasMore)
	}
}

// TestMessagesSearchTitles checks the title of the conversation of a result as
// the searching user sees it.
func TestMessagesSearchTitles(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	direct := e.direct("alice", "bob")
	named := e.group("Launch crew", "alice", "bob", "carol")
	unnamed := e.group("", "alice", "bob", "carol")
	e.send("bob", direct, "titled words")
	e.send("bob", named, "titled words")
	e.send("bob", unnamed, "titled words")

	titles := func(user string) []string {
		var out []string
		for _, r := range e.searchMessages(user, "titled", 0).Results {
			out = append(out, r.ConversationTitle)
		}
		return out
	}
	if got, want := titles("alice"), []string{"Bob, Carol", "Launch crew", "Bob"}; !slices.Equal(got, want) {
		t.Errorf("titles for alice = %v, want %v", got, want)
	}
	if got, want := titles("bob"), []string{"Alice, Carol", "Launch crew", "Alice"}; !slices.Equal(got, want) {
		t.Errorf("titles for bob = %v, want %v", got, want)
	}
}

// TestMessagesSearchSnippet checks that the snippet is HTML-safe: the text is
// escaped, the matches are in <mark>, and a script in a message is text.
func TestMessagesSearchSnippet(t *testing.T) {
	t.Parallel()
	e := newMsgEnv(t)
	c := e.direct("alice", "bob")
	e.send("alice", c, `<script>alert("x")</script> & the <b>Report</b> is ready`)
	e.send("alice", c, "the fake marker and a report")

	page := e.searchMessages("bob", "report", 0)
	if len(page.Results) != 2 {
		t.Fatalf("%d results, want 2", len(page.Results))
	}
	snippet := string(page.Results[1].Snippet)
	want := `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; the &lt;b&gt;<mark>Report</mark>&lt;/b&gt; is ready`
	if snippet != want {
		t.Errorf("snippet = %s\nwant      %s", snippet, want)
	}
	if strings.Contains(snippet, "<script") || strings.Contains(snippet, "<b>") {
		t.Errorf("snippet holds raw HTML: %s", snippet)
	}
	var plain strings.Builder
	var marked []string
	for _, s := range page.Results[1].Segments {
		plain.WriteString(s.Text)
		if s.Match {
			marked = append(marked, s.Text)
		}
	}
	if plain.String() != `<script>alert("x")</script> & the <b>Report</b> is ready` || !slices.Equal(marked, []string{"Report"}) {
		t.Errorf("segments = %+v", page.Results[1].Segments)
	}
	// A message cannot fake a match: its own marker characters are dropped
	// from the index text.
	if s := string(page.Results[0].Snippet); strings.Count(s, "<mark>") != 1 || strings.Contains(s, "fake</mark>") {
		t.Errorf("snippet of the message with markers = %s", s)
	}
}

// TestPostsSearchNeverFindsAMessage checks the privacy rule of SPEC §7.11: the
// search of §7.8 does not search private messages.
func TestPostsSearchNeverFindsAMessage(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	msgs := app.NewMessages(e.st, e.clk, e.files, app.MessagesConfig{}, e.rec)
	conv, err := msgs.StartDirect(t.Context(), e.as("alice"), e.uid("bob"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msgs.Send(t.Context(), e.as("alice"), conv, app.MessageInput{Body: "zebrafish in a message"}); err != nil {
		t.Fatal(err)
	}
	post := e.write("bob", "lobby", "zebrafish in a post")

	for _, user := range []string{"alice", "bob", "admin"} {
		if got := e.found(user, "zebrafish"); !reflect.DeepEqual(got, []uint64{post.ID}) {
			t.Errorf("%s finds %v, want only the post %d", user, got, post.ID)
		}
	}
	// And the messages search does not return the post.
	page, err := msgs.Search(t.Context(), e.as("alice"), "zebrafish", 0)
	if err != nil || len(page.Results) != 1 {
		t.Errorf("Search = %+v, %v; want only the message", page, err)
	}
}
