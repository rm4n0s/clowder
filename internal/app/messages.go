package app

import (
	"context"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// ConversationKind says how many people a conversation has. It is the store's
// type, so the web layer can name the values without importing the store.
type ConversationKind = store.ConversationKind

// The two kinds of conversation (SPEC §7.11).
const (
	// ConversationDirect is a conversation between two users.
	ConversationDirect ConversationKind = store.ConversationDirect
	// ConversationGroup is a conversation of up to 50 users.
	ConversationGroup ConversationKind = store.ConversationGroup
)

// The rules of SPEC §7.11 and D32.
const (
	// groupMaxMembers is the most members of a group, the starter included.
	groupMaxMembers = 50
	// groupMinOthers is the fewest other users a group is started with.
	groupMinOthers = 2
	// conversationNameMaxLength is the longest name of a group, in characters.
	conversationNameMaxLength = 80
	// messageBodyMaxLength is the longest message, in characters.
	messageBodyMaxLength = 4_000
	// historyDefaultLimit is the page size of History when the caller gives
	// none.
	historyDefaultLimit = 50
	// historyMaxLimit is the largest page History gives.
	historyMaxLimit = 100
	// messagePreviewLength is the number of characters a preview keeps.
	messagePreviewLength = 100
)

// MessageDeletedPreview is the text shown instead of a deleted message, in a
// preview or a quote.
const MessageDeletedPreview = "message deleted"

// unnamedGroupTitle is the title of a group with no name and no other member.
const unnamedGroupTitle = "Group"

// The names of the fields of the private messages in the metadata "fields" of
// a TagInvalidInput error (FieldUser, FieldName, FieldBody, FieldEmoji and
// FieldAttachments are named elsewhere).
const (
	// FieldUsers names the people chosen for a group.
	FieldUsers = "users"
	// FieldReplyTo names the message that a message replies to.
	FieldReplyTo = "replyTo"
	// FieldMessage names the message that MarkRead was given.
	FieldMessage = "message"
)

// MessagesConfig holds the settings of the messages service; it has the fields
// of PostsConfig, and the same defaults.
type MessagesConfig = PostsConfig

// MessageInput is what a person writes in the composer of a conversation.
type MessageInput struct {
	// Body is the text: at most 4,000 characters, trimmed. It may be empty
	// when the message has an attachment.
	Body string
	// ReplyToID is the ID of the earlier message of the same conversation
	// that this one answers; 0 for none.
	ReplyToID uint64
	// Uploads are the files of the message, as Messages.Upload returned them.
	Uploads []Upload
}

// MessageQuote is the message that a message replies to, as a quote.
type MessageQuote struct {
	// ID is the ID of the quoted message.
	ID uint64
	// Author is the author of the quoted message.
	Author UserRef
	// Preview is the start of its text in one line, the names of its files
	// when it has no text, or MessageDeletedPreview.
	Preview string
	// Deleted is true when the quoted message was deleted.
	Deleted bool
}

// Message is a private message as the services show it to one actor.
type Message struct {
	// ID is the ID of the message. Newer messages have larger IDs.
	ID uint64
	// ConversationID is the conversation of the message.
	ConversationID uint64
	// Author is the author.
	Author UserRef
	// Body is the text as written; empty for a deleted message.
	Body string
	// CreatedAt is when the message was sent.
	CreatedAt time.Time
	// EditedAt is when the author last edited the message; nil if never.
	EditedAt *time.Time
	// Deleted is true for a deleted message, which shows as "message deleted".
	Deleted bool
	// ReplyTo is the quoted message; nil when the message is no reply.
	ReplyTo *MessageQuote
	// Attachments are the files of the message; empty for a deleted message.
	Attachments []AttachmentView
	// Reactions are the emoji that people put on the message, in the order the
	// emoji were first used; empty for a deleted message.
	Reactions []ReactionCount
	// CanEdit is true when the asking actor may edit the message: its author,
	// if it is not deleted.
	CanEdit bool
	// CanDelete is true when the asking actor may delete the message: its
	// author, if it is not deleted.
	CanDelete bool
	// CanReact is true when the asking actor may react to the message: it is
	// not deleted.
	CanReact bool
}

// HistoryPage is a page of the messages of a conversation.
type HistoryPage struct {
	// Messages are the messages of the page, oldest first.
	Messages []Message
	// HasOlder is true when older messages exist.
	HasOlder bool
	// NextBefore is the before that asks for the next, older page: the ID of
	// the first (oldest) message of this page. It is 0 when HasOlder is false.
	NextBefore uint64
}

// LastMessage is the newest message of a conversation, for the list.
type LastMessage struct {
	// ID is the ID of the message.
	ID uint64
	// Author is the author.
	Author UserRef
	// Preview is the start of its text in one line (about 100 characters),
	// the names of its files when it has no text, or MessageDeletedPreview.
	Preview string
	// Deleted is true when the message was deleted.
	Deleted bool
	// CreatedAt is when the message was sent.
	CreatedAt time.Time
}

// ConversationSummary is a conversation in the list of one user.
type ConversationSummary struct {
	// ID is the ID of the conversation.
	ID uint64
	// Kind says whether it is direct or a group.
	Kind ConversationKind
	// Name is the name of a group as set; empty for none and for a direct
	// conversation.
	Name string
	// DisplayName is what the list shows: the name of a group, the other
	// user's display name in a direct conversation, or the display names of
	// the other members of a group with no name.
	DisplayName string
	// Members are the current members, the asking actor included.
	Members []UserRef
	// LastMessage is the newest message; nil when there is none.
	LastMessage *LastMessage
	// LastActivity is when the newest message was sent, or when the
	// conversation was started if it has none.
	LastActivity time.Time
	// Unread is true when the conversation has a message of someone else,
	// not deleted, after the last one the actor has read.
	Unread bool
	// CanSend is false in a direct conversation whose other user is disabled
	// (D32), as ConversationHeader.CanSend.
	CanSend bool
	// SeenUpToID is the last message the other member of a direct
	// conversation has read; 0 for a group and when they have read none. It
	// drives "Seen" for a client that shows the list.
	SeenUpToID uint64
}

// ConversationMemberView is a member of a conversation.
type ConversationMemberView struct {
	// User is the member.
	User UserRef
	// LastReadMessageID is the last message the member has read; 0 if none.
	// It drives "Seen".
	LastReadMessageID uint64
	// Disabled is true when an admin disabled the user.
	Disabled bool
}

// ConversationHeader is what the top of a conversation shows.
type ConversationHeader struct {
	// ID is the ID of the conversation.
	ID uint64
	// Kind says whether it is direct or a group.
	Kind ConversationKind
	// Name is the name of a group as set; empty for none.
	Name string
	// DisplayName is the title as ConversationSummary.DisplayName.
	DisplayName string
	// Members are the current members, the asking actor included, in the
	// order they joined.
	Members []ConversationMemberView
	// CanSend is false in a direct conversation whose other user is disabled:
	// the messages are read but no new ones are sent (D32).
	CanSend bool
	// IsGroup is true for a group, where members are added, the group is
	// renamed and members leave.
	IsGroup bool
}

// Quote returns the message as a quote, which is what a reply to it shows: its
// author and the start of its text (or MessageDeletedPreview).
func (m Message) Quote() MessageQuote {
	names := make([]string, len(m.Attachments))
	for i, a := range m.Attachments {
		names[i] = a.FileName
	}
	return MessageQuote{ID: m.ID, Author: m.Author, Preview: previewOf(m.Deleted, m.Body, names), Deleted: m.Deleted}
}

// Messages is the service of private conversations and messages (SPEC §7.11,
// D32). Make it with NewMessages. It is safe for concurrent use. Only the
// members of a conversation use it: anyone else, admins included, and a member
// who left, get TagNotFound. It writes no audit events and makes no
// notifications.
type Messages struct {
	st      *store.Store
	clk     clock.Clock
	files   *files.Store
	cfg     MessagesConfig
	cleaner fileCleaner
	pub     Publisher
}

// NewMessages makes the service. The clock times the edits, deletions and
// departures; fs holds the files of the attachments; cfg has their limits.
// pub receives the live events of the changes, after they are committed; nil
// publishes nothing.
func NewMessages(st *store.Store, clk clock.Clock, fs *files.Store, cfg MessagesConfig, pub Publisher) *Messages {
	cfg = cfg.withDefaults()
	return &Messages{st: st, clk: clk, files: fs, cfg: cfg, cleaner: newFileCleaner(fs, cfg.Logger), pub: orNop(pub)}
}

// StartDirect returns the ID of the direct conversation between the actor and
// the user, and makes it when there is none (SPEC D28: one for each pair). It
// is OpenDirect without the answer whether the conversation is new, and fails
// as OpenDirect does.
func (m *Messages) StartDirect(ctx context.Context, actor Actor, userID uint64) (uint64, error) {
	id, _, err := m.OpenDirect(ctx, actor, userID)
	return id, err
}

// OpenDirect returns the ID of the direct conversation between the actor and
// the user, and makes it when there is none (SPEC D28: one for each pair), and
// says whether this call made it: created is false when the conversation
// existed, also when another call made it at the same time.
// Called at the same time for the same pair it still gives one conversation.
// It fails with TagInvalidInput, with the message under FieldUser, when the
// user is the actor, does not exist or is disabled. Both users hear of a new
// conversation as conversation.changed.
func (m *Messages) OpenDirect(ctx context.Context, actor Actor, userID uint64) (uint64, bool, error) {
	if actor.UserID == 0 {
		return 0, false, errors.New(TagUnauthenticated, "messages need a login", "reason", "none")
	}
	key := store.DirectKeyOf(actor.UserID, userID)
	var convID uint64
	var created bool
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		convID, created = 0, false
		events = nil
		if userID == actor.UserID {
			return errors.New(TagInvalidInput, "a conversation needs another user",
				"fields", map[string]string{FieldUser: "Choose someone else."})
		}
		bad, err := unusablePeople(ctx, tx, []uint64{userID})
		if err != nil {
			return err
		}
		if bad {
			return errors.New(TagInvalidInput, "the user does not exist or is disabled",
				"fields", map[string]string{FieldUser: messageUsersInvalid})
		}
		existing, err := tx.DirectConversation(ctx, key)
		if err == nil {
			convID = existing.ID
			return nil
		}
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
			return err
		}
		conv := store.Conversation{Kind: ConversationDirect, CreatedBy: actor.UserID, DirectKey: &key}
		if err := tx.CreateConversation(ctx, &conv); err != nil {
			return err
		}
		adder := actor.UserID
		if err := tx.CreateConversationMembers(ctx, []store.ConversationMember{
			{ConversationID: conv.ID, UserID: actor.UserID},
			{ConversationID: conv.ID, UserID: userID, AddedBy: &adder},
		}); err != nil {
			return err
		}
		convID, created = conv.ID, true
		events = changedEvents(conv.ID, actor.UserID, userID)
		return nil
	})
	if err != nil {
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != store.TagConflict {
			return 0, false, err
		}
		// Another call made the conversation of the pair first.
		existing, err := m.st.DirectConversation(ctx, key)
		if err != nil {
			return 0, false, err
		}
		return existing.ID, false, nil
	}
	publishAll(m.pub, events)
	return convID, created, nil
}

