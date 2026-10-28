package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rm4n0s/errors"
)

// RoomRead says how far a member has read a room (SPEC §7.17, D55): the
// highest ID of a post or comment of the room that the member has seen. Posts
// and comments share one ID sequence, so one number covers both. Every
// membership of a room has exactly one row: it is made with the membership, at
// the room's highest ID so far (a new member starts with nothing unread), and
// deleted with it. The primary key is (UserID, RoomID); the row goes with the
// room and with the user.
type RoomRead struct {
	// UserID is the member.
	UserID uint64 `gorm:"primaryKey;autoIncrement:false"`
	// RoomID is the room.
	RoomID uint64 `gorm:"primaryKey;autoIncrement:false;index:idx_room_reads_room_id"`
	// LastSeenID is the highest ID of a post or comment of the room that the
	// member has seen. It only moves forward.
	LastSeenID uint64 `gorm:"not null;default:0"`
	// UpdatedAt is when the row was made or last moved.
	UpdatedAt time.Time

	// Relations: declared so that GORM creates the foreign keys.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	Room *Room `gorm:"foreignKey:RoomID;constraint:OnDelete:CASCADE"`
}

// UnreadPostsCap is the largest number of unread posts that the count of a room
// gives: 100 means 100 or more. The pages show "99+" above 99, so no more is
// needed, and counting stops at the cap, which keeps the query for a member
// who has not read a busy room for a long time as cheap as for one who has
// (step 20.2: 28 ms instead of 6 ms on PostgreSQL for 100 rooms of the large
// demo with nothing seen).
const UnreadPostsCap = 100

// RoomUnread holds the unread counts of one room for one member (SPEC §7.17):
// the top-level posts of other people that the member has not seen, and their
// unread mention notifications in the room.
type RoomUnread struct {
	// RoomID is the room.
	RoomID uint64
	// Posts is the number of top-level posts of the room that are not deleted,
	// were written by someone else and have an ID above the member's mark, up
	// to UnreadPostsCap.
	Posts int
	// Mentions is the number of unread notifications of the member in the room
	// of the kinds mention, group_mention and keyword.
	Mentions int
}

// unreadCapSQL is UnreadPostsCap as it is written in the query.
const unreadCapSQL = "100"

// mentionKindsSQL lists the notification kinds that count as a mention of the
// user in a room (SPEC §7.17).
const mentionKindsSQL = "('mention','group_mention','keyword')"

// unreadSelect is the query of the unread counts of a member. The posts are
// counted per room, up to UnreadPostsCap, with a correlated subquery that the
// index of the feed answers (room_id, then top-level, then newer than the
// mark); the mentions
// come from one grouped read of the member's unread notifications. The two %s
// places take a condition on the notifications and one on the room.
const unreadSelect = `SELECT m.room_id AS room_id,
  (SELECT COUNT(*) FROM (SELECT 1 FROM posts p
    WHERE p.room_id = m.room_id AND p.parent_id IS NULL AND p.deleted_at IS NULL
      AND p.author_id <> m.user_id AND p.id > COALESCE(r.last_seen_id, 0)
    LIMIT ` + unreadCapSQL + `) AS capped) AS posts,
  COALESCE(n.c, 0) AS mentions
FROM room_members m
LEFT JOIN room_reads r ON r.user_id = m.user_id AND r.room_id = m.room_id
LEFT JOIN (SELECT room_id, COUNT(*) AS c FROM notifications
    WHERE user_id = ? AND read_at IS NULL AND kind IN ` + mentionKindsSQL + `%s
    GROUP BY room_id) n ON n.room_id = m.room_id
WHERE m.user_id = ?%s
ORDER BY m.room_id`

// RoomUnreadCounts returns the unread counts of every room the user is a
// member of, in one query, ordered by room ID. A membership without a row in
// room_reads counts every post of others as unread.
func (s *Store) RoomUnreadCounts(ctx context.Context, userID uint64) ([]RoomUnread, error) {
	var rows []RoomUnread
	query := fmt.Sprintf(unreadSelect, "", "")
	if err := s.db(ctx).Raw(query, userID, userID).Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomUnreadCounts", "userID", userID)
	}
	return rows, nil
}

// RoomUnreadOf returns the unread counts of the user in one room, and false
// when the user is not a member of it. It is the query of RoomUnreadCounts for
// one room.
func (s *Store) RoomUnreadOf(ctx context.Context, userID, roomID uint64) (RoomUnread, bool, error) {
	var rows []RoomUnread
	query := fmt.Sprintf(unreadSelect, " AND room_id = ?", " AND m.room_id = ?")
	if err := s.db(ctx).Raw(query, userID, roomID, userID, roomID).Scan(&rows).Error; err != nil {
		return RoomUnread{}, false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomUnreadOf", "userID", userID, "roomID", roomID)
	}
	if len(rows) == 0 {
		return RoomUnread{}, false, nil
	}
	return rows[0], true, nil
}

