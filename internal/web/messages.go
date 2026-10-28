package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// Element selectors of the page of a conversation.
const (
	messageListSelector     = "#message-list"
	messagesOlderSelector   = "#messages-older"
	composerFormSelector    = "#message-composer-form"
	messageSeenMarkSelector = ".message-seen"
)

// messageSelector is the selector of the item of a message in the list.
func messageSelector(id uint64) string {
	return "#" + views.MessageElementID(id)
}

// duplicateMessageSelector finds the copies of the item of a message that come
// after the first one. Removing them leaves one item, the first.
func duplicateMessageSelector(id uint64) string {
	return messageSelector(id) + " ~ " + messageSelector(id)
}

// usernamesOf splits the text of a field of usernames at spaces and commas and
// takes a leading @ off each name.
func usernamesOf(text string) []string {
	names := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	for i, n := range names {
		names[i] = strings.TrimPrefix(n, "@")
	}
	return names
}

// conversationID reads the ID of the conversation from the path. When it is not
// a number it has answered with the 404 page and returns false.
func (s *Server) conversationID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 63)
	if err != nil || id == 0 {
		s.notFound(w, r)
		return 0, false
	}
	return id, true
}

// messageID reads the ID of the message from the path. When it is not a number
// it has answered with the 404 page and returns false.
func (s *Server) messageID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("msgID"), 10, 63)
	if err != nil || id == 0 {
		s.notFound(w, r)
		return 0, false
	}
	return id, true
}

// conversationFail answers a failed read of a conversation: the error page,
// except that a Datastar request for a conversation that is gone, or that the
// person is no member of, is sent to the page of the conversation, which says
// so (the client drops the body of a 404).
func (s *Server) conversationFail(w http.ResponseWriter, r *http.Request, err error, id uint64) {
	if isDatastar(r) && isTag(err, app.TagNotFound) {
		s.redirect(w, r, views.ConversationPath(id), http.StatusSeeOther)
		return
	}
	s.fail(w, r, err)
}

// messageFail answers a failed read of a message the same way, sending a
// Datastar request to the list of conversations (the conversation of a message
// that is missing is not known).
func (s *Server) messageFail(w http.ResponseWriter, r *http.Request, err error) {
	if isDatastar(r) && isTag(err, app.TagNotFound) {
		s.redirect(w, r, views.MessagesPath, http.StatusSeeOther)
		return
	}
	s.fail(w, r, err)
}

// messageViewer makes the views of the messages of one conversation for one
// person.
type messageViewer struct {
	s        *Server
	viewerID uint64
	direct   bool
	canSend  bool
}

// viewerOf makes the messageViewer of the header of a conversation.
func (s *Server) viewerOf(actor app.Actor, h app.ConversationHeader) messageViewer {
	return messageViewer{s: s, viewerID: actor.UserID, direct: h.Kind == app.ConversationDirect, canSend: h.CanSend}
}

// data makes the view of a message: its body rendered as text (D33). seenID is
// the message "Seen" shows under, 0 for none.
func (v messageViewer) data(m app.Message, seenID uint64) (views.MessageData, error) {
	html, err := v.s.md.Render(markdown.FormatText, m.Body)
	if err != nil {
		return views.MessageData{}, err
	}
	return views.MessageData{
		Message:  m,
		HTML:     html,
		Own:      m.Author.ID == v.viewerID,
		Direct:   v.direct,
		Seen:     seenID != 0 && m.ID == seenID,
		CanReply: v.canSend && !m.Deleted,
	}, nil
}

