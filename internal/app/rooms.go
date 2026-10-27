package app

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// Visibility says who sees a room. It is the store's type, so the web layer
// can name the values without importing the store.
type Visibility = store.Visibility

// The two visibilities of SPEC §7.2.
const (
	// VisibilityVisible is a room that every user sees and reads (D26).
	VisibilityVisible Visibility = store.VisibilityVisible
	// VisibilityPrivate is a room that only its members see.
	VisibilityPrivate Visibility = store.VisibilityPrivate
)

// The rules of SPEC §7.2 for what a person types about a room.
const (
	roomNameMinLength        = 1
	roomNameMaxLength        = 64
	roomDescriptionMaxLength = 500
)

// The names of the fields of a room in the metadata "fields" of a
// TagInvalidInput error. The field "user" of AddMember is FieldUser.
const (
	// FieldName names the name of a room.
	FieldName = "name"
	// FieldDescription names the description of a room.
	FieldDescription = "description"
	// FieldVisibility names the visibility of a room.
	FieldVisibility = "visibility"
)

// defaultColumns are the columns that every new room gets, from left to right
// (SPEC §7.2).
var defaultColumns = []string{"To do", "In progress", "Done"}

// RoomInput is what a person types when they make or change a room.
type RoomInput struct {
	// Name is the name of the room: 1 to 64 characters, unique without regard
	// to case. It is trimmed.
	Name string
	// Description is at most 500 characters. It is trimmed.
	Description string
	// Visibility is who sees the room. Create reads it; Update ignores it,
	// because the visibility of a room does not change.
	Visibility Visibility
}

// Room is a room as the services show it to one actor.
type Room struct {
	// ID is the ID of the room.
	ID uint64
	// Name is the unique name of the room.
	Name string
	// Description tells what the room is for.
	Description string
	// Visibility says who sees the room.
	Visibility Visibility
	// CreatedBy is the ID of the user who made the room.
	CreatedBy uint64
	// CreatedAt is when the room was made.
	CreatedAt time.Time
	// MemberCount is the number of members.
	MemberCount int
	// IsMember is true when the asking actor is a member.
	IsMember bool
	// CanManage is true when the asking actor may rename and delete the room:
	// an admin, or the creator.
	CanManage bool
}

// Member is a member of a room as the services show it.
type Member struct {
	// UserID is the ID of the user.
	UserID uint64
	// Username is the username of the user.
	Username string
	// DisplayName is the name shown to other people.
	DisplayName string
	// AddedBy is the ID of the member who added the user; nil when the user
	// joined by themselves or made the room.
	AddedBy *uint64
	// AddedByDisplayName is the display name of AddedBy; empty when AddedBy
	// is nil.
	AddedByDisplayName string
	// JoinedAt is when the user became a member.
	JoinedAt time.Time
}

// Rooms is the service of rooms and their memberships (SPEC §7.2). Make it
// with NewRooms. It is safe for concurrent use.
type Rooms struct {
	st      *store.Store
	cleaner fileCleaner
	pub     Publisher
}

// NewRooms makes the service. fs holds the files of the attachments, which
// Delete removes with the room; the logger receives the files that could not
// be removed, and may be nil. pub receives the live events of the changes,
// after they are committed; nil publishes nothing.
func NewRooms(st *store.Store, fs *files.Store, logger *slog.Logger, pub Publisher) *Rooms {
	return &Rooms{st: st, cleaner: newFileCleaner(fs, logger), pub: orNop(pub)}
}