// RoomLastSeen returns the mark of the user in the room, and false when the
// user has no row there.
func (s *Store) RoomLastSeen(ctx context.Context, userID, roomID uint64) (uint64, bool, error) {
	var rows []RoomRead
	err := s.db(ctx).Where("user_id = ? AND room_id = ?", userID, roomID).Limit(1).Find(&rows).Error
	if err != nil {
		return 0, false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomLastSeen", "userID", userID, "roomID", roomID)
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	return rows[0].LastSeenID, true, nil
}

// RoomMaxPostID returns the highest ID of a post or comment of the room, 0
// when it has none. A deleted post counts: its ID was handed out.
func (s *Store) RoomMaxPostID(ctx context.Context, roomID uint64) (uint64, error) {
	var ids []uint64
	err := s.db(ctx).Raw("SELECT id FROM posts WHERE room_id = ? ORDER BY id DESC LIMIT 1", roomID).Scan(&ids).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomMaxPostID", "roomID", roomID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

// MoveRoomRead moves the mark of the user in the room forward to upToID. It
// never moves it back, and makes the row when the user has none. It reports
// whether the mark changed. The user must be a member of the room (the row has
// foreign keys, not a membership check).
func (s *Store) MoveRoomRead(ctx context.Context, userID, roomID, upToID uint64) (bool, error) {
	res := s.db(ctx).Exec(`INSERT INTO room_reads (user_id, room_id, last_seen_id, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (user_id, room_id) DO UPDATE SET last_seen_id = excluded.last_seen_id, updated_at = excluded.updated_at
WHERE room_reads.last_seen_id < excluded.last_seen_id`,
		userID, roomID, upToID, s.clk.Now().UTC())
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "moveRoomRead", "userID", userID, "roomID", roomID)
	}
	return res.RowsAffected > 0, nil
}

// createRoomRead makes the row of a new membership at the room's highest ID,
// or puts an existing row there. It is part of CreateRoomMember.
func (s *Store) createRoomRead(ctx context.Context, userID, roomID uint64) error {
	err := s.db(ctx).Exec(`INSERT INTO room_reads (user_id, room_id, last_seen_id, updated_at)
SELECT ?, ?, COALESCE(MAX(id), 0), ? FROM posts WHERE room_id = ?
ON CONFLICT (user_id, room_id) DO UPDATE SET last_seen_id = excluded.last_seen_id, updated_at = excluded.updated_at`,
		userID, roomID, s.clk.Now().UTC(), roomID).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createRoomRead", "userID", userID, "roomID", roomID)
	}
	return nil
}

// BackfillRoomReads makes a row of room_reads, at the room's highest ID, for
// every membership that has none. The migration of step 20.2 does it for the
// memberships that exist; the large demo calls it after it wrote rooms and
// posts in bulk. It reports how many rows it made.
func (s *Store) BackfillRoomReads(ctx context.Context) (int, error) {
	res := s.db(ctx).Exec(`INSERT INTO room_reads (user_id, room_id, last_seen_id, updated_at)
SELECT m.user_id, m.room_id, COALESCE((SELECT MAX(p.id) FROM posts p WHERE p.room_id = m.room_id), 0), ?
FROM room_members m
WHERE NOT EXISTS (SELECT 1 FROM room_reads r WHERE r.user_id = m.user_id AND r.room_id = m.room_id)`,
		s.clk.Now().UTC())
	if res.Error != nil {
		return 0, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "backfillRoomReads")
	}
	return int(res.RowsAffected), nil
}

// ResetRoomReads moves every mark of every member of every room to the room's
// highest ID, so that nothing is unread. The large demo calls it after the
// bulk load, so that it starts clean.
func (s *Store) ResetRoomReads(ctx context.Context) error {
	err := s.db(ctx).Exec(`UPDATE room_reads SET last_seen_id =
  COALESCE((SELECT MAX(p.id) FROM posts p WHERE p.room_id = room_reads.room_id), 0), updated_at = ?`,
		s.clk.Now().UTC()).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "resetRoomReads")
	}
	return nil
}

// MarkRoomMentionsRead sets ReadAt, to the time of the Store's clock, on the
// unread notifications of the user in the room that are mentions (the kinds
// mention, group_mention and keyword), and returns how many it changed.
func (s *Store) MarkRoomMentionsRead(ctx context.Context, userID, roomID uint64) (int, error) {
	res := s.db(ctx).Model(&Notification{}).
		Where("user_id = ? AND room_id = ? AND read_at IS NULL AND kind IN "+mentionKindsSQL, userID, roomID).
		Update("read_at", s.clk.Now().UTC())
	if res.Error != nil {
		return 0, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "markRoomMentionsRead", "userID", userID, "roomID", roomID)
	}
	return int(res.RowsAffected), nil
}

// RoomsOfUnreadMentions returns the IDs of the rooms in which the user has an
// unread mention notification with an ID up to upToID, ordered by room ID. It
// is what the reading of notifications changes the counts of.
func (s *Store) RoomsOfUnreadMentions(ctx context.Context, userID, upToID uint64) ([]uint64, error) {
	var ids []uint64
	err := s.db(ctx).Raw(`SELECT DISTINCT room_id FROM notifications
WHERE user_id = ? AND id <= ? AND read_at IS NULL AND room_id IS NOT NULL AND kind IN `+mentionKindsSQL+` ORDER BY room_id`,
		userID, upToID).Scan(&ids).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomsOfUnreadMentions", "userID", userID)
	}
	return ids, nil
}

// RoomMemberIDs returns the IDs of the users who are members of the room,
// except exceptUserID (0 for none), ordered by ID.
func (s *Store) RoomMemberIDs(ctx context.Context, roomID, exceptUserID uint64) ([]uint64, error) {
	var ids []uint64
	err := s.db(ctx).Model(&RoomMember{}).
		Where("room_id = ? AND user_id <> ?", roomID, exceptUserID).
		Order("user_id ASC").Pluck("user_id", &ids).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomMemberIDs", "roomID", roomID)
	}
	return ids, nil
}