// list makes the views of messages.
func (v messageViewer) list(ms []app.Message, seenID uint64) ([]views.MessageData, error) {
	out := make([]views.MessageData, len(ms))
	for i, m := range ms {
		d, err := v.data(m, seenID)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}

// conversationView reads a conversation for the actor and makes the data of its
// page without the sidebar: the header, the page of messages that starts after
// before (0 for the newest) and an empty composer.
func (s *Server) conversationView(ctx context.Context, actor app.Actor, id, before uint64) (views.ConversationData, error) {
	h, err := s.svc.Messages.Conversation(ctx, actor, id)
	if err != nil {
		return views.ConversationData{}, err
	}
	page, err := s.svc.Messages.History(ctx, actor, id, before, 0)
	if err != nil {
		return views.ConversationData{}, err
	}
	items, err := s.viewerOf(actor, h).list(page.Messages, views.SeenMessageID(actor.UserID, h, page.Messages))
	if err != nil {
		return views.ConversationData{}, err
	}
	prefs, err := s.placePrefsData(ctx, actor, app.ConversationRef(id), h.DisplayName)
	if err != nil {
		return views.ConversationData{}, err
	}
	return views.ConversationData{
		ViewerID: actor.UserID, Header: h, Messages: items,
		HasOlder: page.HasOlder, NextBefore: page.NextBefore, Before: before,
		Composer: views.ComposerData{ConversationID: id, CanSend: h.CanSend},
		Members:  views.MembersPanelData{Header: h, ViewerID: actor.UserID, Online: s.onlineSet(ctx, actor, memberIDsOf(h))},
		Prefs:    prefs,
	}, nil
}

// conversationPageData is conversationView with the sidebar of the person.
// change may change the data before it is used. When a read fails it has
// answered (see conversationFail) and returns false.
func (s *Server) conversationPageData(w http.ResponseWriter, r *http.Request, id, before uint64, change func(*views.ConversationData)) (views.ConversationData, bool) {
	actor, _ := actorFrom(r.Context())
	d, err := s.conversationView(r.Context(), actor, id, before)
	if err != nil {
		s.conversationFail(w, r, err, id)
		return views.ConversationData{}, false
	}
	return s.withSidebar(w, r, d, change)
}

// withSidebar adds the sidebar to the data of a conversation page, after
// change has changed it, and reports whether it could.
func (s *Server) withSidebar(w http.ResponseWriter, r *http.Request, d views.ConversationData, change func(*views.ConversationData)) (views.ConversationData, bool) {
	if change != nil {
		change(&d)
	}
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return views.ConversationData{}, false
	}
	d.Sidebar = sb
	return d, true
}

// pageContaining is conversationPageData for a page that has to show the
// message msgID: the newest page, or when it is older, the page that ends with
// it.
func (s *Server) pageContaining(w http.ResponseWriter, r *http.Request, convID, msgID uint64, change func(*views.ConversationData)) (views.ConversationData, bool) {
	actor, _ := actorFrom(r.Context())
	d, err := s.conversationView(r.Context(), actor, convID, 0)
	if err == nil && d.HasOlder && !hasMessage(d, msgID) {
		d, err = s.conversationView(r.Context(), actor, convID, msgID+1)
	}
	if err != nil {
		s.conversationFail(w, r, err, convID)
		return views.ConversationData{}, false
	}
	return s.withSidebar(w, r, d, change)
}

// hasMessage reports whether the page of a conversation shows the message.
func hasMessage(d views.ConversationData, id uint64) bool {
	for _, m := range d.Messages {
		if m.Message.ID == id {
			return true
		}
	}
	return false
}

// noticeOn puts a notice above the message id on the page.
func noticeOn(d *views.ConversationData, id uint64, notice string) {
	for i := range d.Messages {
		if d.Messages[i].Message.ID == id {
			d.Messages[i].Notice = notice
		}
	}
}

// messagesData reads the conversations of the person and makes the data of the
// Messages page.
func (s *Server) messagesData(w http.ResponseWriter, r *http.Request, form views.NewConversationData) (views.ConversationsData, bool) {
	actor, _ := actorFrom(r.Context())
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return views.ConversationsData{}, false
	}
	items, err := s.svc.Messages.Conversations(r.Context(), actor)
	if err != nil {
		s.fail(w, r, err)
		return views.ConversationsData{}, false
	}
	return views.ConversationsData{Sidebar: sb, ViewerID: actor.UserID, Items: items, New: form}, true
}

// messagesPage answers GET /messages: the conversations of the person, the most
// recent first, and the form that starts one. With the query parameter "q" it
// is a search of the person's messages instead (SPEC §7.11, D56): the results
// replace the list, and "before" asks for the older results. A query the search
// refuses shows its message under the box with status 422.
func (s *Server) messagesPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q != "" {
		s.messagesSearchPage(w, r, q)
		return
	}
	d, ok := s.messagesData(w, r, views.NewConversationData{})
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.MessagesPage(d))
}

// messagesSearchPage answers GET /messages?q=: the page of the messages found
// for the query q.
func (s *Server) messagesSearchPage(w http.ResponseWriter, r *http.Request, q string) {
	actor, _ := actorFrom(r.Context())
	d := views.ConversationsData{ViewerID: actor.UserID, Search: views.MessageSearchData{Active: true, Query: q}}
	status := http.StatusOK
	page, err := s.svc.Messages.Search(r.Context(), actor, q, beforeOf(r))
	switch {
	case err == nil:
		d.Search.Results, d.Search.HasMore, d.Search.NextBefore = page.Results, page.HasMore, page.NextBefore
	case isTag(err, app.TagInvalidInput):
		status = http.StatusUnprocessableEntity
		d.Search.Message = formErrorsOf(err).Fields[app.FieldQuery]
		if d.Search.Message == "" {
			d.Search.Message = messageInvalid
		}
	default:
		s.fail(w, r, err)
		return
	}
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return
	}
	d.Sidebar = sb
	s.render(w, r, status, views.MessagesPage(d))
}

