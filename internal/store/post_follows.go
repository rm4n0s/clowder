package store

import (
	"context"
	"time"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// PostFollow says that a user follows a top-level post, or that they stopped
// (SPEC §7.15). The row stays when the user unfollows, with Unfollowed set, so
// that an automatic follow (writing, commenting, being mentioned) does not
// bring the follow back. It goes with the post and with the user.
type PostFollow struct {
	ID        uint64
	CreatedAt time.Time
	// UserID is the follower.
	UserID uint64 `gorm:"not null;uniqueIndex:uq_post_follows_user_post,priority:1"`
	// PostID is the top-level post that is followed.
	PostID uint64 `gorm:"not null;uniqueIndex:uq_post_follows_user_post,priority:2;index:idx_post_follows_post"`
	// Unfollowed is true when the user stopped following the post.
	Unfollowed bool `gorm:"not null;default:false"`

	// Relations: declared so that GORM creates the foreign keys.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	Post *Post `gorm:"foreignKey:PostID;constraint:OnDelete:CASCADE"`
}

// AutoFollow makes each of userIDs a follower of the top-level post postID,
// except those who unfollowed it before: their row is left as it is. It does
// nothing for no users. It fails with TagQueryFailed when a user or the post
// does not exist.
func (s *Store) AutoFollow(ctx context.Context, postID uint64, userIDs []uint64) error {
	if len(userIDs) == 0 {
		return nil
	}
	rows := make([]PostFollow, len(userIDs))
	for i, id := range userIDs {
		rows[i] = PostFollow{UserID: id, PostID: postID}
	}
	err := s.db(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "autoFollow", "postID", postID)
	}
	return nil
}

// SetFollow makes the user follow the post (following true) or not (false),
// whatever the row said before. Not following keeps a row, so that an
// automatic follow does not undo it. It fails with TagQueryFailed when the
// user or the post does not exist.
func (s *Store) SetFollow(ctx context.Context, userID, postID uint64, following bool) error {
	row := PostFollow{UserID: userID, PostID: postID, Unfollowed: !following}
	err := s.db(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "post_id"}},
		DoUpdates: clause.Assignments(map[string]any{"unfollowed": !following}),
	}).Create(&row).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "setFollow", "postID", postID)
	}
	return nil
}

// FollowingPosts returns, of the top-level posts postIDs, the set of those the
// user follows.
func (s *Store) FollowingPosts(ctx context.Context, userID uint64, postIDs []uint64) (map[uint64]bool, error) {
	out := map[uint64]bool{}
	if len(postIDs) == 0 {
		return out, nil
	}
	var ids []uint64
	err := s.db(ctx).Model(&PostFollow{}).
		Where("user_id = ? AND post_id IN ? AND unfollowed = ?", userID, postIDs, false).
		Pluck("post_id", &ids).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "followingPosts", "userID", userID)
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// ThreadFollowers returns the IDs of the users who follow the top-level post
// rootID, except exceptUserID, who are not disabled and whose setting for
// replies is "following" (a user who has no settings has the default). Their
// order is by user ID.
func (s *Store) ThreadFollowers(ctx context.Context, rootID, exceptUserID uint64) ([]uint64, error) {
	var ids []uint64
	err := s.db(ctx).Table("post_follows").
		Joins("JOIN users ON users.id = post_follows.user_id").
		Joins("LEFT JOIN user_settings ON user_settings.user_id = users.id").
		Where("post_follows.post_id = ? AND post_follows.unfollowed = ? AND post_follows.user_id <> ?", rootID, false, exceptUserID).
		Where("users.disabled_at IS NULL").
		Where("(user_settings.replies IS NULL OR user_settings.replies = ?)", string(RepliesFollowing)).
		Order("post_follows.user_id ASC").
		Pluck("post_follows.user_id", &ids).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "threadFollowers", "postID", rootID)
	}
	return ids, nil
}

// foldWindow is how long a reply notification takes more replies of the same
// thread into itself (SPEC §7.15).
const foldWindow = time.Minute

// FoldableReply returns the newest unread reply notification of the user for
// the thread rootID that was made less than a minute ago (by the Store's
// clock), and whether there is one. The burst is counted from the creation of
// that notification.
func (s *Store) FoldableReply(ctx context.Context, userID, rootID uint64) (Notification, bool, error) {
	var rows []Notification
	err := s.db(ctx).Model(&Notification{}).Select("notifications.*").
		Joins("JOIN posts ON posts.id = notifications.post_id").
		Where("notifications.user_id = ? AND notifications.kind = ? AND notifications.read_at IS NULL", userID, string(NotificationReply)).
		Where("notifications.created_at > ?", s.clk.Now().UTC().Add(-foldWindow)).
		Where("COALESCE(posts.root_id, posts.id) = ?", rootID).
		Order("notifications.id DESC").Limit(1).Find(&rows).Error
	if err != nil {
		return Notification{}, false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "foldableReply", "userID", userID)
	}
	if len(rows) == 0 {
		return Notification{}, false, nil
	}
	return rows[0], true, nil
}

// DistinctRepliers returns how many different users, other than the two given
// (the follower and the latest actor), wrote a comment that is not deleted in
// the thread rootID with an ID of fromPostID or more: the people who replied
// since the burst began.
func (s *Store) DistinctRepliers(ctx context.Context, rootID, fromPostID, exceptA, exceptB uint64) (int, error) {
	var n int64
	err := s.db(ctx).Table("posts").
		Where("root_id = ? AND id >= ? AND deleted_at IS NULL AND author_id NOT IN ?", rootID, fromPostID, []uint64{exceptA, exceptB}).
		Distinct("author_id").Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "distinctRepliers", "postID", rootID)
	}
	return int(n), nil
}

// FoldReply makes the notification id name actorID as its latest actor and
// count others more people. It does nothing when the notification is gone.
func (s *Store) FoldReply(ctx context.Context, id, actorID uint64, others int) error {
	err := s.db(ctx).Model(&Notification{}).Where("id = ?", id).
		Updates(map[string]any{"actor_id": actorID, "others": others}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "foldReply", "notificationID", id)
	}
	return nil
}
