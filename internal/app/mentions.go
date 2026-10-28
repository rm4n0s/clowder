package app

import (
	"context"
	"slices"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// The names of the group mentions (SPEC §7.15). They are reserved: nobody
// signs up with one of them as a username.
const (
	// GroupMentionRoom mentions every member of the place.
	GroupMentionRoom = "room"
	// GroupMentionAll is the same as GroupMentionRoom.
	GroupMentionAll = "all"
	// GroupMentionHere mentions the members who are online now.
	GroupMentionHere = "here"
)

// groupMentionConfirmAbove is the number of people above which the editor asks
// before a group mention is sent (SPEC §7.15).
const groupMentionConfirmAbove = 5

// GroupMentionConfirmAbove is the number of people above which a page asks
// "This will notify N people. Send anyway?" before it sends a group mention.
const GroupMentionConfirmAbove = groupMentionConfirmAbove

// isGroupMentionName reports whether the lower-cased name is one of the three
// group mentions.
func isGroupMentionName(name string) bool {
	return name == GroupMentionRoom || name == GroupMentionAll || name == GroupMentionHere
}

// bodyScan is what a body says about whom it mentions: the usernames, which
// group mentions it holds, and its text outside code for the keywords.
type bodyScan struct {
	// usernames are the @username mentions, lower case, in body order, without
	// the group mentions.
	usernames []string
	// room is true for @room or @all.
	room bool
	// here is true for @here.
	here bool
	// matcher finds keywords in the text outside code.
	matcher markdown.KeywordMatcher
}

// scanBody reads the mentions of a body. Group mentions and keywords count
// only where the place allows them; the caller decides what to do with room,
// here and matcher.
func scanBody(md *markdown.Renderer, format markdown.Format, body string) bodyScan {
	var b bodyScan
	for _, name := range md.Mentions(format, body) {
		switch name {
		case GroupMentionRoom, GroupMentionAll:
			b.room = true
		case GroupMentionHere:
			b.here = true
		default:
			b.usernames = append(b.usernames, name)
		}
	}
	b.matcher = markdown.NewKeywordMatcher(md.PlainText(format, body))
	return b
}

// hasGroup reports whether the body holds a group mention.
func (b bodyScan) hasGroup() bool { return b.room || b.here }

// groupReceives reports whether the recipient takes group mentions at all: the
// global setting is on and the place does not ignore them.
func groupReceives(r store.Recipient) bool {
	return r.Settings.GroupMentions() && !r.Place.IgnoreGroupMentions
}

// groupAndKeywordMentions finds, among the recipients of a place (the author
// is not among them), whom the group mentions of the body reach and whose
// keywords it holds. Group mentions only count when groups is true (not in a
// direct conversation). It returns each user once with the stronger kind
// (group before keyword), in the order of the recipients. presence tells who
// is online for @here and is asked only when the body has one.
func groupAndKeywordMentions(ctx context.Context, presence live.Presence, scan bodyScan, recipients []store.Recipient, groups bool) ([]store.MentionedUser, error) {
	var online map[uint64]bool
	if groups && scan.here && !scan.room {
		ids := make([]uint64, len(recipients))
		for i, r := range recipients {
			ids[i] = r.UserID
		}
		var err error
		if online, err = presence.Online(ctx, ids); err != nil {
			return nil, err
		}
	}
	var out []store.MentionedUser
	for _, r := range recipients {
		switch {
		case groups && groupReceives(r) && (scan.room || (scan.here && online[r.UserID])):
			out = append(out, store.MentionedUser{UserID: r.UserID, Kind: store.MentionGroup})
		case matchesKeyword(scan.matcher, r.Settings):
			out = append(out, store.MentionedUser{UserID: r.UserID, Kind: store.MentionKeyword})
		}
	}
	return out, nil
}

// matchesKeyword reports whether the text holds one of the keywords of the
// settings.
func matchesKeyword(m markdown.KeywordMatcher, s store.UserSettings) bool {
	return slices.ContainsFunc(s.Keywords(), m.Matches)
}

// mentionKindOf is the kind of notification that a way of being mentioned
// makes.
func mentionKindOf(kind store.MentionKind) NotificationKind {
	switch kind {
	case store.MentionGroup:
		return store.NotificationGroupMention
	case store.MentionKeyword:
		return store.NotificationKeyword
	default:
		return store.NotificationMention
	}
}

// mentionNoticesOf makes the notices of the users newly mentioned, each of the
// kind its way of being mentioned makes.
func mentionNoticesOf(added []store.MentionedUser) []notice {
	out := make([]notice, len(added))
	for i, m := range added {
		out[i] = notice{userID: m.UserID, kind: mentionKindOf(m.Kind)}
	}
	return out
}

// mentionedByName returns the IDs of the users the mentions name with
// @username, in the order of the list.
func mentionedByName(mentions []store.MentionedUser) []uint64 {
	var ids []uint64
	for _, m := range mentions {
		if m.Kind == store.MentionUser {
			ids = append(ids, m.UserID)
		}
	}
	return ids
}

// syncMentions rebuilds the mentions of the post postID of the room from its
// body (SPEC §7.9, §7.15):
//   - the usernames of the body that belong to enabled users who can read the
//     room (every user for a visible room, the members of a private one, D26);
//     a mention of anybody else is not saved;
//   - the members of the room whom a group mention of the body reaches (@room
//     and @all every member, @here those online now), except the author and
//     those who turned the group mentions off or ignore them in this room;
//   - the members, the author excepted, whose keywords the text outside code
//     holds.
//
// recipients are the members of the room except the author, with their
// settings and preferences. A user is saved once, with the strongest way
// (name, then group, then keyword). It returns all the mentions now (in the
// order: the names in the order of the body, then the others in the order of
// the members) and the users who were not mentioned before, for the caller to
// notify. The author may mention themselves; the caller does not notify them.
func (p *Posts) syncMentions(ctx context.Context, tx *store.Store, room store.Room, postID uint64, format Format, body string, recipients []store.Recipient) (all, added []store.MentionedUser, err error) {
	scan := scanBody(p.md, markdown.Format(format), body)
	users, err := tx.EnabledUsersByUsername(ctx, room.ID, room.Visibility == VisibilityPrivate, scan.usernames)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]uint64, len(users))
	for _, u := range users {
		byName[u.Username] = u.ID
	}
	seen := map[uint64]bool{}
	for _, name := range scan.usernames {
		if id, ok := byName[name]; ok && !seen[id] {
			seen[id] = true
			all = append(all, store.MentionedUser{UserID: id, Kind: store.MentionUser})
		}
	}
	others, err := groupAndKeywordMentions(ctx, p.presence, scan, recipients, true)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range others {
		if !seen[m.UserID] {
			seen[m.UserID] = true
			all = append(all, m)
		}
	}
	before, err := tx.PostMentions(ctx, postID)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.ReplacePostMentions(ctx, postID, all); err != nil {
		return nil, nil, err
	}
	for _, m := range all {
		if !slices.ContainsFunc(before, func(b store.MentionedUser) bool { return b.UserID == m.UserID }) {
			added = append(added, m)
		}
	}
	return all, added, nil
}