// conversationStart answers POST /messages with the form values "users" (the
// usernames, separated by spaces or commas) and "name" (of a group): one other
// person opens the direct conversation with them, making it if there is none;
// several make a group (D33). The person is sent to the conversation. A mistake
// shows the problems in the form and keeps what was typed. The form value
// "from" is views.FromRoomPrefix and a room's ID for the "Message" button of the
// member list of a room, which shows a mistake in that list.
func (s *Server) conversationStart(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	users, name := r.PostFormValue("users"), r.PostFormValue("name")
	id, err := s.startConversation(r.Context(), actor, usernamesOf(users), name)
	if err != nil {
		s.conversationStartFailed(w, r, err, users, name)
		return
	}
	s.redirect(w, r, views.ConversationPath(id), http.StatusSeeOther)
}

// startConversation resolves the usernames and starts the conversation: direct
// with one other person, a group with several. The person named alone, or the
// person's own name among others, is not counted.
func (s *Server) startConversation(ctx context.Context, actor app.Actor, usernames []string, name string) (uint64, error) {
	ids, err := s.svc.Messages.ResolveUsernames(ctx, actor, usernames)
	if err != nil {
		return 0, err
	}
	others := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id != actor.UserID {
			others = append(others, id)
		}
	}
	switch len(others) {
	case 0:
		return 0, errors.New(app.TagInvalidInput, "a conversation needs another person",
			"fields", map[string]string{app.FieldUsers: "Name someone other than yourself."})
	case 1:
		return s.svc.Messages.StartDirect(ctx, actor, others[0])
	default:
		return s.svc.Messages.StartGroup(ctx, actor, others, name)
	}
}

// conversationStartFailed shows the problems of a conversation that was not
// started.
func (s *Server) conversationStartFailed(w http.ResponseWriter, r *http.Request, err error, users, name string) {
	if roomID, ok := roomOfForm(r); ok && !isTag(err, app.TagUnauthenticated) {
		s.roomActionFailed(w, r, err, roomID, targetMembers)
		return
	}
	// A plain post gets the whole page back, so it needs the conversations.
	var base views.ConversationsData
	if !isDatastar(r) && !isTag(err, app.TagUnauthenticated) {
		var ok bool
		if base, ok = s.messagesData(w, r, views.NewConversationData{}); !ok {
			return
		}
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		if problems.Fields == nil {
			problems.Fields = map[string]string{}
		}
		// A direct conversation names its person under "user".
		problems.Fields[app.FieldUsers] = firstNonEmpty(problems.Fields[app.FieldUsers], problems.Fields[app.FieldUser])
		form := views.NewConversationData{Users: users, Name: name, Errors: problems}
		base.New = form
		return formView{fragment: views.NewConversationForm(form), page: views.MessagesPage(base)}
	})
}

// roomOfForm reads the room of the "Message" button the request comes from.
func roomOfForm(r *http.Request) (uint64, bool) {
	from, ok := strings.CutPrefix(r.PostFormValue("from"), views.FromRoomPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseUint(from, 10, 63)
	return id, err == nil && id != 0
}

// conversationPage answers GET /messages/{id}?before=<id>&reply=<id>: the
// conversation with its newest messages (or the page that ends before that
// message), for a member; anyone else, admins included, gets the 404 page.
// Showing the newest messages marks the conversation read up to the newest one
// (D33), and the sidebar shows the count after that. reply sets the composer to
// reply to that message, which is what the link "Reply" asks for without
// JavaScript.
func (s *Server) conversationPage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	before := beforeOf(r)
	actor, _ := actorFrom(r.Context())
	d, err := s.conversationView(r.Context(), actor, id, before)
	if err != nil {
		s.conversationFail(w, r, err, id)
		return
	}
	// A search result opens the conversation at its message ("msg"): the
	// newest page when the message is on it, else the page that ends with it.
	if focus := uintQuery(r, "msg"); focus != 0 && before == 0 {
		if d.HasOlder && !hasMessage(d, focus) {
			d, err = s.conversationView(r.Context(), actor, id, focus+1)
			if err != nil {
				s.conversationFail(w, r, err, id)
				return
			}
			before = d.Before
		}
		if hasMessage(d, focus) {
			d.Focus = focus
		}
	}
	if before == 0 && d.NewestID() != 0 {
		if err := s.svc.Messages.MarkRead(r.Context(), actor, id, d.NewestID()); err != nil {
			s.conversationFail(w, r, err, id)
			return
		}
	}
	d, ok = s.withSidebar(w, r, d, func(d *views.ConversationData) {
		if q := s.replyQuote(r.Context(), actor, id, uintQuery(r, "reply")); q != nil && d.Header.CanSend {
			d.Composer.Reply = q
		}
	})
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.ConversationPage(d))
}

