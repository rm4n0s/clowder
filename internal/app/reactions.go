package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// FieldEmoji names the emoji of a reaction in the metadata "fields" of a
// TagInvalidInput error.
const FieldEmoji = "emoji"

// The rules of SPEC §7.4 for the emoji of a reaction.
const (
	// emojiMaxBytes is the longest emoji in bytes: it holds a flag with its
	// tag characters, such as the flag of England.
	emojiMaxBytes = 32
	// emojiMaxParts is the most parts of a sequence joined by the zero width
	// joiner, such as a family of four.
	emojiMaxParts = 4
	// messageEmojiInvalid is the message for a reaction that is not an emoji.
	messageEmojiInvalid = "Use an emoji, such as \U0001F44D."
)

// The code points of the emoji rules.
const (
	runeZWJ          = '‍'
	runeVS15         = '︎'
	runeVS16         = '️'
	runeKeycap       = '⃣'
	runeSkinToneLow  = '\U0001F3FB'
	runeSkinToneHigh = '\U0001F3FF'
	runeRegionalLow  = '\U0001F1E6'
	runeRegionalHigh = '\U0001F1FF'
	runeBlackFlag    = '\U0001F3F4'
	runeTagLow       = '\U000E0020'
	runeTagHigh      = '\U000E007F'
)

// ReactionCount is one emoji on a post, with how many people put it there.
type ReactionCount struct {
	// Emoji is the emoji.
	Emoji string
	// Count is the number of people who reacted with it.
	Count int
	// Mine is true when the asking actor is one of them.
	Mine bool
	// Names are the display names of the people, in the order they reacted.
	Names []string
	// Users are the same people as Names, in the same order.
	Users []UserRef
}

// Reactions are the reactions on a post, ordered by the first use of each
// emoji.
type Reactions []ReactionCount

// ToggleReaction puts the emoji on the post or comment as the actor, or takes
// it off when the actor put it there already, and returns the reactions of the
// post as the actor sees them. Only members of the room react: a room that the
// actor may not read, a missing post and a deleted post fail with TagNotFound,
// a visible room that the actor has not joined with TagForbidden. An emoji
// that is not one fails with TagInvalidInput, with the message in the metadata
// "fields" under FieldEmoji. The change is published as reaction.changed to
// the room and to the thread after it is committed. Reactions are not audited
// (SPEC §7.10).
func (p *Posts) ToggleReaction(ctx context.Context, actor Actor, postID uint64, emoji string) (Reactions, error) {
	var events []live.Event
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		row, err := tx.PostByID(ctx, postID)
		if err != nil {
			return err
		}
		if _, err := loadReadableRoom(ctx, tx, actor, row.RoomID); err != nil {
			return err
		}
		if row.DeletedAt != nil {
			return errors.New(TagNotFound, "the post is deleted", "reason", "deleted", "postID", postID)
		}
		member, err := isMember(ctx, tx, actor.UserID, row.RoomID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members react",
				"userID", actor.UserID, "roomID", row.RoomID)
		}
		clean, ok := cleanEmoji(emoji)
		if !ok {
			return errors.New(TagInvalidInput, "the reaction is not an emoji",
				"fields", map[string]string{FieldEmoji: messageEmojiInvalid})
		}
		if _, err := tx.ToggleReaction(ctx, row.ID, actor.UserID, clean); err != nil {
			return err
		}
		events = reactionEvents(refOf(row))
		return nil
	})
	if err != nil {
		return nil, err
	}
	publishAll(p.pub, events)
	rows, err := p.st.ReactionsOfPosts(ctx, []uint64{postID})
	if err != nil {
		return nil, err
	}
	return Reactions(reactionCounts(rows[postID], actor.UserID)), nil
}

