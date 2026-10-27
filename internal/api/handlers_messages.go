package api

import (
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/errors"
)

const (
	// messageListsInvalid is the problem with a body that names the people
	// of a conversation in both lists, or in neither.
	messageListsInvalid = "Send either user_ids or usernames, with at least one other person."
	// messageDirectName is the problem with a name for a direct conversation.
	messageDirectName = "Only a group has a name."
	// listUserIDs and listUsernames are the names of the two lists.
	listUserIDs   = "user_ids"
	listUsernames = "usernames"
)

// startRequest is the body of POST /conversations: the other people by ID or
// by username (exactly one of the lists), and the name of a group.
type startRequest struct {
	UserIDs   []uint64 `json:"user_ids"`
	Usernames []string `json:"usernames"`
	Name      *string  `json:"name"`
}

// membersRequest is the body of POST /conversations/{id}/members: the people
// to add by ID or by username (exactly one of the lists).
type membersRequest struct {
	UserIDs   []uint64 `json:"user_ids"`
	Usernames []string `json:"usernames"`
}

// renameRequest is the body of PATCH /conversations/{id}.
type renameRequest struct {
	Name *string `json:"name"`
}

// messageRequest is the body of a new message.
type messageRequest struct {
	Body      string   `json:"body"`
	ReplyToID uint64   `json:"reply_to_id"`
	UploadIDs []string `json:"upload_ids"`
}

// editMessageRequest is the body of PATCH /messages/{id}.
type editMessageRequest struct {
	Body *string `json:"body"`
}

// conversationRoutes are the routes of the conversations.
func (s *Server) conversationRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/conversations", false, s.conversations),
		s.route(http.MethodPost, "/conversations", false, s.startConversation),
		s.route(http.MethodGet, "/conversations/{id}", false, s.conversation),
		s.route(http.MethodPatch, "/conversations/{id}", false, s.renameConversation),
		s.route(http.MethodPost, "/conversations/{id}/members", false, s.addConversationMembers),
		s.route(http.MethodPost, "/conversations/{id}/leave", false, s.leaveConversation),
		s.route(http.MethodGet, "/conversations/{id}/messages", false, s.history),
		s.route(http.MethodPost, "/conversations/{id}/messages", false, s.sendMessage),
		s.route(http.MethodPost, "/conversations/{id}/read", false, s.markConversationRead),
	}
}

// messageRoutes are the routes of single messages, their reactions and the
// download of their files.
func (s *Server) messageRoutes() []Route {
	return []Route{
		s.route(http.MethodPatch, "/messages/{id}", false, s.editMessage),
		s.route(http.MethodDelete, "/messages/{id}", false, s.deleteMessage),
		s.route(http.MethodPut, "/messages/{id}/reactions/{emoji}", false, s.putMessageReaction),
		s.route(http.MethodDelete, "/messages/{id}/reactions/{emoji}", false, s.deleteMessageReaction),
		s.route(http.MethodGet, "/messages/attachments/{id}", false, s.messageAttachment),
	}
}