// replyQuote returns the quote of the message a reply is to answer, or nil when
// it is no message of the conversation or is deleted.
func (s *Server) replyQuote(ctx context.Context, actor app.Actor, convID, msgID uint64) *app.MessageQuote {
	if msgID == 0 {
		return nil
	}
	m, err := s.svc.Messages.Message(ctx, actor, msgID)
	if err != nil || m.ConversationID != convID || m.Deleted {
		return nil
	}
	q := m.Quote()
	return &q
}

// conversationOlder answers GET /messages/{id}/older?before=<id>: the messages
// older than that message, which the list takes on top, and the new top of the
// list (the sentinel of the next page, or nothing). A request that is not from
// Datastar is sent to the page of the conversation that shows the same
// messages.
func (s *Server) conversationOlder(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	before := beforeOf(r)
	if !isDatastar(r) {
		target := views.ConversationPath(id)
		if before != 0 {
			target = views.ConversationBeforePath(id, before)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	actor, _ := actorFrom(r.Context())
	d, err := s.conversationView(r.Context(), actor, id, before)
	if err != nil {
		s.conversationFail(w, r, err, id)
		return
	}
	s.patchAll(w, r,
		patchOp{
			component: views.MessageItems(d.Messages),
			options:   []datastar.PatchElementOption{datastar.WithSelector(messageListSelector), datastar.WithModePrepend()},
		},
		// Replaced, not morphed, so that the new sentinel is a new element and
		// Datastar watches it afresh.
		patchOp{
			component: views.MessagesOlder(id, d.HasOlder, d.NextBefore),
			options:   []datastar.PatchElementOption{datastar.WithSelector(messagesOlderSelector), datastar.WithModeReplace()},
		},
	)
}

// appendMessageOps put the item of a message at the end of the list and then
// remove the copies after the first, whatever the order in which the answer to
// the sender's form and the live event of the stream are applied by the client:
// the last of the removals comes after both insertions.
func appendMessageOps(item views.MessageData) []patchOp {
	return []patchOp{
		{
			component: views.MessageItem(item),
			options:   []datastar.PatchElementOption{datastar.WithSelector(messageListSelector), datastar.WithModeAppend()},
		},
		{remove: duplicateMessageSelector(item.Message.ID)},
	}
}

// messageSend answers POST /messages/{id} with the form values "body" and
// "reply_to" and the files "files" (a multipart form, or a urlencoded one
// without files): the message is sent. A Datastar request gets the message at
// the end of the list and a fresh, empty composer; a plain post is sent back to
// the conversation. A mistake shows the problems in the composer and keeps what
// was typed.
func (s *Server) messageSend(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	uploads, ok, err := s.readUploadForm(w, r, true, s.messageUploader())
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	var msg app.Message
	if err == nil {
		body := r.PostFormValue("body")
		if n := s.groupMentionToConfirm(r, body, func() (int, error) {
			return s.svc.Messages.GroupMentionCount(r.Context(), actor, id, body)
		}); n > 0 {
			s.svc.Messages.DiscardUploads(r.Context(), actor, uploads)
			s.messageSendAsk(w, r, id, n)
			return
		}
		msg, err = s.svc.Messages.Send(r.Context(), actor, id, app.MessageInput{
			Body: r.PostFormValue("body"), ReplyToID: idOfText(r.PostFormValue("reply_to")), Uploads: uploads,
		})
	}
	if err != nil {
		s.messageSendFailed(w, r, err, id)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.ConversationPath(id), http.StatusSeeOther)
		return
	}
	h, err := s.svc.Messages.Conversation(r.Context(), actor, id)
	if err != nil {
		s.conversationFail(w, r, err, id)
		return
	}
	item, err := s.viewerOf(actor, h).data(msg, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ops := appendMessageOps(item)
	// Replaced, not morphed: a morph leaves the text the person typed in the
	// fields, and the composer must be empty again.
	ops = append(ops, patchOp{
		component: views.MessageComposerForm(views.ComposerData{ConversationID: id, CanSend: h.CanSend}),
		options:   []datastar.PatchElementOption{datastar.WithSelector(composerFormSelector), datastar.WithModeReplace()},
	})
	s.patchAll(w, r, ops...)
}

// messageSendAsk asks "This will notify n people. Send anyway?" instead of
// sending the message: a Datastar request gets the question in the composer,
// which keeps what was typed and the files chosen; a plain post gets the
// conversation page with the composer holding what was typed and the question.
func (s *Server) messageSendAsk(w http.ResponseWriter, r *http.Request, id uint64, n int) {
	if isDatastar(r) {
		s.patch(w, r, views.GroupMentionConfirm(views.ComposerConfirm(id, n)))
		return
	}
	actor, _ := actorFrom(r.Context())
	reply := s.replyQuote(r.Context(), actor, id, idOfText(r.PostFormValue("reply_to")))
	d, ok := s.conversationPageData(w, r, id, 0, nil)
	if !ok {
		return
	}
	d.Composer = views.ComposerData{
		ConversationID: id, CanSend: true, Body: r.PostFormValue("body"), Reply: reply, ConfirmCount: n,
	}
	s.render(w, r, http.StatusOK, views.ConversationPage(d))
}

// messageSendFailed shows the problems of a message that was not sent. A
// conversation that is gone or hidden is the 404 page.
func (s *Server) messageSendFailed(w http.ResponseWriter, r *http.Request, err error, id uint64) {
	if isTag(err, app.TagNotFound) {
		s.conversationFail(w, r, err, id)
		return
	}
	actor, _ := actorFrom(r.Context())
	// The quote of the message the person was replying to stays.
	reply := s.replyQuote(r.Context(), actor, id, idOfText(r.PostFormValue("reply_to")))
	var base views.ConversationData
	if !isDatastar(r) && !isTag(err, app.TagUnauthenticated) {
		var ok bool
		if base, ok = s.conversationPageData(w, r, id, 0, nil); !ok {
			return
		}
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		composer := views.ComposerData{
			ConversationID: id, CanSend: true, Body: r.PostFormValue("body"), Reply: reply, Errors: problems,
		}
		d := base
		d.Composer = composer
		return formView{fragment: views.MessageComposerForm(composer), page: views.ConversationPage(d)}
	})
}

// membersPanelFor makes the data of the members panel of a conversation.
func (s *Server) membersPanelFor(ctx context.Context, actor app.Actor, id uint64) (views.MembersPanelData, error) {
	h, err := s.svc.Messages.Conversation(ctx, actor, id)
	if err != nil {
		return views.MembersPanelData{}, err
	}
	return views.MembersPanelData{Header: h, ViewerID: actor.UserID, Online: s.onlineSet(ctx, actor, memberIDsOf(h))}, nil
}

// memberIDsOf lists the IDs of the members of a conversation.
func memberIDsOf(h app.ConversationHeader) []uint64 {
	ids := make([]uint64, len(h.Members))
	for i, m := range h.Members {
		ids[i] = m.User.ID
	}
	return ids
}

// groupChanged answers a successful change of a group: a Datastar request gets
// the members panel patched, a plain post is sent back to the conversation.
func (s *Server) groupChanged(w http.ResponseWriter, r *http.Request, id uint64) {
	if !isDatastar(r) {
		http.Redirect(w, r, views.ConversationPath(id), http.StatusSeeOther)
		return
	}
	actor, _ := actorFrom(r.Context())
	panel, err := s.membersPanelFor(r.Context(), actor, id)
	if err != nil {
		s.conversationFail(w, r, err, id)
		return
	}
	s.patchAll(w, r,
		patchOp{component: views.ConversationHeader(views.ConversationData{Header: panel.Header, ViewerID: actor.UserID})},
		patchOp{component: views.ConversationMembers(panel)},
	)
}

// groupActionFailed shows the problems of a failed action on a group in its
// members panel, with the panel made by change from the data of the page.
// A conversation that is gone or hidden is the 404 page.
func (s *Server) groupActionFailed(w http.ResponseWriter, r *http.Request, err error, id uint64, change func(*views.MembersPanelData, views.FormErrors)) {
	if isTag(err, app.TagNotFound) {
		s.conversationFail(w, r, err, id)
		return
	}
	actor, _ := actorFrom(r.Context())
	var base views.ConversationData
	var panel views.MembersPanelData
	if !isTag(err, app.TagUnauthenticated) {
		var readErr error
		if panel, readErr = s.membersPanelFor(r.Context(), actor, id); readErr != nil {
			s.conversationFail(w, r, readErr, id)
			return
		}
		if !panel.Header.IsGroup {
			// A conversation of two has no panel to show the problem in.
			s.conversationNotice(w, r, err, panel.Header)
			return
		}
		if !isDatastar(r) {
			var ok bool
			if base, ok = s.conversationPageData(w, r, id, 0, nil); !ok {
				return
			}
			panel = base.Members
		}
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		p := panel
		change(&p, problems)
		d := base
		d.Members = p
		return formView{fragment: views.ConversationMembers(p), page: views.ConversationPage(d)}
	})
}

