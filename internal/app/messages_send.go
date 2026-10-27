package app

import (
	"context"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// Messages of the fields of a message.
const (
	messageWriteSomething = "Write something."
	messageBodyTooLong    = "Use at most 4,000 characters."
	messageReplyInvalid   = "Choose a message of this conversation to reply to."
	messageCannotSend     = "This person is disabled, so you cannot send them messages."
)

// Send writes a message in a conversation as the actor and marks the
// conversation read up to it. The body is trimmed, at most 4,000 characters;
// it may be empty when there are uploads. in.ReplyToID must be a message of
// the same conversation that is not deleted. in.Uploads become the attachments
// (the limits are those of posts); when Send fails for any reason their files
// are removed from disk. It fails with TagNotFound unless the actor is a
// member, and with TagInvalidInput, with all the problems in the metadata
// "fields", for input that breaks a rule and in a direct conversation whose
// other user is disabled (D32). The conversation hears message.created, and
// every member conversation.changed.
func (m *Messages) Send(ctx context.Context, actor Actor, convID uint64, in MessageInput) (Message, error) {
	var created store.Message
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		conv, _, err := loadConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		rows, err := tx.ConversationMembers(ctx, []uint64{convID}, false)
		if err != nil {
			return err
		}
		body, fields := checkMessageBody(in.Body, len(in.Uploads))
		if conv.Kind == ConversationDirect {
			for _, r := range rows[convID] {
				if r.UserID != actor.UserID && r.DisabledAt != nil {
					addOnce(fields, FieldBody, messageCannotSend)
				}
			}
		}
		if in.ReplyToID != 0 {
			target, err := tx.MessageByID(ctx, in.ReplyToID)
			if err != nil {
				if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
					return err
				}
				fields[FieldReplyTo] = messageReplyInvalid
			} else if target.ConversationID != convID || target.DeletedAt != nil {
				fields[FieldReplyTo] = messageReplyInvalid
			}
		}
		if err := checkUploadFiles(ctx, tx, m.files, m.cfg.MaxAttachments, fields, in.Uploads, 0); err != nil {
			return err
		}
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the message has mistakes", "fields", fields)
		}
		created = store.Message{ConversationID: convID, AuthorID: actor.UserID, Body: body}
		if in.ReplyToID != 0 {
			reply := in.ReplyToID
			created.ReplyToID = &reply
		}
		if err := tx.CreateMessage(ctx, &created); err != nil {
			return err
		}
		if err := tx.CreateMessageAttachments(ctx, messageAttachmentRows(created.ID, actor.UserID, in.Uploads)); err != nil {
			return err
		}
		if _, err := tx.AdvanceLastRead(ctx, convID, actor.UserID, created.ID); err != nil {
			return err
		}
		events = append([]live.Event{messageEvent(live.KindMessageCreated, convID, created.ID)},
			changedEvents(convID, userIDsOf(rows[convID])...)...)
		return nil
	})
	if err != nil {
		m.cleaner.discard(ctx, m.st, in.Uploads)
		return Message{}, err
	}
	publishAll(m.pub, events)
	return m.viewOne(ctx, actor, created.ID)
}

// Edit changes the body of a message and sets its EditedAt. Only the author
// may: another member gets TagForbidden. A message that does not exist, one of
// a conversation the actor is no member of and a deleted message fail with
// TagNotFound. The body is trimmed, at most 4,000 characters, and empty only
// when the message has attachments; otherwise Edit fails with TagInvalidInput
// with the message under FieldBody. It does nothing, and publishes nothing,
// when the body stays. The conversation hears message.updated and every member
// conversation.changed.
func (m *Messages) Edit(ctx context.Context, actor Actor, msgID uint64, body string) (Message, error) {
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		msg, err := tx.MessageByID(ctx, msgID)
		if err != nil {
			return err
		}
		if _, _, err := loadConversation(ctx, tx, actor, msg.ConversationID); err != nil {
			return err
		}
		if msg.DeletedAt != nil {
			return errors.New(TagNotFound, "the message is deleted", "reason", "deleted", "messageID", msgID)
		}
		if msg.AuthorID != actor.UserID {
			return errors.New(TagForbidden, "only the author edits a message",
				"userID", actor.UserID, "messageID", msgID)
		}
		attached, err := tx.MessageAttachmentsOf(ctx, []uint64{msgID})
		if err != nil {
			return err
		}
		clean, fields := checkMessageBody(body, len(attached[msgID]))
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the message has mistakes", "fields", fields)
		}
		if clean == msg.Body {
			return nil
		}
		if err := tx.UpdateMessageBody(ctx, msgID, clean, m.clk.Now()); err != nil {
			return err
		}
		rows, err := tx.ConversationMembers(ctx, []uint64{msg.ConversationID}, false)
		if err != nil {
			return err
		}
		events = append([]live.Event{messageEvent(live.KindMessageUpdated, msg.ConversationID, msgID)},
			changedEvents(msg.ConversationID, userIDsOf(rows[msg.ConversationID])...)...)
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	publishAll(m.pub, events)
	return m.viewOne(ctx, actor, msgID)
}

