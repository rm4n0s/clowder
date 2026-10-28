package views

import (
	"html/template"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
)

// MessagesPath is the path of the page that lists the conversations of the
// user; a POST to it starts a conversation.
const MessagesPath = "/messages"

// MessagesStreamPath is the path of the stream of the Messages page: the
// stream of the pages that have none (MeStreamPath), told to keep the list of
// conversations live too.
const MessagesStreamPath = MeStreamPath + "?messages=1"

// FromRoomPrefix starts the value of the form field "from" of a "Message"
// button in the member list of a room: FromRoomPrefix + the room's ID. It says
// where the message of a failure is shown.
const FromRoomPrefix = "room-"

// ConversationPath returns the path of the page of a conversation,
// /messages/<id>. Its actions are ConversationPath + "/members", "/rename" and
// "/leave"; "/stream" is its live stream and "/older" loads older messages.
func ConversationPath(id uint64) string {
	return MessagesPath + "/" + strconv.FormatUint(id, 10)
}

// ConversationOlderPath returns the path that loads the messages older than
// before, as a Datastar patch.
func ConversationOlderPath(convID, before uint64) string {
	return ConversationPath(convID) + "/older?before=" + strconv.FormatUint(before, 10)
}

// ConversationBeforePath returns the path of the page of the conversation that
// shows the messages older than before, for a browser without JavaScript.
func ConversationBeforePath(convID, before uint64) string {
	return ConversationPath(convID) + "?before=" + strconv.FormatUint(before, 10)
}

// ConversationMessagePath returns the path of the page of the conversation
// that shows the message msgID and scrolls to it: the newest messages when it
// is among them, else the page that ends with it.
func ConversationMessagePath(convID, msgID uint64) string {
	id := strconv.FormatUint(msgID, 10)
	return ConversationPath(convID) + "?msg=" + id + "#" + MessageElementID(msgID)
}

// MessageSearchPath returns the path of the Messages page with a search: the
// text of the box and, for an older page of results, the before that asks for
// it. An empty query gives MessagesPath.
func MessageSearchPath(q string, before uint64) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if before != 0 {
		v.Set("before", strconv.FormatUint(before, 10))
	}
	if enc := v.Encode(); enc != "" {
		return MessagesPath + "?" + enc
	}
	return MessagesPath
}

// ConversationReplyPath returns the path of the page of the conversation with
// the composer set to reply to the message, for a browser without JavaScript.
func ConversationReplyPath(convID, msgID uint64) string {
	return ConversationPath(convID) + "?reply=" + strconv.FormatUint(msgID, 10) + "#message-composer"
}

// ConversationStreamPath returns the path of the live stream of the
// conversation. newest is the ID of the newest message the page showed (0 for
// none); before is the before of the page (0 for the newest messages), whose
// messages a stream does not keep up to date.
func ConversationStreamPath(convID, newest, before uint64) string {
	path := ConversationPath(convID) + "/stream?seen=" + strconv.FormatUint(newest, 10)
	if before != 0 {
		path += "&before=" + strconv.FormatUint(before, 10)
	}
	return path
}

// MessageActionPath returns the path of an action on a message,
// /messages/m/<id>/<action> with the action "edit" (the form, and the POST
// that saves it), "view", "delete", "react" or "reply".
func MessageActionPath(msgID uint64, action string) string {
	return MessagesPath + "/m/" + strconv.FormatUint(msgID, 10) + "/" + action
}

// MessageFilePath returns the path that downloads an attachment of a message,
// /messages/files/<id>/<name>. The name only gives the download a good name;
// the ID decides.
func MessageFilePath(a app.AttachmentView) string {
	return MessagesPath + "/files/" + strconv.FormatUint(a.ID, 10) + "/" + url.PathEscape(a.FileName)
}

// MessageElementID is the id of the element of a message in the list.
func MessageElementID(id uint64) string {
	return "msg-" + strconv.FormatUint(id, 10)
}

// MessageContentID is the id of the element that holds the content of a
// message (see MessageContent).
func MessageContentID(id uint64) string {
	return "msg-content-" + strconv.FormatUint(id, 10)
}

// messageReactionsID is the id of the reaction bar of a message.
func messageReactionsID(id uint64) string {
	return "msg-reactions-" + strconv.FormatUint(id, 10)
}