// conversationNotice shows the problem of a failed action on a conversation
// above it, in its header: the message of the first field or of the form. A
// Datastar request gets the header patched (status 200); a plain post gets the
// whole page with the status of the problem.
func (s *Server) conversationNotice(w http.ResponseWriter, r *http.Request, err error, h app.ConversationHeader) {
	var notice string
	switch {
	case isTag(err, app.TagInvalidInput):
		problems := formErrorsOf(err)
		notice = firstNonEmpty(problems.Fields[app.FieldUsers], problems.Fields[app.FieldName], problems.Form, messageActionInvalid)
	case isTag(err, app.TagForbidden):
		notice = messageForbidden
	case isTag(err, app.TagConflict):
		notice = messageConflict
	default:
		s.logFailure(r, err)
		notice = messageTryAgain
	}
	actor, _ := actorFrom(r.Context())
	if isDatastar(r) {
		s.patch(w, r, views.ConversationHeader(views.ConversationData{Header: h, ViewerID: actor.UserID, Notice: notice}))
		return
	}
	d, ok := s.conversationPageData(w, r, h.ID, 0, func(d *views.ConversationData) { d.Notice = notice })
	if !ok {
		return
	}
	s.render(w, r, statusOf(err), views.ConversationPage(d))
}

// conversationAddMembers answers POST /messages/{id}/members with the form
// value "users": the usernames of the people to add to the group.
func (s *Server) conversationAddMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	users := r.PostFormValue("users")
	ids, err := s.svc.Messages.ResolveUsernames(r.Context(), actor, usernamesOf(users))
	if err == nil {
		err = s.svc.Messages.AddMembers(r.Context(), actor, id, ids)
	}
	if err != nil {
		s.groupActionFailed(w, r, err, id, func(p *views.MembersPanelData, problems views.FormErrors) {
			p.AddUsers, p.AddErrors = users, problems
		})
		return
	}
	s.groupChanged(w, r, id)
}

