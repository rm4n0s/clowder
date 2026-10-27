package store

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// DirectKeyOf makes the DirectKey of the direct conversation between two
// users: the smaller ID first, a colon, the larger ("3:7").
func DirectKeyOf(a, b uint64) string {
	if a > b {
		a, b = b, a
	}
	return strconv.FormatUint(a, 10) + ":" + strconv.FormatUint(b, 10)
}

// ConversationMemberRow is a membership with the user it belongs to.
type ConversationMemberRow struct {
	// ID is the ID of the membership; a smaller ID joined earlier.
	ID uint64
	// ConversationID is the conversation.
	ConversationID uint64
	// UserID is the member.
	UserID uint64
	// AddedBy is the member who added the user; nil for the one who started
	// the conversation.
	AddedBy *uint64
	// LastReadMessageID is the last message the member has read; nil if none.
	LastReadMessageID *uint64
	// LeftAt is when the member left; nil while a member.
	LeftAt *time.Time
	// Username is the username of the member.
	Username string
	// DisplayName is the display name of the member.
	DisplayName string
	// AvatarKey is the avatar key of the member; empty for none.
	AvatarKey string
	// DisabledAt is when the user was disabled; nil for an active user.
	DisabledAt *time.Time
}

// MessageRow is a message with the user who wrote it.
type MessageRow struct {
	// ID is the ID of the message; a larger ID is a newer message.
	ID uint64
	// CreatedAt is when the message was sent.
	CreatedAt time.Time
	// ConversationID is the conversation of the message.
	ConversationID uint64
	// AuthorID is the author.
	AuthorID uint64
	// Body is the text; empty for a deleted message.
	Body string
	// ReplyToID is the message that this one quotes; nil for none.
	ReplyToID *uint64
	// EditedAt is when the author last edited the message; nil if never.
	EditedAt *time.Time
	// DeletedAt is when the author deleted the message; nil if never.
	DeletedAt *time.Time
	// AuthorUsername is the username of the author.
	AuthorUsername string
	// AuthorDisplayName is the display name of the author.
	AuthorDisplayName string
	// AuthorAvatarKey is the avatar key of the author; empty for none.
	AuthorAvatarKey string
}

// MessageReactionRow is one reaction on a message with the display name of the
// user who made it.
type MessageReactionRow struct {
	// ID is the ID of the reaction; a smaller ID is an older reaction.
	ID uint64
	// MessageID is the message reacted to.
	MessageID uint64
	// UserID is the user who reacted.
	UserID uint64
	// Emoji is the emoji of the reaction.
	Emoji string
	// Username is the username of the user who reacted.
	Username string
	// DisplayName is the display name of the user who reacted.
	DisplayName string
}

// ConversationListItem is a conversation of the list of a user, with
// everything the list shows.
type ConversationListItem struct {
	// Conversation is the conversation.
	Conversation Conversation
	// LastReadMessageID is the last message the user has read; 0 if none.
	LastReadMessageID uint64
	// Unread is true when the conversation has a message of someone else,
	// not deleted, after the last message the user has read.
	Unread bool
	// LastMessage is the newest message, deleted ones included; nil when the
	// conversation has none.
	LastMessage *MessageRow
	// LastAttachmentNames are the names of the files of the last message, in
	// the order they were attached.
	LastAttachmentNames []string
	// Members are the current members, in the order they joined.
	Members []ConversationMemberRow
}

const (
	messageRowColumns = `messages.id AS id, messages.created_at AS created_at, messages.conversation_id AS conversation_id,
messages.author_id AS author_id, messages.body AS body, messages.reply_to_id AS reply_to_id,
messages.edited_at AS edited_at, messages.deleted_at AS deleted_at,
authors.username AS author_username, authors.display_name AS author_display_name, authors.avatar_key AS author_avatar_key`
	memberRowColumns = `conversation_members.id AS id, conversation_members.conversation_id AS conversation_id,
conversation_members.user_id AS user_id, conversation_members.added_by AS added_by,
conversation_members.last_read_message_id AS last_read_message_id, conversation_members.left_at AS left_at,
users.username AS username, users.display_name AS display_name, users.avatar_key AS avatar_key, users.disabled_at AS disabled_at`
	// unreadMessageExists is true for a membership (conversation_members) of
	// a conversation that has a message of someone else, not deleted, after
	// the last message the member read.
	unreadMessageExists = `EXISTS (SELECT 1 FROM messages WHERE messages.conversation_id = conversation_members.conversation_id
AND messages.id > COALESCE(conversation_members.last_read_message_id, 0)
AND messages.author_id <> conversation_members.user_id AND messages.deleted_at IS NULL)`
)

