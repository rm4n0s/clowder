package store

import (
	"context"
	"strings"

	"github.com/rm4n0s/errors"
)

// CreateRoom inserts r and sets its ID. It fails with TagConflict when the
// name is taken (the unique index ignores the case of the letters a to z).
func (s *Store) CreateRoom(ctx context.Context, r *Room) error {
	err := s.db(ctx).Create(r).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createRoom")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createRoom")
		}
	}
	return nil
}

// RoomByID returns the room with the given ID. It fails with TagNotFound when
// there is none.
func (s *Store) RoomByID(ctx context.Context, id uint64) (Room, error) {
	var r Room
	err := s.db(ctx).First(&r, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Room{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "roomByID", "roomID", id)
		default:
			return Room{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomByID", "roomID", id)
		}
	}
	return r, nil
}

// RoomNameTaken reports whether another room than exceptID has the name,
// without regard to case. The case of any letter counts, not only a to z (the
// unique index of the table folds only a to z, so this is the stricter
// check). Pass 0 as exceptID to look at every room.
func (s *Store) RoomNameTaken(ctx context.Context, name string, exceptID uint64) (bool, error) {
	var rooms []Room
	err := s.db(ctx).Select("id", "name").Where("id <> ?", exceptID).Find(&rooms).Error
	if err != nil {
		return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomNameTaken")
	}
	for _, r := range rooms {
		if strings.EqualFold(r.Name, name) {
			return true, nil
		}
	}
	return false, nil
}

// UpdateRoomText sets the name and the description of the room id. It fails
// with TagNotFound when there is no such room and with TagConflict when the
// name is taken.
func (s *Store) UpdateRoomText(ctx context.Context, id uint64, name, description string) error {
	res := s.db(ctx).Model(&Room{}).Where("id = ?", id).
		Updates(map[string]any{"name": name, "description": description})
	if res.Error != nil {
		switch classify(res.Error) {
		case failureConflict:
			return errors.NewErr(TagConflict, res.Error).SetMetadata("op", "updateRoomText", "roomID", id)
		default:
			return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "updateRoomText", "roomID", id)
		}
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no room with this ID", "op", "updateRoomText", "roomID", id)
	}
	return nil
}

// DeleteRoom deletes the room id with everything that belongs to it (members,
// columns, posts and the rest cascade). Audit events have no foreign key and
// stay. It fails with TagNotFound when there is no such room.
func (s *Store) DeleteRoom(ctx context.Context, id uint64) error {
	res := s.db(ctx).Where("id = ?", id).Delete(&Room{})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deleteRoom", "roomID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no room with this ID", "op", "deleteRoom", "roomID", id)
	}
	return nil
}

// ListRooms returns every room, ordered by name.
func (s *Store) ListRooms(ctx context.Context) ([]Room, error) {
	var rooms []Room
	err := s.db(ctx).Order("name ASC, id ASC").Find(&rooms).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listRooms")
	}
	return rooms, nil
}

// ListVisibleRooms returns the rooms with visibility visible, ordered by name.
func (s *Store) ListVisibleRooms(ctx context.Context) ([]Room, error) {
	var rooms []Room
	err := s.db(ctx).Where("visibility = ?", VisibilityVisible).Order("name ASC, id ASC").Find(&rooms).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listVisibleRooms")
	}
	return rooms, nil
}

// ListRoomsOfUser returns the rooms the user is a member of, visible and
// private, ordered by name.
func (s *Store) ListRoomsOfUser(ctx context.Context, userID uint64) ([]Room, error) {
	var rooms []Room
	err := s.db(ctx).
		Joins("JOIN room_members ON room_members.room_id = rooms.id").
		Where("room_members.user_id = ?", userID).
		Order("rooms.name ASC, rooms.id ASC").
		Find(&rooms).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listRoomsOfUser", "userID", userID)
	}
	return rooms, nil
}

// CreateRoomMember inserts m, sets its ID, and makes the member's row in
// room_reads at the room's highest ID so far, so that a new member starts with
// nothing unread (SPEC §7.17, D55). Call it inside Store.Tx so that both
// writes are one: the services do. It fails with TagConflict when the user is a
// member of the room already.
func (s *Store) CreateRoomMember(ctx context.Context, m *RoomMember) error {
	err := s.db(ctx).Create(m).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createRoomMember")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createRoomMember")
		}
	}
	return s.createRoomRead(ctx, m.UserID, m.RoomID)
}

