package store

import (
	"context"
	"time"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// EnabledUsersByUsername returns the users who are not disabled and whose
// username is one of usernames (already lower-cased), and who can read the
// room: every one of them for a visible room, only its members when private
// is true. It is how the mentions of a post are resolved (SPEC D26, D30). The
// order of the result is by username.
func (s *Store) EnabledUsersByUsername(ctx context.Context, roomID uint64, private bool, usernames []string) ([]User, error) {
	if len(usernames) == 0 {
		return nil, nil
	}
	q := s.db(ctx).Where("username IN ? AND disabled_at IS NULL", usernames)
	if private {
		q = q.Where("id IN (SELECT user_id FROM room_members WHERE room_id = ?)", roomID)
	}
	var users []User
	if err := q.Order("username ASC").Find(&users).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "enabledUsersByUsername", "roomID", roomID)
	}
	return users, nil
}

// MentionedUser is a user mentioned in a post, and how.
type MentionedUser struct {
	// UserID is the user.
	UserID uint64
	// Kind says how the user is mentioned.
	Kind MentionKind
}

// PostMentions returns the users the post mentions, with how, in the order
// the mentions were saved.
func (s *Store) PostMentions(ctx context.Context, postID uint64) ([]MentionedUser, error) {
	var rows []Mention
	err := s.db(ctx).Where("post_id = ?", postID).Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "postMentions", "postID", postID)
	}
	out := make([]MentionedUser, len(rows))
	for i, r := range rows {
		out[i] = MentionedUser{UserID: r.UserID, Kind: r.Kind}
	}
	return out, nil
}

// ReplacePostMentions makes mentioned the mentions of the post: the mentions
// of other users are deleted, the missing ones are added and a user who stays
// gets the new kind. The users must be without duplicates. It fails with
// TagQueryFailed when a user does not exist.
func (s *Store) ReplacePostMentions(ctx context.Context, postID uint64, mentioned []MentionedUser) error {
	del := s.db(ctx).Where("post_id = ?", postID)
	if len(mentioned) > 0 {
		ids := make([]uint64, len(mentioned))
		for i, m := range mentioned {
			ids[i] = m.UserID
		}
		del = del.Where("user_id NOT IN ?", ids)
	}
	if err := del.Delete(&Mention{}).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePostMentions", "postID", postID)
	}
	if len(mentioned) == 0 {
		return nil
	}
	rows := make([]Mention, len(mentioned))
	for i, m := range mentioned {
		kind := m.Kind
		if kind == "" {
			kind = MentionUser
		}
		rows[i] = Mention{PostID: postID, UserID: m.UserID, Kind: kind}
	}
	err := s.db(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "post_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"kind"}),
	}).Create(&rows).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePostMentions", "postID", postID)
	}
	return nil
}

// MentionedUsernamesOfPosts returns the usernames mentioned by "@username" in
// each of the posts (not those reached by a group mention or a keyword), in
// the order the mentions were saved, with one query. A post without
// mentions has no key in the map.
func (s *Store) MentionedUsernamesOfPosts(ctx context.Context, postIDs []uint64) (map[uint64][]string, error) {
	out := map[uint64][]string{}
	if len(postIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		PostID   uint64
		Username string
	}
	err := s.db(ctx).Table("mentions").
		Select("mentions.post_id AS post_id, users.username AS username").
		Joins("JOIN users ON users.id = mentions.user_id").
		Where("mentions.post_id IN ? AND mentions.kind = ?", postIDs, string(MentionUser)).
		Order("mentions.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "mentionedUsernamesOfPosts")
	}
	for _, r := range rows {
		out[r.PostID] = append(out[r.PostID], r.Username)
	}
	return out, nil
}

// CreateNotifications inserts the rows and sets their IDs and CreatedAt (the
// time of the Store's clock). It does nothing for no rows. It fails with
// TagQueryFailed when a user, post, task or room does not exist.
func (s *Store) CreateNotifications(ctx context.Context, rows []Notification) error {
	if len(rows) == 0 {
		return nil
	}
	if err := s.db(ctx).Create(&rows).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createNotifications")
	}
	return nil
}