// UsersByIDs returns the users with these IDs, in no particular order; an ID
// that is no user is left out.
func (s *Store) UsersByIDs(ctx context.Context, ids []uint64) ([]User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var users []User
	if err := s.db(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "usersByIDs")
	}
	return users, nil
}

// UsersByUsernames returns the users with these usernames, which the caller
// has already lower-cased, in no particular order; a username that is no user
// is left out.
func (s *Store) UsersByUsernames(ctx context.Context, usernames []string) ([]User, error) {
	if len(usernames) == 0 {
		return nil, nil
	}
	var users []User
	if err := s.db(ctx).Where("username IN ?", usernames).Find(&users).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "usersByUsernames")
	}
	return users, nil
}

// ConversationByID returns the conversation with the given ID. It fails with
// TagNotFound when there is none.
func (s *Store) ConversationByID(ctx context.Context, id uint64) (Conversation, error) {
	var c Conversation
	err := s.db(ctx).First(&c, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Conversation{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "conversationByID", "conversationID", id)
		default:
			return Conversation{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "conversationByID", "conversationID", id)
		}
	}
	return c, nil
}

// DirectConversation returns the direct conversation with the given
// DirectKey (see DirectKeyOf). It fails with TagNotFound when there is none.
func (s *Store) DirectConversation(ctx context.Context, directKey string) (Conversation, error) {
	var c Conversation
	err := s.db(ctx).Where("direct_key = ?", directKey).First(&c).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Conversation{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "directConversation", "directKey", directKey)
		default:
			return Conversation{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "directConversation", "directKey", directKey)
		}
	}
	return c, nil
}

// CreateConversation inserts the conversation and sets its ID and CreatedAt.
// It fails with TagConflict when a direct conversation with the same
// DirectKey exists.
func (s *Store) CreateConversation(ctx context.Context, c *Conversation) error {
	err := s.db(ctx).Create(c).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createConversation")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createConversation")
		}
	}
	return nil
}

// CreateConversationMembers inserts the memberships. No rows is a no-op. It
// fails with TagConflict when a user is already in the conversation.
func (s *Store) CreateConversationMembers(ctx context.Context, rows []ConversationMember) error {
	if len(rows) == 0 {
		return nil
	}
	err := s.db(ctx).Create(&rows).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createConversationMembers")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createConversationMembers")
		}
	}
	return nil
}

// ConversationMemberOf returns the membership of the user in the conversation,
// one that has left included. It fails with TagNotFound when there is none.
func (s *Store) ConversationMemberOf(ctx context.Context, conversationID, userID uint64) (ConversationMember, error) {
	var m ConversationMember
	err := s.db(ctx).Where("conversation_id = ? AND user_id = ?", conversationID, userID).First(&m).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return ConversationMember{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "conversationMemberOf", "conversationID", conversationID)
		default:
			return ConversationMember{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "conversationMemberOf", "conversationID", conversationID)
		}
	}
	return m, nil
}

// ConversationMembers returns the members of the conversations with the users
// they are, in the order they joined, with one query. Members who left are
// included only when withLeft is true. A conversation without members has no
// key in the map.
func (s *Store) ConversationMembers(ctx context.Context, conversationIDs []uint64, withLeft bool) (map[uint64][]ConversationMemberRow, error) {
	out := map[uint64][]ConversationMemberRow{}
	if len(conversationIDs) == 0 {
		return out, nil
	}
	q := s.db(ctx).Table("conversation_members").
		Select(memberRowColumns).
		Joins("JOIN users ON users.id = conversation_members.user_id").
		Where("conversation_members.conversation_id IN ?", conversationIDs)
	if !withLeft {
		q = q.Where("conversation_members.left_at IS NULL")
	}
	var rows []ConversationMemberRow
	if err := q.Order("conversation_members.id ASC").Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "conversationMembers")
	}
	for _, r := range rows {
		out[r.ConversationID] = append(out[r.ConversationID], r)
	}
	return out, nil
}

