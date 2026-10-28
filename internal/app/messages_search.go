package app

import (
	"context"
	"html/template"
	"time"

	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// messageSearchPageSize is the number of results on a page of the search of
// private messages (SPEC §7.11).
const messageSearchPageSize = 30

// The problems of the query of the search of private messages, shown under
// the field FieldQuery.
const (
	// messageSearchNoWords is the answer to a query with no word to look for:
	// empty, only punctuation, or only filters.
	messageSearchNoWords = "Type a word to search the messages."
	// messageSearchNoFilters is the answer to a query that has words and also
	// a filter, which the messages do not have.
	messageSearchNoFilters = "Filters such as #tag, room: and from: do not work in messages. Type words, or put the text in quotes."
)

// MessageSearchResult is a private message found by a search.
type MessageSearchResult struct {
	// MessageID is the ID of the message.
	MessageID uint64
	// ConversationID is the conversation of the message.
	ConversationID uint64
	// ConversationKind says whether the conversation is direct or a group.
	ConversationKind ConversationKind
	// ConversationTitle is the title of the conversation as the searching
	// user sees it: the name of a group, else the display names of the other
	// members.
	ConversationTitle string
	// Author is the author of the message.
	Author UserRef
	// CreatedAt is when the message was sent.
	CreatedAt time.Time
	// Snippet is the matching text, escaped, with the matches in <mark>; it
	// holds no other HTML.
	Snippet template.HTML
	// Segments are Snippet as plain text in pieces, for a client that shows
	// no HTML (see SearchResult.Segments).
	Segments []SnippetSegment
}

// MessageSearchPage is a page of the results of a search of private messages.
type MessageSearchPage struct {
	// Query is the text that was searched, as typed.
	Query string
	// Results are the results of the page, newest first.
	Results []MessageSearchResult
	// HasMore is true when older results follow this page.
	HasMore bool
	// NextBefore is the before that asks for the next, older page: the ID of
	// the oldest message of this page. It is 0 when HasMore is false.
	NextBefore uint64
}

// Search finds the private messages that match the query in the conversations
// the actor is a member of now, newest first, 30 a page (SPEC §7.11, D56).
// Admins are no exception, and neither is a member who left: a conversation
// the actor is no member of has no results. The messages from before the
// actor joined a group are searched, as the conversation shows them. before 0
// starts from the newest message; another value returns the messages older
// than that ID, as the NextBefore of the page before it.
//
// The query is parsed by ParseQuery, so words and phrases work as in SPEC
// §7.8 (a word matches as a prefix, a phrase in double quotes exactly), but
// the filters do not: a query with no word, which includes one made of
// filters only, fails with TagInvalidInput and the message "Type a word to
// search the messages."; a filter next to words fails with TagInvalidInput and
// a message that says filters do not work here (quoted, the same text is
// plain words). The message is under FieldQuery in the metadata "fields". A
// malformed date fails as ParseQuery says. An actor whose account is disabled
// or missing fails with TagUnauthenticated.
func (m *Messages) Search(ctx context.Context, actor Actor, query string, before uint64) (MessageSearchPage, error) {
	if _, err := checkActive(ctx, m.st, actor); err != nil {
		return MessageSearchPage{}, err
	}
	parsed, err := ParseQuery(query)
	if err != nil {
		return MessageSearchPage{}, err
	}
	if !parsed.HasWords() {
		return MessageSearchPage{}, errors.New(TagInvalidInput, "the query has no word",
			"fields", map[string]string{FieldQuery: messageSearchNoWords})
	}
	if len(parsed.Tags) > 0 || parsed.Room != "" || parsed.From != "" || !parsed.After.IsZero() ||
		!parsed.Before.IsZero() || parsed.PostsOnly || parsed.CommentsOnly {
		return MessageSearchPage{}, errors.New(TagInvalidInput, "the query has a filter",
			"fields", map[string]string{FieldQuery: messageSearchNoFilters})
	}
	terms := make([]store.SearchTerm, len(parsed.Terms))
	for i, t := range parsed.Terms {
		terms[i] = store.SearchTerm{Text: t.Text, Phrase: t.Phrase}
	}
	// One more than a page tells whether an older page exists.
	hits, err := m.st.SearchMessages(ctx, store.MessageSearchQuery{
		ReaderID: actor.UserID, Terms: terms, Before: before, Limit: messageSearchPageSize + 1,
	})
	if err != nil {
		return MessageSearchPage{}, err
	}
	out := MessageSearchPage{Query: query, Results: []MessageSearchResult{}}
	if len(hits) > messageSearchPageSize {
		hits = hits[:messageSearchPageSize]
		out.HasMore = true
	}
	var convIDs []uint64
	seen := map[uint64]bool{}
	for _, h := range hits {
		if !seen[h.ConversationID] {
			seen[h.ConversationID] = true
			convIDs = append(convIDs, h.ConversationID)
		}
	}
	members, err := m.st.ConversationMembers(ctx, convIDs, false)
	if err != nil {
		return MessageSearchPage{}, err
	}
	for _, h := range hits {
		conv := store.Conversation{ID: h.ConversationID, Kind: h.ConversationKind, Name: h.ConversationName}
		out.Results = append(out.Results, MessageSearchResult{
			MessageID:         h.MessageID,
			ConversationID:    h.ConversationID,
			ConversationKind:  h.ConversationKind,
			ConversationTitle: conversationTitle(conv, actor.UserID, members[h.ConversationID]),
			Author:            UserRef{ID: h.AuthorID, Username: h.AuthorUsername, DisplayName: h.AuthorDisplayName, AvatarKey: h.AuthorAvatarKey},
			CreatedAt:         h.CreatedAt,
			Snippet:           snippetHTML(h.Snippet),
			Segments:          snippetSegments(h.Snippet),
		})
	}
	if out.HasMore && len(out.Results) > 0 {
		out.NextBefore = out.Results[len(out.Results)-1].MessageID
	}
	return out, nil
}