// messageReactionPickerID is the id of the picker of the reaction bar of a
// message, which the live changes of the reactions do not patch.
func messageReactionPickerID(id uint64) string {
	return "msg-reaction-picker-" + strconv.FormatUint(id, 10)
}

// messageSeenID is the id of the place under an own message where "Seen" may
// show.
func messageSeenID(id uint64) string {
	return "msg-seen-" + strconv.FormatUint(id, 10)
}

// conversationElementID is the id of the entry of a conversation in the list.
func conversationElementID(id uint64) string {
	return "conversation-" + strconv.FormatUint(id, 10)
}

// MessageData is what one message shows.
type MessageData struct {
	// Message is the message.
	Message app.Message
	// HTML is the rendered body, which comes from internal/markdown; empty for
	// a message without text.
	HTML template.HTML
	// Own is true when the viewer wrote the message.
	Own bool
	// Direct is true in a direct conversation, where an own message has the
	// place for "Seen".
	Direct bool
	// Seen is true for the message "Seen" shows under.
	Seen bool
	// CanReply shows the Reply link: the viewer may send messages here.
	CanReply bool
	// Notice is the problem of the last action on the message (it may not be
	// edited or deleted), shown above it; empty shows none.
	Notice string
}

// messageClasses are the classes of the element of a message.
func messageClasses(d MessageData) string {
	classes := "message"
	if d.Own {
		classes += " own"
	}
	if d.Message.Deleted {
		classes += " message-deleted"
	}
	if d.Message.Call != nil {
		classes += " message-call"
	}
	return classes
}

// MessageEditData is a message replaced by its editor.
type MessageEditData struct {
	// Message is the message being edited.
	Message MessageData
	// Body is the text in the field.
	Body string
	// Errors are the problems of the last submit; the field is named body.
	Errors FormErrors
}

// MessageReactionsData is what the reaction bar of a message shows.
type MessageReactionsData struct {
	// Message is the message: its reactions, and whether the person may react.
	Message app.Message
	// Notice is the problem of the last reaction, shown above the bar; empty
	// shows none.
	Notice string
	// Problem is the message of the emoji field of the last reaction that was
	// not an emoji; the picker is open and shows it.
	Problem string
	// Typed is what the person typed in the emoji field, kept with Problem.
	Typed string
}

// ComposerData is what the composer of a conversation shows.
type ComposerData struct {
	// ConversationID is the conversation the composer writes in.
	ConversationID uint64
	// CanSend is false when the other user of a direct conversation is
	// disabled: the composer is replaced by a note.
	CanSend bool
	// Body is the text in the field, kept after a failed submit.
	Body string
	// Reply is the message the new one replies to; nil for none.
	Reply *app.MessageQuote
	// Errors are the problems of the last submit; the fields are named body,
	// attachments and replyTo.
	Errors FormErrors
	// ConfirmCount, when it is not 0, is the number of people a group mention
	// of the body would notify, and the composer asks "This will notify N
	// people. Send anyway?" (SPEC §7.15).
	ConfirmCount int
}

// composerConfirmID is the ID of the composer in the id of the place of its
// group mention question.
const composerConfirmID = "composer"

// ComposerConfirm is the data of the question of the composer of the
// conversation: n people would be notified.
func ComposerConfirm(conversationID uint64, n int) GroupMentionConfirmData {
	return GroupMentionConfirmData{
		ID: composerConfirmID, Count: n, SendPath: GroupMentionSendPath(ConversationPath(conversationID)),
	}
}

// NewConversationData is the form that starts a conversation.
type NewConversationData struct {
	// Users and Name are the values of the last submit.
	Users string
	Name  string
	// Errors are the problems of the last submit; the fields are named users
	// and name.
	Errors FormErrors
}

// MessageSearchData is the search of the Messages page. The zero value is no
// search.
type MessageSearchData struct {
	// Active is true when the page shows a search: the text of the box is not
	// empty. The results then replace the list of conversations.
	Active bool
	// Query is the text of the search box, as typed.
	Query string
	// Message is the problem with the query, such as a filter that does not
	// work in messages; empty shows none.
	Message string
	// Results are the results of the page, newest first.
	Results []app.MessageSearchResult
	// HasMore is true when older results follow; NextBefore is the before that
	// asks for them.
	HasMore    bool
	NextBefore uint64
}