// ReaddConversationMember lets a member who left come back: LeftAt is cleared
// and AddedBy is set. The last read message stays.
func (s *Store) ReaddConversationMember(ctx context.Context, conversationID, userID, addedBy uint64) error {
	err := s.db(ctx).Model(&ConversationMember{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Updates(map[string]any{"left_at": nil, "added_by": addedBy}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "readdConversationMember", "conversationID", conversationID)
	}
	return nil
}

// MarkConversationMemberLeft sets LeftAt of the membership.
func (s *Store) MarkConversationMemberLeft(ctx context.Context, conversationID, userID uint64, at time.Time) error {
	err := s.db(ctx).Model(&ConversationMember{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Update("left_at", at.UTC()).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "markConversationMemberLeft", "conversationID", conversationID)
	}
	return nil
}

// SetConversationName sets the name of the conversation; empty for none.
func (s *Store) SetConversationName(ctx context.Context, id uint64, name string) error {
	err := s.db(ctx).Model(&Conversation{}).Where("id = ?", id).Update("name", name).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "setConversationName", "conversationID", id)
	}
	return nil
}

// AdvanceLastRead moves the last read message of the membership forward to
// messageID. It never moves it back: it returns false, and changes nothing,
// when the member has read that message or a later one already.
func (s *Store) AdvanceLastRead(ctx context.Context, conversationID, userID, messageID uint64) (bool, error) {
	res := s.db(ctx).Model(&ConversationMember{}).
		Where("conversation_id = ? AND user_id = ? AND (last_read_message_id IS NULL OR last_read_message_id < ?)",
			conversationID, userID, messageID).
		Update("last_read_message_id", messageID)
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "advanceLastRead", "conversationID", conversationID)
	}
	return res.RowsAffected > 0, nil
}

// CountUnreadConversations returns the number of conversations of the user
// that have a message of someone else, not deleted, after the last message the
// user read. A conversation the user left is not counted. It is one query.
func (s *Store) CountUnreadConversations(ctx context.Context, userID uint64) (int, error) {
	var n int64
	err := s.db(ctx).Model(&ConversationMember{}).
		Where("user_id = ? AND left_at IS NULL", userID).
		Where(unreadMessageExists).
		Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countUnreadConversations", "userID", userID)
	}
	return int(n), nil
}

// conversationListRow is a conversation of the list, before the rest of the
// list is read.
type conversationListRow struct {
	ID                uint64
	CreatedAt         time.Time
	Kind              ConversationKind
	Name              string
	LastReadMessageID uint64
	LastMessageID     uint64
	Unread            bool
}

// ListConversations returns the conversations the user is a member of, with
// the last message, the unread mark and the members of each, in no particular
// order. It runs at most four queries, however many conversations there are.
func (s *Store) ListConversations(ctx context.Context, userID uint64) ([]ConversationListItem, error) {
	var rows []conversationListRow
	err := s.db(ctx).Table("conversation_members").
		Select(`conversations.id AS id, conversations.created_at AS created_at, conversations.kind AS kind,
conversations.name AS name,
COALESCE(conversation_members.last_read_message_id, 0) AS last_read_message_id,
COALESCE((SELECT MAX(messages.id) FROM messages WHERE messages.conversation_id = conversations.id), 0) AS last_message_id,
`+unreadMessageExists+` AS unread`).
		Joins("JOIN conversations ON conversations.id = conversation_members.conversation_id").
		Where("conversation_members.user_id = ? AND conversation_members.left_at IS NULL", userID).
		Order("conversations.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listConversations", "userID", userID)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uint64, len(rows))
	var lastIDs []uint64
	for i, r := range rows {
		ids[i] = r.ID
		if r.LastMessageID != 0 {
			lastIDs = append(lastIDs, r.LastMessageID)
		}
	}
	last, err := s.MessageRowsByIDs(ctx, lastIDs)
	if err != nil {
		return nil, err
	}
	attachments, err := s.MessageAttachmentsOf(ctx, lastIDs)
	if err != nil {
		return nil, err
	}
	members, err := s.ConversationMembers(ctx, ids, false)
	if err != nil {
		return nil, err
	}
	items := make([]ConversationListItem, len(rows))
	for i, r := range rows {
		item := ConversationListItem{
			Conversation:      Conversation{ID: r.ID, CreatedAt: r.CreatedAt, Kind: r.Kind, Name: r.Name},
			LastReadMessageID: r.LastReadMessageID,
			Unread:            r.Unread,
			Members:           members[r.ID],
		}
		if m, ok := last[r.LastMessageID]; ok {
			item.LastMessage = &m
			for _, a := range attachments[m.ID] {
				item.LastAttachmentNames = append(item.LastAttachmentNames, a.FileName)
			}
		}
		items[i] = item
	}
	return items, nil
}