// Create makes a room and makes the actor its first member, with the columns
// To do, In progress and Done, and writes the event room.created. Visible
// rooms are made by admins only (D15); private rooms by anyone. It fails with
// TagForbidden for a visible room of a member, and with TagInvalidInput, with
// all the problems in the metadata "fields", for input that breaks a rule (a
// name that is taken is one of them).
func (r *Rooms) Create(ctx context.Context, actor Actor, in RoomInput) (Room, error) {
	if in.Visibility == VisibilityVisible && !actor.IsAdmin() {
		return Room{}, errors.New(TagForbidden, "only admins make visible rooms", "userID", actor.UserID)
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	fields := roomProblems(in)
	if in.Visibility != VisibilityVisible && in.Visibility != VisibilityPrivate {
		fields[FieldVisibility] = "Choose visible or private."
	}
	if len(fields) > 0 {
		return Room{}, errors.New(TagInvalidInput, "the room has mistakes", "fields", fields)
	}

	var created store.Room
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		// Look first, so that a taken name is a mistake in the form. The unique
		// index still decides when two requests race: that one is TagConflict.
		taken, err := tx.RoomNameTaken(ctx, in.Name, 0)
		if err != nil {
			return err
		}
		if taken {
			return errors.New(TagInvalidInput, "the room name is taken",
				"fields", map[string]string{FieldName: "This name is taken."})
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		created = store.Room{
			Name:        in.Name,
			Description: in.Description,
			Visibility:  in.Visibility,
			CreatedBy:   actor.UserID,
		}
		if err := tx.CreateRoom(ctx, &created); err != nil {
			return err
		}
		if err := tx.CreateRoomMember(ctx, &store.RoomMember{RoomID: created.ID, UserID: actor.UserID}); err != nil {
			return err
		}
		columns := make([]store.BoardColumn, len(defaultColumns))
		for i, name := range defaultColumns {
			columns[i] = store.BoardColumn{RoomID: created.ID, Name: name, Position: i}
		}
		if err := tx.CreateBoardColumns(ctx, columns); err != nil {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionRoomCreated,
			RoomID:     created.ID,
			EntityType: EntityRoom,
			EntityID:   created.ID,
			Summary:    who + " created #" + created.Name,
			Details: map[string]any{
				"name":        changeOf(nil, created.Name),
				"description": changeOf(nil, created.Description),
				"visibility":  changeOf(nil, string(created.Visibility)),
			},
		})
	})
	if err != nil {
		return Room{}, err
	}
	return roomView(actor, created, 1, true), nil
}

// Visible returns every visible room, ordered by name. IsMember says which of
// them the actor has joined.
func (r *Rooms) Visible(ctx context.Context, actor Actor) ([]Room, error) {
	rows, err := r.st.ListVisibleRooms(ctx)
	if err != nil {
		return nil, err
	}
	return roomViews(ctx, r.st, actor, rows)
}

// Mine returns the rooms the actor is a member of, visible and private,
// ordered by name.
func (r *Rooms) Mine(ctx context.Context, actor Actor) ([]Room, error) {
	rows, err := r.st.ListRoomsOfUser(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	return roomViews(ctx, r.st, actor, rows)
}

// Get returns a room that the actor may read: a visible room, or a room the
// actor is a member of. For any other room, including one that does not
// exist, it fails with TagNotFound.
func (r *Rooms) Get(ctx context.Context, actor Actor, roomID uint64) (Room, error) {
	row, err := loadReadableRoom(ctx, r.st, actor, roomID)
	if err != nil {
		return Room{}, err
	}
	views, err := roomViews(ctx, r.st, actor, []store.Room{row})
	if err != nil {
		return Room{}, err
	}
	return views[0], nil
}

// Members returns the members of a room that the actor may read, ordered by
// display name. For any other room it fails with TagNotFound.
func (r *Rooms) Members(ctx context.Context, actor Actor, roomID uint64) ([]Member, error) {
	if _, err := loadReadableRoom(ctx, r.st, actor, roomID); err != nil {
		return nil, err
	}
	return memberViews(ctx, r.st, roomID)
}

// AdminMembers returns the members of any room, private ones included,
// ordered by display name, for the admin rooms page (D29). It needs no
// membership: only admins may, and anyone else gets TagForbidden. A room that
// does not exist fails with TagNotFound.
func (r *Rooms) AdminMembers(ctx context.Context, actor Actor, roomID uint64) ([]Member, error) {
	if !actor.IsAdmin() {
		return nil, errors.New(TagForbidden, "only admins list the members of any room",
			"userID", actor.UserID, "roomID", roomID)
	}
	if _, err := r.st.RoomByID(ctx, roomID); err != nil {
		return nil, err
	}
	return memberViews(ctx, r.st, roomID)
}

// memberViews reads the members of the room and makes their views.
func memberViews(ctx context.Context, st *store.Store, roomID uint64) ([]Member, error) {
	rows, err := st.ListRoomMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	members := make([]Member, len(rows))
	for i, row := range rows {
		m := Member{UserID: row.UserID, AddedBy: row.AddedBy, JoinedAt: row.CreatedAt}
		if row.User != nil {
			m.Username = row.User.Username
			m.DisplayName = row.User.DisplayName
		}
		if row.Adder != nil {
			m.AddedByDisplayName = row.Adder.DisplayName
		}
		members[i] = m
	}
	return members, nil
}

// Join makes the actor a member of a visible room and writes member.joined.
// It does nothing when the actor is a member already. A private room that the
// actor is not a member of fails with TagNotFound, like a room that does not
// exist.
func (r *Rooms) Join(ctx context.Context, actor Actor, roomID uint64) error {
	var events []live.Event
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		room, err := loadReadableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil || member {
			return err
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		m := store.RoomMember{RoomID: room.ID, UserID: actor.UserID}
		if err := tx.CreateRoomMember(ctx, &m); err != nil {
			return err
		}
		events = memberEvents(room.ID, actor.UserID, live.KindMemberJoined)
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionMemberJoined,
			RoomID:     room.ID,
			EntityType: EntityMember,
			EntityID:   m.ID,
			Summary:    who + " joined #" + room.Name,
			Details:    memberDetails(actor.UserID, actor.Username),
		})
	})
	if err != nil {
		return err
	}
	publishAll(r.pub, events)
	return nil
}