// StartGroup makes a group of the actor and the users and returns its ID. At
// least two other users are needed (the actor and repeated users are not
// counted), at most 50 members including the actor; none may be disabled. The
// name is optional, trimmed, at most 80 characters. It fails with
// TagInvalidInput, with the messages under FieldUsers and FieldName. Every
// member hears of the new group as conversation.changed.
func (m *Messages) StartGroup(ctx context.Context, actor Actor, userIDs []uint64, name string) (uint64, error) {
	if actor.UserID == 0 {
		return 0, errors.New(TagUnauthenticated, "messages need a login", "reason", "none")
	}
	var convID uint64
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		convID = 0
		events = nil
		others := distinctOthers(actor.UserID, userIDs)
		fields := map[string]string{}
		switch {
		case len(others) < groupMinOthers:
			fields[FieldUsers] = "Choose at least two other people."
		case len(others)+1 > groupMaxMembers:
			fields[FieldUsers] = "A group has at most 50 members."
		default:
			bad, err := unusablePeople(ctx, tx, others)
			if err != nil {
				return err
			}
			if bad {
				fields[FieldUsers] = messageUsersInvalid
			}
		}
		clean, problem := cleanConversationName(name)
		if problem != "" {
			fields[FieldName] = problem
		}
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the group has mistakes", "fields", fields)
		}
		conv := store.Conversation{Kind: ConversationGroup, Name: clean, CreatedBy: actor.UserID}
		if err := tx.CreateConversation(ctx, &conv); err != nil {
			return err
		}
		adder := actor.UserID
		rows := []store.ConversationMember{{ConversationID: conv.ID, UserID: actor.UserID}}
		for _, id := range others {
			rows = append(rows, store.ConversationMember{ConversationID: conv.ID, UserID: id, AddedBy: &adder})
		}
		if err := tx.CreateConversationMembers(ctx, rows); err != nil {
			return err
		}
		convID = conv.ID
		events = changedEvents(conv.ID, append([]uint64{actor.UserID}, others...)...)
		return nil
	})
	if err != nil {
		return 0, err
	}
	publishAll(m.pub, events)
	return convID, nil
}