// CreateMessage inserts the message and sets its ID and CreatedAt.
func (s *Store) CreateMessage(ctx context.Context, m *Message) error {
	if err := s.db(ctx).Create(m).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createMessage", "conversationID", m.ConversationID)
	}
	return nil
}

// MessageByID returns the message with the given ID. It fails with
// TagNotFound when there is none.
func (s *Store) MessageByID(ctx context.Context, id uint64) (Message, error) {
	var m Message
	err := s.db(ctx).First(&m, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Message{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "messageByID", "messageID", id)
		default:
			return Message{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "messageByID", "messageID", id)
		}
	}
	return m, nil
}

// UpdateMessageBody sets the body and EditedAt of the message.
func (s *Store) UpdateMessageBody(ctx context.Context, id uint64, body string, editedAt time.Time) error {
	err := s.db(ctx).Model(&Message{}).Where("id = ?", id).
		Updates(map[string]any{"body": body, "edited_at": editedAt.UTC()}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "updateMessageBody", "messageID", id)
	}
	return nil
}

// MarkMessageDeleted clears the body of the message and sets DeletedAt. The
// row stays, so that the conversation keeps its shape.
func (s *Store) MarkMessageDeleted(ctx context.Context, id uint64, at time.Time) error {
	err := s.db(ctx).Model(&Message{}).Where("id = ?", id).
		Updates(map[string]any{"body": "", "deleted_at": at.UTC()}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "markMessageDeleted", "messageID", id)
	}
	return nil
}

// DeleteMessageExtras deletes the attachment rows and the reactions of the
// message. The files on disk are not touched.
func (s *Store) DeleteMessageExtras(ctx context.Context, id uint64) error {
	if err := s.db(ctx).Where("message_id = ?", id).Delete(&MessageAttachment{}).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteMessageExtras", "messageID", id)
	}
	if err := s.db(ctx).Where("message_id = ?", id).Delete(&MessageReaction{}).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteMessageExtras", "messageID", id)
	}
	return nil
}

// MessagePage returns the messages of the conversation, newest first (by ID),
// at most limit of them. A beforeID other than 0 keeps only those with a
// smaller ID.
func (s *Store) MessagePage(ctx context.Context, conversationID, beforeID uint64, limit int) ([]MessageRow, error) {
	q := s.db(ctx).Table("messages").
		Select(messageRowColumns).
		Joins("JOIN users authors ON authors.id = messages.author_id").
		Where("messages.conversation_id = ?", conversationID)
	if beforeID != 0 {
		q = q.Where("messages.id < ?", beforeID)
	}
	var rows []MessageRow
	if err := q.Order("messages.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "messagePage", "conversationID", conversationID)
	}
	return rows, nil
}

// MessageRowsByIDs returns the messages with these IDs, by ID, with one
// query. An ID that is no message has no key in the map.
func (s *Store) MessageRowsByIDs(ctx context.Context, ids []uint64) (map[uint64]MessageRow, error) {
	out := map[uint64]MessageRow{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []MessageRow
	err := s.db(ctx).Table("messages").
		Select(messageRowColumns).
		Joins("JOIN users authors ON authors.id = messages.author_id").
		Where("messages.id IN ?", ids).
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "messageRowsByIDs")
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// CreateMessageAttachments inserts the rows and sets their IDs and CreatedAt.
// No rows is a no-op. It fails with TagConflict when a storage key is already
// used by another attachment.
func (s *Store) CreateMessageAttachments(ctx context.Context, rows []MessageAttachment) error {
	if len(rows) == 0 {
		return nil
	}
	err := s.db(ctx).Create(&rows).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createMessageAttachments")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createMessageAttachments")
		}
	}
	return nil
}