// SetReaction puts the emoji on the post or comment as the actor (present
// true) or takes it off (present false) and returns the reactions of the post
// as the actor sees them. Unlike ToggleReaction it is idempotent: putting an
// emoji that is there, or taking off one that is not, changes nothing. The
// permissions and the errors are those of ToggleReaction. The change is
// published as reaction.changed only when something changed. Reactions are not
// audited (SPEC §7.10).
func (p *Posts) SetReaction(ctx context.Context, actor Actor, postID uint64, emoji string, present bool) (Reactions, error) {
	var events []live.Event
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		row, err := tx.PostByID(ctx, postID)
		if err != nil {
			return err
		}
		if _, err := loadReadableRoom(ctx, tx, actor, row.RoomID); err != nil {
			return err
		}
		if row.DeletedAt != nil {
			return errors.New(TagNotFound, "the post is deleted", "reason", "deleted", "postID", postID)
		}
		member, err := isMember(ctx, tx, actor.UserID, row.RoomID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members react",
				"userID", actor.UserID, "roomID", row.RoomID)
		}
		clean, ok := cleanEmoji(emoji)
		if !ok {
			return errors.New(TagInvalidInput, "the reaction is not an emoji",
				"fields", map[string]string{FieldEmoji: messageEmojiInvalid})
		}
		var changed bool
		if present {
			changed, err = tx.PutReaction(ctx, row.ID, actor.UserID, clean)
		} else {
			changed, err = tx.DeleteReaction(ctx, row.ID, actor.UserID, clean)
		}
		if err != nil {
			return err
		}
		if changed {
			events = reactionEvents(refOf(row))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	publishAll(p.pub, events)
	rows, err := p.st.ReactionsOfPosts(ctx, []uint64{postID})
	if err != nil {
		return nil, err
	}
	return Reactions(reactionCounts(rows[postID], actor.UserID)), nil
}

// reactionEvents are the live events of a change to the reactions of a post or
// comment: the room and the thread of the post hear of it.
func reactionEvents(ref postRef) []live.Event {
	thread := ref.rootID
	if thread == 0 {
		thread = ref.id
	}
	return []live.Event{
		{Topic: live.RoomTopic(ref.roomID), Kind: live.KindReactionChanged, RoomID: ref.roomID, PostID: ref.id},
		{Topic: live.PostTopic(thread), Kind: live.KindReactionChanged, RoomID: ref.roomID, PostID: ref.id},
	}
}

// reactionCounts groups the reactions of a post, which are in the order they
// were made, by emoji: the emoji come in the order of their first use. userID
// is the asking user, for Mine.
func reactionCounts(rows []store.ReactionRow, userID uint64) []ReactionCount {
	out := []ReactionCount{}
	at := map[string]int{}
	for _, r := range rows {
		i, ok := at[r.Emoji]
		if !ok {
			i = len(out)
			at[r.Emoji] = i
			out = append(out, ReactionCount{Emoji: r.Emoji, Names: []string{}, Users: []UserRef{}})
		}
		out[i].Count++
		out[i].Names = append(out[i].Names, r.DisplayName)
		out[i].Users = append(out[i].Users, UserRef{ID: r.UserID, Username: r.Username, DisplayName: r.DisplayName})
		if r.UserID == userID {
			out[i].Mine = true
		}
	}
	return out
}

// cleanEmoji trims s and reports whether the result is exactly one emoji
// (SPEC §7.4), at most 32 bytes. One emoji is one of:
//   - a unit, or up to 4 units joined by the zero width joiner; a unit is an
//     emoji code point, then optionally a variation selector, then optionally
//     one skin tone modifier (a skin tone alone is an emoji too, but not as a
//     part of a joined sequence);
//   - a flag: exactly two regional indicators;
//   - a keycap: one of 0-9, # and *, an optional emoji presentation selector
//     and the combining keycap;
//   - a tag sequence: the black flag, tag characters and the cancel tag.
//
// Letters, other digits, spaces, punctuation and several emoji in a row are not
// emoji.
func cleanEmoji(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > emojiMaxBytes || !utf8.ValidString(s) {
		return s, false
	}
	runes := []rune(s)
	ok := isFlag(runes) || isKeycap(runes) || isTagSequence(runes) || isJoinedSequence(runes)
	return s, ok
}

