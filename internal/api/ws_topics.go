package api

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// The kinds of topic a client may subscribe to (SPEC §7.13, §8).
const (
	topicRoom         = "room"
	topicPost         = "post"
	topicBoard        = "board"
	topicConversation = "conversation"
	topicUser         = "user"
)

// The problems with the topics of a request, as the "fields" of its error.
const (
	messageTopicInvalid  = "Use room:<id>, post:<id>, board:<id>, conversation:<id> or user:<your id>."
	messageTopicOwnUser  = "Only your own user topic can be used."
	messageTopicComment  = "That is a comment. Subscribe to the post of its thread."
	messageTopicsTooMany = "A connection takes at most 200 topics."
)

// wsTopic is a topic a connection listens to.
type wsTopic struct {
	// name is the topic as the client writes it and the hub knows it, such as
	// room:3.
	name string
	// kind is one of the topic* constants and id the number after the colon.
	kind string
	id   uint64
	// roomID is the room the topic is about, for the topics of a room, its
	// board and its threads; 0 for the others. It is known once the topic has
	// been checked.
	roomID uint64
}

// parseTopic reads the name of a topic. The ID is written in decimal with no
// sign and no leading zero, so that one topic has one name. A name that is not
// a topic, and the user topic of another user, fail with tagTopicInvalid and
// the metadata "fields" and "topic".
func parseTopic(name string, ownID uint64) (wsTopic, error) {
	kind, text, found := strings.Cut(name, ":")
	id, err := strconv.ParseUint(text, 10, 64)
	switch kind {
	case topicRoom, topicPost, topicBoard, topicConversation, topicUser:
	default:
		found = false
	}
	if !found || err != nil || id == 0 || strconv.FormatUint(id, 10) != text {
		return wsTopic{}, errors.New(tagTopicInvalid, "this is not a topic",
			"fields", map[string]string{"topics": messageTopicInvalid}, "topic", shortTopic(name))
	}
	if kind == topicUser && id != ownID {
		return wsTopic{}, errors.New(tagTopicInvalid, "this is the user topic of another user",
			"fields", map[string]string{"topics": messageTopicOwnUser}, "topic", shortTopic(name))
	}
	return wsTopic{name: name, kind: kind, id: id}, nil
}

// resolve checks that the user may read what the topic shows, with the
// service call that the page of the topic uses, and returns the topic with its
// room. A topic the user may not read fails with app.TagNotFound, a post topic
// that names a comment with tagTopicInvalid.
func (ws *wsSession) resolve(ctx context.Context, t wsTopic) (wsTopic, error) {
	switch t.kind {
	case topicRoom, topicBoard:
		room, err := ws.s.svc.Rooms.Get(ctx, ws.actor, t.id)
		if err != nil {
			return t, err
		}
		t.roomID = room.ID
	case topicPost:
		post, err := ws.s.svc.Posts.Get(ctx, ws.actor, t.id)
		if err != nil {
			return t, err
		}
		if post.IsComment {
			return t, errors.New(tagTopicInvalid, "the post is a comment",
				"fields", map[string]string{"topics": messageTopicComment}, "topic", t.name)
		}
		t.roomID = post.RoomID
	case topicConversation:
		if _, err := ws.s.svc.Messages.Conversation(ctx, ws.actor, t.id); err != nil {
			return t, err
		}
	}
	return t, nil
}

// plan checks a request to subscribe and returns the topics it adds, each
// resolved. It changes nothing, and it fails at the first topic that cannot be
// subscribed: a name that is not a topic or is the user topic of someone else
// (tagTopicInvalid), a request that would take the connection over the limit
// of 200 topics (tagTopicsTooMany), a topic the user may not read
// (app.TagNotFound). A topic the connection has already, and the user topic,
// are left out of the result.
func (ws *wsSession) plan(ctx context.Context, names []string) ([]wsTopic, error) {
	var added []wsTopic
	for _, name := range names {
		t, err := parseTopic(name, ws.user.ID)
		if err != nil {
			return nil, err
		}
		_, there := ws.topics[name]
		if there || t.kind == topicUser || slices.ContainsFunc(added, func(a wsTopic) bool { return a.name == name }) {
			continue
		}
		added = append(added, t)
	}
	if len(ws.topics)+len(added) > wsMaxTopics {
		return nil, errors.New(tagTopicsTooMany, "the request takes the connection over its limit of topics",
			"fields", map[string]string{"topics": messageTopicsTooMany}, "limit", wsMaxTopics)
	}
	for i, t := range added {
		resolved, err := ws.resolve(ctx, t)
		if err != nil {
			// Name the topic that failed.
			return nil, withTopic(err, t.name)
		}
		added[i] = resolved
	}
	return added, nil
}