// NotificationRow is a notification of a user with what a page needs to show
// it. The fields that do not apply to the kind are zero.
type NotificationRow struct {
	// ID is the ID of the notification; a larger ID is a newer notification.
	ID uint64
	// CreatedAt is when the notification was made.
	CreatedAt time.Time
	// Kind says why the user is notified.
	Kind NotificationKind
	// ActorID, ActorUsername and ActorDisplayName tell who caused it.
	ActorID          uint64
	ActorUsername    string
	ActorDisplayName string
	// ActorAvatarKey is the avatar key of the actor; empty for none.
	ActorAvatarKey string
	// RoomID and RoomName are the room of the notification.
	RoomID   uint64
	RoomName string
	// PostID is the post or comment concerned; RootID is the top-level post of
	// its thread (PostID itself for a top-level post).
	PostID uint64
	RootID uint64
	// TaskID and TaskTitle are the task of an assignment.
	TaskID    uint64
	TaskTitle string
	// Others is the number of other people folded into a reply notification.
	Others int
	// ReadAt is when the user read it; nil while unread.
	ReadAt *time.Time
}

// readableNotifications limits a query on notifications to those of the user
// whose room the user can still read: a visible room, or a room the user is a
// member of (SPEC D29, D30). The query must join rooms.
const readableNotifications = `notifications.user_id = ?
AND (rooms.visibility = 'visible'
     OR EXISTS (SELECT 1 FROM room_members WHERE room_members.room_id = rooms.id AND room_members.user_id = notifications.user_id))`

// ListNotifications returns the notifications of the user whose room the user
// can read, newest first (by ID), at most limit of them. A beforeID other than
// 0 keeps only those with a smaller ID.
func (s *Store) ListNotifications(ctx context.Context, userID, beforeID uint64, limit int) ([]NotificationRow, error) {
	q := s.db(ctx).Table("notifications").
		Select(`notifications.id AS id, notifications.created_at AS created_at, notifications.kind AS kind,
notifications.actor_id AS actor_id, actors.username AS actor_username, actors.display_name AS actor_display_name, actors.avatar_key AS actor_avatar_key,
rooms.id AS room_id, rooms.name AS room_name,
COALESCE(notifications.post_id, 0) AS post_id, COALESCE(posts.root_id, posts.id, 0) AS root_id,
COALESCE(notifications.task_id, 0) AS task_id, COALESCE(tasks.title, '') AS task_title,
notifications.others AS others, notifications.read_at AS read_at`).
		Joins("JOIN users actors ON actors.id = notifications.actor_id").
		Joins("JOIN rooms ON rooms.id = notifications.room_id").
		Joins("LEFT JOIN posts ON posts.id = notifications.post_id").
		Joins("LEFT JOIN tasks ON tasks.id = notifications.task_id").
		Where(readableNotifications, userID)
	if beforeID != 0 {
		q = q.Where("notifications.id < ?", beforeID)
	}
	var rows []NotificationRow
	if err := q.Order("notifications.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listNotifications", "userID", userID)
	}
	return rows, nil
}

// CountUnreadNotifications returns the number of unread notifications of the
// user whose room the user can read.
func (s *Store) CountUnreadNotifications(ctx context.Context, userID uint64) (int, error) {
	var n int64
	err := s.db(ctx).Table("notifications").
		Joins("JOIN rooms ON rooms.id = notifications.room_id").
		Where(readableNotifications, userID).
		Where("notifications.read_at IS NULL").
		Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countUnreadNotifications", "userID", userID)
	}
	return int(n), nil
}

// MarkNotificationsRead sets ReadAt, to the time of the Store's clock, on the
// unread notifications of the user with an ID up to upToID, and returns how
// many it changed. Notifications of other users are never touched.
func (s *Store) MarkNotificationsRead(ctx context.Context, userID, upToID uint64) (int, error) {
	res := s.db(ctx).Model(&Notification{}).
		Where("user_id = ? AND id <= ? AND read_at IS NULL", userID, upToID).
		Update("read_at", s.clk.Now().UTC())
	if res.Error != nil {
		return 0, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "markNotificationsRead", "userID", userID)
	}
	return int(res.RowsAffected), nil
}

// EnabledReaders returns, of the users userIDs, the set of those who are not
// disabled and can read the room: every enabled user for a visible room, only
// the members when private is true. Users who do not exist are left out.
func (s *Store) EnabledReaders(ctx context.Context, roomID uint64, private bool, userIDs []uint64) (map[uint64]bool, error) {
	out := map[uint64]bool{}
	if len(userIDs) == 0 {
		return out, nil
	}
	q := s.db(ctx).Model(&User{}).Where("id IN ? AND disabled_at IS NULL", userIDs)
	if private {
		q = q.Where("id IN (SELECT user_id FROM room_members WHERE room_id = ?)", roomID)
	}
	var ids []uint64
	if err := q.Pluck("id", &ids).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "enabledReaders", "roomID", roomID)
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