// conversationRename answers POST /messages/{id}/rename with the form value
// "name": the new name of the group; an empty one takes the name away.
func (s *Server) conversationRename(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	name := r.PostFormValue("name")
	if err := s.svc.Messages.Rename(r.Context(), actor, id, name); err != nil {
		s.groupActionFailed(w, r, err, id, func(p *views.MembersPanelData, problems views.FormErrors) {
			p.RenameTo, p.RenameOpen, p.RenameErrors = name, true, problems
		})
		return
	}
	s.groupChanged(w, r, id)
}

// conversationLeave answers POST /messages/{id}/leave: the person leaves the
// group and is sent to the list of conversations.
func (s *Server) conversationLeave(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	if err := s.svc.Messages.Leave(r.Context(), actor, id); err != nil {
		s.groupActionFailed(w, r, err, id, func(p *views.MembersPanelData, problems views.FormErrors) {
			p.Message = firstNonEmpty(problems.Form, messageActionInvalid)
		})
		return
	}
	s.redirect(w, r, views.MessagesPath, http.StatusSeeOther)
}

// messageContentFor makes the view of a message for the actor, with the
// conversation it is in.
func (s *Server) messageContentFor(ctx context.Context, actor app.Actor, m app.Message) (views.MessageData, error) {
	h, err := s.svc.Messages.Conversation(ctx, actor, m.ConversationID)
	if err != nil {
		return views.MessageData{}, err
	}
	return s.viewerOf(actor, h).data(m, 0)
}

