package store

import (
	"context"
	"time"

	"github.com/rm4n0s/errors"
)

// BoardColumnByID returns the column with the given ID. It fails with
// TagNotFound when there is none.
func (s *Store) BoardColumnByID(ctx context.Context, id uint64) (BoardColumn, error) {
	var c BoardColumn
	err := s.db(ctx).First(&c, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return BoardColumn{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "boardColumnByID", "columnID", id)
		default:
			return BoardColumn{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "boardColumnByID", "columnID", id)
		}
	}
	return c, nil
}

// UpdateBoardColumnName sets the name of the column id. It fails with
// TagNotFound when there is no such column.
func (s *Store) UpdateBoardColumnName(ctx context.Context, id uint64, name string) error {
	res := s.db(ctx).Model(&BoardColumn{}).Where("id = ?", id).Update("name", name)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "updateBoardColumnName", "columnID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no column with this ID", "op", "updateBoardColumnName", "columnID", id)
	}
	return nil
}

// SetBoardColumnPosition sets the position of the column id. It fails with
// TagNotFound when there is no such column.
func (s *Store) SetBoardColumnPosition(ctx context.Context, id uint64, position int) error {
	res := s.db(ctx).Model(&BoardColumn{}).Where("id = ?", id).Update("position", position)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "setBoardColumnPosition", "columnID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no column with this ID", "op", "setBoardColumnPosition", "columnID", id)
	}
	return nil
}

// DeleteBoardColumn deletes the column id. The tasks of the column cascade:
// the application deletes a column only when it has none. It fails with
// TagNotFound when there is no such column.
func (s *Store) DeleteBoardColumn(ctx context.Context, id uint64) error {
	res := s.db(ctx).Where("id = ?", id).Delete(&BoardColumn{})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deleteBoardColumn", "columnID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no column with this ID", "op", "deleteBoardColumn", "columnID", id)
	}
	return nil
}

// CreateTask inserts t and sets its ID and CreatedAt. It fails with
// TagQueryFailed when the room, the post or the column does not exist.
func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	if err := s.db(ctx).Create(t).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createTask")
	}
	return nil
}

// TaskByID returns the task with the given ID. It fails with TagNotFound when
// there is none.
func (s *Store) TaskByID(ctx context.Context, id uint64) (Task, error) {
	var t Task
	err := s.db(ctx).First(&t, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Task{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "taskByID", "taskID", id)
		default:
			return Task{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "taskByID", "taskID", id)
		}
	}
	return t, nil
}

// ListTasksOfColumn returns the tasks of the column from top to bottom.
func (s *Store) ListTasksOfColumn(ctx context.Context, columnID uint64) ([]Task, error) {
	var tasks []Task
	err := s.db(ctx).Where("column_id = ?", columnID).Order("position ASC, id ASC").Find(&tasks).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listTasksOfColumn", "columnID", columnID)
	}
	return tasks, nil
}

// SetTaskPlacement puts the task id in the column at the position. It fails
// with TagNotFound when there is no such task.
func (s *Store) SetTaskPlacement(ctx context.Context, id, columnID uint64, position int) error {
	res := s.db(ctx).Model(&Task{}).Where("id = ?", id).
		Updates(map[string]any{"column_id": columnID, "position": position})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "setTaskPlacement", "taskID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no task with this ID", "op", "setTaskPlacement", "taskID", id)
	}
	return nil
}

// UpdateTaskFields sets the title and the due date of the task id; a nil
// dueDate clears it. It fails with TagNotFound when there is no such task.
func (s *Store) UpdateTaskFields(ctx context.Context, id uint64, title string, dueDate *time.Time) error {
	var due any
	if dueDate != nil {
		due = dueDate.UTC()
	}
	res := s.db(ctx).Model(&Task{}).Where("id = ?", id).
		Updates(map[string]any{"title": title, "due_date": due})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "updateTaskFields", "taskID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no task with this ID", "op", "updateTaskFields", "taskID", id)
	}
	return nil
}

// ReplaceTaskAssignees makes userIDs the assignees of the task, in that
// order. The IDs must be without duplicates. It fails with TagQueryFailed
// when a user does not exist.
func (s *Store) ReplaceTaskAssignees(ctx context.Context, taskID uint64, userIDs []uint64) error {
	if err := s.db(ctx).Where("task_id = ?", taskID).Delete(&TaskAssignee{}).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replaceTaskAssignees", "taskID", taskID)
	}
	if len(userIDs) == 0 {
		return nil
	}
	rows := make([]TaskAssignee, len(userIDs))
	for i, id := range userIDs {
		rows[i] = TaskAssignee{TaskID: taskID, UserID: id}
	}
	if err := s.db(ctx).Create(&rows).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replaceTaskAssignees", "taskID", taskID)
	}
	return nil
}

// DeleteTasks deletes the tasks with these IDs and their assignees. IDs that
// do not exist are ignored.
func (s *Store) DeleteTasks(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.db(ctx).Where("id IN ?", ids).Delete(&Task{}).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteTasks")
	}
	return nil
}