// withTopic adds the metadata "topic" to a tagged error that has none, so that
// the answer can name the topic; it returns the error as it is.
func withTopic(err error, topic string) error {
	if appErr, ok := errors.FromError(err); ok {
		if _, has := appErr.Metadata["topic"]; !has {
			_ = appErr.SetMetadata("topic", topic)
		}
	}
	return err
}

// isNotFound reports whether err is an app.TagNotFound.
func isNotFound(err error) bool {
	appErr, ok := errors.FromError(err)
	return ok && appErr.Tag == app.TagNotFound
}

// subscribe handles a subscribe message: all the topics or none. The answer is
// an ack with the topics of the request, or one error message for the first
// topic that failed.
func (ws *wsSession) subscribe(ctx context.Context, msg wsClientMessage) error {
	added, err := ws.plan(ctx, msg.Topics)
	if err != nil {
		return ws.sendProblem(ctx, msg.ID, failedTopic(err), err)
	}
	names := make([]string, len(added))
	for i, t := range added {
		ws.topics[t.name] = t
		names[i] = t.name
	}
	// With no gap: an event published from here on reaches the connection.
	ws.sub.AddTopics(names...)
	return ws.send(ctx, wsAckMessage{Type: wsAck, ID: *msg.ID, Topics: dedupe(msg.Topics)})
}

// failedTopic returns the topic that an error of plan is about.
func failedTopic(err error) string {
	if appErr, ok := errors.FromError(err); ok {
		topic, _ := appErr.Metadata["topic"].(string)
		return topic
	}
	return ""
}

// dedupe returns the names without repeats, in order.
func dedupe(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// unsubscribe handles an unsubscribe message. A topic the connection does not
// have is ignored; the user topic cannot be removed.
func (ws *wsSession) unsubscribe(ctx context.Context, msg wsClientMessage) error {
	removed, err := ws.unplan(msg.Topics)
	if err != nil {
		return ws.sendProblem(ctx, msg.ID, failedTopic(err), err)
	}
	for _, name := range removed {
		delete(ws.topics, name)
	}
	ws.sub.RemoveTopics(removed...)
	return ws.send(ctx, wsAckMessage{Type: wsAck, ID: *msg.ID, Topics: dedupe(msg.Topics)})
}

// unplan checks a request to unsubscribe and returns the topics the
// connection has among them. It changes nothing. A name that is not a topic
// fails with tagTopicInvalid, and the user topic with tagTopicInvalid too.
func (ws *wsSession) unplan(names []string) ([]string, error) {
	var removed []string
	for _, name := range names {
		t, err := parseTopic(name, ws.user.ID)
		if err != nil {
			return nil, err
		}
		if t.kind == topicUser {
			return nil, errors.New(tagTopicInvalid, "the user topic cannot be removed",
				"fields", map[string]string{"topics": messageTopicRemoveUser}, "topic", name)
		}
		if _, there := ws.topics[name]; there && !slices.Contains(removed, name) {
			removed = append(removed, name)
		}
	}
	return removed, nil
}

// messageTopicRemoveUser is the problem with an unsubscribe of the user topic.
const messageTopicRemoveUser = "Your user topic is always subscribed."

// topicNames returns the names of the subscribed topics, sorted.
func (ws *wsSession) topicNames() []string {
	names := make([]string, 0, len(ws.topics))
	for name := range ws.topics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// recheck reads again whether the user may still read the subscribed topics
// that match, and drops those they may not: the topic is removed and the client
// gets one error message of not_found that names it. Any other failure of a
// check is returned.
func (ws *wsSession) recheck(ctx context.Context, match func(wsTopic) bool) error {
	for _, name := range ws.topicNames() {
		t := ws.topics[name]
		if !match(t) {
			continue
		}
		if _, err := ws.resolve(ctx, t); err != nil {
			if !isNotFound(err) {
				return err
			}
			if err := ws.drop(ctx, t); err != nil {
				return err
			}
		}
	}
	return nil
}

// drop removes a topic the user may no longer read and tells the client.
func (ws *wsSession) drop(ctx context.Context, t wsTopic) error {
	delete(ws.topics, t.name)
	ws.sub.RemoveTopics(t.name)
	return ws.send(ctx, wsErrorMessage{Type: wsError, Topic: t.name, Error: errorObject{Code: codeNotFound, Message: messageNotFound}})
}

// sameRoom matches the topics about a room.
func sameRoom(roomID uint64) func(wsTopic) bool {
	return func(t wsTopic) bool { return t.roomID != 0 && t.roomID == roomID }
}

// only matches the topic with the name.
func only(name string) func(wsTopic) bool {
	return func(t wsTopic) bool { return t.name == name }
}

const (
	tagTopicInvalid  = "TopicInvalid"
	tagTopicsTooMany = "TopicsTooMany"
)