// Delete deletes a message: it stays in its place as "message deleted" with
// no body, attachments or reactions (D32), and the files of its attachments
// are removed from disk after the change is committed. Only the author may:
// another member gets TagForbidden. A message that does not exist, one of a
// conversation the actor is no member of and one that is deleted already fail
// with TagNotFound. The conversation hears message.deleted and every member
// conversation.changed.
func (m *Messages) Delete(ctx context.Context, actor Actor, msgID uint64) error {
	var keys []string
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		keys = nil
		events = nil
		msg, err := tx.MessageByID(ctx, msgID)
		if err != nil {
			return err
		}
		if _, _, err := loadConversation(ctx, tx, actor, msg.ConversationID); err != nil {
			return err
		}
		if msg.DeletedAt != nil {
			return errors.New(TagNotFound, "the message is deleted", "reason", "deleted", "messageID", msgID)
		}
		if msg.AuthorID != actor.UserID {
			return errors.New(TagForbidden, "only the author deletes a message",
				"userID", actor.UserID, "messageID", msgID)
		}
		attached, err := tx.MessageAttachmentsOf(ctx, []uint64{msgID})
		if err != nil {
			return err
		}
		for _, a := range attached[msgID] {
			keys = append(keys, a.StorageKey)
		}
		if err := tx.DeleteMessageExtras(ctx, msgID); err != nil {
			return err
		}
		if err := tx.MarkMessageDeleted(ctx, msgID, m.clk.Now()); err != nil {
			return err
		}
		rows, err := tx.ConversationMembers(ctx, []uint64{msg.ConversationID}, false)
		if err != nil {
			return err
		}
		events = append([]live.Event{messageEvent(live.KindMessageDeleted, msg.ConversationID, msgID)},
			changedEvents(msg.ConversationID, userIDsOf(rows[msg.ConversationID])...)...)
		return nil
	})
	if err != nil {
		return err
	}
	publishAll(m.pub, events)
	m.cleaner.remove(ctx, keys)
	return nil
}