// AddMembers adds users to a group. A user who is a member already is left
// alone; one who left comes back and sees the whole history (D32). It fails
// with TagNotFound unless the actor is a member, with TagInvalidInput, with
// the messages under FieldUsers, for a direct conversation, an empty list, a
// user who does not exist or is disabled, and more than 50 members. The
// conversation hears conversation.updated and every member, the added ones
// too, conversation.changed; nothing is published when nobody was added.
func (m *Messages) AddMembers(ctx context.Context, actor Actor, convID uint64, userIDs []uint64) error {
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		conv, _, err := loadConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		if conv.Kind != ConversationGroup {
			return errors.New(TagInvalidInput, "people are added to groups only",
				"fields", map[string]string{FieldUsers: "People cannot be added to a conversation of two."})
		}
		rows, err := tx.ConversationMembers(ctx, []uint64{convID}, true)
		if err != nil {
			return err
		}
		known := map[uint64]store.ConversationMemberRow{}
		current := 0
		for _, r := range rows[convID] {
			known[r.UserID] = r
			if r.LeftAt == nil {
				current++
			}
		}
		wanted := distinctOthers(actor.UserID, userIDs)
		if len(wanted) == 0 {
			return errors.New(TagInvalidInput, "no one was chosen",
				"fields", map[string]string{FieldUsers: "Choose at least one person."})
		}
		var added []uint64
		for _, id := range wanted {
			if r, ok := known[id]; ok && r.LeftAt == nil {
				continue
			}
			added = append(added, id)
		}
		bad, err := unusablePeople(ctx, tx, added)
		if err != nil {
			return err
		}
		if bad {
			return errors.New(TagInvalidInput, "a user does not exist or is disabled",
				"fields", map[string]string{FieldUsers: messageUsersInvalid})
		}
		if current+len(added) > groupMaxMembers {
			return errors.New(TagInvalidInput, "the group would have too many members",
				"fields", map[string]string{FieldUsers: "A group has at most 50 members."})
		}
		if len(added) == 0 {
			return nil
		}
		adder := actor.UserID
		var fresh []store.ConversationMember
		for _, id := range added {
			if _, ok := known[id]; ok {
				if err := tx.ReaddConversationMember(ctx, convID, id, adder); err != nil {
					return err
				}
				continue
			}
			fresh = append(fresh, store.ConversationMember{ConversationID: convID, UserID: id, AddedBy: &adder})
		}
		if err := tx.CreateConversationMembers(ctx, fresh); err != nil {
			return err
		}
		heard := slices.Clone(added)
		for _, r := range rows[convID] {
			if r.LeftAt == nil {
				heard = append(heard, r.UserID)
			}
		}
		events = append([]live.Event{updatedEvent(convID)}, changedEvents(convID, heard...)...)
		return nil
	})
	if err != nil {
		return err
	}
	publishAll(m.pub, events)
	return nil
}