// Leave removes the actor from a room and writes member.left. It does nothing
// when the actor is not a member. A private room that the actor is not a
// member of fails with TagNotFound. A private room may end with no members;
// admins keep it until they delete it.
func (r *Rooms) Leave(ctx context.Context, actor Actor, roomID uint64) error {
	var events []live.Event
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		room, err := loadReadableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		m, found, err := membershipOf(ctx, tx, room.ID, actor.UserID)
		if err != nil || !found {
			return err
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if err := tx.DeleteRoomMember(ctx, m.ID); err != nil {
			return err
		}
		events = memberEvents(room.ID, actor.UserID, live.KindMemberLeft)
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionMemberLeft,
			RoomID:     room.ID,
			EntityType: EntityMember,
			EntityID:   m.ID,
			Summary:    who + " left #" + room.Name,
			Details:    memberDetails(actor.UserID, actor.Username),
		})
	})
	if err != nil {
		return err
	}
	publishAll(r.pub, events)
	return nil
}

// AddMember adds a user to a room and writes member.added. The actor must be
// a member of the room (any member adds any user, D15): a private room that
// the actor is not a member of fails with TagNotFound, a visible room that
// the actor has not joined with TagForbidden. It does nothing when the user is a member already. It fails with
// TagInvalidInput (field "user") when the user does not exist or is disabled.
func (r *Rooms) AddMember(ctx context.Context, actor Actor, roomID, userID uint64) error {
	var events []live.Event
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		room, err := loadReadableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members add members",
				"userID", actor.UserID, "roomID", room.ID)
		}
		user, err := tx.UserByID(ctx, userID)
		if err != nil {
			if appErr, ok := errors.FromError(err); ok && appErr.Tag == TagNotFound {
				return errors.New(TagInvalidInput, "the user does not exist",
					"fields", map[string]string{FieldUser: "Choose a user."})
			}
			return err
		}
		if user.DisabledAt != nil {
			return errors.New(TagInvalidInput, "the user is disabled",
				"fields", map[string]string{FieldUser: "This user is disabled."})
		}
		already, err := isMember(ctx, tx, user.ID, room.ID)
		if err != nil || already {
			return err
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		adder := actor.UserID
		m := store.RoomMember{RoomID: room.ID, UserID: user.ID, AddedBy: &adder}
		if err := tx.CreateRoomMember(ctx, &m); err != nil {
			return err
		}
		events = memberEvents(room.ID, user.ID, live.KindMemberJoined)
		// TODO(step 11.1): notify the added user.
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionMemberAdded,
			RoomID:     room.ID,
			EntityType: EntityMember,
			EntityID:   m.ID,
			Summary:    who + " added " + user.DisplayName + " to #" + room.Name,
			Details:    memberDetails(user.ID, user.Username),
		})
	})
	if err != nil {
		return err
	}
	publishAll(r.pub, events)
	return nil
}