// isJoinedSequence reports whether runes are one to four emoji units joined by
// the zero width joiner, or a lone skin tone.
func isJoinedSequence(runes []rune) bool {
	if len(runes) == 1 && isSkinTone(runes[0]) {
		return true
	}
	i := 0
	for parts := 1; parts <= emojiMaxParts; parts++ {
		n := emojiUnitLen(runes[i:])
		if n == 0 {
			return false
		}
		i += n
		if i == len(runes) {
			return true
		}
		if runes[i] != runeZWJ {
			return false
		}
		i++
	}
	return false
}

// emojiUnitLen returns the number of runes of the emoji unit at the start of
// runes: an emoji code point (not a regional indicator or a skin tone), then
// optionally a variation selector, then optionally a skin tone. It is 0 when
// runes do not start with one.
func emojiUnitLen(runes []rune) int {
	if len(runes) == 0 || !isEmojiBase(runes[0]) || isRegionalIndicator(runes[0]) || isSkinTone(runes[0]) {
		return 0
	}
	n := 1
	if n < len(runes) && (runes[n] == runeVS15 || runes[n] == runeVS16) {
		n++
	}
	if n < len(runes) && isSkinTone(runes[n]) {
		n++
	}
	return n
}

// isFlag reports whether runes are exactly two regional indicators.
func isFlag(runes []rune) bool {
	return len(runes) == 2 && isRegionalIndicator(runes[0]) && isRegionalIndicator(runes[1])
}

// isKeycap reports whether runes are a keycap base, an optional emoji
// presentation selector and the combining keycap.
func isKeycap(runes []rune) bool {
	if len(runes) < 2 || len(runes) > 3 || !isKeycapBase(runes[0]) || runes[len(runes)-1] != runeKeycap {
		return false
	}
	return len(runes) == 2 || runes[1] == runeVS16
}

// isTagSequence reports whether runes are the black flag, at least one tag
// character and the cancel tag, as the flags of England, Scotland and Wales.
func isTagSequence(runes []rune) bool {
	if len(runes) < 3 || runes[0] != runeBlackFlag || runes[len(runes)-1] != runeTagHigh {
		return false
	}
	for _, r := range runes[1 : len(runes)-1] {
		if r < runeTagLow || r >= runeTagHigh {
			return false
		}
	}
	return true
}

// isKeycapBase reports whether r starts a keycap: 0 to 9, "#" and "*".
func isKeycapBase(r rune) bool {
	return r >= '0' && r <= '9' || r == '#' || r == '*'
}

// isSkinTone reports whether r is one of the five skin tone modifiers.
func isSkinTone(r rune) bool {
	return r >= runeSkinToneLow && r <= runeSkinToneHigh
}

// isRegionalIndicator reports whether r is a regional indicator letter, the
// halves of a flag.
func isRegionalIndicator(r rune) bool {
	return r >= runeRegionalLow && r <= runeRegionalHigh
}

// isEmojiBase reports whether r is an emoji by itself: the pictographs,
// symbols, dingbats, arrows and flags of the Unicode blocks that hold emoji,
// and the few emoji outside them.
func isEmojiBase(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF, // pictographs, emoticons, transport, flags
		r >= 0x2600 && r <= 0x27BF, // symbols and dingbats
		r >= 0x2300 && r <= 0x23FF, // technical (watch, hourglass)
		r >= 0x2B00 && r <= 0x2BFF, // arrows and stars
		r >= 0x2190 && r <= 0x21FF, // arrows
		r >= 0x2900 && r <= 0x297F: // supplemental arrows
		return true
	}
	switch r {
	case 0x3030, 0x303D, 0x3297, 0x3299, 0x00A9, 0x00AE, 0x2122:
		return true
	}
	return false
}