// conversations answers GET /conversations: the conversations the caller is a
// member of, the most recent activity first.
func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	list, err := s.svc.Messages.Conversations(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := conversationListResponse{Conversations: make([]conversationJSON, len(list))}
	for i, conv := range list {
		out.Conversations[i] = conversationOf(conv)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// peopleOf resolves the two lists of a request to the IDs of the other people.
// Exactly one of them must hold someone (tagListsInvalid otherwise). A list of
// usernames is resolved by the service (a name that belongs to no active user
// fails with its message). The caller's own ID is left out, and repeats are
// dropped. list names the list the request used, for the field of a problem.
func (s *Server) peopleOf(r *http.Request, actor app.Actor, ids []uint64, usernames []string) (others []uint64, list string, err error) {
	if (len(ids) > 0) == (len(usernames) > 0) {
		return nil, "", errors.New(tagListsInvalid, "the people are named in both lists, or in neither",
			"fields", map[string]string{listUserIDs: messageListsInvalid})
	}
	list = listUserIDs
	if len(usernames) > 0 {
		list = listUsernames
		if ids, err = s.svc.Messages.ResolveUsernames(r.Context(), actor, usernames); err != nil {
			return nil, list, renameField(renameField(err, app.FieldUsers, list), app.FieldUser, list)
		}
	}
	seen := map[uint64]bool{}
	for _, id := range ids {
		if id != actor.UserID && !seen[id] {
			seen[id] = true
			others = append(others, id)
		}
	}
	return others, list, nil
}

// startConversation answers POST /conversations: one other person opens the
// direct conversation with them (200 when it existed, 201 when it is new);
// several make a group (201), with the optional name. The answer is the
// conversation.
func (s *Server) startConversation(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body startRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	others, list, err := s.peopleOf(r, c.actor, body.UserIDs, body.Usernames)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	name := ""
	if body.Name != nil {
		name = *body.Name
	}
	if err := checkStart(others, list, name); err != nil {
		s.writeError(w, r, err)
		return
	}
	var id uint64
	created := true
	if len(others) == 1 {
		id, created, err = s.svc.Messages.OpenDirect(r.Context(), c.actor, others[0])
		err = renameField(err, app.FieldUser, list)
	} else {
		id, err = s.svc.Messages.StartGroup(r.Context(), c.actor, others, name)
		err = renameField(err, app.FieldUsers, list)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	w.Header().Set("Location", Prefix+"/conversations/"+strconv.FormatUint(id, 10))
	s.writeConversation(w, r, c.actor, id, status)
}

// checkStart checks what a request to start a conversation asks for, once the
// people are known: someone other than the caller (tagNoOthers), and a name
// only for a group (tagDirectName). list names the list the request used.
func checkStart(others []uint64, list, name string) error {
	switch {
	case len(others) == 0:
		return errors.New(tagNoOthers, "the list names no one but the caller",
			"fields", map[string]string{list: messageListsInvalid})
	case len(others) == 1 && name != "":
		return errors.New(tagDirectName, "a direct conversation has no name",
			"fields", map[string]string{"name": messageDirectName})
	}
	return nil
}

// requiredText returns the text of a field that the body must have: a field
// that is not there (or null) fails with tagFieldMissing.
func requiredText(field string, v *string) (string, error) {
	if v == nil {
		return "", errors.New(tagFieldMissing, field+" is missing",
			"fields", map[string]string{field: messageFieldMissing})
	}
	return *v, nil
}

// writeConversation reads the conversation for the actor and answers with it.
func (s *Server) writeConversation(w http.ResponseWriter, r *http.Request, actor app.Actor, id uint64, status int) {
	conv, err := s.svc.Messages.Summary(r.Context(), actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, status, conversationOf(conv))
}

// conversation answers GET /conversations/{id}: a conversation the caller is a
// member of. Anyone else, admins included, and a member who left get 404.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeConversation(w, r, c.actor, id, http.StatusOK)
}

// renameConversation answers PATCH /conversations/{id}: a member renames a
// group; an empty name takes the name away. The answer is the conversation.
func (s *Server) renameConversation(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body renameRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	name, err := requiredText("name", body.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Messages.Rename(r.Context(), c.actor, id, name); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeConversation(w, r, c.actor, id, http.StatusOK)
}

// addConversationMembers answers POST /conversations/{id}/members: a member
// adds people to a group. The answer is the conversation.
func (s *Server) addConversationMembers(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body membersRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	others, list, err := s.peopleOf(r, c.actor, body.UserIDs, body.Usernames)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Messages.AddMembers(r.Context(), c.actor, id, others); err != nil {
		s.writeError(w, r, renameField(err, app.FieldUsers, list))
		return
	}
	s.writeConversation(w, r, c.actor, id, http.StatusOK)
}

// leaveConversation answers POST /conversations/{id}/leave: a member leaves a
// group. A direct conversation is left by nobody (422).
func (s *Server) leaveConversation(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Messages.Leave(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// history answers GET /conversations/{id}/messages?before=&limit=: a page of
// the messages, oldest first within the page, 50 by default and 100 at most.
// next_before is the before of the next, older page, null on the last.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	before, limit, err := feedParams(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.svc.Messages.History(r.Context(), c.actor, id, before, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := historyResponse{Messages: make([]messageJSON, len(page.Messages))}
	for i, m := range page.Messages {
		if out.Messages[i], err = s.messageOf(m); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	if page.HasOlder {
		next := page.NextBefore
		out.NextBefore = &next
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// sendMessage answers POST /conversations/{id}/messages: a member writes a
// message, optionally a reply and with uploaded files.
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body messageRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	uploads, err := s.svc.Messages.StagedUploads(r.Context(), c.actor, body.UploadIDs)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	msg, err := s.svc.Messages.Send(r.Context(), c.actor, id, app.MessageInput{
		Body: body.Body, ReplyToID: body.ReplyToID, Uploads: uploads,
	})
	s.svc.Messages.ForgetStaged(r.Context(), c.actor, uploads)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.messageOf(msg)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, out)
}

// markConversationRead answers POST /conversations/{id}/read: the conversation
// is read up to the message up_to_id, which must be one of it.
func (s *Server) markConversationRead(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body notificationsReadRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	upTo, err := upToID(body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Messages.MarkRead(r.Context(), c.actor, id, upTo); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// editMessage answers PATCH /messages/{id}: the author changes the text and
// gets the message back.
func (s *Server) editMessage(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body editMessageRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	text, err := requiredText("body", body.Body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	msg, err := s.svc.Messages.Edit(r.Context(), c.actor, id, text)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.messageOf(msg)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// deleteMessage answers DELETE /messages/{id}: the author deletes a message,
// which stays in its place as deleted.
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Messages.Delete(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// putMessageReaction answers PUT /messages/{id}/reactions/{emoji}: a member
// puts the emoji on the message. Putting one that is there is not an error.
// The answer is the reactions of the message.
func (s *Server) putMessageReaction(w http.ResponseWriter, r *http.Request) {
	s.setMessageReaction(w, r, true)
}

// deleteMessageReaction answers DELETE /messages/{id}/reactions/{emoji}: a
// member takes the emoji off. Taking off one that is not there is not an
// error. The answer is the reactions of the message.
func (s *Server) deleteMessageReaction(w http.ResponseWriter, r *http.Request) {
	s.setMessageReaction(w, r, false)
}

// setMessageReaction is putMessageReaction and deleteMessageReaction.
func (s *Server) setMessageReaction(w http.ResponseWriter, r *http.Request, present bool) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	got, err := s.svc.Messages.SetReaction(r.Context(), c.actor, id, r.PathValue("emoji"), present)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, reactionsResponse{Reactions: reactionsOf(got)})
}

// messageAttachment answers GET /messages/attachments/{id} with the file of an
// attachment of a message of a conversation the caller is a member of: the
// same headers as the download of a post's attachment (files.ServeDownload).
// Anyone else, admins included, a missing attachment and one of a deleted
// message get 404.
func (s *Server) messageAttachment(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	att, f, err := s.svc.Messages.Attachment(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Debug("closing a download failed", "pattern", r.Pattern, "err", closeErr)
		}
	}()
	files.ServeDownload(w, r, files.Download{
		FileName:    att.FileName,
		ContentType: att.ContentType,
		Inline:      att.IsImage,
		ModTime:     att.CreatedAt,
	}, f)
}

const (
	tagListsInvalid = "ListsInvalid"
	tagNoOthers     = "NoOtherPeople"
	tagDirectName   = "DirectNameInvalid"
)