// GroupMentionCount says how many people the group mentions of a body would
// notify if the actor posted it in the room: the members of the room except
// the actor and those who turned the group mentions off or ignore them there,
// all of them for @room and @all, those online now for @here. It is 0 when the
// body holds no group mention. A page asks it before a post or comment is
// written, to put the question "This will notify 14 people. Send anyway?"
// when it is above GroupMentionConfirmAbove. The permission is that of
// posting: a room the actor may not read fails with TagNotFound, a visible
// room the actor has not joined with TagForbidden, a disabled or missing
// account with TagUnauthenticated.
func (p *Posts) GroupMentionCount(ctx context.Context, actor Actor, roomID uint64, format Format, body string) (int, error) {
	if _, err := checkActive(ctx, p.st, actor); err != nil {
		return 0, err
	}
	room, err := loadReadableRoom(ctx, p.st, actor, roomID)
	if err != nil {
		return 0, err
	}
	member, err := isMember(ctx, p.st, actor.UserID, room.ID)
	if err != nil {
		return 0, err
	}
	if !member {
		return 0, errors.New(TagForbidden, "only members post", "userID", actor.UserID, "roomID", room.ID)
	}
	scan := scanBody(p.md, markdown.Format(format), body)
	if !scan.hasGroup() {
		return 0, nil
	}
	recipients, err := p.st.RoomMemberRecipients(ctx, room.ID, actor.UserID)
	if err != nil {
		return 0, err
	}
	return countGroupMentions(ctx, p.presence, scan, recipients)
}

// countGroupMentions counts the recipients whom the group mentions of the body
// reach.
func countGroupMentions(ctx context.Context, presence live.Presence, scan bodyScan, recipients []store.Recipient) (int, error) {
	scan.matcher = markdown.KeywordMatcher{}
	reached, err := groupAndKeywordMentions(ctx, presence, scan, recipients, true)
	if err != nil {
		return 0, err
	}
	return len(reached), nil
}

// checkActive returns the user of the actor when the account exists and is
// not disabled; otherwise it fails with TagUnauthenticated.
func checkActive(ctx context.Context, tx *store.Store, actor Actor) (store.User, error) {
	user, err := tx.UserByID(ctx, actor.UserID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return store.User{}, errors.New(TagUnauthenticated, "the user does not exist", "userID", actor.UserID)
		}
		return store.User{}, err
	}
	if user.DisabledAt != nil {
		return store.User{}, errors.New(TagUnauthenticated, "the user is disabled", "userID", actor.UserID)
	}
	return user, nil
}
