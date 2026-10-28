package app

import (
	"context"
	"strings"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// PlaceKind says what kind of place an event happened in (SPEC §7.15).
type PlaceKind int

const (
	// PlaceRoom is a room.
	PlaceRoom PlaceKind = iota
	// PlaceDirect is a direct conversation of two users.
	PlaceDirect
	// PlaceGroup is a group conversation.
	PlaceGroup
)

// EventKind says what happened, as far as the ringing decision cares.
type EventKind int

const (
	// EventNotification is a notification of §7.9: a mention, a reply, an
	// assignment or an addition to a room. It always reaches the bell; the
	// decision is only about the sound and the browser notification.
	EventNotification EventKind = iota
	// EventPost is a new top-level post in a room.
	EventPost
	// EventMessage is a new message in a conversation.
	EventMessage
)

// Ring is the outcome of the ringing decision for one user and one event: the
// server puts it in the event for that user, and the page only obeys it.
type Ring struct {
	// Rings is true when the event rings at all: the place's choice of what
	// notifies covers it and the place is not muted.
	Rings bool
	// Sound is the name of the sound to play; empty when the event rings
	// without a sound (the user turned it off, globally or in the place), and
	// always empty when Rings is false.
	Sound SoundName
	// Browser is true when a browser notification is wanted: the event rings
	// and the user turned the browser notifications on.
	Browser bool
}

// ringFor decides whether an event rings for a user, and how (SPEC §7.15). It
// is the single source of truth: every place that sends an event to a user's
// stream calls it, and the page does no deciding of its own.
//
//   - A muted place rings for nothing, not even a mention (the mention still
//     reaches the bell, which is not this function's business).
//   - The choice of the place, when it is not "default", wins over the global
//     one, as in Mattermost: a place set to All or Mentions rings even when
//     the global setting is Nothing, and a place set to Nothing is silent
//     even when the global setting is All.
//   - A place on "default" follows the global setting. In a conversation the
//     global Mentions means every message (SPEC §7.15: "mentions, direct
//     messages and group messages"), so only the global Nothing silences it.
//   - What each choice covers: Nothing covers nothing; Mentions covers the
//     notifications of §7.9 and the messages that mention the user; All also
//     covers every new post and message.
//   - The sound plays when the place's Sound is on, or default and the
//     global sound is on.
//
// mentionsUser is true when a message mentions the user (@username); a
// notification is a mention-like event by itself.
func ringFor(global store.UserSettings, place store.PlacePref, kind PlaceKind, event EventKind, mentionsUser bool) Ring {
	if place.Muted {
		return Ring{}
	}
	level := effectiveLevel(global, place, kind)
	var covered bool
	switch event {
	case EventNotification:
		covered = level != NotifyNothing
	case EventPost:
		covered = level == NotifyAll
	case EventMessage:
		covered = level == NotifyAll || (level == NotifyMentions && mentionsUser)
	}
	if !covered {
		return Ring{}
	}
	out := Ring{Rings: true, Browser: global.BrowserNotifications}
	sound := global.Sound
	switch place.Sound {
	case store.PlaceSoundOn:
		sound = true
	case store.PlaceSoundOff:
		sound = false
	}
	if sound {
		out.Sound = global.SoundName
		if out.Sound == "" {
			out.Sound = SoundPurr
		}
	}
	return out
}

// effectiveLevel is what the user is notified about in the place: the place's
// own choice, or what the default means for that kind of place.
func effectiveLevel(global store.UserSettings, place store.PlacePref, kind PlaceKind) NotifyLevel {
	switch place.Notify {
	case store.PlaceNotifyAll:
		return NotifyAll
	case store.PlaceNotifyMentions:
		return NotifyMentions
	case store.PlaceNotifyNothing:
		return NotifyNothing
	}
	if kind != PlaceRoom && global.Notify != NotifyNothing {
		// Messages of a conversation: "mentions" includes every one of them.
		return NotifyAll
	}
	return global.Notify
}

// ringEvent applies a decision to the live event for one user: when the event
// rings it sets Ring, Sound and Browser on it. It returns false, leaving the
// event alone, when it does not ring.
func ringEvent(ev *live.Event, r Ring) bool {
	if !r.Rings {
		return false
	}
	ev.Ring = true
	ev.Sound = string(r.Sound)
	ev.Browser = r.Browser
	return true
}

// postRingEvents are the events that ring the members of a room for a new
// top-level post: one on the topic of each member, except the author and the
// users in skip (who have a notification of the same action already), whose
// choice for the room is All and who are not muted (SPEC §7.15). The settings
// and the room preferences of all the members come from one query. The event
// holds only the IDs; the pages read the place's name and the author, never
// the body, when they render it.
func postRingEvents(ctx context.Context, tx *store.Store, roomID, postID, authorID uint64, skip map[uint64]bool) ([]live.Event, error) {
	recipients, err := tx.RoomMemberRecipients(ctx, roomID, authorID)
	if err != nil {
		return nil, err
	}
	var events []live.Event
	for _, r := range recipients {
		if skip[r.UserID] {
			continue
		}
		ev := live.Event{Topic: live.UserTopic(r.UserID), Kind: live.KindPostCreated, RoomID: roomID, PostID: postID, UserID: r.UserID}
		if ringEvent(&ev, ringFor(r.Settings, r.Place, PlaceRoom, EventPost, false)) {
			events = append(events, ev)
		}
	}
	return events, nil
}

// usersOf returns the set of the users that the events concern (Event.UserID).
func usersOf(events []live.Event) map[uint64]bool {
	set := make(map[uint64]bool, len(events))
	for _, ev := range events {
		set[ev.UserID] = true
	}
	return set
}

// messageRingEvents are the events that ring the members of a conversation for
// a new message: one on the topic of each member except the author whose
// choice for the conversation covers it (SPEC §7.15). mentioned is the set of
// usernames, lower case, that the message mentions. The settings and the
// preferences of all the members come from one query.
func messageRingEvents(ctx context.Context, tx *store.Store, conv store.Conversation, messageID, authorID uint64, mentioned map[string]bool) ([]live.Event, error) {
	recipients, err := tx.ConversationRecipients(ctx, conv.ID, authorID)
	if err != nil {
		return nil, err
	}
	kind := PlaceGroup
	if conv.Kind == ConversationDirect {
		kind = PlaceDirect
	}
	var events []live.Event
	for _, r := range recipients {
		ev := live.Event{
			Topic: live.UserTopic(r.UserID), Kind: live.KindMessageCreated,
			ConversationID: conv.ID, MessageID: messageID, UserID: r.UserID,
		}
		if ringEvent(&ev, ringFor(r.Settings, r.Place, kind, EventMessage, mentioned[strings.ToLower(r.Username)])) {
			events = append(events, ev)
		}
	}
	return events, nil
}