// Rename sets the name of a group; an empty name removes it. The name is
// trimmed, at most 80 characters. It fails with TagNotFound unless the actor
// is a member, with TagInvalidInput, with the message under FieldName, for a
// direct conversation or a name that is too long. The conversation hears
// conversation.updated and every member conversation.changed; nothing is
// published when the name stays.
func (m *Messages) Rename(ctx context.Context, actor Actor, convID uint64, name string) error {
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		conv, _, err := loadConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		clean, problem := cleanConversationName(name)
		switch {
		case conv.Kind != ConversationGroup:
			problem = "A conversation of two has no name."
		case problem == "" && clean == conv.Name:
			return nil
		}
		if problem != "" {
			return errors.New(TagInvalidInput, "the name is not valid", "fields", map[string]string{FieldName: problem})
		}
		if err := tx.SetConversationName(ctx, convID, clean); err != nil {
			return err
		}
		rows, err := tx.ConversationMembers(ctx, []uint64{convID}, false)
		if err != nil {
			return err
		}
		events = append([]live.Event{updatedEvent(convID)}, changedEvents(convID, userIDsOf(rows[convID])...)...)
		return nil
	})
	if err != nil {
		return err
	}
	publishAll(m.pub, events)
	return nil
}

// Leave takes the actor out of a group. The group stays for the others, and
// the actor no longer reads it. It fails with TagNotFound unless the actor is
// a member, with TagInvalidInput for a direct conversation, which nobody
// leaves. The conversation hears conversation.updated; the members who stay,
// and the one who left, hear conversation.changed.
func (m *Messages) Leave(ctx context.Context, actor Actor, convID uint64) error {
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		conv, _, err := loadConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		if conv.Kind != ConversationGroup {
			return errors.New(TagInvalidInput, "nobody leaves a direct conversation",
				"fields", map[string]string{FieldForm: "You cannot leave a conversation of two."})
		}
		if err := tx.MarkConversationMemberLeft(ctx, convID, actor.UserID, m.clk.Now()); err != nil {
			return err
		}
		rows, err := tx.ConversationMembers(ctx, []uint64{convID}, false)
		if err != nil {
			return err
		}
		heard := append(userIDsOf(rows[convID]), actor.UserID)
		events = append([]live.Event{updatedEvent(convID)}, changedEvents(convID, heard...)...)
		return nil
	})
	if err != nil {
		return err
	}
	publishAll(m.pub, events)
	return nil
}