// ConversationsData is what the Messages page shows.
type ConversationsData struct {
	// Sidebar is the sidebar of the page.
	Sidebar SidebarData
	// ViewerID is the ID of the person who looks at the page.
	ViewerID uint64
	// Items are the conversations, the most recent first.
	Items []app.ConversationSummary
	// New is the state of the form that starts a conversation.
	New NewConversationData
	// Search is the search of the page, if any (SPEC §7.11, D56).
	Search MessageSearchData
}

// MembersPanelData is what the members panel of a group shows.
type MembersPanelData struct {
	// Header is the conversation.
	Header app.ConversationHeader
	// ViewerID is the ID of the person who looks at the page.
	ViewerID uint64
	// Online is the set of the members who are online when the page loads
	// (SPEC §7.15); the dots are not live.
	Online map[uint64]bool
	// AddUsers is the text in the field of the people to add.
	AddUsers string
	// RenameTo is the text in the name field when RenameOpen is set; the
	// current name otherwise.
	RenameTo string
	// RenameOpen says that RenameTo is what the person typed.
	RenameOpen bool
	// AddErrors and RenameErrors are the problems of the last add and rename.
	AddErrors, RenameErrors FormErrors
	// Message is the problem of the last action on the panel (leave); empty
	// shows none.
	Message string
}

// ConversationData is what the page of a conversation shows.
type ConversationData struct {
	// Sidebar is the sidebar of the page.
	Sidebar SidebarData
	// ViewerID is the ID of the person who looks at the page.
	ViewerID uint64
	// Header is the conversation.
	Header app.ConversationHeader
	// Messages are the messages of the page, oldest first.
	Messages []MessageData
	// HasOlder is true when older messages exist; NextBefore is the before
	// that asks for them.
	HasOlder   bool
	NextBefore uint64
	// Before is the before that asked for this page; 0 for the newest
	// messages.
	Before uint64
	// Composer is the composer.
	Composer ComposerData
	// Members is the members panel of a group.
	Members MembersPanelData
	// Edit is the editor of the message being edited; nil for none.
	Edit *MessageEditData
	// Notice is the problem of the last action on the conversation, shown in
	// the header; empty shows none.
	Notice string
	// Prefs is the dialog of the notification preferences of the viewer in
	// the conversation.
	Prefs PlacePrefsData
	// Focus is the message the page was opened for (a search result links to
	// it); 0 for none. The page scrolls to it and marks it.
	Focus uint64
	// Call is the calls of the conversation: the buttons and the call in
	// progress (SPEC §7.18). The zero value is a conversation without calls.
	Call CallData
}

// messagePaneInit is the expression that runs when the pane of messages is
// shown: the pane scrolls to the newest message, or to the message the page
// was opened for.
func messagePaneInit(focus uint64) string {
	if focus == 0 {
		return "el.scrollTop = el.scrollHeight"
	}
	return "el.scrollTop = el.scrollHeight; document.getElementById('" + MessageElementID(focus) + "')?.scrollIntoView({block: 'center'})"
}

// messageResultTime is the time of a message found by a search, as shown.
func messageResultTime(r app.MessageSearchResult) string {
	return postTimeText(r.CreatedAt)
}

// NewestID is the ID of the newest message of the page, 0 for none. The
// stream of the page is told it, to find out what was sent while the page was
// loading.
func (d ConversationData) NewestID() uint64 {
	if len(d.Messages) == 0 {
		return 0
	}
	return d.Messages[len(d.Messages)-1].Message.ID
}

// SeenMessageID returns the ID of the message that "Seen" shows under in a
// direct conversation: the newest message of the viewer, not deleted, among
// messages that the other member has read. It is 0 for a group, when the
// other member has read none of them, and when none is the viewer's.
func SeenMessageID(viewerID uint64, h app.ConversationHeader, messages []app.Message) uint64 {
	if h.Kind != app.ConversationDirect {
		return 0
	}
	var readUpTo uint64
	for _, m := range h.Members {
		if m.User.ID != viewerID {
			readUpTo = m.LastReadMessageID
		}
	}
	var seen uint64
	for _, m := range messages {
		if m.Author.ID == viewerID && !m.Deleted && m.ID <= readUpTo && m.ID > seen {
			seen = m.ID
		}
	}
	return seen
}