// React puts the emoji on the message as the actor, or takes it off when the
// actor put it there already, and returns the reactions of the message as the
// actor sees them. A message that does not exist, one of a conversation the
// actor is no member of and a deleted message fail with TagNotFound. An emoji
// that is not one fails with TagInvalidInput, with the message under
// FieldEmoji (SPEC §7.4). The conversation hears message.reaction.
func (m *Messages) React(ctx context.Context, actor Actor, msgID uint64, emoji string) (Reactions, error) {
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		msg, err := tx.MessageByID(ctx, msgID)
		if err != nil {
			return err
		}
		if _, _, err := loadConversation(ctx, tx, actor, msg.ConversationID); err != nil {
			return err
		}
		if msg.DeletedAt != nil {
			return errors.New(TagNotFound, "the message is deleted", "reason", "deleted", "messageID", msgID)
		}
		clean, ok := cleanEmoji(emoji)
		if !ok {
			return errors.New(TagInvalidInput, "the reaction is not an emoji",
				"fields", map[string]string{FieldEmoji: messageEmojiInvalid})
		}
		if _, err := tx.ToggleMessageReaction(ctx, msgID, actor.UserID, clean); err != nil {
			return err
		}
		events = []live.Event{messageEvent(live.KindMessageReaction, msg.ConversationID, msgID)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	publishAll(m.pub, events)
	rows, err := m.st.ReactionsOfMessages(ctx, []uint64{msgID})
	if err != nil {
		return nil, err
	}
	return Reactions(reactionCounts(messageReactionRows(rows[msgID]), actor.UserID)), nil
}

// SetReaction puts the emoji on the message as the actor (present true) or
// takes it off (present false) and returns the reactions of the message as the
// actor sees them. Unlike React it is idempotent: putting an emoji that is
// there, or taking off one that is not, changes nothing, and nothing is
// published then. The permissions and the errors are those of React.
func (m *Messages) SetReaction(ctx context.Context, actor Actor, msgID uint64, emoji string, present bool) (Reactions, error) {
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		msg, err := tx.MessageByID(ctx, msgID)
		if err != nil {
			return err
		}
		if _, _, err := loadConversation(ctx, tx, actor, msg.ConversationID); err != nil {
			return err
		}
		if msg.DeletedAt != nil {
			return errors.New(TagNotFound, "the message is deleted", "reason", "deleted", "messageID", msgID)
		}
		clean, ok := cleanEmoji(emoji)
		if !ok {
			return errors.New(TagInvalidInput, "the reaction is not an emoji",
				"fields", map[string]string{FieldEmoji: messageEmojiInvalid})
		}
		var changed bool
		if present {
			changed, err = tx.PutMessageReaction(ctx, msgID, actor.UserID, clean)
		} else {
			changed, err = tx.DeleteMessageReaction(ctx, msgID, actor.UserID, clean)
		}
		if err != nil {
			return err
		}
		if changed {
			events = []live.Event{messageEvent(live.KindMessageReaction, msg.ConversationID, msgID)}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	publishAll(m.pub, events)
	rows, err := m.st.ReactionsOfMessages(ctx, []uint64{msgID})
	if err != nil {
		return nil, err
	}
	return Reactions(reactionCounts(messageReactionRows(rows[msgID]), actor.UserID)), nil
}

// History returns a page of the messages of a conversation, oldest first
// within the page. before 0 starts from the newest message; another value
// returns the messages older than that ID. limit is the page size: 50 when it
// is 0 or less, at most 100. Deleted messages are included, as deleted. It
// fails with TagNotFound unless the actor is a member.
func (m *Messages) History(ctx context.Context, actor Actor, convID, before uint64, limit int) (HistoryPage, error) {
	if limit <= 0 {
		limit = historyDefaultLimit
	}
	limit = min(limit, historyMaxLimit)
	if _, _, err := loadConversation(ctx, m.st, actor, convID); err != nil {
		return HistoryPage{}, err
	}
	// One more than a page tells whether an older page exists.
	rows, err := m.st.MessagePage(ctx, convID, before, limit+1)
	if err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{HasOlder: len(rows) > limit}
	if page.HasOlder {
		rows = rows[:limit]
	}
	// The query gives the newest first; the page is read oldest first.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if page.Messages, err = m.messageViews(ctx, actor, rows); err != nil {
		return HistoryPage{}, err
	}
	if page.HasOlder {
		page.NextBefore = rows[0].ID
	}
	return page, nil
}

// Attachment returns an attachment of a message and its file for download.
// Only the members of the conversation may: a missing attachment, one of a
// deleted message and one of a conversation the actor is no member of fail
// with TagNotFound. The caller closes the file.
func (m *Messages) Attachment(ctx context.Context, actor Actor, attachmentID uint64) (AttachmentView, io.ReadSeekCloser, error) {
	att, err := m.st.MessageAttachmentByID(ctx, attachmentID)
	if err != nil {
		return AttachmentView{}, nil, err
	}
	msg, err := m.st.MessageByID(ctx, att.MessageID)
	if err != nil {
		return AttachmentView{}, nil, err
	}
	if _, _, err := loadConversation(ctx, m.st, actor, msg.ConversationID); err != nil {
		return AttachmentView{}, nil, err
	}
	if msg.DeletedAt != nil {
		return AttachmentView{}, nil, errors.New(TagNotFound, "the message is deleted",
			"reason", "deleted", "messageID", msg.ID)
	}
	f, err := m.files.Open(att.StorageKey)
	if err != nil {
		return AttachmentView{}, nil, err
	}
	return messageAttachmentView(att), f, nil
}

// Message returns one message as the actor sees it, deleted or not. It fails
// with TagNotFound for a message that does not exist and for one of a
// conversation the actor is no member of (admins included), or left.
func (m *Messages) Message(ctx context.Context, actor Actor, msgID uint64) (Message, error) {
	msg, err := m.st.MessageByID(ctx, msgID)
	if err != nil {
		return Message{}, err
	}
	if _, _, err := loadConversation(ctx, m.st, actor, msg.ConversationID); err != nil {
		return Message{}, err
	}
	return m.viewOne(ctx, actor, msgID)
}

// viewOne returns the view of one message for the actor.
func (m *Messages) viewOne(ctx context.Context, actor Actor, msgID uint64) (Message, error) {
	rows, err := m.st.MessageRowsByIDs(ctx, []uint64{msgID})
	if err != nil {
		return Message{}, err
	}
	row, ok := rows[msgID]
	if !ok {
		return Message{}, errors.New(TagNotFound, "no such message", "reason", "missing", "messageID", msgID)
	}
	views, err := m.messageViews(ctx, actor, []store.MessageRow{row})
	if err != nil {
		return Message{}, err
	}
	return views[0], nil
}

// messageViews makes the views of the messages for the actor, with a fixed
// number of queries: the quoted messages, the attachments and the reactions.
func (m *Messages) messageViews(ctx context.Context, actor Actor, rows []store.MessageRow) ([]Message, error) {
	if len(rows) == 0 {
		return []Message{}, nil
	}
	ids := make([]uint64, 0, len(rows))
	var replyIDs []uint64
	for _, r := range rows {
		ids = append(ids, r.ID)
		if r.ReplyToID != nil {
			replyIDs = append(replyIDs, *r.ReplyToID)
		}
	}
	quoted, err := m.st.MessageRowsByIDs(ctx, replyIDs)
	if err != nil {
		return nil, err
	}
	attachments, err := m.st.MessageAttachmentsOf(ctx, append(append([]uint64{}, ids...), replyIDs...))
	if err != nil {
		return nil, err
	}
	reactions, err := m.st.ReactionsOfMessages(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make([]Message, len(rows))
	for i, r := range rows {
		deleted := r.DeletedAt != nil
		v := Message{
			ID:             r.ID,
			ConversationID: r.ConversationID,
			Author:         UserRef{ID: r.AuthorID, Username: r.AuthorUsername, DisplayName: r.AuthorDisplayName},
			Body:           r.Body,
			CreatedAt:      r.CreatedAt,
			EditedAt:       r.EditedAt,
			Deleted:        deleted,
			Attachments:    []AttachmentView{},
			Reactions:      []ReactionCount{},
			CanEdit:        !deleted && r.AuthorID == actor.UserID,
			CanDelete:      !deleted && r.AuthorID == actor.UserID,
			CanReact:       !deleted,
		}
		if !deleted {
			v.Attachments = messageAttachmentViews(attachments[r.ID])
			v.Reactions = reactionCounts(messageReactionRows(reactions[r.ID]), actor.UserID)
		}
		if r.ReplyToID != nil {
			if q, ok := quoted[*r.ReplyToID]; ok {
				v.ReplyTo = &MessageQuote{
					ID:      q.ID,
					Author:  UserRef{ID: q.AuthorID, Username: q.AuthorUsername, DisplayName: q.AuthorDisplayName},
					Preview: previewOf(q.DeletedAt != nil, q.Body, attachmentNames(attachments[q.ID])),
					Deleted: q.DeletedAt != nil,
				}
			}
		}
		views[i] = v
	}
	return views, nil
}

// checkMessageBody trims the body of a message and returns it with the
// problems as field name to message. attachments is the number of attachments
// the message has when the call is done; a message needs a body or one.
func checkMessageBody(body string, attachments int) (string, map[string]string) {
	fields := map[string]string{}
	clean := strings.TrimSpace(body)
	switch {
	case !utf8.ValidString(clean):
		fields[FieldBody] = "Use valid text."
	case clean == "" && attachments == 0:
		fields[FieldBody] = messageWriteSomething
	case utf8.RuneCountInString(clean) > messageBodyMaxLength:
		fields[FieldBody] = messageBodyTooLong
	}
	return clean, fields
}

// messageAttachmentRows makes the rows of the uploads of one message.
func messageAttachmentRows(msgID, uploaderID uint64, uploads []Upload) []store.MessageAttachment {
	rows := make([]store.MessageAttachment, len(uploads))
	for i, a := range attachmentRows(0, uploaderID, uploads) {
		rows[i] = store.MessageAttachment{
			MessageID:   msgID,
			UploaderID:  a.UploaderID,
			FileName:    a.FileName,
			ContentType: a.ContentType,
			Size:        a.Size,
			StorageKey:  a.StorageKey,
			Width:       a.Width,
			Height:      a.Height,
		}
	}
	return rows
}

// messageAttachmentView makes the view of an attachment row of a message.
func messageAttachmentView(a store.MessageAttachment) AttachmentView {
	return attachmentView(store.Attachment{
		ID:          a.ID,
		CreatedAt:   a.CreatedAt,
		FileName:    a.FileName,
		ContentType: a.ContentType,
		Size:        a.Size,
		Width:       a.Width,
		Height:      a.Height,
	})
}

// messageAttachmentViews makes the views of rows; never nil.
func messageAttachmentViews(rows []store.MessageAttachment) []AttachmentView {
	views := make([]AttachmentView, len(rows))
	for i, a := range rows {
		views[i] = messageAttachmentView(a)
	}
	return views
}

// attachmentNames returns the file names of the rows.
func attachmentNames(rows []store.MessageAttachment) []string {
	names := make([]string, len(rows))
	for i, a := range rows {
		names[i] = a.FileName
	}
	return names
}

// messageReactionRows makes the rows reactionCounts takes from the reactions
// of a message.
func messageReactionRows(rows []store.MessageReactionRow) []store.ReactionRow {
	out := make([]store.ReactionRow, len(rows))
	for i, r := range rows {
		out[i] = store.ReactionRow{ID: r.ID, PostID: r.MessageID, UserID: r.UserID, Emoji: r.Emoji, Username: r.Username, DisplayName: r.DisplayName}
	}
	return out
}