// Conversations returns the conversations the actor is a member of, the most
// recent activity first. It runs a few queries, however many conversations
// there are. An actor with no ID fails with TagUnauthenticated.
func (m *Messages) Conversations(ctx context.Context, actor Actor) ([]ConversationSummary, error) {
	if actor.UserID == 0 {
		return nil, errors.New(TagUnauthenticated, "messages need a login", "reason", "none")
	}
	items, err := m.st.ListConversations(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]ConversationSummary, len(items))
	newest := make([]uint64, len(items))
	for i, it := range items {
		conv := it.Conversation
		s := ConversationSummary{
			ID:           conv.ID,
			Kind:         conv.Kind,
			Name:         conv.Name,
			DisplayName:  conversationTitle(conv, actor.UserID, it.Members),
			Members:      make([]UserRef, len(it.Members)),
			LastActivity: conv.CreatedAt,
			Unread:       it.Unread,
			CanSend:      true,
		}
		for j, mem := range it.Members {
			s.Members[j] = UserRef{ID: mem.UserID, Username: mem.Username, DisplayName: mem.DisplayName, AvatarKey: mem.AvatarKey}
			if conv.Kind == ConversationDirect && mem.UserID != actor.UserID {
				s.SeenUpToID = derefID(mem.LastReadMessageID)
				if mem.DisabledAt != nil {
					s.CanSend = false
				}
			}
		}
		if last := it.LastMessage; last != nil {
			s.LastMessage = &LastMessage{
				ID:        last.ID,
				Author:    UserRef{ID: last.AuthorID, Username: last.AuthorUsername, DisplayName: last.AuthorDisplayName, AvatarKey: last.AuthorAvatarKey},
				Preview:   previewOf(last.DeletedAt != nil, last.Body, it.LastAttachmentNames),
				Deleted:   last.DeletedAt != nil,
				CreatedAt: last.CreatedAt,
			}
			s.LastActivity = last.CreatedAt
			newest[i] = last.ID
		}
		out[i] = s
	}
	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if !out[x].LastActivity.Equal(out[y].LastActivity) {
			return out[x].LastActivity.After(out[y].LastActivity)
		}
		if newest[x] != newest[y] {
			return newest[x] > newest[y]
		}
		return out[x].ID > out[y].ID
	})
	sorted := make([]ConversationSummary, len(out))
	for i, at := range order {
		sorted[i] = out[at]
	}
	return sorted, nil
}