// messageNotice shows the problem of a failed action on a message (it may not
// be edited or deleted, or the action failed) above the message. A message that
// is gone or hidden is the 404 page (a Datastar request is sent to the list of
// conversations). A Datastar request gets the content of the message patched
// (status 200); a plain post gets the page of its conversation, with the status
// of the problem.
func (s *Server) messageNotice(w http.ResponseWriter, r *http.Request, err error, msgID uint64) {
	status := statusOf(err)
	if status == http.StatusFound {
		s.redirectToLogin(w, r)
		return
	}
	if isTag(err, app.TagNotFound) {
		s.messageFail(w, r, err)
		return
	}
	var notice string
	switch {
	case isTag(err, app.TagInvalidInput):
		problems := formErrorsOf(err)
		notice = firstNonEmpty(problems.Fields[app.FieldBody], problems.Fields[app.FieldEmoji], problems.Form, messageActionInvalid)
	case isTag(err, app.TagConflict):
		notice = messageConflict
	case isTag(err, app.TagForbidden):
		notice = messageForbidden
	default:
		s.logFailure(r, err)
		notice = messageTryAgain
	}
	actor, _ := actorFrom(r.Context())
	msg, getErr := s.svc.Messages.Message(r.Context(), actor, msgID)
	if getErr != nil {
		s.messageFail(w, r, getErr)
		return
	}
	if isDatastar(r) {
		item, err := s.messageContentFor(r.Context(), actor, msg)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		item.Notice = notice
		s.patch(w, r, views.MessageContent(item))
		return
	}
	d, ok := s.pageContaining(w, r, msg.ConversationID, msgID, func(d *views.ConversationData) { noticeOn(d, msgID, notice) })
	if !ok {
		return
	}
	s.render(w, r, status, views.ConversationPage(d))
}

// messageView answers GET /messages/m/{msgID}/view, which the Cancel of the
// editor asks for: the content of the message as it is, in place of the editor.
// A plain request is sent to the message in its conversation.
func (s *Server) messageView(w http.ResponseWriter, r *http.Request) {
	msgID, ok := s.messageID(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	msg, err := s.svc.Messages.Message(r.Context(), actor, msgID)
	if err != nil {
		s.messageFail(w, r, err)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, afterMessagePath(msg), http.StatusSeeOther)
		return
	}
	item, err := s.messageContentFor(r.Context(), actor, msg)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.patch(w, r, views.MessageContent(item))
}

// afterMessagePath is where a plain form goes after an action on a message: the
// message in its conversation.
func afterMessagePath(m app.Message) string {
	return views.ConversationPath(m.ConversationID) + "#" + views.MessageElementID(m.ID)
}

// messageEditForm answers GET /messages/m/{msgID}/edit: the editor of the
// message in place of its content, for its author. Anyone else gets the 403
// page; a deleted message, and anyone who is no member of its conversation, the
// 404 page. A plain request gets the page of the conversation with the editor
// in it.
func (s *Server) messageEditForm(w http.ResponseWriter, r *http.Request) {
	msgID, ok := s.messageID(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	msg, err := s.svc.Messages.Message(r.Context(), actor, msgID)
	if err == nil && msg.Deleted {
		err = errors.New(app.TagNotFound, "the message is deleted", "reason", "deleted", "messageID", msgID)
	}
	if err == nil && !msg.CanEdit {
		err = errors.New(app.TagForbidden, "only the author edits a message", "userID", actor.UserID, "messageID", msgID)
	}
	if err != nil {
		s.messageNotice(w, r, err, msgID)
		return
	}
	item, err := s.messageContentFor(r.Context(), actor, msg)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	edit := views.MessageEditData{Message: item, Body: msg.Body}
	if isDatastar(r) {
		s.patch(w, r, views.MessageEdit(edit))
		return
	}
	d, ok := s.pageContaining(w, r, msg.ConversationID, msgID, func(d *views.ConversationData) { d.Edit = &edit })
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.ConversationPage(d))
}

// messageEditSave answers POST /messages/m/{msgID}/edit with the form value
// "body": the message is saved and shown again in place of the editor. A
// mistake shows the problems in the editor and keeps what was typed.
func (s *Server) messageEditSave(w http.ResponseWriter, r *http.Request) {
	msgID, ok := s.messageID(w, r)
	if !ok {
		return
	}
	if !s.parseBodyForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	body := r.PostFormValue("body")
	msg, err := s.svc.Messages.Edit(r.Context(), actor, msgID, body)
	if err != nil {
		s.messageEditFailed(w, r, err, msgID, body)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, afterMessagePath(msg), http.StatusSeeOther)
		return
	}
	item, err := s.messageContentFor(r.Context(), actor, msg)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.patch(w, r, views.MessageContent(item))
}

