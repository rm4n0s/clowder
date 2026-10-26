package store

import "time"

// BoardColumn is a column of the board of a room (SPEC §7.6). The application
// makes three with every room.
type BoardColumn struct {
	ID        uint64
	CreatedAt time.Time
	RoomID    uint64 `gorm:"not null;index:idx_board_columns_room_position,priority:1"`
	Name      string `gorm:"not null"`
	// Position orders the columns of a room from left to right.
	Position int `gorm:"not null;index:idx_board_columns_room_position,priority:2"`

	// Relation: declared so that GORM creates the foreign key.
	Room *Room `gorm:"foreignKey:RoomID;constraint:OnDelete:CASCADE"`
}

// Task is a card of the board, written in the editor of a post or comment
// (D17). Deleting its column deletes it; the application refuses to delete a
// column that still has cards.
type Task struct {
	ID        uint64
	CreatedAt time.Time
	RoomID    uint64 `gorm:"not null;index:idx_tasks_board,priority:1"`
	PostID    uint64 `gorm:"not null;index:idx_tasks_post_id"`
	ColumnID  uint64 `gorm:"not null;index:idx_tasks_board,priority:2"`
	// Position orders the cards of a column from top to bottom.
	Position int    `gorm:"not null;index:idx_tasks_board,priority:3"`
	Title    string `gorm:"not null"`
	// DueDate is the day the task is due, as midnight UTC of that day; only
	// the date counts. Nil when the task has no due date.
	DueDate *time.Time

	// Relations: declared so that GORM creates the foreign keys.
	Room   *Room        `gorm:"foreignKey:RoomID;constraint:OnDelete:CASCADE"`
	Post   *Post        `gorm:"foreignKey:PostID;constraint:OnDelete:CASCADE"`
	Column *BoardColumn `gorm:"foreignKey:ColumnID;constraint:OnDelete:CASCADE"`
}

// TaskAssignee assigns a task to a user.
type TaskAssignee struct {
	ID        uint64
	CreatedAt time.Time
	TaskID    uint64 `gorm:"not null;uniqueIndex:uq_task_assignees_task_user,priority:1"`
	UserID    uint64 `gorm:"not null;uniqueIndex:uq_task_assignees_task_user,priority:2;index:idx_task_assignees_user_id"`

	// Relations: declared so that GORM creates the foreign keys.
	Task *Task `gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE"`
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}