// Conversation returns the header of a conversation: its title and its
// members with the last message each has read. It fails with TagNotFound
// unless the actor is a member.
func (m *Messages) Conversation(ctx context.Context, actor Actor, convID uint64) (ConversationHeader, error) {
	conv, _, err := loadConversation(ctx, m.st, actor, convID)
	if err != nil {
		return ConversationHeader{}, err
	}
	rows, err := m.st.ConversationMembers(ctx, []uint64{convID}, false)
	if err != nil {
		return ConversationHeader{}, err
	}
	h := ConversationHeader{
		ID:          conv.ID,
		Kind:        conv.Kind,
		Name:        conv.Name,
		DisplayName: conversationTitle(conv, actor.UserID, rows[convID]),
		Members:     make([]ConversationMemberView, len(rows[convID])),
		CanSend:     true,
		IsGroup:     conv.Kind == ConversationGroup,
	}
	for i, r := range rows[convID] {
		h.Members[i] = ConversationMemberView{
			User:              UserRef{ID: r.UserID, Username: r.Username, DisplayName: r.DisplayName, AvatarKey: r.AvatarKey},
			LastReadMessageID: derefID(r.LastReadMessageID),
			Disabled:          r.DisabledAt != nil,
		}
		if conv.Kind == ConversationDirect && r.UserID != actor.UserID && r.DisabledAt != nil {
			h.CanSend = false
		}
	}
	return h, nil
}

// Summary returns one conversation as the list shows it to the actor: the
// member list, the last message, whether it is unread, whether the actor can
// send and, in a direct conversation, how far the other user has read. It
// fails with TagNotFound unless the actor is a member (admins included, and a
// member who left).
func (m *Messages) Summary(ctx context.Context, actor Actor, convID uint64) (ConversationSummary, error) {
	if _, _, err := loadConversation(ctx, m.st, actor, convID); err != nil {
		return ConversationSummary{}, err
	}
	list, err := m.Conversations(ctx, actor)
	if err != nil {
		return ConversationSummary{}, err
	}
	for _, s := range list {
		if s.ID == convID {
			return s, nil
		}
	}
	// The actor left between the two reads.
	return ConversationSummary{}, errors.New(TagNotFound, "no such conversation",
		"reason", "left", "conversationID", convID, "userID", actor.UserID)
}

// ResolveUsernames returns the IDs of the users with these usernames, in the
// order given, for a form where people are named. Names are trimmed and lower
// cased, empty ones and repeats are dropped, and the actor may be among them.
// It fails with TagInvalidInput, with the message under FieldUsers, when no
// name is left, when there are more than 50, or when a name belongs to no user
// or to a disabled one (the message lists those names). An actor with no ID
// fails with TagUnauthenticated.
func (m *Messages) ResolveUsernames(ctx context.Context, actor Actor, usernames []string) ([]uint64, error) {
	if actor.UserID == 0 {
		return nil, errors.New(TagUnauthenticated, "messages need a login", "reason", "none")
	}
	names := make([]string, 0, len(usernames))
	for _, n := range usernames {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	switch {
	case len(names) == 0:
		return nil, errors.New(TagInvalidInput, "no one was named",
			"fields", map[string]string{FieldUsers: "Name at least one person."})
	case len(names) > groupMaxMembers:
		return nil, errors.New(TagInvalidInput, "too many people were named",
			"fields", map[string]string{FieldUsers: "A group has at most 50 members."})
	}
	users, err := m.st.UsersByUsernames(ctx, names)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]uint64, len(users))
	for _, u := range users {
		if u.DisabledAt == nil {
			ids[u.Username] = u.ID
		}
	}
	out := make([]uint64, 0, len(names))
	var bad []string
	for _, n := range names {
		if id, ok := ids[n]; ok {
			out = append(out, id)
		} else {
			bad = append(bad, n)
		}
	}
	if len(bad) > 0 {
		return nil, errors.New(TagInvalidInput, "a name belongs to no user or to a disabled one",
			"fields", map[string]string{FieldUsers: "No active user named " + strings.Join(bad, ", ") + "."})
	}
	return out, nil
}

// UnreadCount returns the number of conversations of the actor that have a
// message of someone else, not deleted, after the last one the actor has read
// (D32), with one query. An actor with no ID fails with TagUnauthenticated.
func (m *Messages) UnreadCount(ctx context.Context, actor Actor) (int, error) {
	if actor.UserID == 0 {
		return 0, errors.New(TagUnauthenticated, "messages need a login", "reason", "none")
	}
	return m.st.CountUnreadConversations(ctx, actor.UserID)
}