// Candidates returns the active users who are not members of the room, ordered
// by username: the people the actor can add with AddMember. The rules are those
// of AddMember: a private room that the actor is not a member of fails with
// TagNotFound, a visible room that the actor has not joined with TagForbidden.
func (r *Rooms) Candidates(ctx context.Context, actor Actor, roomID uint64) ([]User, error) {
	room, err := loadReadableRoom(ctx, r.st, actor, roomID)
	if err != nil {
		return nil, err
	}
	member, err := isMember(ctx, r.st, actor.UserID, room.ID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, errors.New(TagForbidden, "only members add members",
			"userID", actor.UserID, "roomID", room.ID)
	}
	members, err := r.st.ListRoomMembers(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	taken := make(map[uint64]bool, len(members))
	for _, m := range members {
		taken[m.UserID] = true
	}
	rows, err := r.st.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	users := []User{}
	for _, row := range rows {
		if row.DisabledAt == nil && !taken[row.ID] {
			users = append(users, viewOf(row))
		}
	}
	return users, nil
}

// RemoveMember removes a user from a room and writes member.removed. Only
// admins may, in any room, private ones too (D29); anyone else gets
// TagForbidden, or TagNotFound for a private room they are not a member of.
// It does nothing when the user is not a member.
func (r *Rooms) RemoveMember(ctx context.Context, actor Actor, roomID, userID uint64) error {
	var events []live.Event
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		var room store.Room
		var err error
		if actor.IsAdmin() {
			room, err = tx.RoomByID(ctx, roomID)
		} else {
			room, err = loadReadableRoom(ctx, tx, actor, roomID)
		}
		if err != nil {
			return err
		}
		if !actor.IsAdmin() {
			return errors.New(TagForbidden, "only admins remove members",
				"userID", actor.UserID, "roomID", room.ID)
		}
		m, found, err := membershipOf(ctx, tx, room.ID, userID)
		if err != nil || !found {
			return err
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		user, err := tx.UserByID(ctx, userID)
		if err != nil {
			return err
		}
		if err := tx.DeleteRoomMember(ctx, m.ID); err != nil {
			return err
		}
		events = memberEvents(room.ID, user.ID, live.KindMemberLeft)
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionMemberRemoved,
			RoomID:     room.ID,
			EntityType: EntityMember,
			EntityID:   m.ID,
			Summary:    who + " removed " + user.DisplayName + " from #" + room.Name,
			Details:    memberDetails(user.ID, user.Username),
		})
	})
	if err != nil {
		return err
	}
	publishAll(r.pub, events)
	return nil
}

// Update changes the name and the description of a room and writes
// room.renamed with the fields that changed; it does nothing when nothing
// changed. The visibility of a room does not change, so in.Visibility is
// ignored. Admins and the creator may; the creator of a private room must
// still be a member. Anyone else gets TagForbidden, or TagNotFound for a
// private room they are not a member of. It fails with TagInvalidInput, with
// all the problems in the metadata "fields", for input that breaks a rule.
func (r *Rooms) Update(ctx context.Context, actor Actor, roomID uint64, in RoomInput) error {
	var events []live.Event
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		room, err := loadManageableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		in.Name = strings.TrimSpace(in.Name)
		in.Description = strings.TrimSpace(in.Description)
		fields := roomProblems(in)
		if len(fields) == 0 && in.Name != room.Name {
			taken, err := tx.RoomNameTaken(ctx, in.Name, room.ID)
			if err != nil {
				return err
			}
			if taken {
				fields[FieldName] = "This name is taken."
			}
		}
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the room has mistakes", "fields", fields)
		}

		details := map[string]any{}
		renamed := in.Name != room.Name
		redescribed := in.Description != room.Description
		if renamed {
			details["name"] = changeOf(room.Name, in.Name)
		}
		if redescribed {
			details["description"] = changeOf(room.Description, in.Description)
		}
		if len(details) == 0 {
			return nil
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if err := tx.UpdateRoomText(ctx, room.ID, in.Name, in.Description); err != nil {
			return err
		}
		events = []live.Event{{Topic: live.RoomTopic(room.ID), Kind: live.KindRoomUpdated, RoomID: room.ID}}
		summary := who + " changed the description of #" + room.Name
		if renamed {
			summary = who + " renamed #" + room.Name + " to #" + in.Name
			if redescribed {
				summary += " and changed its description"
			}
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionRoomRenamed,
			RoomID:     room.ID,
			EntityType: EntityRoom,
			EntityID:   room.ID,
			Summary:    summary,
			Details:    details,
		})
	})
	if err != nil {
		return err
	}
	publishAll(r.pub, events)
	return nil
}