// RoomMemberOf returns the membership of the user in the room. It fails with
// TagNotFound when the user is not a member.
func (s *Store) RoomMemberOf(ctx context.Context, roomID, userID uint64) (RoomMember, error) {
	var m RoomMember
	err := s.db(ctx).Where("room_id = ? AND user_id = ?", roomID, userID).First(&m).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return RoomMember{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "roomMemberOf", "roomID", roomID, "userID", userID)
		default:
			return RoomMember{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomMemberOf", "roomID", roomID, "userID", userID)
		}
	}
	return m, nil
}

// IsRoomMember reports whether the user is a member of the room.
func (s *Store) IsRoomMember(ctx context.Context, roomID, userID uint64) (bool, error) {
	var n int64
	err := s.db(ctx).Model(&RoomMember{}).Where("room_id = ? AND user_id = ?", roomID, userID).Count(&n).Error
	if err != nil {
		return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "isRoomMember", "roomID", roomID, "userID", userID)
	}
	return n > 0, nil
}

// DeleteRoomMember deletes the membership id and the member's row in
// room_reads (SPEC §7.17). Call it inside Store.Tx so that both writes are one:
// the services do. It fails with TagNotFound when there is no such membership.
func (s *Store) DeleteRoomMember(ctx context.Context, id uint64) error {
	err := s.db(ctx).Exec(`DELETE FROM room_reads WHERE EXISTS (SELECT 1 FROM room_members m
WHERE m.id = ? AND m.user_id = room_reads.user_id AND m.room_id = room_reads.room_id)`, id).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteRoomMember", "memberID", id, "step", "room_reads")
	}
	res := s.db(ctx).Where("id = ?", id).Delete(&RoomMember{})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deleteRoomMember", "memberID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no membership with this ID", "op", "deleteRoomMember", "memberID", id)
	}
	return nil
}

// ListRoomMembers returns the memberships of the room, ordered by the display
// name of the user, with User and Adder loaded.
func (s *Store) ListRoomMembers(ctx context.Context, roomID uint64) ([]RoomMember, error) {
	var members []RoomMember
	err := s.db(ctx).
		Joins("JOIN users ON users.id = room_members.user_id").
		Where("room_members.room_id = ?", roomID).
		Order("users.display_name COLLATE NOCASE ASC, users.id ASC").
		Preload("User").Preload("Adder").
		Find(&members).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listRoomMembers", "roomID", roomID)
	}
	return members, nil
}

// RoomMemberCounts returns the number of members of each of the rooms. A room
// without members has no key in the map.
func (s *Store) RoomMemberCounts(ctx context.Context, roomIDs []uint64) (map[uint64]int, error) {
	counts := map[uint64]int{}
	if len(roomIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		RoomID uint64
		N      int
	}
	err := s.db(ctx).Model(&RoomMember{}).
		Select("room_id, COUNT(*) AS n").
		Where("room_id IN ?", roomIDs).
		Group("room_id").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomMemberCounts")
	}
	for _, r := range rows {
		counts[r.RoomID] = r.N
	}
	return counts, nil
}

// RoomIDsOfUser returns the IDs of the rooms the user is a member of.
func (s *Store) RoomIDsOfUser(ctx context.Context, userID uint64) ([]uint64, error) {
	var ids []uint64
	err := s.db(ctx).Model(&RoomMember{}).Where("user_id = ?", userID).Pluck("room_id", &ids).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomIDsOfUser", "userID", userID)
	}
	return ids, nil
}

// CreateBoardColumns inserts the columns and sets their IDs.
func (s *Store) CreateBoardColumns(ctx context.Context, columns []BoardColumn) error {
	if len(columns) == 0 {
		return nil
	}
	err := s.db(ctx).Create(&columns).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createBoardColumns")
	}
	return nil
}

// ListBoardColumns returns the columns of the room from left to right.
func (s *Store) ListBoardColumns(ctx context.Context, roomID uint64) ([]BoardColumn, error) {
	var columns []BoardColumn
	err := s.db(ctx).Where("room_id = ?", roomID).Order("position ASC, id ASC").Find(&columns).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listBoardColumns", "roomID", roomID)
	}
	return columns, nil
}