// MarkRead marks the conversation read up to the message upToID, which must
// be a message of the conversation: the last read message becomes the larger
// of the current one and upToID, so it never moves back. It fails with
// TagNotFound unless the actor is a member, and with TagInvalidInput, with the
// message under FieldMessage, for a message that is not in the conversation.
// When the last read message moved, conversation.read is published to the
// conversation (it drives "Seen") and to the actor.
func (m *Messages) MarkRead(ctx context.Context, actor Actor, convID, upToID uint64) error {
	var events []live.Event
	err := m.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		if _, _, err := loadConversation(ctx, tx, actor, convID); err != nil {
			return err
		}
		msg, err := tx.MessageByID(ctx, upToID)
		missing := false
		if err != nil {
			if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
				return err
			}
			missing = true
		}
		if missing || msg.ConversationID != convID {
			return errors.New(TagInvalidInput, "the message is not in the conversation",
				"fields", map[string]string{FieldMessage: "Choose a message of this conversation."})
		}
		moved, err := tx.AdvanceLastRead(ctx, convID, actor.UserID, upToID)
		if err != nil {
			return err
		}
		if moved {
			events = []live.Event{
				{Topic: live.ConversationTopic(convID), Kind: live.KindConversationRead, ConversationID: convID, MessageID: upToID, UserID: actor.UserID},
				{Topic: live.UserTopic(actor.UserID), Kind: live.KindConversationRead, ConversationID: convID, MessageID: upToID, UserID: actor.UserID},
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	publishAll(m.pub, events)
	return nil
}

// loadConversation returns the conversation and the actor's membership of it.
// A conversation that does not exist, one that the actor is no member of
// (admins included) and one that the actor left fail with the same
// TagNotFound; the metadata "reason" ("missing", "member" or "left") tells
// them apart, for the log.
func loadConversation(ctx context.Context, st *store.Store, actor Actor, convID uint64) (store.Conversation, store.ConversationMember, error) {
	conv, err := st.ConversationByID(ctx, convID)
	if err != nil {
		return store.Conversation{}, store.ConversationMember{}, err
	}
	member, err := st.ConversationMemberOf(ctx, convID, actor.UserID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return store.Conversation{}, store.ConversationMember{}, errors.New(TagNotFound, "no such conversation",
				"reason", "member", "conversationID", convID, "userID", actor.UserID)
		}
		return store.Conversation{}, store.ConversationMember{}, err
	}
	if member.LeftAt != nil {
		return store.Conversation{}, store.ConversationMember{}, errors.New(TagNotFound, "no such conversation",
			"reason", "left", "conversationID", convID, "userID", actor.UserID)
	}
	return conv, member, nil
}

// unusablePeople reports whether any of the users does not exist or is
// disabled.
func unusablePeople(ctx context.Context, tx *store.Store, ids []uint64) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	users, err := tx.UsersByIDs(ctx, ids)
	if err != nil {
		return false, err
	}
	enabled := 0
	for _, u := range users {
		if u.DisabledAt == nil {
			enabled++
		}
	}
	return enabled != len(ids), nil
}