// Delete deletes a room with everything in it and writes room.deleted, which
// stays after the room is gone. Admins and the creator may; the creator of a
// private room must still be a member. Anyone else gets TagForbidden, or
// TagNotFound for a private room they are not a member of. The files of every
// attachment in the room are removed from disk after the change is committed.
func (r *Rooms) Delete(ctx context.Context, actor Actor, roomID uint64) error {
	var keys []string
	var events []live.Event
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		room, err := loadManageableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		keys, err = tx.AttachmentKeysOfRoom(ctx, room.ID)
		if err != nil {
			return err
		}
		// The members are read before the room goes: each of them hears of it.
		members, err := tx.ListRoomMembers(ctx, room.ID)
		if err != nil {
			return err
		}
		events = make([]live.Event, 0, len(members)+1)
		events = append(events, live.Event{Topic: live.RoomTopic(room.ID), Kind: live.KindRoomDeleted, RoomID: room.ID})
		for _, m := range members {
			events = append(events, live.Event{Topic: live.UserTopic(m.UserID), Kind: live.KindRoomDeleted, RoomID: room.ID, UserID: m.UserID})
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		// The event comes first: it has no foreign key to the room, and the
		// transaction commits both or neither.
		err = recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionRoomDeleted,
			RoomID:     room.ID,
			EntityType: EntityRoom,
			EntityID:   room.ID,
			Summary:    who + " deleted #" + room.Name,
			Details: map[string]any{
				"name":        changeOf(room.Name, nil),
				"description": changeOf(room.Description, nil),
				"visibility":  changeOf(string(room.Visibility), nil),
			},
		})
		if err != nil {
			return err
		}
		// The posts of the room leave the search index before they cascade away.
		if err := tx.UnindexRoom(ctx, room.ID); err != nil {
			return err
		}
		return tx.DeleteRoom(ctx, room.ID)
	})
	if err != nil {
		return err
	}
	publishAll(r.pub, events)
	r.cleaner.remove(ctx, keys)
	return nil
}

// AllRooms returns every room, private ones included, ordered by name, for
// the admin rooms page (D29). Only admins may; anyone else gets TagForbidden.
func (r *Rooms) AllRooms(ctx context.Context, actor Actor) ([]Room, error) {
	if !actor.IsAdmin() {
		return nil, errors.New(TagForbidden, "only admins list every room", "userID", actor.UserID)
	}
	rows, err := r.st.ListRooms(ctx)
	if err != nil {
		return nil, err
	}
	return roomViews(ctx, r.st, actor, rows)
}

// roomProblems returns the rules of SPEC §7.2 that the (already trimmed) name
// and description break, as field name to message. It is empty when all hold.
func roomProblems(in RoomInput) map[string]string {
	fields := map[string]string{}
	if n := utf8.RuneCountInString(in.Name); n < roomNameMinLength || n > roomNameMaxLength || !utf8.ValidString(in.Name) {
		fields[FieldName] = "Use 1 to 64 characters."
	}
	if utf8.RuneCountInString(in.Description) > roomDescriptionMaxLength || !utf8.ValidString(in.Description) {
		fields[FieldDescription] = "Use at most 500 characters."
	}
	return fields
}

// changeOf is the Details entry of a changed field.
func changeOf(oldValue, newValue any) map[string]any {
	return map[string]any{"old": oldValue, "new": newValue}
}

