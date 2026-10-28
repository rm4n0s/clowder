package store

// allModels lists every model for AutoMigrate, a table after the tables that
// its foreign keys point to.
func allModels() []any {
	return []any{
		&User{},
		&UserSettings{},
		&Invitation{},
		&PasswordReset{},
		&Session{},
		&Room{},
		&RoomMember{},
		&Post{},
		&Tag{},
		&PostTag{},
		&Attachment{},
		&Reaction{},
		&Mention{},
		&BoardColumn{},
		&Task{},
		&TaskAssignee{},
		&Notification{},
		&Conversation{},
		&Message{},
		&ConversationMember{},
		&MessageAttachment{},
		&MessageReaction{},
		&AuditEvent{},
	}
}
