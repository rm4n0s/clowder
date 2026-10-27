package store

import (
	"context"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// PutReaction adds the reaction (post, user, emoji) when it is not there and
// leaves it when it is. It returns true when it was added. It fails with
// TagQueryFailed when the post or the user does not exist.
func (s *Store) PutReaction(ctx context.Context, postID, userID uint64, emoji string) (bool, error) {
	r := Reaction{PostID: postID, UserID: userID, Emoji: emoji}
	res := s.db(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "putReaction", "postID", postID)
	}
	return res.RowsAffected > 0, nil
}

// DeleteReaction removes the reaction (post, user, emoji) when it is there and
// does nothing when it is not. It returns true when it was removed.
func (s *Store) DeleteReaction(ctx context.Context, postID, userID uint64, emoji string) (bool, error) {
	res := s.db(ctx).Where("post_id = ? AND user_id = ? AND emoji = ?", postID, userID, emoji).Delete(&Reaction{})
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deleteReaction", "postID", postID)
	}
	return res.RowsAffected > 0, nil
}

// ToggleReaction adds the reaction (post, user, emoji) when it is not there and
// removes it when it is. It returns true when the reaction was added and false
// when it was removed. It fails with TagConflict when a concurrent write added
// the same reaction first, and with TagQueryFailed when the post or the user
// does not exist.
func (s *Store) ToggleReaction(ctx context.Context, postID, userID uint64, emoji string) (bool, error) {
	res := s.db(ctx).Where("post_id = ? AND user_id = ? AND emoji = ?", postID, userID, emoji).Delete(&Reaction{})
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "toggleReaction", "postID", postID)
	}
	if res.RowsAffected > 0 {
		return false, nil
	}
	r := Reaction{PostID: postID, UserID: userID, Emoji: emoji}
	if err := s.db(ctx).Create(&r).Error; err != nil {
		switch classify(err) {
		case failureConflict:
			return false, errors.NewErr(TagConflict, err).SetMetadata("op", "toggleReaction", "postID", postID)
		default:
			return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "toggleReaction", "postID", postID)
		}
	}
	return true, nil
}

// ReactionRow is one reaction with the username and the display name of the
// user who made it.
type ReactionRow struct {
	// ID is the ID of the reaction; a smaller ID is an older reaction.
	ID uint64
	// PostID is the post or comment reacted to.
	PostID uint64
	// UserID is the user who reacted.
	UserID uint64
	// Emoji is the emoji of the reaction.
	Emoji string
	// Username is the username of the user who reacted.
	Username string
	// DisplayName is the display name of the user who reacted.
	DisplayName string
}

// ReactionsOfPosts returns the reactions of each of the posts, oldest first
// (by ID), with the display names of the users, with one query. A post without
// reactions has no key in the map.
func (s *Store) ReactionsOfPosts(ctx context.Context, postIDs []uint64) (map[uint64][]ReactionRow, error) {
	out := map[uint64][]ReactionRow{}
	if len(postIDs) == 0 {
		return out, nil
	}
	var rows []ReactionRow
	err := s.db(ctx).Table("reactions").
		Select("reactions.id AS id, reactions.post_id AS post_id, reactions.user_id AS user_id, reactions.emoji AS emoji, users.username AS username, users.display_name AS display_name").
		Joins("JOIN users ON users.id = reactions.user_id").
		Where("reactions.post_id IN ?", postIDs).
		Order("reactions.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "reactionsOfPosts")
	}
	for _, r := range rows {
		out[r.PostID] = append(out[r.PostID], r)
	}
	return out, nil
}