// memberDetails is the Details of a member event: the user it is about.
func memberDetails(userID uint64, username string) map[string]any {
	return map[string]any{"userID": userID, "username": username}
}

// roomView makes the view of a room for the actor.
func roomView(actor Actor, row store.Room, memberCount int, member bool) Room {
	return Room{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Visibility:  row.Visibility,
		CreatedBy:   row.CreatedBy,
		CreatedAt:   row.CreatedAt,
		MemberCount: memberCount,
		IsMember:    member,
		CanManage:   actor.IsAdmin() || row.CreatedBy == actor.UserID,
	}
}

// roomViews makes the views of the rooms for the actor, in the order of rows,
// with two queries: the member counts and the rooms of the actor.
func roomViews(ctx context.Context, st *store.Store, actor Actor, rows []store.Room) ([]Room, error) {
	ids := make([]uint64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	counts, err := st.RoomMemberCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	mine, err := st.RoomIDsOfUser(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	joined := make(map[uint64]bool, len(mine))
	for _, id := range mine {
		joined[id] = true
	}
	views := make([]Room, len(rows))
	for i, row := range rows {
		views[i] = roomView(actor, row, counts[row.ID], joined[row.ID])
	}
	return views, nil
}

// displayNameOf returns the display name of the user, for the summary of an
// audit event.
func displayNameOf(ctx context.Context, st *store.Store, userID uint64) (string, error) {
	u, err := st.UserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.DisplayName, nil
}

// isMember reports whether the user is a member of the room. It takes the
// Store so that it works inside a transaction. Every later service uses it.
func isMember(ctx context.Context, st *store.Store, userID, roomID uint64) (bool, error) {
	return st.IsRoomMember(ctx, roomID, userID)
}

// membershipOf returns the membership of the user in the room, and false when
// there is none.
func membershipOf(ctx context.Context, st *store.Store, roomID, userID uint64) (store.RoomMember, bool, error) {
	m, err := st.RoomMemberOf(ctx, roomID, userID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == TagNotFound {
			return store.RoomMember{}, false, nil
		}
		return store.RoomMember{}, false, err
	}
	return m, true, nil
}

// canRead reports whether the actor may read the room: it is visible, or the
// actor is a member (D26). Admins are no exception (D29). Every later service
// that shows what is in a room asks it, through loadReadableRoom.
func canRead(ctx context.Context, st *store.Store, actor Actor, room store.Room) (bool, error) {
	if room.Visibility == VisibilityVisible {
		return true, nil
	}
	return isMember(ctx, st, actor.UserID, room.ID)
}

// loadReadableRoom returns the room if the actor may read it. A room that
// does not exist and a private room that the actor is not a member of fail
// with the same TagNotFound, so that a private room stays unknown; the
// metadata "reason" ("missing" or "private") tells them apart, for the log.
func loadReadableRoom(ctx context.Context, st *store.Store, actor Actor, roomID uint64) (store.Room, error) {
	room, err := st.RoomByID(ctx, roomID)
	if err != nil {
		return store.Room{}, err
	}
	ok, err := canRead(ctx, st, actor, room)
	if err != nil {
		return store.Room{}, err
	}
	if !ok {
		return store.Room{}, errors.New(TagNotFound, "no such room",
			"reason", "private", "roomID", roomID, "userID", actor.UserID)
	}
	return room, nil
}

// loadManageableRoom returns the room if the actor may rename and delete it:
// an admin, in any room, or the creator, who for a private room must still be
// a member. A private room that the actor may not read is TagNotFound, a
// room that the actor reads but may not manage is TagForbidden.
func loadManageableRoom(ctx context.Context, st *store.Store, actor Actor, roomID uint64) (store.Room, error) {
	if actor.IsAdmin() {
		return st.RoomByID(ctx, roomID)
	}
	room, err := loadReadableRoom(ctx, st, actor, roomID)
	if err != nil {
		return store.Room{}, err
	}
	if room.CreatedBy != actor.UserID {
		return store.Room{}, errors.New(TagForbidden, "only admins and the creator change a room",
			"userID", actor.UserID, "roomID", roomID)
	}
	return room, nil
}