// distinctOthers returns ids without repeats and without the actor, in order.
func distinctOthers(actorID uint64, ids []uint64) []uint64 {
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id != actorID && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// messageUsersInvalid is the message for people who cannot be chosen.
const messageUsersInvalid = "Choose people who exist and are not disabled."

// cleanConversationName trims the name of a group and returns it with the
// message of its problem, or an empty one.
func cleanConversationName(name string) (string, string) {
	name = strings.TrimSpace(name)
	switch {
	case !utf8.ValidString(name):
		return name, "Use valid text."
	case utf8.RuneCountInString(name) > conversationNameMaxLength:
		return name, "Use at most 80 characters."
	}
	return name, ""
}

// conversationTitle is the title of a conversation for one user: the name of a
// group, else the display names of the members other than the user, joined by
// commas.
func conversationTitle(conv store.Conversation, userID uint64, members []store.ConversationMemberRow) string {
	if conv.Kind == ConversationGroup && conv.Name != "" {
		return conv.Name
	}
	var names []string
	for _, r := range members {
		if r.UserID != userID {
			names = append(names, r.DisplayName)
		}
	}
	if len(names) == 0 {
		return unnamedGroupTitle
	}
	return strings.Join(names, ", ")
}

// previewOf makes the one-line preview of a message: the start of its text
// with the white space collapsed, the names of its files when it has no text,
// or MessageDeletedPreview.
func previewOf(deleted bool, body string, fileNames []string) string {
	if deleted {
		return MessageDeletedPreview
	}
	text := strings.Join(strings.Fields(body), " ")
	if text == "" {
		text = strings.Join(fileNames, ", ")
	}
	if utf8.RuneCountInString(text) > messagePreviewLength {
		text = string([]rune(text)[:messagePreviewLength]) + "…"
	}
	return text
}

// derefID returns *id, or 0 for nil.
func derefID(id *uint64) uint64 {
	if id == nil {
		return 0
	}
	return *id
}

// userIDsOf returns the users of the memberships.
func userIDsOf(rows []store.ConversationMemberRow) []uint64 {
	ids := make([]uint64, len(rows))
	for i, r := range rows {
		ids[i] = r.UserID
	}
	return ids
}

// changedEvents are conversation.changed for each of the users, on their own
// topic: their list and unread count refresh.
func changedEvents(convID uint64, userIDs ...uint64) []live.Event {
	events := make([]live.Event, len(userIDs))
	for i, id := range userIDs {
		events[i] = live.Event{Topic: live.UserTopic(id), Kind: live.KindConversationChanged, ConversationID: convID, UserID: id}
	}
	return events
}

// updatedEvent is conversation.updated on the conversation.
func updatedEvent(convID uint64) live.Event {
	return live.Event{Topic: live.ConversationTopic(convID), Kind: live.KindConversationUpdated, ConversationID: convID}
}

// messageEvent is an event of one message on the conversation.
func messageEvent(kind string, convID, msgID uint64) live.Event {
	return live.Event{Topic: live.ConversationTopic(convID), Kind: kind, ConversationID: convID, MessageID: msgID}
}

// Upload stores a file that a person attached to the composer and returns what
// Send needs to attach it (MessageInput.Uploads). The limits and the cleaning
// of the name are those of Posts.Upload. An actor with no ID fails with
// TagUnauthenticated, a file over the limit with TagTooLarge. An upload that
// no message takes is left on disk: callers pass every upload to Send, which
// removes the files when it fails.
func (m *Messages) Upload(ctx context.Context, actor Actor, name string, r io.Reader) (Upload, error) {
	if actor.UserID == 0 {
		return Upload{}, errors.New(TagUnauthenticated, "an upload needs a login", "reason", "none")
	}
	saved, err := m.files.Save(ctx, r, m.cfg.MaxAttachmentSize)
	if err != nil {
		return Upload{}, err
	}
	return Upload{
		Key:         saved.Key,
		FileName:    cleanFileName(name),
		ContentType: saved.ContentType,
		Size:        saved.Size,
		Width:       saved.Width,
		Height:      saved.Height,
	}, nil
}

// DiscardUploads removes the files of uploads that no message took, for a
// caller that made them with Upload and then failed before it could pass them
// to Send. A file that an attachment already uses is left alone. An actor with
// no ID does nothing. Failures are logged, never returned.
func (m *Messages) DiscardUploads(ctx context.Context, actor Actor, uploads []Upload) {
	if actor.UserID == 0 {
		return
	}
	m.cleaner.discard(ctx, m.st, uploads)
}

// StagedUploads turns the IDs that POST /uploads returned (Posts.UploadStaged)
// into the uploads that Send takes: a file is the same kind of thing for a
// post and a message, and one staged upload is attached to one of them. The
// permissions and the errors are those of Posts.StagedUploads: only the
// person who uploaded a file gets it back, and an ID that is not theirs fails
// the whole call with TagInvalidInput and the message under FieldAttachments.
// An actor with no ID fails with TagUnauthenticated. The call changes
// nothing; ForgetStaged ends the notes after Send.
func (m *Messages) StagedUploads(ctx context.Context, actor Actor, ids []string) ([]Upload, error) {
	return stagedUploads(m.files, actor, ids)
}

// ForgetStaged ends the notes of uploads that StagedUploads returned and that
// Send took or refused, as Posts.ForgetStaged does. Failures are logged,
// never returned. An actor with no ID does nothing.
func (m *Messages) ForgetStaged(ctx context.Context, actor Actor, uploads []Upload) {
	forgetStaged(ctx, m.files, m.cleaner, actor, uploads)
}