// conversationMembersText is the line under the title of a conversation: who
// else is in it.
func conversationMembersText(h app.ConversationHeader, viewerID uint64) string {
	text := ""
	for _, m := range h.Members {
		if m.User.ID == viewerID {
			continue
		}
		if text != "" {
			text += ", "
		}
		text += m.User.DisplayName
		if m.Disabled {
			text += " (disabled)"
		}
	}
	if text == "" {
		return "Only you"
	}
	return "With " + text
}

// conversationKindText says what kind of conversation a summary is.
func conversationKindText(s app.ConversationSummary) string {
	if s.Kind != app.ConversationGroup {
		return ""
	}
	return "Group of " + strconv.Itoa(len(s.Members))
}

// memberUsernames is the usernames of the members of a conversation, sorted
// and separated by spaces; the proof tool finds a conversation by them.
func memberUsernames(s app.ConversationSummary) string {
	names := make([]string, len(s.Members))
	for i, m := range s.Members {
		names[i] = m.Username
	}
	slices.Sort(names)
	return strings.Join(names, " ")
}

// lastMessageText is the line under the title of a conversation of the list:
// the author, "You" for the viewer, and the start of the message.
func lastMessageText(l *app.LastMessage, viewerID uint64) string {
	if l == nil {
		return "No messages yet"
	}
	author := l.Author.DisplayName
	if l.Author.ID == viewerID {
		author = "You"
	}
	return author + ": " + l.Preview
}

// unreadConversationsTitle is "1 conversation with unread messages" or "5
// conversations with unread messages".
func unreadConversationsTitle(n int) string {
	if n == 1 {
		return "1 conversation with unread messages"
	}
	return strconv.Itoa(n) + " conversations with unread messages"
}

// sendOnEnter is the Datastar expression of the key press in the field of the
// composer: Enter sends the message, Shift+Enter makes a new line.
const sendOnEnter = "evt.key === 'Enter' && !evt.shiftKey && !evt.isComposing && (evt.preventDefault(), el.form.requestSubmit())"

// clearReplyAction is the Datastar expression that takes the reply away from
// the composer, in the browser.
const clearReplyAction = "el.closest('#message-reply').replaceChildren()"

// loadOlderAction returns the Datastar action that loads the messages older
// than before into the conversation.
func loadOlderAction(convID, before uint64) string {
	return "@get('" + escapeQuote(ConversationOlderPath(convID, before)) + "')"
}

// getAction returns the Datastar action that fetches path.
func getAction(path string) string {
	return "@get('" + escapeQuote(path) + "')"
}

// deleteMessageConfirm is the question asked before a message is deleted.
const deleteMessageConfirm = "Delete this message? This cannot be undone."

// conversationOthers are the people of a conversation of the list other than
// the viewer, at most three, whose avatars the row shows. A conversation of one
// (only the viewer) shows the viewer.
func conversationOthers(c app.ConversationSummary, viewerID uint64) []app.UserRef {
	var others []app.UserRef
	for _, m := range c.Members {
		if m.ID != viewerID {
			others = append(others, m)
		}
	}
	if len(others) == 0 {
		others = c.Members
	}
	if len(others) > conversationAvatarMax {
		others = others[:conversationAvatarMax]
	}
	return others
}

// conversationAvatarMax is the number of avatars a row of the list shows.
const conversationAvatarMax = 3

// conversationAvatarSize is the size of the avatars of a row: one large
// avatar for a direct conversation, small ones side by side for a group.
func conversationAvatarSize(c app.ConversationSummary) string {
	if c.Kind == app.ConversationGroup {
		return "md"
	}
	return "xl"
}

// conversationOtherMembers are the members of a conversation other than the
// viewer, in the order they joined: the people the header shows.
func conversationOtherMembers(h app.ConversationHeader, viewerID uint64) []app.ConversationMemberView {
	var others []app.ConversationMemberView
	for _, m := range h.Members {
		if m.User.ID != viewerID {
			others = append(others, m)
		}
	}
	return others
}