// MemberUsernames returns, for those of the users who are members of the
// room, their usernames by user ID. A user who is not a member, or does not
// exist, has no key in the map.
func (s *Store) MemberUsernames(ctx context.Context, roomID uint64, userIDs []uint64) (map[uint64]string, error) {
	found := map[uint64]string{}
	if len(userIDs) == 0 {
		return found, nil
	}
	var rows []struct {
		UserID   uint64
		Username string
	}
	err := s.db(ctx).Table("room_members").
		Select("room_members.user_id AS user_id, users.username AS username").
		Joins("JOIN users ON users.id = room_members.user_id").
		Where("room_members.room_id = ? AND room_members.user_id IN ?", roomID, userIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "memberUsernames", "roomID", roomID)
	}
	for _, r := range rows {
		found[r.UserID] = r.Username
	}
	return found, nil
}

// TaskAssigneeRow is a user assigned to a task.
type TaskAssigneeRow struct {
	// TaskID is the task.
	TaskID uint64
	// UserID is the assigned user.
	UserID uint64
	// Username is the username of the user.
	Username string
	// DisplayName is the display name of the user.
	DisplayName string
}

// TaskRow is a task with what a view of it needs.
type TaskRow struct {
	// ID, RoomID, PostID, ColumnID, Position, Title and DueDate are the fields
	// of the task.
	ID       uint64
	RoomID   uint64
	PostID   uint64
	ColumnID uint64
	Position int
	Title    string
	DueDate  *time.Time
	// ColumnName is the name of the column of the task.
	ColumnName string
	// ColumnLast is true when the column is the last of the board.
	ColumnLast bool
	// RootID is the top-level post of the thread of the task's post: the post
	// itself when it is top-level.
	RootID uint64
	// Assignees are the assigned users, in the order they were assigned.
	Assignees []TaskAssigneeRow `gorm:"-"`
}

// taskRowsSelect selects the TaskRow columns.
const taskRowsSelect = `tasks.id AS id, tasks.room_id AS room_id, tasks.post_id AS post_id,
tasks.column_id AS column_id, tasks.position AS position, tasks.title AS title, tasks.due_date AS due_date,
board_columns.name AS column_name,
CASE WHEN board_columns.position = (SELECT MAX(c.position) FROM board_columns c WHERE c.room_id = tasks.room_id)
  THEN 1 ELSE 0 END AS column_last,
COALESCE(posts.root_id, posts.id) AS root_id`

// TaskRowsOfRoom returns the tasks of the room ordered by column, then by
// position, with their assignees, with three queries. Rows of one column are
// together.
func (s *Store) TaskRowsOfRoom(ctx context.Context, roomID uint64) ([]TaskRow, error) {
	var rows []TaskRow
	err := s.db(ctx).Table("tasks").
		Select(taskRowsSelect).
		Joins("JOIN board_columns ON board_columns.id = tasks.column_id").
		Joins("JOIN posts ON posts.id = tasks.post_id").
		Where("tasks.room_id = ?", roomID).
		Order("board_columns.position ASC, board_columns.id ASC, tasks.position ASC, tasks.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "taskRowsOfRoom", "roomID", roomID)
	}
	if err := s.attachAssignees(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// TaskRowsOfPosts returns the tasks of each of the posts, oldest first (by
// ID), with their assignees, with two queries. A post without tasks has no
// key in the map.
func (s *Store) TaskRowsOfPosts(ctx context.Context, postIDs []uint64) (map[uint64][]TaskRow, error) {
	out := map[uint64][]TaskRow{}
	if len(postIDs) == 0 {
		return out, nil
	}
	var rows []TaskRow
	err := s.db(ctx).Table("tasks").
		Select(taskRowsSelect).
		Joins("JOIN board_columns ON board_columns.id = tasks.column_id").
		Joins("JOIN posts ON posts.id = tasks.post_id").
		Where("tasks.post_id IN ?", postIDs).
		Order("tasks.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "taskRowsOfPosts")
	}
	if err := s.attachAssignees(ctx, rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.PostID] = append(out[r.PostID], r)
	}
	return out, nil
}

// attachAssignees reads the assignees of the rows with one query and sets
// them on the rows.
func (s *Store) attachAssignees(ctx context.Context, rows []TaskRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]uint64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	var assignees []TaskAssigneeRow
	err := s.db(ctx).Table("task_assignees").
		Select("task_assignees.task_id AS task_id, task_assignees.user_id AS user_id, users.username AS username, users.display_name AS display_name").
		Joins("JOIN users ON users.id = task_assignees.user_id").
		Where("task_assignees.task_id IN ?", ids).
		Order("task_assignees.id ASC").
		Scan(&assignees).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "attachAssignees")
	}
	byTask := make(map[uint64][]TaskAssigneeRow, len(rows))
	for _, a := range assignees {
		byTask[a.TaskID] = append(byTask[a.TaskID], a)
	}
	for i := range rows {
		rows[i].Assignees = byTask[rows[i].ID]
	}
	return nil
}
