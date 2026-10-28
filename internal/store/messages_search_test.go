package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// reindexMessages has the store index the messages, for rows made without it
// (the fixtures write through GORM), and fails the test on an error.
func (f *fix) reindexMessages(msgs ...*Message) {
	f.tb.Helper()
	for _, m := range msgs {
		if err := f.s.ReindexMessage(f.ctx, m.ID); err != nil {
			f.tb.Fatalf("ReindexMessage: %+v", err)
		}
	}
}

// send writes a message through the store, which indexes it.
func (f *fix) send(c *Conversation, author *User, body string) *Message {
	f.tb.Helper()
	m := &Message{ConversationID: c.ID, AuthorID: author.ID, Body: body}
	if err := f.s.CreateMessage(f.ctx, m); err != nil {
		f.tb.Fatalf("CreateMessage: %+v", err)
	}
	return m
}

// searchIDs returns the IDs of the messages that reader finds for the words,
// newest first.
func (f *fix) searchIDs(reader *User, q MessageSearchQuery) []uint64 {
	f.tb.Helper()
	q.ReaderID = reader.ID
	if q.Limit == 0 {
		q.Limit = 100
	}
	hits, err := f.s.SearchMessages(f.ctx, q)
	if err != nil {
		f.tb.Fatalf("SearchMessages: %+v", err)
	}
	ids := make([]uint64, len(hits))
	for i, h := range hits {
		ids[i] = h.MessageID
	}
	return ids
}

func words(ws ...string) []SearchTerm {
	out := make([]SearchTerm, len(ws))
	for i, w := range ws {
		out[i] = SearchTerm{Text: w}
	}
	return out
}

func phrase(p string) []SearchTerm {
	return []SearchTerm{{Text: p, Phrase: true}}
}