// messageEditFailed shows the problems of an edit that was not saved, in the
// editor. A message that is gone or hidden is the 404 page, and someone who is
// not its author gets the 403 page, or a notice above the message.
func (s *Server) messageEditFailed(w http.ResponseWriter, r *http.Request, err error, msgID uint64, body string) {
	if isTag(err, app.TagNotFound) || isTag(err, app.TagUnauthenticated) || isTag(err, app.TagForbidden) {
		// Someone who may not edit the message has no editor to show it in.
		s.messageNotice(w, r, err, msgID)
		return
	}
	actor, _ := actorFrom(r.Context())
	msg, getErr := s.svc.Messages.Message(r.Context(), actor, msgID)
	if getErr != nil {
		s.messageFail(w, r, getErr)
		return
	}
	item, getErr := s.messageContentFor(r.Context(), actor, msg)
	if getErr != nil {
		s.fail(w, r, getErr)
		return
	}
	var base views.ConversationData
	if !isDatastar(r) {
		var ok bool
		if base, ok = s.pageContaining(w, r, msg.ConversationID, msgID, nil); !ok {
			return
		}
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		edit := views.MessageEditData{Message: item, Body: body, Errors: problems}
		d := base
		d.Edit = &edit
		return formView{fragment: views.MessageEdit(edit), page: views.ConversationPage(d)}
	})
}

// messageDelete answers POST /messages/m/{msgID}/delete: the author deletes the
// message, which stays in its place as "message deleted". A Datastar request
// gets the content of the message patched; a plain post is sent back to the
// conversation.
func (s *Server) messageDelete(w http.ResponseWriter, r *http.Request) {
	msgID, ok := s.messageID(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	if err := s.svc.Messages.Delete(r.Context(), actor, msgID); err != nil {
		s.messageNotice(w, r, err, msgID)
		return
	}
	msg, err := s.svc.Messages.Message(r.Context(), actor, msgID)
	if err != nil {
		s.messageFail(w, r, err)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, afterMessagePath(msg), http.StatusSeeOther)
		return
	}
	item, err := s.messageContentFor(r.Context(), actor, msg)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.patch(w, r, views.MessageContent(item))
}

// messageReact answers POST /messages/m/{msgID}/react with the form value
// "emoji": the emoji is put on the message, or taken off when the person put it
// there already. A Datastar request gets the reaction bar of the message and
// its picker; a plain post is sent to the message in its conversation. Text
// that is not an emoji is reported in the picker.
func (s *Server) messageReact(w http.ResponseWriter, r *http.Request) {
	msgID, ok := s.messageID(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	emoji := r.PostFormValue("emoji")
	if _, err := s.svc.Messages.React(r.Context(), actor, msgID, emoji); err != nil {
		s.messageReactFailed(w, r, err, msgID, emoji)
		return
	}
	msg, err := s.svc.Messages.Message(r.Context(), actor, msgID)
	if err != nil {
		s.messageFail(w, r, err)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, afterMessagePath(msg), http.StatusSeeOther)
		return
	}
	// The chips, and the picker, which the person's own reaction closes and
	// empties (a reaction made by someone else leaves it alone).
	data := views.MessageReactionsData{Message: msg}
	s.patchAll(w, r,
		patchOp{component: views.MessageReactions(data)},
		patchOp{component: views.MessageReactionPicker(data)},
	)
}

// messageReactFailed shows the problem of a reaction that was not made. Text
// that is not an emoji is reported in the picker of the reaction bar (only the
// picker is patched, the chips stay); every other problem is a notice above the
// message.
func (s *Server) messageReactFailed(w http.ResponseWriter, r *http.Request, err error, msgID uint64, emoji string) {
	if !isTag(err, app.TagInvalidInput) {
		s.messageNotice(w, r, err, msgID)
		return
	}
	problem := firstNonEmpty(formErrorsOf(err).Fields[app.FieldEmoji], messageInvalid)
	if !isDatastar(r) {
		s.messageNotice(w, r, err, msgID)
		return
	}
	actor, _ := actorFrom(r.Context())
	msg, getErr := s.svc.Messages.Message(r.Context(), actor, msgID)
	if getErr != nil {
		s.messageFail(w, r, getErr)
		return
	}
	data := views.MessageReactionsData{Message: msg, Problem: problem}
	if len(emoji) <= typedEmojiMax {
		data.Typed = emoji
	}
	s.patch(w, r, views.MessageReactionPicker(data))
}

// messageReplySlot answers GET /messages/m/{msgID}/reply: the composer is set
// to reply to the message, which shows as a quote above the field, with a
// cancel button. A plain request is sent to the page of the conversation with
// the composer set the same way.
func (s *Server) messageReplySlot(w http.ResponseWriter, r *http.Request) {
	msgID, ok := s.messageID(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	msg, err := s.svc.Messages.Message(r.Context(), actor, msgID)
	if err != nil {
		s.messageFail(w, r, err)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.ConversationReplyPath(msg.ConversationID, msgID), http.StatusSeeOther)
		return
	}
	var quote *app.MessageQuote
	if !msg.Deleted {
		q := msg.Quote()
		quote = &q
	}
	s.patch(w, r, views.MessageReplySlot(quote, msg.ConversationID))
}
