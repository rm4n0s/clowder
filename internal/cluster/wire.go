package cluster

import (
	"encoding/json"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// wireMessage is what travels on EventsChannel: the event and the id of the
// process that published it, so that the process skips its own events.
type wireMessage struct {
	Origin string    `json:"origin"`
	Event  wireEvent `json:"event"`
}

// wireEvent is live.Event on the wire. It has its own JSON names so that the
// format does not change with the Go type.
type wireEvent struct {
	Topic          string `json:"topic"`
	Kind           string `json:"kind"`
	RoomID         uint64 `json:"room_id,omitempty"`
	PostID         uint64 `json:"post_id,omitempty"`
	RootID         uint64 `json:"root_id,omitempty"`
	UserID         uint64 `json:"user_id,omitempty"`
	ConversationID uint64 `json:"conversation_id,omitempty"`
	MessageID      uint64 `json:"message_id,omitempty"`
	NotificationID uint64 `json:"notification_id,omitempty"`
	Ring           bool   `json:"ring,omitempty"`
	Sound          string `json:"sound,omitempty"`
	Browser        bool   `json:"browser,omitempty"`
}

// wireOf converts an event for the wire.
func wireOf(ev live.Event) wireEvent {
	return wireEvent{
		Topic:          ev.Topic,
		Kind:           ev.Kind,
		RoomID:         ev.RoomID,
		PostID:         ev.PostID,
		RootID:         ev.RootID,
		UserID:         ev.UserID,
		ConversationID: ev.ConversationID,
		MessageID:      ev.MessageID,
		NotificationID: ev.NotificationID,
		Ring:           ev.Ring,
		Sound:          ev.Sound,
		Browser:        ev.Browser,
	}
}

// event converts the wire form back.
func (w wireEvent) event() live.Event {
	return live.Event{
		Topic:          w.Topic,
		Kind:           w.Kind,
		RoomID:         w.RoomID,
		PostID:         w.PostID,
		RootID:         w.RootID,
		UserID:         w.UserID,
		ConversationID: w.ConversationID,
		MessageID:      w.MessageID,
		NotificationID: w.NotificationID,
		Ring:           w.Ring,
		Sound:          w.Sound,
		Browser:        w.Browser,
	}
}

// decodeMessage reads a message of EventsChannel. The error never holds the
// payload.
func decodeMessage(payload []byte) (string, live.Event, error) {
	var msg wireMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return "", live.Event{}, errors.NewErr(TagEventMalformed, err).SetMetadata("bytes", len(payload))
	}
	if msg.Origin == "" || msg.Event.Topic == "" || msg.Event.Kind == "" {
		return "", live.Event{}, errors.New(TagEventMalformed, "the message has no origin, topic or kind",
			"bytes", len(payload))
	}
	return msg.Origin, msg.Event.event(), nil
}

// TagEventMalformed is the tag of a message of the Redis channel that is not a valid event.
const TagEventMalformed = "EventMalformed"
