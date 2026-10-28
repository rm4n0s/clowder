package api

import (
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/markdown"
)

// messageSummaryJSON is the newest message of a conversation in the list.
type messageSummaryJSON struct {
	ID        uint64      `json:"id"`
	Author    userRefJSON `json:"author"`
	Preview   string      `json:"preview"`
	Deleted   bool        `json:"deleted"`
	CreatedAt string      `json:"created_at"`
}

// conversationJSON is a conversation in the answers of the API.
type conversationJSON struct {
	ID           uint64              `json:"id"`
	Kind         string              `json:"kind"`
	Name         *string             `json:"name"`
	DisplayName  string              `json:"display_name"`
	Members      []userRefJSON       `json:"members"`
	LastMessage  *messageSummaryJSON `json:"last_message"`
	LastActivity string              `json:"last_activity"`
	Unread       bool                `json:"unread"`
	CanSend      bool                `json:"can_send"`
	SeenUpToID   *uint64             `json:"seen_up_to_id"`
}

// conversationOf makes the JSON of a conversation.
func conversationOf(c app.ConversationSummary) conversationJSON {
	out := conversationJSON{
		ID:           c.ID,
		Kind:         string(c.Kind),
		DisplayName:  c.DisplayName,
		Members:      userRefsOf(c.Members),
		LastActivity: rfc3339(c.LastActivity),
		Unread:       c.Unread,
		CanSend:      c.CanSend,
	}
	if c.Name != "" {
		name := c.Name
		out.Name = &name
	}
	if c.SeenUpToID != 0 {
		seen := c.SeenUpToID
		out.SeenUpToID = &seen
	}
	if last := c.LastMessage; last != nil {
		out.LastMessage = &messageSummaryJSON{
			ID: last.ID, Author: userRefOf(last.Author), Preview: last.Preview,
			Deleted: last.Deleted, CreatedAt: rfc3339(last.CreatedAt),
		}
	}
	return out
}

// conversationListResponse is the answer of GET /conversations.
type conversationListResponse struct {
	Conversations []conversationJSON `json:"conversations"`
}

// quoteJSON is the message that a message replies to.
type quoteJSON struct {
	ID      uint64      `json:"id"`
	Author  userRefJSON `json:"author"`
	Excerpt string      `json:"excerpt"`
	Deleted bool        `json:"deleted"`
}

// messageJSON is a private message in the answers of the API.
type messageJSON struct {
	ID             uint64           `json:"id"`
	ConversationID uint64           `json:"conversation_id"`
	Author         userRefJSON      `json:"author"`
	Body           *string          `json:"body"`
	HTML           *string          `json:"html"`
	ReplyTo        *quoteJSON       `json:"reply_to"`
	Attachments    []attachmentJSON `json:"attachments"`
	Reactions      []reactionJSON   `json:"reactions"`
	CreatedAt      string           `json:"created_at"`
	EditedAt       *string          `json:"edited_at"`
	Deleted        bool             `json:"deleted"`
	CanEdit        bool             `json:"can_edit"`
	CanDelete      bool             `json:"can_delete"`
}

// messageAttachmentURL is the path a client downloads the file of a message
// from.
func messageAttachmentURL(id uint64) string {
	return Prefix + "/messages/attachments/" + strconv.FormatUint(id, 10)
}

// messageOf makes the JSON of a message, with its text rendered as the pages
// do (SPEC D33). A deleted message keeps its author and time and has no body,
// html, edit time, files or reactions, as the pages show it.
func (s *Server) messageOf(m app.Message) (messageJSON, error) {
	out := messageJSON{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		Author:         userRefOf(m.Author),
		Attachments:    make([]attachmentJSON, len(m.Attachments)),
		Reactions:      reactionsOf(m.Reactions),
		CreatedAt:      rfc3339(m.CreatedAt),
		Deleted:        m.Deleted,
		CanEdit:        m.CanEdit,
		CanDelete:      m.CanDelete,
	}
	for i, a := range m.Attachments {
		out.Attachments[i] = attachmentOf(a)
		out.Attachments[i].URL = messageAttachmentURL(a.ID)
	}
	if m.ReplyTo != nil {
		q := m.ReplyTo
		out.ReplyTo = &quoteJSON{ID: q.ID, Author: userRefOf(q.Author), Excerpt: q.Preview, Deleted: q.Deleted}
	}
	if m.Deleted {
		return out, nil
	}
	html, err := s.md.Render(markdown.FormatText, m.Body)
	if err != nil {
		return messageJSON{}, err
	}
	body, rendered := m.Body, string(html)
	out.Body, out.HTML = &body, &rendered
	if m.EditedAt != nil {
		edited := rfc3339(*m.EditedAt)
		out.EditedAt = &edited
	}
	return out, nil
}

// historyResponse is the answer of GET /conversations/{id}/messages.
type historyResponse struct {
	Messages   []messageJSON `json:"messages"`
	NextBefore *uint64       `json:"next_before"`
}

// conversationRefJSON names the conversation of a message that a search found.
type conversationRefJSON struct {
	ID    uint64 `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// messageSearchResultJSON is a private message that a search found.
type messageSearchResultJSON struct {
	MessageID    uint64               `json:"message_id"`
	Conversation conversationRefJSON  `json:"conversation"`
	Author       userRefJSON          `json:"author"`
	CreatedAt    string               `json:"created_at"`
	Snippet      []snippetSegmentJSON `json:"snippet"`
}

// messageSearchResultOf makes the JSON of a message that a search found.
func messageSearchResultOf(r app.MessageSearchResult) messageSearchResultJSON {
	out := messageSearchResultJSON{
		MessageID:    r.MessageID,
		Conversation: conversationRefJSON{ID: r.ConversationID, Title: r.ConversationTitle, Kind: string(r.ConversationKind)},
		Author:       userRefOf(r.Author),
		CreatedAt:    rfc3339(r.CreatedAt),
		Snippet:      make([]snippetSegmentJSON, len(r.Segments)),
	}
	for i, seg := range r.Segments {
		out.Snippet[i] = snippetSegmentJSON{Text: seg.Text, Match: seg.Match}
	}
	return out
}

// messageSearchResponse is the answer of GET /messages/search.
type messageSearchResponse struct {
	Results    []messageSearchResultJSON `json:"results"`
	NextBefore *uint64                   `json:"next_before"`
}