// TestMessageIndexFollowsTheMessage checks that the index of messages holds
// the current body: created, edited, deleted, with no text, and after a
// transaction that was rolled back.
func TestMessageIndexFollowsTheMessage(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	conv := f.groupWith(alice, bob)

	m := f.send(conv, alice, "Quarterly budget review")
	if got := f.messageIndex(); len(got) != 1 || got[m.ID] != "Quarterly budget review" {
		t.Fatalf("index after create = %v", got)
	}

	// An edit replaces the text: the old word is gone, the new one is found.
	if err := f.s.UpdateMessageBody(f.ctx, m.ID, "Annual roadmap", f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := f.messageIndex(); len(got) != 1 || got[m.ID] != "Annual roadmap" {
		t.Errorf("index after edit = %v", got)
	}
	if ids := f.searchIDs(alice, MessageSearchQuery{Terms: words("quarterly")}); len(ids) != 0 {
		t.Errorf("the old word still finds %v", ids)
	}
	if ids := f.searchIDs(alice, MessageSearchQuery{Terms: words("roadmap")}); !slices.Equal(ids, []uint64{m.ID}) {
		t.Errorf("the new word finds %v, want [%d]", ids, m.ID)
	}

	// A failed transaction leaves the index as it was.
	errStop := context.Canceled
	err := f.s.Tx(f.ctx, func(tx *Store) error {
		if err := tx.UpdateMessageBody(f.ctx, m.ID, "Rolled back words", f.clk.Now()); err != nil {
			return err
		}
		if err := tx.MarkMessageDeleted(f.ctx, m.ID, f.clk.Now()); err != nil {
			return err
		}
		return errStop
	})
	if err == nil {
		t.Fatal("the transaction did not fail")
	}
	if got := f.messageIndex(); len(got) != 1 || got[m.ID] != "Annual roadmap" {
		t.Errorf("index after a rolled back change = %v", got)
	}

	// The markers of a snippet are not in the index text, so a message cannot
	// fake a match.
	faked := f.send(conv, bob, "abc plain")
	if got := f.messageIndex()[faked.ID]; got != "abc plain" {
		t.Errorf("index text of a message with markers = %q, want %q", got, "abc plain")
	}

	// A message with no text (only files) has no row, and neither has a
	// deleted one.
	files := f.send(conv, bob, "")
	if _, ok := f.messageIndex()[files.ID]; ok {
		t.Error("a message without text is in the index")
	}
	if err := f.s.UpdateMessageBody(f.ctx, files.ID, "now with words", f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := f.messageIndex()[files.ID]; got != "now with words" {
		t.Errorf("index text after adding words = %q", got)
	}
	if err := f.s.MarkMessageDeleted(f.ctx, files.ID, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.messageIndex()[files.ID]; ok {
		t.Error("a deleted message is in the index")
	}
	if ids := f.searchIDs(bob, MessageSearchQuery{Terms: words("words")}); len(ids) != 0 {
		t.Errorf("a deleted message is found: %v", ids)
	}
}

// messageIndex returns the index of messages as a map from message ID to text.
func (f *fix) messageIndex() map[uint64]string {
	f.tb.Helper()
	rows, err := f.s.MessageIndexRows(f.ctx)
	if err != nil {
		f.tb.Fatalf("MessageIndexRows: %+v", err)
	}
	return rows
}

// TestRebuildMessageSearchIndex checks that the bulk rebuild gives the index
// the rows the services would have made, and drops the rest.
func TestRebuildMessageSearchIndex(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	conv := f.groupWith(alice, bob)
	// The fixtures write through GORM, as a bulk load does: no index rows.
	kept := f.say(conv, alice, "keep this")
	gone := f.say(conv, bob, "deleted soon")
	f.say(conv, bob, "")
	f.exec("UPDATE messages SET body = '', deleted_at = ? WHERE id = ?", fakeStart, gone.ID)
	table, idColumn := messageSearchIndex(f.s.dialect)
	f.exec("INSERT INTO " + table + " (" + idColumn + ", text) VALUES (999, 'stray')")

	for run := 1; run <= 2; run++ {
		if err := f.s.RebuildMessageSearchIndex(f.ctx); err != nil {
			t.Fatalf("RebuildMessageSearchIndex (run %d): %+v", run, err)
		}
		if got := f.messageIndex(); len(got) != 1 || got[kept.ID] != "keep this" {
			t.Errorf("run %d: index = %v", run, got)
		}
	}
	st, err := f.s.Stats(f.ctx)
	if err != nil || st.UnindexedMessages != 0 || st.StrayMessageIndexRows != 0 {
		t.Errorf("Stats = %+v, %v", st, err)
	}
}

// TestSearchMessages is the table of the queries: words, prefixes, phrases,
// accents and case, the same on both databases.
func TestSearchMessages(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	conv := f.groupWith(alice, bob)
	byBody := map[string]*Message{}
	for _, body := range []string{
		"Please deploy the staging server tonight",
		"The deployment of the release is done",
		"Café at noon? Bring the Résumé",
		"server deploy order matters",
		"Release notes: budget-2026.xlsx attached",
		"Quote \"this\" line and 100% done",
	} {
		byBody[body] = f.send(conv, alice, body)
	}
	id := func(body string) uint64 { return byBody[body].ID }
	deploy, deployment := id("Please deploy the staging server tonight"), id("The deployment of the release is done")
	cafe, order := id("Café at noon? Bring the Résumé"), id("server deploy order matters")
	notes, quoted := id("Release notes: budget-2026.xlsx attached"), id(`Quote "this" line and 100% done`)

	tests := []struct {
		name  string
		terms []SearchTerm
		want  []uint64 // newest first
	}{
		{"a word", words("staging"), []uint64{deploy}},
		{"a prefix", words("deploy"), []uint64{order, deployment, deploy}},
		{"a longer prefix", words("deployme"), []uint64{deployment}},
		{"a word is not found by its middle", words("ploy"), nil},
		{"two words, both must match", words("deploy", "server"), []uint64{order, deploy}},
		{"two words that never meet", words("staging", "release"), nil},
		{"a phrase", phrase("staging server"), []uint64{deploy}},
		{"a phrase in the wrong order", phrase("server staging"), nil},
		{"case does not matter", words("DEPLOY", "Server"), []uint64{order, deploy}},
		{"accents do not matter", words("cafe", "resume"), []uint64{cafe}},
		{"accents typed", words("café"), []uint64{cafe}},
		{"punctuation splits words", words("budget"), []uint64{notes}},
		{"a file name part", words("xlsx"), []uint64{notes}},
		{"a quote in the word", words(`"this"`), []uint64{quoted}},
		{"a phrase with a quote", phrase(`"this" line`), []uint64{quoted}},
		{"operators are plain text", words("deploy OR server"), nil},
		{"a star is plain text", words("depl*"), []uint64{order, deployment, deploy}},
		{"no words find nothing", nil, nil},
		{"only punctuation finds nothing", words("!!!"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := f.searchIDs(alice, MessageSearchQuery{Terms: tt.terms})
			if !slices.Equal(got, tt.want) {
				t.Errorf("found %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSearchMessagesOfMembers checks who finds a message: the members of the
// conversation now, and nobody else, in the query itself.
func TestSearchMessagesOfMembers(t *testing.T) {
	f := newFix(t)
	alice, bob, carol, dave := f.user(), f.user(), f.user(), f.user()
	conv := f.groupWith(alice, bob)
	early := f.send(conv, alice, "early crew news")
	// carol joins later: she reads, and so finds, what was written before.
	f.create(&ConversationMember{ConversationID: conv.ID, UserID: carol.ID, AddedBy: &alice.ID})
	late := f.send(conv, bob, "late crew news")
	left := f.groupWith(alice, dave)
	secret := f.send(left, dave, "crew secret")
	if err := f.s.MarkConversationMemberLeft(f.ctx, left.ID, dave.ID, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	other := f.groupWith(bob, dave)
	hidden := f.send(other, bob, "crew hidden")

	tests := []struct {
		name   string
		reader *User
		want   []uint64
	}{
		{"a member", alice, []uint64{secret.ID, late.ID, early.ID}},
		{"a member who joined later finds the earlier messages", carol, []uint64{late.ID, early.ID}},
		{"a member who left finds nothing of that conversation", dave, []uint64{hidden.ID}},
		{"a user in no conversation", f.user(), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := f.searchIDs(tt.reader, MessageSearchQuery{Terms: words("crew")})
			if !slices.Equal(got, tt.want) {
				t.Errorf("found %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("coming back brings the conversation back", func(t *testing.T) {
		if err := f.s.ReaddConversationMember(f.ctx, left.ID, dave.ID, alice.ID); err != nil {
			t.Fatal(err)
		}
		got := f.searchIDs(dave, MessageSearchQuery{Terms: words("secret")})
		if !slices.Equal(got, []uint64{secret.ID}) {
			t.Errorf("found %v, want [%d]", got, secret.ID)
		}
	})
}

// TestSearchMessagesPaging checks the cursor: 31 matches are a page of 30 and
// then 1, newest first, and conversations the reader is not in do not use up
// the room of a page.
func TestSearchMessagesPaging(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	mine := f.groupWith(alice, bob)
	theirs := f.groupWith(bob, f.user())
	var ids []uint64
	for i := range 31 {
		ids = append(ids, f.send(mine, alice, "pager line "+string(rune('a'+i%26))).ID)
		// Every message of mine is followed by one the reader cannot see.
		f.send(theirs, bob, "pager line hidden")
	}
	slices.Reverse(ids)

	first := f.searchIDs(alice, MessageSearchQuery{Terms: words("pager"), Limit: 30})
	if !slices.Equal(first, ids[:30]) {
		t.Fatalf("first page = %v, want %v", first, ids[:30])
	}
	second := f.searchIDs(alice, MessageSearchQuery{Terms: words("pager"), Limit: 30, Before: first[len(first)-1]})
	if !slices.Equal(second, ids[30:]) {
		t.Errorf("second page = %v, want %v", second, ids[30:])
	}
	if third := f.searchIDs(alice, MessageSearchQuery{Terms: words("pager"), Limit: 30, Before: second[0]}); len(third) != 0 {
		t.Errorf("third page = %v, want none", third)
	}
}

// TestSearchMessagesHits checks what a hit carries: the conversation, the
// author, the time and a snippet with the matches between the markers and the
// rest of the text untouched (it is escaped later, by the service).
func TestSearchMessagesHits(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	conv := f.groupWith(alice, bob)
	conv.Name = "Launch crew"
	if err := f.s.SetConversationName(f.ctx, conv.ID, conv.Name); err != nil {
		t.Fatal(err)
	}
	f.send(conv, bob, "see <script>alert(1)</script> and the Report today")

	hits, err := f.s.SearchMessages(f.ctx, MessageSearchQuery{ReaderID: alice.ID, Terms: words("report"), Limit: 10})
	if err != nil || len(hits) != 1 {
		t.Fatalf("SearchMessages = %v, %v", hits, err)
	}
	h := hits[0]
	if h.ConversationID != conv.ID || h.ConversationKind != ConversationGroup || h.ConversationName != "Launch crew" ||
		h.AuthorID != bob.ID || h.AuthorUsername != bob.Username || h.CreatedAt.IsZero() {
		t.Errorf("hit = %+v", h)
	}
	want := "see <script>alert(1)</script> and the " + SearchMarkStart + "Report" + SearchMarkEnd + " today"
	if h.Snippet != want {
		t.Errorf("snippet = %q, want %q", h.Snippet, want)
	}

	// A phrase is marked as a whole on both databases.
	hits, err = f.s.SearchMessages(f.ctx, MessageSearchQuery{ReaderID: alice.ID, Terms: phrase("the report"), Limit: 10})
	if err != nil || len(hits) != 1 {
		t.Fatalf("SearchMessages = %v, %v", hits, err)
	}
	if want := "and " + SearchMarkStart + "the Report" + SearchMarkEnd + " today"; !strings.HasSuffix(hits[0].Snippet, want) {
		t.Errorf("phrase snippet = %q, want it to end with %q", hits[0].Snippet, want)
	}
}

// TestMessageSearchFailures checks the route of every failure.
func TestMessageSearchFailures(t *testing.T) {
	f := newFix(t)
	canceled, cancel := context.WithCancel(f.ctx)
	cancel()
	searchRoute := "Store.SearchMessages.QueryFailed"
	if onPostgres() {
		searchRoute = "Store.SearchMessages->Store.searchMessagesPostgres.QueryFailed"
	}
	tests := []struct {
		name  string
		route string
		op    string
		do    func() error
	}{
		{"reindex", "Store.ReindexMessage.QueryFailed", "reindexMessage", func() error {
			return f.s.ReindexMessage(canceled, 1)
		}},
		{"rebuild", "Store.RebuildMessageSearchIndex.QueryFailed", "rebuildMessageSearchIndex", func() error {
			return f.s.RebuildMessageSearchIndex(canceled)
		}},
		{"index rows", "Store.MessageIndexRows.QueryFailed", "messageIndexRows", func() error {
			_, err := f.s.MessageIndexRows(canceled)
			return err
		}},
		{"search", searchRoute, "searchMessages", func() error {
			_, err := f.s.SearchMessages(canceled, MessageSearchQuery{ReaderID: 1, Terms: words("x"), Limit: 5})
			return err
		}},
		{"create", "Store.CreateMessage.QueryFailed", "createMessage", func() error {
			return f.s.CreateMessage(canceled, &Message{ConversationID: 1, AuthorID: 1, Body: "x"})
		}},
		{"edit", "Store.UpdateMessageBody.QueryFailed", "updateMessageBody", func() error {
			return f.s.UpdateMessageBody(canceled, 1, "x", f.clk.Now())
		}},
		{"delete", "Store.MarkMessageDeleted.QueryFailed", "markMessageDeleted", func() error {
			return f.s.MarkMessageDeleted(canceled, 1, f.clk.Now())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.do(), TagQueryFailed, tt.route, tt.op)
		})
	}

	t.Run("a failure of the index while editing", func(t *testing.T) {
		// The edit itself works, the index does not: the table is gone.
		alice, bob := f.user(), f.user()
		conv := f.groupWith(alice, bob)
		m := f.send(conv, alice, "something")
		table, _ := messageSearchIndex(f.s.dialect)
		f.exec("DROP TABLE " + table)
		err := f.s.UpdateMessageBody(f.ctx, m.ID, "changed", f.clk.Now())
		requireTagged(t, err, TagQueryFailed, "Store.UpdateMessageBody->Store.ReindexMessage.QueryFailed", "reindexMessage")
	})
}

// TestMigrationMessagesSearch checks migration 0006: a database of step 20.2,
// which has messages but no index of them, gets the index filled with the
// messages that are not deleted and have text, and a second run changes
// nothing. The old state is made by dropping the index and forgetting that the
// migration ran.
func TestMigrationMessagesSearch(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	conv := f.groupWith(alice, bob)
	keep := f.send(conv, alice, "Résumé of the plan")
	f.send(conv, bob, "")
	gone := f.send(conv, bob, "removed words")
	if err := f.s.MarkMessageDeleted(f.ctx, gone.ID, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	table, _ := messageSearchIndex(f.s.dialect)
	f.exec("DROP TABLE " + table)
	f.exec("DELETE FROM schema_migrations WHERE id = ?", "0006_messages_search")

	for run := 1; run <= 2; run++ {
		if err := f.s.migrate(f.ctx, allModels(), rawMigrations(f.s.dialect)); err != nil {
			t.Fatalf("migrate (run %d): %+v", run, err)
		}
		if got := f.messageIndex(); len(got) != 1 || got[keep.ID] != "Résumé of the plan" {
			t.Errorf("run %d: index = %v", run, got)
		}
		if ids := f.searchIDs(alice, MessageSearchQuery{Terms: words("resume")}); !slices.Equal(ids, []uint64{keep.ID}) {
			t.Errorf("run %d: the search finds %v, want [%d]", run, ids, keep.ID)
		}
		st, err := f.s.Stats(f.ctx)
		if err != nil || st.UnindexedMessages != 0 || st.StrayMessageIndexRows != 0 {
			t.Errorf("run %d: Stats = %+v, %v", run, st, err)
		}
	}
}

// TestMessageSearchIndexPerDialect checks the table and the column of each
// dialect and that the text expression drops the markers.
func TestMessageSearchIndexPerDialect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d               dialect
		table, idColumn string
		function        string
	}{
		{dialectSQLite, "messages_fts", "rowid", "char(57344)"},
		{dialectPostgres, "messages_search", "message_id", "chr(57344)"},
	}
	for _, tt := range tests {
		t.Run(tt.d.String(), func(t *testing.T) {
			t.Parallel()
			table, idColumn := messageSearchIndex(tt.d)
			if table != tt.table || idColumn != tt.idColumn {
				t.Errorf("messageSearchIndex = %s, %s", table, idColumn)
			}
			if text := messageIndexText(tt.d); !strings.Contains(text, tt.function) || !strings.Contains(text, "m.body") {
				t.Errorf("messageIndexText = %s", text)
			}
		})
	}
}