// MessageAttachmentByID returns the attachment of a message with the given
// ID. It fails with TagNotFound when there is none.
func (s *Store) MessageAttachmentByID(ctx context.Context, id uint64) (MessageAttachment, error) {
	var a MessageAttachment
	err := s.db(ctx).First(&a, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return MessageAttachment{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "messageAttachmentByID", "attachmentID", id)
		default:
			return MessageAttachment{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "messageAttachmentByID", "attachmentID", id)
		}
	}
	return a, nil
}

// MessageAttachmentsOf returns the attachments of each of the messages,
// oldest first (by ID), with one query. A message without attachments has no
// key in the map.
func (s *Store) MessageAttachmentsOf(ctx context.Context, messageIDs []uint64) (map[uint64][]MessageAttachment, error) {
	out := map[uint64][]MessageAttachment{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	var rows []MessageAttachment
	err := s.db(ctx).Where("message_id IN ?", slices.Clone(messageIDs)).Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "messageAttachmentsOf")
	}
	for _, a := range rows {
		out[a.MessageID] = append(out[a.MessageID], a)
	}
	return out, nil
}

// ToggleMessageReaction adds the reaction (message, user, emoji) when it is
// not there and removes it when it is. It returns true when the reaction was
// added and false when it was removed. It fails with TagConflict when a
// concurrent write added the same reaction first.
func (s *Store) ToggleMessageReaction(ctx context.Context, messageID, userID uint64, emoji string) (bool, error) {
	res := s.db(ctx).Where("message_id = ? AND user_id = ? AND emoji = ?", messageID, userID, emoji).Delete(&MessageReaction{})
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "toggleMessageReaction", "messageID", messageID)
	}
	if res.RowsAffected > 0 {
		return false, nil
	}
	r := MessageReaction{MessageID: messageID, UserID: userID, Emoji: emoji}
	if err := s.db(ctx).Create(&r).Error; err != nil {
		switch classify(err) {
		case failureConflict:
			return false, errors.NewErr(TagConflict, err).SetMetadata("op", "toggleMessageReaction", "messageID", messageID)
		default:
			return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "toggleMessageReaction", "messageID", messageID)
		}
	}
	return true, nil
}

// PutMessageReaction adds the reaction (message, user, emoji) when it is not
// there and leaves it when it is. It returns true when it was added. It fails
// with TagQueryFailed when the message or the user does not exist.
func (s *Store) PutMessageReaction(ctx context.Context, messageID, userID uint64, emoji string) (bool, error) {
	r := MessageReaction{MessageID: messageID, UserID: userID, Emoji: emoji}
	res := s.db(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "putMessageReaction", "messageID", messageID)
	}
	return res.RowsAffected > 0, nil
}

// DeleteMessageReaction removes the reaction (message, user, emoji) when it is
// there and does nothing when it is not. It returns true when it was removed.
func (s *Store) DeleteMessageReaction(ctx context.Context, messageID, userID uint64, emoji string) (bool, error) {
	res := s.db(ctx).Where("message_id = ? AND user_id = ? AND emoji = ?", messageID, userID, emoji).Delete(&MessageReaction{})
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deleteMessageReaction", "messageID", messageID)
	}
	return res.RowsAffected > 0, nil
}

// ReactionsOfMessages returns the reactions of each of the messages, oldest
// first (by ID), with the display names of the users, with one query. A
// message without reactions has no key in the map.
func (s *Store) ReactionsOfMessages(ctx context.Context, messageIDs []uint64) (map[uint64][]MessageReactionRow, error) {
	out := map[uint64][]MessageReactionRow{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	var rows []MessageReactionRow
	err := s.db(ctx).Table("message_reactions").
		Select("message_reactions.id AS id, message_reactions.message_id AS message_id, message_reactions.user_id AS user_id, message_reactions.emoji AS emoji, users.username AS username, users.display_name AS display_name").
		Joins("JOIN users ON users.id = message_reactions.user_id").
		Where("message_reactions.message_id IN ?", messageIDs).
		Order("message_reactions.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "reactionsOfMessages")
	}
	for _, r := range rows {
		out[r.MessageID] = append(out[r.MessageID], r)
	}
	return out, nil
}
